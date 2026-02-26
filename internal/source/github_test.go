package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscover(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug 1", Body: "Body 1", HTMLURL: "https://github.com/owner/repo/issues/1", Labels: []githubLabel{{Name: "bug"}}},
		{Number: 2, Title: "Bug 2", Body: "Body 2", HTMLURL: "https://github.com/owner/repo/issues/2", Labels: []githubLabel{{Name: "bug"}, {Name: "help wanted"}}},
		{Number: 3, Title: "Feature", Body: "Body 3", HTMLURL: "https://github.com/owner/repo/issues/3", Labels: nil},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments" ||
			r.URL.Path == "/repos/owner/repo/issues/2/comments" ||
			r.URL.Path == "/repos/owner/repo/issues/3/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	if items[0].ID != "1" || items[0].Title != "Bug 1" || items[0].Body != "Body 1" {
		t.Errorf("unexpected item[0]: %+v", items[0])
	}
	if items[0].URL != "https://github.com/owner/repo/issues/1" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
	if len(items[0].Labels) != 1 || items[0].Labels[0] != "bug" {
		t.Errorf("unexpected labels: %v", items[0].Labels)
	}
	if items[1].Number != 2 {
		t.Errorf("expected Number 2, got %d", items[1].Number)
	}
	if len(items[1].Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(items[1].Labels))
	}
}

func TestDiscoverLabelFiltering(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			receivedQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		Labels:  []string{"bug", "help wanted"},
		BaseURL: server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedQuery == "" {
		t.Fatal("no query received")
	}
	// Check that labels param is present
	if got := receivedQuery; got == "" {
		t.Fatal("empty query")
	}
	// The URL should contain labels=bug%2Chelp+wanted or similar encoding
	if !containsParam(receivedQuery, "labels") {
		t.Errorf("expected labels param in query: %s", receivedQuery)
	}
}

func TestDiscoverAssigneeFiltering(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			receivedQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:    "owner",
		Repo:     "repo",
		Assignee: "octocat",
		BaseURL:  server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "assignee=octocat") {
		t.Errorf("expected assignee=octocat in query: %s", receivedQuery)
	}
}

func TestDiscoverAssigneeNone(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			receivedQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:    "owner",
		Repo:     "repo",
		Assignee: "none",
		BaseURL:  server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "assignee=none") {
		t.Errorf("expected assignee=none in query: %s", receivedQuery)
	}
}

func TestDiscoverAuthorFiltering(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			receivedQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		Author:  "octocat",
		BaseURL: server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "creator=octocat") {
		t.Errorf("expected creator=octocat in query: %s", receivedQuery)
	}
}

func TestDiscoverAssigneeAndAuthorTogether(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			receivedQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:    "owner",
		Repo:     "repo",
		Assignee: "alice",
		Author:   "bob",
		BaseURL:  server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "assignee=alice") {
		t.Errorf("expected assignee=alice in query: %s", receivedQuery)
	}
	if !containsParam(receivedQuery, "creator=bob") {
		t.Errorf("expected creator=bob in query: %s", receivedQuery)
	}
}

func TestDiscoverStateFiltering(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			receivedQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		State:   "closed",
		BaseURL: server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "state=closed") {
		t.Errorf("expected state=closed in query: %s", receivedQuery)
	}
}

func TestDiscoverAuthHeader(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" {
			authHeader = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode([]githubIssue{})
		}
	}))
	defer server.Close()

	// With token
	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "test-token",
		BaseURL: server.URL,
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authHeader != "token test-token" {
		t.Errorf("expected 'token test-token', got %q", authHeader)
	}

	// Without token
	authHeader = ""
	s.Token = ""
	_, err = s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authHeader != "" {
		t.Errorf("expected no auth header, got %q", authHeader)
	}
}

func TestDiscoverPagination(t *testing.T) {
	page1 := []githubIssue{{Number: 1, Title: "Issue 1", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1"}}
	page2 := []githubIssue{{Number: 2, Title: "Issue 2", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2"}}

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			if r.URL.Query().Get("page") == "2" {
				json.NewEncoder(w).Encode(page2)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next"`, serverURL))
			json.NewEncoder(w).Encode(page1)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments" ||
			r.URL.Path == "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()
	serverURL = server.URL

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Number != 1 || items[1].Number != 2 {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestDiscoverAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"rate limit exceeded"}`))
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDiscoverEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]githubIssue{})
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestDiscoverComments(t *testing.T) {
	issues := []githubIssue{
		{Number: 42, Title: "Bug", Body: "Details", HTMLURL: "https://github.com/o/r/issues/42"},
	}
	comments := []githubComment{
		{Body: "First comment"},
		{Body: "Second comment"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case "/repos/owner/repo/issues/42/comments":
			json.NewEncoder(w).Encode(comments)
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	expected := "First comment\n---\nSecond comment"
	if items[0].Comments != expected {
		t.Errorf("expected comments %q, got %q", expected, items[0].Comments)
	}
}

func TestDiscoverExcludeLabels(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug 1", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1", Labels: []githubLabel{{Name: "bug"}}},
		{Number: 2, Title: "Needs input", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2", Labels: []githubLabel{{Name: "bug"}, {Name: "axon/needs-input"}}},
		{Number: 3, Title: "Feature", Body: "Body 3", HTMLURL: "https://github.com/o/r/issues/3", Labels: []githubLabel{{Name: "enhancement"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:         "owner",
		Repo:          "repo",
		ExcludeLabels: []string{"axon/needs-input"},
		BaseURL:       server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected issue #1 first, got #%d", items[0].Number)
	}
	if items[1].Number != 3 {
		t.Errorf("expected issue #3 second, got #%d", items[1].Number)
	}
}

func TestDiscoverExcludeLabelsNoMatch(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug 1", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1", Labels: []githubLabel{{Name: "bug"}}},
		{Number: 2, Title: "Feature", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2", Labels: []githubLabel{{Name: "enhancement"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:         "owner",
		Repo:          "repo",
		ExcludeLabels: []string{"axon/needs-input"},
		BaseURL:       server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items (none excluded), got %d", len(items))
	}
}

func TestDiscoverTypesIssuesOnly(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug", Body: "Body", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "PR", Body: "Body", HTMLURL: "https://github.com/o/r/pull/2", PullRequest: &struct{}{}},
		{Number: 3, Title: "Feature", Body: "Body", HTMLURL: "https://github.com/o/r/issues/3"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		Types:   []string{"issues"},
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for _, item := range items {
		if item.Kind != "Issue" {
			t.Errorf("expected Kind 'Issue', got %q for item #%d", item.Kind, item.Number)
		}
	}
}

func TestDiscoverTypesPullsOnly(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug", Body: "Body", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "PR 1", Body: "Body", HTMLURL: "https://github.com/o/r/pull/2", PullRequest: &struct{}{}},
		{Number: 3, Title: "PR 2", Body: "Body", HTMLURL: "https://github.com/o/r/pull/3", PullRequest: &struct{}{}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		Types:   []string{"pulls"},
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for _, item := range items {
		if item.Kind != "PR" {
			t.Errorf("expected Kind 'PR', got %q for item #%d", item.Kind, item.Number)
		}
	}
}

func TestDiscoverTypesBoth(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug", Body: "Body", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "PR", Body: "Body", HTMLURL: "https://github.com/o/r/pull/2", PullRequest: &struct{}{}},
		{Number: 3, Title: "Feature", Body: "Body", HTMLURL: "https://github.com/o/r/issues/3"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		Types:   []string{"issues", "pulls"},
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	kinds := map[string]int{}
	for _, item := range items {
		kinds[item.Kind]++
	}
	if kinds["Issue"] != 2 {
		t.Errorf("expected 2 issues, got %d", kinds["Issue"])
	}
	if kinds["PR"] != 1 {
		t.Errorf("expected 1 PR, got %d", kinds["PR"])
	}
}

func TestDiscoverTypesDefault(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Bug", Body: "Body", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "PR", Body: "Body", HTMLURL: "https://github.com/o/r/pull/2", PullRequest: &struct{}{}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			json.NewEncoder(w).Encode([]githubComment{})
		}
	}))
	defer server.Close()

	// No Types set — should default to issues only
	s := &GitHubSource{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item (issues only by default), got %d", len(items))
	}
	if items[0].Kind != "Issue" {
		t.Errorf("expected Kind 'Issue', got %q", items[0].Kind)
	}
}

func containsParam(query, param string) bool {
	return strings.Contains(query, param)
}

func TestDiscoverTriggerComment(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Triggered", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "Not triggered", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "/axon pick-up"}})
		case r.URL.Path == "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "Just a regular comment"}})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:          "owner",
		Repo:           "repo",
		BaseURL:        server.URL,
		TriggerComment: "/axon pick-up",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected issue #1, got #%d", items[0].Number)
	}
}

func TestDiscoverExcludeComment(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Active", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "Needs input", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "Normal comment"}})
		case r.URL.Path == "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "/axon needs-input"}})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:           "owner",
		Repo:            "repo",
		BaseURL:         server.URL,
		ExcludeComments: []string{"/axon needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected issue #1, got #%d", items[0].Number)
	}
}

func TestDiscoverMultipleExcludeComments(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Active", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "Needs input", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2"},
		{Number: 3, Title: "Paused", Body: "Body 3", HTMLURL: "https://github.com/o/r/issues/3"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "Normal comment"}})
		case r.URL.Path == "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "/axon needs-input"}})
		case r.URL.Path == "/repos/owner/repo/issues/3/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "/axon pause"}})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:           "owner",
		Repo:            "repo",
		BaseURL:         server.URL,
		ExcludeComments: []string{"/axon needs-input", "/axon pause"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected issue #1, got #%d", items[0].Number)
	}
}

func TestDiscoverTriggerAsResume(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Resumed", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "Still excluded", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments":
			// Trigger, then exclude, then trigger again — trigger is most recent, so issue should be included
			json.NewEncoder(w).Encode([]githubComment{
				{Body: "/axon pick-up"},
				{Body: "/axon needs-input"},
				{Body: "/axon pick-up"},
			})
		case r.URL.Path == "/repos/owner/repo/issues/2/comments":
			// Trigger then exclude — exclude is most recent, so issue should be excluded
			json.NewEncoder(w).Encode([]githubComment{
				{Body: "/axon pick-up"},
				{Body: "/axon needs-input"},
			})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:           "owner",
		Repo:            "repo",
		BaseURL:         server.URL,
		TriggerComment:  "/axon pick-up",
		ExcludeComments: []string{"/axon needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected issue #1, got #%d", items[0].Number)
	}
}

func TestDiscoverTriggerAndExcludeComment(t *testing.T) {
	issues := []githubIssue{
		{Number: 1, Title: "Triggered and active", Body: "Body 1", HTMLURL: "https://github.com/o/r/issues/1"},
		{Number: 2, Title: "Triggered but excluded", Body: "Body 2", HTMLURL: "https://github.com/o/r/issues/2"},
		{Number: 3, Title: "Not triggered", Body: "Body 3", HTMLURL: "https://github.com/o/r/issues/3"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues":
			json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "/axon pick-up"}})
		case r.URL.Path == "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]githubComment{
				{Body: "/axon pick-up"},
				{Body: "/axon needs-input"},
			})
		case r.URL.Path == "/repos/owner/repo/issues/3/comments":
			json.NewEncoder(w).Encode([]githubComment{{Body: "Just a comment"}})
		}
	}))
	defer server.Close()

	s := &GitHubSource{
		Owner:           "owner",
		Repo:            "repo",
		BaseURL:         server.URL,
		TriggerComment:  "/axon pick-up",
		ExcludeComments: []string{"/axon needs-input"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 1 {
		t.Errorf("expected issue #1, got #%d", items[0].Number)
	}
}

func TestPassesCommentFilter(t *testing.T) {
	tests := []struct {
		name            string
		triggerComment  string
		excludeComments []string
		comments        string
		want            bool
	}{
		{
			name:     "no filters configured",
			comments: "some comment",
			want:     true,
		},
		{
			name:           "trigger present",
			triggerComment: "/axon pick-up",
			comments:       "/axon pick-up",
			want:           true,
		},
		{
			name:           "trigger absent",
			triggerComment: "/axon pick-up",
			comments:       "no trigger here",
			want:           false,
		},
		{
			name:           "trigger empty comments",
			triggerComment: "/axon pick-up",
			comments:       "",
			want:           false,
		},
		{
			name:            "exclude present",
			excludeComments: []string{"/axon needs-input"},
			comments:        "/axon needs-input",
			want:            false,
		},
		{
			name:            "exclude absent",
			excludeComments: []string{"/axon needs-input"},
			comments:        "normal comment",
			want:            true,
		},
		{
			name:            "trigger as resume after exclude",
			triggerComment:  "/axon pick-up",
			excludeComments: []string{"/axon needs-input"},
			comments:        "/axon pick-up\n---\n/axon needs-input\n---\n/axon pick-up",
			want:            true,
		},
		{
			name:            "exclude after trigger",
			triggerComment:  "/axon pick-up",
			excludeComments: []string{"/axon needs-input"},
			comments:        "/axon pick-up\n---\n/axon needs-input",
			want:            false,
		},
		{
			name:            "both set but neither found",
			triggerComment:  "/axon pick-up",
			excludeComments: []string{"/axon needs-input"},
			comments:        "normal comment",
			want:            false,
		},
		{
			name:            "command must be on its own line",
			excludeComments: []string{"/axon needs-input"},
			comments:        "please do /axon needs-input for me",
			want:            true,
		},
		{
			name:            "multiple exclude comments",
			excludeComments: []string{"/axon needs-input", "/axon pause"},
			comments:        "/axon pause",
			want:            false,
		},
		{
			name:            "multiple exclude comments none match",
			excludeComments: []string{"/axon needs-input", "/axon pause"},
			comments:        "normal comment",
			want:            true,
		},
		{
			name:            "multiple exclude with trigger resume",
			triggerComment:  "/axon pick-up",
			excludeComments: []string{"/axon needs-input", "/axon pause"},
			comments:        "/axon pick-up\n---\n/axon pause\n---\n/axon pick-up",
			want:            true,
		},
		{
			name:            "multiple exclude second matches most recent",
			triggerComment:  "/axon pick-up",
			excludeComments: []string{"/axon needs-input", "/axon pause"},
			comments:        "/axon pick-up\n---\n/axon pause",
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &GitHubSource{
				TriggerComment:  tt.triggerComment,
				ExcludeComments: tt.excludeComments,
			}
			got := s.passesCommentFilter(tt.comments)
			if got != tt.want {
				t.Errorf("passesCommentFilter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainsCommand(t *testing.T) {
	tests := []struct {
		name string
		body string
		cmd  string
		want bool
	}{
		{"exact match", "/axon pick-up", "/axon pick-up", true},
		{"with whitespace", "  /axon pick-up  ", "/axon pick-up", true},
		{"multiline match", "some text\n/axon pick-up\nmore text", "/axon pick-up", true},
		{"no match", "some text without command", "/axon pick-up", false},
		{"partial match in word", "do /axon pick-up now", "/axon pick-up", false},
		{"empty body", "", "/axon pick-up", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsCommand(tt.body, tt.cmd)
			if got != tt.want {
				t.Errorf("containsCommand(%q, %q) = %v, want %v", tt.body, tt.cmd, got, tt.want)
			}
		})
	}
}
