package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJiraDiscover(t *testing.T) {
	response := jiraSearchResponse{
		IsLast: true,
		Issues: []jiraIssue{
			{
				Key: "PROJ-1",
				Fields: jiraIssueFields{
					Summary: "Fix login bug",
					Labels:  []string{"bug", "critical"},
					IssueType: &jiraIssueType{
						Name: "Bug",
					},
				},
			},
			{
				Key: "PROJ-2",
				Fields: jiraIssueFields{
					Summary: "Add feature",
					Labels:  nil,
					IssueType: &jiraIssueType{
						Name: "Story",
					},
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/search/jql" {
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		User:    "user@example.com",
		Token:   "test-token",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	if items[0].ID != "PROJ-1" {
		t.Errorf("expected ID %q, got %q", "PROJ-1", items[0].ID)
	}
	if items[0].Number != 1 {
		t.Errorf("expected Number 1, got %d", items[0].Number)
	}
	if items[0].Title != "Fix login bug" {
		t.Errorf("expected Title %q, got %q", "Fix login bug", items[0].Title)
	}
	if !strings.HasSuffix(items[0].URL, "/browse/PROJ-1") {
		t.Errorf("expected URL to end with /browse/PROJ-1, got %q", items[0].URL)
	}
	if len(items[0].Labels) != 2 || items[0].Labels[0] != "bug" {
		t.Errorf("unexpected labels: %v", items[0].Labels)
	}
	if items[0].Kind != "Bug" {
		t.Errorf("expected Kind %q, got %q", "Bug", items[0].Kind)
	}

	if items[1].ID != "PROJ-2" {
		t.Errorf("expected ID %q, got %q", "PROJ-2", items[1].ID)
	}
	if items[1].Number != 2 {
		t.Errorf("expected Number 2, got %d", items[1].Number)
	}
	if items[1].Kind != "Story" {
		t.Errorf("expected Kind %q, got %q", "Story", items[1].Kind)
	}
}

func TestJiraDiscoverJQLFilter(t *testing.T) {
	var receivedJQL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/search/jql" {
			receivedJQL = r.URL.Query().Get("jql")
			json.NewEncoder(w).Encode(jiraSearchResponse{IsLast: true})
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		JQL:     "status = Open",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "project = PROJ AND (status = Open)"
	if receivedJQL != expected {
		t.Errorf("expected JQL %q, got %q", expected, receivedJQL)
	}
}

func TestJiraDiscoverDefaultJQL(t *testing.T) {
	var receivedJQL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/search/jql" {
			receivedJQL = r.URL.Query().Get("jql")
			json.NewEncoder(w).Encode(jiraSearchResponse{IsLast: true})
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "project = PROJ"
	if receivedJQL != expected {
		t.Errorf("expected JQL %q, got %q", expected, receivedJQL)
	}
}

func TestJiraDiscoverAuth(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/search/jql" {
			authHeader = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(jiraSearchResponse{IsLast: true})
		}
	}))
	defer server.Close()

	// With user + token: Jira Cloud basic auth
	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		User:    "user@example.com",
		Token:   "api-token",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(authHeader, "Basic ") {
		t.Errorf("expected Basic auth header, got %q", authHeader)
	}

	// With token only: Jira Data Center/Server PAT (Bearer auth)
	authHeader = ""
	s.User = ""
	s.Token = "pat-token"
	_, err = s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authHeader != "Bearer pat-token" {
		t.Errorf("expected Bearer auth header, got %q", authHeader)
	}

	// Without credentials
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

func TestJiraDiscoverAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errorMessages":["Unauthorized"]}`))
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
	}

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain status 401, got %v", err)
	}
}

func TestJiraDiscoverEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jiraSearchResponse{IsLast: true})
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestJiraDiscoverPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("nextPageToken")
		if token == "" {
			json.NewEncoder(w).Encode(jiraSearchResponse{
				NextPageToken: "page-2",
				IsLast:        false,
				Issues: []jiraIssue{
					{Key: "PROJ-1", Fields: jiraIssueFields{Summary: "Issue 1"}},
				},
			})
		} else {
			json.NewEncoder(w).Encode(jiraSearchResponse{
				IsLast: true,
				Issues: []jiraIssue{
					{Key: "PROJ-2", Fields: jiraIssueFields{Summary: "Issue 2"}},
				},
			})
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != "PROJ-1" || items[1].ID != "PROJ-2" {
		t.Errorf("unexpected items: %+v", items)
	}
}

func TestJiraDiscoverComments(t *testing.T) {
	response := jiraSearchResponse{
		IsLast: true,
		Issues: []jiraIssue{
			{
				Key: "PROJ-42",
				Fields: jiraIssueFields{
					Summary: "Bug",
					Comment: &jiraComments{
						Comments: []jiraComment{
							{Body: "First comment"},
							{Body: "Second comment"},
						},
					},
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
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

func TestJiraDiscoverADFComments(t *testing.T) {
	// Simulate Atlassian Document Format comment body
	adfBody := map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": []interface{}{
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "This is an ADF comment",
					},
				},
			},
		},
	}

	response := jiraSearchResponse{
		IsLast: true,
		Issues: []jiraIssue{
			{
				Key: "PROJ-1",
				Fields: jiraIssueFields{
					Summary: "ADF issue",
					Comment: &jiraComments{
						Comments: []jiraComment{
							{Body: adfBody},
						},
					},
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Comments != "This is an ADF comment" {
		t.Errorf("expected ADF comment text, got %q", items[0].Comments)
	}
}

func TestJiraDiscoverNoIssueType(t *testing.T) {
	response := jiraSearchResponse{
		IsLast: true,
		Issues: []jiraIssue{
			{
				Key: "PROJ-1",
				Fields: jiraIssueFields{
					Summary: "Issue without type",
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if items[0].Kind != "Issue" {
		t.Errorf("expected Kind %q, got %q", "Issue", items[0].Kind)
	}
}

func TestJiraDiscoverJQLWithOrderBy(t *testing.T) {
	var receivedJQL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/search/jql" {
			receivedJQL = r.URL.Query().Get("jql")
			json.NewEncoder(w).Encode(jiraSearchResponse{IsLast: true})
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		JQL:     "status = Open ORDER BY created DESC",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "project = PROJ AND (status = Open) ORDER BY created DESC"
	if receivedJQL != expected {
		t.Errorf("expected JQL %q, got %q", expected, receivedJQL)
	}
}

func TestSplitJQLOrderBy(t *testing.T) {
	tests := []struct {
		name        string
		jql         string
		wantFilter  string
		wantOrderBy string
	}{
		{
			name:        "no ORDER BY",
			jql:         "status = Open",
			wantFilter:  "status = Open",
			wantOrderBy: "",
		},
		{
			name:        "with ORDER BY",
			jql:         "status = Open ORDER BY created DESC",
			wantFilter:  "status = Open",
			wantOrderBy: "ORDER BY created DESC",
		},
		{
			name:        "case insensitive order by",
			jql:         "status = Open order by created",
			wantFilter:  "status = Open",
			wantOrderBy: "order by created",
		},
		{
			name:        "ORDER BY only",
			jql:         "ORDER BY created DESC",
			wantFilter:  "",
			wantOrderBy: "ORDER BY created DESC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, orderBy := splitJQLOrderBy(tt.jql)
			if filter != tt.wantFilter {
				t.Errorf("filter: got %q, want %q", filter, tt.wantFilter)
			}
			if orderBy != tt.wantOrderBy {
				t.Errorf("orderBy: got %q, want %q", orderBy, tt.wantOrderBy)
			}
		})
	}
}

func TestExtractIssueNumber(t *testing.T) {
	tests := []struct {
		key  string
		want int
	}{
		{"PROJ-42", 42},
		{"ABC-1", 1},
		{"PROJ-0", 0},
		{"INVALID", 0},
		{"PROJ-abc", 0},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := extractIssueNumber(tt.key)
			if got != tt.want {
				t.Errorf("extractIssueNumber(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}

// TestJiraDiscoverLegacySearchFallback verifies that when the Cloud-only
// token-paginated /rest/api/2/search/jql endpoint returns 404 (as on Jira
// Data Center/Server), the source falls back to the classic offset-paginated
// /rest/api/2/search endpoint and still returns all discovered issues.
func TestJiraDiscoverLegacySearchFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/search/jql":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"HTTP 404 Not Found","status-code":404,"sub-code":-1}`))
		case "/rest/api/2/search":
			startAt := r.URL.Query().Get("startAt")
			if startAt == "" || startAt == "0" {
				json.NewEncoder(w).Encode(jiraSearchResponse{
					StartAt: 0,
					Total:   2,
					Issues: []jiraIssue{
						{Key: "PROJ-1", Fields: jiraIssueFields{Summary: "Issue 1"}},
					},
				})
				return
			}
			json.NewEncoder(w).Encode(jiraSearchResponse{
				StartAt: 1,
				Total:   2,
				Issues: []jiraIssue{
					{Key: "PROJ-2", Fields: jiraIssueFields{Summary: "Issue 2"}},
				},
			})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Token:   "test-pat",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != "PROJ-1" || items[1].ID != "PROJ-2" {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestJiraDiscoverNonNotFoundErrorNotRetried verifies that non-404 errors
// from the Cloud endpoint are returned directly, without attempting the
// legacy fallback (avoiding masking real failures like auth errors).
func TestJiraDiscoverNonNotFoundErrorNotRetried(t *testing.T) {
	var legacyHit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/search/jql":
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"unauthorized"}`))
		case "/rest/api/2/search":
			legacyHit = true
			json.NewEncoder(w).Encode(jiraSearchResponse{IsLast: true})
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Token:   "test-pat",
	}

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to mention status 401, got: %v", err)
	}
	if legacyHit {
		t.Error("legacy endpoint should not be hit for non-404 errors")
	}
}

// TestJiraDiscoverLegacyFallbackAfterCloudPageMidRun verifies that when the
// Cloud endpoint succeeds for one or more pages before 404ing on a later
// page (e.g. a flaky proxy or an endpoint that disappears mid-run), the
// legacy fallback resumes pagination from the issues already collected
// instead of restarting at offset 0 and duplicating them.
func TestJiraDiscoverLegacyFallbackAfterCloudPageMidRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/search/jql":
			if r.URL.Query().Get("nextPageToken") == "" {
				json.NewEncoder(w).Encode(jiraSearchResponse{
					NextPageToken: "tok1",
					Issues: []jiraIssue{
						{Key: "PROJ-1", Fields: jiraIssueFields{Summary: "Issue 1"}},
					},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"HTTP 404 Not Found","status-code":404,"sub-code":-1}`))
		case "/rest/api/2/search":
			startAt := r.URL.Query().Get("startAt")
			if startAt != "1" {
				t.Errorf("expected legacy fallback to resume at startAt=1, got %q", startAt)
			}
			json.NewEncoder(w).Encode(jiraSearchResponse{
				StartAt: 1,
				Total:   2,
				Issues: []jiraIssue{
					{Key: "PROJ-2", Fields: jiraIssueFields{Summary: "Issue 2"}},
				},
			})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &JiraSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Token:   "test-pat",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
	}
	if items[0].ID != "PROJ-1" || items[1].ID != "PROJ-2" {
		t.Errorf("unexpected items: %+v", items)
	}
}
