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

func containsParam(query, param string) bool {
	return strings.Contains(query, param)
}
