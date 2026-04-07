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
		wantErr    bool
	}{
		{
			name:       "successful fetch",
			statusCode: http.StatusOK,
			response: map[string]interface{}{
				"head": map[string]interface{}{
					"ref": "feature-branch",
				},
			},
			wantBranch: "feature-branch",
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
				},
			},
			wantBranch: "",
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

			branch, err := fetchGitHubPRBranchWithToken(context.Background(), server.URL, "test-token")
			if (err != nil) != tt.wantErr {
				t.Errorf("fetchGitHubPRBranchWithToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if branch != tt.wantBranch {
				t.Errorf("fetchGitHubPRBranchWithToken() = %q, want %q", branch, tt.wantBranch)
			}
		})
	}
}

func TestFetchGitHubPRBranchWithToken_EmptyToken(t *testing.T) {
	branch, err := fetchGitHubPRBranchWithToken(context.Background(), "http://unused", "")
	if err != nil {
		t.Errorf("Expected no error for empty token, got %v", err)
	}
	if branch != "" {
		t.Errorf("Expected empty branch for empty token, got %q", branch)
	}
}

func TestTokenProvider_PATPreferred(t *testing.T) {
	t.Setenv(githubTokenEnvVar, "ghp_my_pat_token")
	// Also set App vars to prove PAT takes precedence
	t.Setenv(githubAppIDEnvVar, "12345")
	t.Setenv(githubInstallationIDEnvVar, "67890")
	t.Setenv(githubPrivateKeyEnvVar, "fake-key")

	tp := &tokenProvider{}
	token, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token != "ghp_my_pat_token" {
		t.Errorf("Token() = %q, want %q", token, "ghp_my_pat_token")
	}
}

func TestTokenProvider_NoCredentials(t *testing.T) {
	// Ensure no env vars are set
	t.Setenv(githubTokenEnvVar, "")
	t.Setenv(githubAppIDEnvVar, "")
	t.Setenv(githubInstallationIDEnvVar, "")
	t.Setenv(githubPrivateKeyEnvVar, "")

	tp := &tokenProvider{}
	token, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token != "" {
		t.Errorf("Token() = %q, want empty", token)
	}
}

func TestTokenProvider_PartialAppCredentials(t *testing.T) {
	// Only set some app vars — should return empty, not error
	t.Setenv(githubTokenEnvVar, "")
	t.Setenv(githubAppIDEnvVar, "12345")
	t.Setenv(githubInstallationIDEnvVar, "")
	t.Setenv(githubPrivateKeyEnvVar, "")

	tp := &tokenProvider{}
	token, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token != "" {
		t.Errorf("Token() = %q, want empty for partial credentials", token)
	}
}

func TestEnrichGitHubIssueCommentBranch(t *testing.T) {
	// Swap in a stub fetcher
	orig := githubPRBranchFetcher
	defer func() { githubPRBranchFetcher = orig }()

	t.Run("enriches branch from API", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (string, error) {
			if prAPIURL != "https://api.github.com/repos/org/repo/pulls/42" {
				t.Errorf("Unexpected prAPIURL: %s", prAPIURL)
			}
			return "my-feature-branch", nil
		}

		eventData := &GitHubEventData{
			PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
		}

		enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "my-feature-branch" {
			t.Errorf("Expected Branch = %q, got %q", "my-feature-branch", eventData.Branch)
		}
	})

	t.Run("no-op when PullRequestAPIURL is empty", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (string, error) {
			t.Error("Fetcher should not be called when PullRequestAPIURL is empty")
			return "", nil
		}

		eventData := &GitHubEventData{}
		enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "" {
			t.Errorf("Expected empty Branch, got %q", eventData.Branch)
		}
	})

	t.Run("handles no credentials gracefully", func(t *testing.T) {
		githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (string, error) {
			return "", nil // simulates no credentials configured
		}

		eventData := &GitHubEventData{
			PullRequestAPIURL: "https://api.github.com/repos/org/repo/pulls/42",
		}

		enrichGitHubIssueCommentBranch(context.Background(), logr.Discard(), eventData)

		if eventData.Branch != "" {
			t.Errorf("Expected empty Branch when no credentials configured, got %q", eventData.Branch)
		}
	})
}
