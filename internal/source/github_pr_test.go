package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDiscoverPullRequests(t *testing.T) {
	draft := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Fix flaky test",
					Body:    "Fixes #123",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Labels:  []githubLabel{{Name: "generated-by-kelos"}},
					User:    githubUser{Login: "kelos-bot"},
					Head: githubPullRequestHead{
						Ref: "kelos-task-123",
						SHA: "head-sha-1",
					},
				},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{
				{
					Body:      "Please take another look",
					CreatedAt: "2026-01-02T11:00:00Z",
				},
			})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{
				{
					Body:      "Handle the error path",
					Path:      "internal/source/github.go",
					Line:      42,
					CreatedAt: "2026-01-02T12:01:00Z",
					CommitID:  "head-sha-1",
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:       "owner",
		Repo:        "repo",
		BaseURL:     server.URL,
		ReviewState: "changes_requested",
		Labels:      []string{"generated-by-kelos"},
		Draft:       &draft,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	item := items[0]
	if item.Kind != "PR" {
		t.Errorf("Kind = %q, want %q", item.Kind, "PR")
	}
	if item.Branch != "kelos-task-123" {
		t.Errorf("Branch = %q, want %q", item.Branch, "kelos-task-123")
	}
	if item.ReviewState != "changes_requested" {
		t.Errorf("ReviewState = %q, want %q", item.ReviewState, "changes_requested")
	}
	if item.ReviewComments != "internal/source/github.go:42\nHandle the error path" {
		t.Errorf("ReviewComments = %q, want formatted review comment", item.ReviewComments)
	}
	if item.Comments != "Please take another look" {
		t.Errorf("Comments = %q, want PR conversation comments", item.Comments)
	}

	wantTriggerTime := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	if !item.TriggerTime.Equal(wantTriggerTime) {
		t.Errorf("TriggerTime = %v, want %v", item.TriggerTime, wantTriggerTime)
	}
}

func TestDiscoverPullRequestsBlockedByPauseComment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Fix flaky test",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head: githubPullRequestHead{
						Ref: "kelos-task-123",
						SHA: "head-sha-1",
					},
				},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{
				{
					Body:      "/kelos needs-input",
					CreatedAt: "2026-01-03T12:00:00Z",
				},
			})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:           "owner",
		Repo:            "repo",
		BaseURL:         server.URL,
		ReviewState:     "changes_requested",
		ExcludeComments: []string{"/kelos needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestDiscoverPullRequestsResumeAfterNewReview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Fix flaky test",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head: githubPullRequestHead{
						Ref: "kelos-task-123",
						SHA: "head-sha-1",
					},
				},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer-a"},
				},
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-04T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer-b"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{
				{
					Body:      "/kelos needs-input",
					CreatedAt: "2026-01-03T12:00:00Z",
				},
			})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:           "owner",
		Repo:            "repo",
		BaseURL:         server.URL,
		ReviewState:     "changes_requested",
		ExcludeComments: []string{"/kelos needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestDiscoverPullRequestsFiltersByLabelAuthorDraftAndExcludeLabel(t *testing.T) {
	draft := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Good PR",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Labels:  []githubLabel{{Name: "generated-by-kelos"}},
					User:    githubUser{Login: "kelos-bot"},
					Head: githubPullRequestHead{
						Ref: "kelos-task-1",
						SHA: "head-sha-1",
					},
				},
				{
					Number:  2,
					Title:   "Bad PR",
					HTMLURL: "https://github.com/owner/repo/pull/2",
					Labels:  []githubLabel{{Name: "generated-by-kelos"}, {Name: "wontfix"}},
					User:    githubUser{Login: "someone-else"},
					Draft:   true,
					Head: githubPullRequestHead{
						Ref: "kelos-task-2",
						SHA: "head-sha-2",
					},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		case "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/2/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{})
		case "/repos/owner/repo/pulls/2/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:         "owner",
		Repo:          "repo",
		BaseURL:       server.URL,
		ReviewState:   "any",
		Labels:        []string{"generated-by-kelos"},
		ExcludeLabels: []string{"wontfix"},
		Author:        "kelos-bot",
		Draft:         &draft,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("Number = %d, want %d", items[0].Number, 1)
	}
}

func TestAggregatePullRequestReviewState(t *testing.T) {
	tests := []struct {
		name      string
		headSHA   string
		reviews   []githubPullRequestReview
		wantState string
		wantTime  time.Time
	}{
		{
			name:    "changes requested wins over approved",
			headSHA: "head-sha",
			reviews: []githubPullRequestReview{
				{
					State:       "APPROVED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha",
					User:        githubUser{Login: "reviewer-a"},
				},
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-03T12:00:00Z",
					CommitID:    "head-sha",
					User:        githubUser{Login: "reviewer-b"},
				},
			},
			wantState: "changes_requested",
			wantTime:  time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
		},
		{
			name:    "latest review per reviewer wins",
			headSHA: "head-sha",
			reviews: []githubPullRequestReview{
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha",
					User:        githubUser{Login: "reviewer-a"},
				},
				{
					State:       "APPROVED",
					SubmittedAt: "2026-01-03T12:00:00Z",
					CommitID:    "head-sha",
					User:        githubUser{Login: "reviewer-a"},
				},
			},
			wantState: "approved",
			wantTime:  time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
		},
		{
			name:    "stale sha ignored",
			headSHA: "head-sha",
			reviews: []githubPullRequestReview{
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "old-sha",
					User:        githubUser{Login: "reviewer-a"},
				},
				{
					State:       "APPROVED",
					SubmittedAt: "2026-01-03T12:00:00Z",
					CommitID:    "head-sha",
					User:        githubUser{Login: "reviewer-a"},
				},
			},
			wantState: "approved",
			wantTime:  time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
		},
		{
			name:    "non qualifying reviews ignored",
			headSHA: "head-sha",
			reviews: []githubPullRequestReview{
				{
					State:       "COMMENTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha",
					User:        githubUser{Login: "reviewer-a"},
				},
			},
			wantState: "",
			wantTime:  time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotTime := aggregatePullRequestReviewState(tt.reviews, tt.headSHA)
			if gotState != tt.wantState {
				t.Errorf("state = %q, want %q", gotState, tt.wantState)
			}
			if !gotTime.Equal(tt.wantTime) {
				t.Errorf("time = %v, want %v", gotTime, tt.wantTime)
			}
		})
	}
}
