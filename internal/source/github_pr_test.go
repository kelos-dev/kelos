package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
					Body:      "Old comment should be ignored",
					Path:      "internal/source/github.go",
					Line:      41,
					CreatedAt: "2026-01-01T12:01:00Z",
					CommitID:  "old-sha",
				},
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

func TestDiscoverPullRequestsTriggerCommentRequiredForDiscovery(t *testing.T) {
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
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:          "owner",
		Repo:           "repo",
		BaseURL:        server.URL,
		ReviewState:    "changes_requested",
		TriggerComment: "/kelos pick-up",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected 0 items (trigger comment required but absent), got %d", len(items))
	}
}

func TestDiscoverPullRequestsTriggerCommentInBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Trigger in body",
					Body:    "/kelos pick-up",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head: githubPullRequestHead{
						Ref: "kelos-task-123",
						SHA: "head-sha-1",
					},
				},
				{
					Number:  2,
					Title:   "No trigger",
					Body:    "Just a description",
					HTMLURL: "https://github.com/owner/repo/pull/2",
					Head: githubPullRequestHead{
						Ref: "kelos-task-456",
						SHA: "head-sha-2",
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
		case "/repos/owner/repo/pulls/2/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{
				{
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-02T12:00:00Z",
					CommitID:    "head-sha-2",
					User:        githubUser{Login: "reviewer"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		case "/repos/owner/repo/pulls/2/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:          "owner",
		Repo:           "repo",
		BaseURL:        server.URL,
		ReviewState:    "changes_requested",
		TriggerComment: "/kelos pick-up",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected PR #1, got #%d", items[0].Number)
	}
}

func TestDiscoverPullRequestsResumeAfterTriggerComment(t *testing.T) {
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
				{
					Body:      "/kelos pick-up",
					CreatedAt: "2026-01-04T12:00:00Z",
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
		TriggerComment:  "/kelos pick-up",
		ExcludeComments: []string{"/kelos needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	wantTriggerTime := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	if !items[0].TriggerTime.Equal(wantTriggerTime) {
		t.Errorf("TriggerTime = %v, want %v", items[0].TriggerTime, wantTriggerTime)
	}
}

func TestDiscoverPullRequestsExcludeCommentNotClearedByNewReview(t *testing.T) {
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
					SubmittedAt: "2026-01-05T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{
				{
					Body:      "/kelos needs-input",
					CreatedAt: "2026-01-04T12:00:00Z",
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
		TriggerComment:  "/kelos pick-up",
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

func TestBuildPullRequestsURLSortedByUpdated(t *testing.T) {
	s := &GitHubPullRequestSource{
		Owner: "owner",
		Repo:  "repo",
		State: "all",
	}

	u, err := url.Parse(s.buildPullRequestsURL())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	q := u.Query()
	if q.Get("state") != "all" {
		t.Errorf("state = %q, want %q", q.Get("state"), "all")
	}
	if q.Get("sort") != "updated" {
		t.Errorf("sort = %q, want %q", q.Get("sort"), "updated")
	}
	if q.Get("direction") != "desc" {
		t.Errorf("direction = %q, want %q", q.Get("direction"), "desc")
	}
}

func TestMergeComments(t *testing.T) {
	conversation := []githubComment{
		{Body: "conversation comment", CreatedAt: "2026-01-01T12:00:00Z"},
	}
	review := []githubPullRequestComment{
		{Body: "review comment", CreatedAt: "2026-01-02T12:00:00Z", Path: "file.go", Line: 10, CommitID: "sha1"},
	}

	merged := mergeComments(conversation, review)
	if len(merged) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(merged))
	}
	if merged[0].Body != "conversation comment" {
		t.Errorf("merged[0].Body = %q, want %q", merged[0].Body, "conversation comment")
	}
	if merged[1].Body != "review comment" {
		t.Errorf("merged[1].Body = %q, want %q", merged[1].Body, "review comment")
	}
	if merged[1].CreatedAt != "2026-01-02T12:00:00Z" {
		t.Errorf("merged[1].CreatedAt = %q, want %q", merged[1].CreatedAt, "2026-01-02T12:00:00Z")
	}
}

func TestDiscoverPullRequestsTriggerCommentInReviewComment(t *testing.T) {
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
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{
				{
					Body:      "/kelos pick-up",
					Path:      "internal/source/github.go",
					Line:      10,
					CreatedAt: "2026-01-03T12:00:00Z",
					CommitID:  "head-sha-1",
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:          "owner",
		Repo:           "repo",
		BaseURL:        server.URL,
		ReviewState:    "changes_requested",
		TriggerComment: "/kelos pick-up",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item (trigger in review comment), got %d", len(items))
	}

	wantTriggerTime := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	if !items[0].TriggerTime.Equal(wantTriggerTime) {
		t.Errorf("TriggerTime = %v, want %v", items[0].TriggerTime, wantTriggerTime)
	}
}

func TestDiscoverPullRequestsExcludeCommentInReviewComment(t *testing.T) {
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
					Body:      "/kelos pick-up",
					CreatedAt: "2026-01-02T12:00:00Z",
				},
			})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{
				{
					Body:      "/kelos needs-input",
					Path:      "internal/source/github.go",
					Line:      10,
					CreatedAt: "2026-01-03T12:00:00Z",
					CommitID:  "head-sha-1",
				},
			})
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
		TriggerComment:  "/kelos pick-up",
		ExcludeComments: []string{"/kelos needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected 0 items (exclude in review comment), got %d", len(items))
	}
}

func TestDiscoverPullRequestsTriggerCommentInReviewBody(t *testing.T) {
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
					Body:        "/kelos pick-up",
					State:       "COMMENTED",
					SubmittedAt: "2026-01-03T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:          "owner",
		Repo:           "repo",
		BaseURL:        server.URL,
		TriggerComment: "/kelos pick-up",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item (trigger in review body), got %d", len(items))
	}

	wantTriggerTime := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	if !items[0].TriggerTime.Equal(wantTriggerTime) {
		t.Errorf("TriggerTime = %v, want %v", items[0].TriggerTime, wantTriggerTime)
	}
}

func TestDiscoverPullRequestsExcludeCommentInReviewBody(t *testing.T) {
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
					Body:        "/kelos needs-input",
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-01-03T12:00:00Z",
					CommitID:    "head-sha-1",
					User:        githubUser{Login: "reviewer"},
				},
			})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{
				{
					Body:      "/kelos pick-up",
					CreatedAt: "2026-01-02T12:00:00Z",
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
		TriggerComment:  "/kelos pick-up",
		ExcludeComments: []string{"/kelos needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected 0 items (exclude in review body), got %d", len(items))
	}
}

func TestAppendReviewBodies(t *testing.T) {
	existing := []githubComment{
		{Body: "conversation comment", CreatedAt: "2026-01-01T12:00:00Z"},
	}
	reviews := []githubPullRequestReview{
		{
			Body:        "review body comment",
			State:       "COMMENTED",
			SubmittedAt: "2026-01-02T12:00:00Z",
			CommitID:    "sha1",
			User:        githubUser{Login: "reviewer"},
		},
		{
			Body:        "",
			State:       "APPROVED",
			SubmittedAt: "2026-01-03T12:00:00Z",
			CommitID:    "sha1",
			User:        githubUser{Login: "reviewer2"},
		},
	}

	result := appendReviewBodies(existing, reviews)
	if len(result) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(result))
	}
	if result[0].Body != "conversation comment" {
		t.Errorf("result[0].Body = %q, want %q", result[0].Body, "conversation comment")
	}
	if result[1].Body != "review body comment" {
		t.Errorf("result[1].Body = %q, want %q", result[1].Body, "review body comment")
	}
	if result[1].CreatedAt != "2026-01-02T12:00:00Z" {
		t.Errorf("result[1].CreatedAt = %q, want %q", result[1].CreatedAt, "2026-01-02T12:00:00Z")
	}
}

func TestResolvePullRequestTriggerTime(t *testing.T) {
	reviewTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	commentTime := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)

	s := &GitHubPullRequestSource{ReviewState: "changes_requested"}
	if got := s.resolveTriggerTime(reviewTime, commentTime); !got.Equal(reviewTime) {
		t.Errorf("resolveTriggerTime() = %v, want %v", got, reviewTime)
	}

	s = &GitHubPullRequestSource{ReviewState: "any"}
	if got := s.resolveTriggerTime(reviewTime, commentTime); !got.Equal(commentTime) {
		t.Errorf("resolveTriggerTime() with reviewState=any = %v, want %v", got, commentTime)
	}
}

func TestMatchesFilePatterns(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		patterns *FilePatternFilter
		want     bool
	}{
		{
			name:     "nil patterns matches everything",
			files:    []string{"foo.go", "bar.md"},
			patterns: nil,
			want:     true,
		},
		{
			name:     "include match",
			files:    []string{"internal/auth/handler.go", "README.md"},
			patterns: &FilePatternFilter{Include: []string{"internal/auth/**"}},
			want:     true,
		},
		{
			name:     "include no match",
			files:    []string{"docs/guide.md", "README.md"},
			patterns: &FilePatternFilter{Include: []string{"internal/**"}},
			want:     false,
		},
		{
			name:     "exclude rejects on any match",
			files:    []string{"internal/auth/handler.go", "vendor/lib.go"},
			patterns: &FilePatternFilter{Exclude: []string{"vendor/**"}},
			want:     false,
		},
		{
			name:     "exclude no match passes",
			files:    []string{"internal/auth/handler.go"},
			patterns: &FilePatternFilter{Exclude: []string{"vendor/**"}},
			want:     true,
		},
		{
			name:  "include and exclude combined",
			files: []string{"internal/auth/handler.go", "internal/auth/handler_test.go"},
			patterns: &FilePatternFilter{
				Include: []string{"internal/**"},
				Exclude: []string{"**/*_test.go"},
			},
			want: false,
		},
		{
			name:  "excludeOnly skips docs-only PRs",
			files: []string{"docs/guide.md", "README.md"},
			patterns: &FilePatternFilter{
				Exclude:     []string{"docs/**", "*.md"},
				ExcludeOnly: true,
			},
			want: false,
		},
		{
			name:  "excludeOnly passes when not all files match exclude",
			files: []string{"docs/guide.md", "internal/handler.go"},
			patterns: &FilePatternFilter{
				Exclude:     []string{"docs/**", "*.md"},
				ExcludeOnly: true,
			},
			want: true,
		},
		{
			name:  "excludeOnly with include",
			files: []string{"internal/handler.go", "docs/guide.md"},
			patterns: &FilePatternFilter{
				Include:     []string{"internal/**"},
				Exclude:     []string{"docs/**"},
				ExcludeOnly: true,
			},
			want: true,
		},
		{
			name:     "empty files with include patterns does not match",
			files:    []string{},
			patterns: &FilePatternFilter{Include: []string{"*.go"}},
			want:     false,
		},
		{
			name:     "empty files with empty include matches",
			files:    []string{},
			patterns: &FilePatternFilter{},
			want:     true,
		},
		{
			name:     "glob star pattern",
			files:    []string{"main.go", "util.go"},
			patterns: &FilePatternFilter{Include: []string{"*.go"}},
			want:     true,
		},
		{
			name:     "doublestar recursive pattern",
			files:    []string{"a/b/c/deep.go"},
			patterns: &FilePatternFilter{Include: []string{"a/**/deep.go"}},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesFilePatterns(tt.files, tt.patterns)
			if got != tt.want {
				t.Errorf("MatchesFilePatterns() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscoverPullRequestsWithFilePatterns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Backend change",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head:    githubPullRequestHead{Ref: "feature-1", SHA: "sha-1"},
				},
				{
					Number:  2,
					Title:   "Docs only",
					HTMLURL: "https://github.com/owner/repo/pull/2",
					Head:    githubPullRequestHead{Ref: "docs-update", SHA: "sha-2"},
				},
			})
		case "/repos/owner/repo/pulls/1/files":
			json.NewEncoder(w).Encode([]githubPullRequestFile{
				{Filename: "internal/handler.go"},
				{Filename: "internal/handler_test.go"},
			})
		case "/repos/owner/repo/pulls/2/files":
			json.NewEncoder(w).Encode([]githubPullRequestFile{
				{Filename: "docs/guide.md"},
				{Filename: "README.md"},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			// PR 2 shouldn't reach review/comment fetches since it's filtered out
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
		FilePatterns: &FilePatternFilter{
			Include: []string{"internal/**"},
		},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected PR #1, got #%d", items[0].Number)
	}
	if len(items[0].ChangedFiles) != 2 {
		t.Errorf("expected 2 changed files, got %d", len(items[0].ChangedFiles))
	}
	if items[0].ChangedFiles[0] != "internal/handler.go" {
		t.Errorf("ChangedFiles[0] = %q, want %q", items[0].ChangedFiles[0], "internal/handler.go")
	}
}

func TestDiscoverPullRequestsExcludeOnlyFilePatterns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Mixed change",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head:    githubPullRequestHead{Ref: "mixed", SHA: "sha-1"},
				},
				{
					Number:  2,
					Title:   "Docs only",
					HTMLURL: "https://github.com/owner/repo/pull/2",
					Head:    githubPullRequestHead{Ref: "docs", SHA: "sha-2"},
				},
			})
		case "/repos/owner/repo/pulls/1/files":
			json.NewEncoder(w).Encode([]githubPullRequestFile{
				{Filename: "internal/handler.go"},
				{Filename: "docs/guide.md"},
			})
		case "/repos/owner/repo/pulls/2/files":
			json.NewEncoder(w).Encode([]githubPullRequestFile{
				{Filename: "docs/guide.md"},
				{Filename: "README.md"},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
		FilePatterns: &FilePatternFilter{
			Exclude:     []string{"docs/**", "*.md"},
			ExcludeOnly: true,
		},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item (docs-only PR excluded), got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected PR #1, got #%d", items[0].Number)
	}
}

func TestDiscoverPullRequestsNeedsChangedFilesWithoutFilePatterns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Backend change",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head:    githubPullRequestHead{Ref: "feature-1", SHA: "sha-1"},
				},
			})
		case "/repos/owner/repo/pulls/1/files":
			json.NewEncoder(w).Encode([]githubPullRequestFile{
				{Filename: "internal/handler.go"},
				{Filename: "internal/handler_test.go"},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:             "owner",
		Repo:              "repo",
		BaseURL:           server.URL,
		NeedsChangedFiles: true,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].ChangedFiles) != 2 {
		t.Fatalf("expected 2 changed files, got %d", len(items[0].ChangedFiles))
	}
	if items[0].ChangedFiles[0] != "internal/handler.go" {
		t.Errorf("ChangedFiles[0] = %q, want %q", items[0].ChangedFiles[0], "internal/handler.go")
	}
	if items[0].ChangedFiles[1] != "internal/handler_test.go" {
		t.Errorf("ChangedFiles[1] = %q, want %q", items[0].ChangedFiles[1], "internal/handler_test.go")
	}
}

func TestDiscoverPullRequestsChangedFilesEmptyWithoutFlag(t *testing.T) {
	filesFetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]githubPullRequest{
				{
					Number:  1,
					Title:   "Some PR",
					HTMLURL: "https://github.com/owner/repo/pull/1",
					Head:    githubPullRequestHead{Ref: "feature-1", SHA: "sha-1"},
				},
			})
		case "/repos/owner/repo/pulls/1/files":
			filesFetched = true
			json.NewEncoder(w).Encode([]githubPullRequestFile{
				{Filename: "main.go"},
			})
		case "/repos/owner/repo/pulls/1/reviews":
			json.NewEncoder(w).Encode([]githubPullRequestReview{})
		case "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		case "/repos/owner/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]githubPullRequestComment{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &GitHubPullRequestSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if filesFetched {
		t.Error("Expected files API to NOT be called when NeedsChangedFiles is false and FilePatterns is nil")
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].ChangedFiles) != 0 {
		t.Errorf("expected 0 changed files, got %d", len(items[0].ChangedFiles))
	}
}
