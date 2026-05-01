package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
)

func TestFetchGitHubPRBranchWithToken(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantBranch string
		wantSHA    string
		wantErr    bool
	}{
		{
			name:       "successful fetch",
			statusCode: http.StatusOK,
			response: map[string]interface{}{
				"head": map[string]interface{}{
					"ref": "feature-branch",
					"sha": "abc123",
				},
			},
			wantBranch: "feature-branch",
			wantSHA:    "abc123",
		},
		{
			name:       "API error",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantErr:    true,
		},
		{
			name:       "empty head ref",
			statusCode: http.StatusOK,
			response: map[string]interface{}{
				"head": map[string]interface{}{
					"ref": "",
					"sha": "",
				},
			},
			wantBranch: "",
			wantSHA:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("Expected Authorization header 'Bearer test-token', got %q", r.Header.Get("Authorization"))
				}
				if r.Header.Get("Accept") != "application/vnd.github+json" {
					t.Errorf("Expected Accept header 'application/vnd.github+json', got %q", r.Header.Get("Accept"))
				}
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			head, err := fetchGitHubPRBranchWithToken(context.Background(), server.URL, "test-token")
			if (err != nil) != tt.wantErr {
				t.Errorf("fetchGitHubPRBranchWithToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if head.Branch != tt.wantBranch {
				t.Errorf("fetchGitHubPRBranchWithToken() Branch = %q, want %q", head.Branch, tt.wantBranch)
			}
			if head.SHA != tt.wantSHA {
				t.Errorf("fetchGitHubPRBranchWithToken() SHA = %q, want %q", head.SHA, tt.wantSHA)
			}
		})
	}
}

func TestFetchGitHubPRBranchWithToken_EmptyToken(t *testing.T) {
	head, err := fetchGitHubPRBranchWithToken(context.Background(), "http://unused", "")
	if err != nil {
		t.Errorf("Expected no error for empty token, got %v", err)
	}
	if head.Branch != "" {
		t.Errorf("Expected empty branch for empty token, got %q", head.Branch)
	}
}

func TestSetGitHubTokenResolver(t *testing.T) {
	orig := githubTokenResolver
	defer func() { githubTokenResolver = orig }()

	SetGitHubTokenResolver(func(context.Context) (string, error) {
		return "injected-token", nil
	})

	token, err := githubTokenResolver(context.Background())
	if err != nil {
		t.Fatalf("resolver() error = %v", err)
	}
	if token != "injected-token" {
		t.Errorf("resolver() = %q, want %q", token, "injected-token")
	}
}

func TestFetchGitHubPRBranch_NilResolver(t *testing.T) {
	orig := githubTokenResolver
	defer func() { githubTokenResolver = orig }()

	githubTokenResolver = nil
	head, err := fetchGitHubPRBranch(context.Background(), "http://unused")
	if err != nil {
		t.Fatalf("fetchGitHubPRBranch() error = %v", err)
	}
	if head.Branch != "" {
		t.Errorf("fetchGitHubPRBranch() Branch = %q, want empty for nil resolver", head.Branch)
	}
}

func TestEnrichGitHubIssueCommentBranch(t *testing.T) {
	// Swap in a stub fetcher
	orig := githubPRBranchFetcher
	defer func() { githubPRBranchFetcher = orig }()

	t.Run("enriches branch and SHA from API", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (githubPRHeadInfo, error) {
			if prAPIURL != "https://api.github.com/repos/org/repo/pulls/42" {
				t.Errorf("Unexpected prAPIURL: %s", prAPIURL)
			}
			return githubPRHeadInfo{Branch: "my-feature-branch", SHA: "abc123sha"}, nil
		}

		eventData := &GitHubEventData{
			PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
		}

		enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "my-feature-branch" {
			t.Errorf("Expected Branch = %q, got %q", "my-feature-branch", eventData.Branch)
		}
		if eventData.HeadSHA != "abc123sha" {
			t.Errorf("Expected HeadSHA = %q, got %q", "abc123sha", eventData.HeadSHA)
		}
	})

	t.Run("no-op when PullRequestAPIURL is empty", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (githubPRHeadInfo, error) {
			t.Error("Fetcher should not be called when PullRequestAPIURL is empty")
			return githubPRHeadInfo{}, nil
		}

		eventData := &GitHubEventData{}
		enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "" {
			t.Errorf("Expected empty Branch, got %q", eventData.Branch)
		}
		if eventData.HeadSHA != "" {
			t.Errorf("Expected empty HeadSHA, got %q", eventData.HeadSHA)
		}
	})

	t.Run("handles no credentials gracefully", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (githubPRHeadInfo, error) {
			return githubPRHeadInfo{}, nil // simulates no credentials configured
		}

		eventData := &GitHubEventData{
			PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
		}

		enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "" {
			t.Errorf("Expected empty Branch when no credentials configured, got %q", eventData.Branch)
		}
		if eventData.HeadSHA != "" {
			t.Errorf("Expected empty HeadSHA when no credentials configured, got %q", eventData.HeadSHA)
		}
	})
}
