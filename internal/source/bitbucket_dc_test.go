package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBitbucketDCDiscover(t *testing.T) {
	prs := []bitbucketPR{
		{ID: 1, Title: "Add feature", Description: "Feature body", State: "OPEN", Links: bitbucketPRLinks{Self: []bitbucketLink{{Href: "https://bb.example.com/projects/PROJ/repos/repo/pull-requests/1"}}}},
		{ID: 2, Title: "Fix bug", Description: "Bug body", State: "OPEN", Links: bitbucketPRLinks{Self: []bitbucketLink{{Href: "https://bb.example.com/projects/PROJ/repos/repo/pull-requests/2"}}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests":
			page := bitbucketPage{IsLastPage: true, Size: len(prs)}
			raw, _ := json.Marshal(prs)
			page.Values = raw
			json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests/1/activities" ||
			r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests/2/activities":
			page := bitbucketPage{IsLastPage: true, Size: 0}
			page.Values, _ = json.Marshal([]bitbucketActivity{})
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(items))
	}
	if items[0].ID != "1" || items[0].Title != "Add feature" || items[0].Body != "Feature body" {
		t.Errorf("Unexpected item[0]: %+v", items[0])
	}
	if items[0].Kind != "PR" {
		t.Errorf("Expected Kind 'PR', got %q", items[0].Kind)
	}
	if items[0].URL != "https://bb.example.com/projects/PROJ/repos/repo/pull-requests/1" {
		t.Errorf("Unexpected URL: %s", items[0].URL)
	}
	if items[1].Number != 2 {
		t.Errorf("Expected Number 2, got %d", items[1].Number)
	}
}

func TestBitbucketDCDiscoverStateFilter(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests" {
			receivedQuery = r.URL.RawQuery
			page := bitbucketPage{IsLastPage: true}
			page.Values, _ = json.Marshal([]bitbucketPR{})
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
		State:   "MERGED",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "state=MERGED") {
		t.Errorf("Expected state=MERGED in query: %s", receivedQuery)
	}
}

func TestBitbucketDCDiscoverStateALL(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests" {
			receivedQuery = r.URL.RawQuery
			page := bitbucketPage{IsLastPage: true}
			page.Values, _ = json.Marshal([]bitbucketPR{})
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
		State:   "ALL",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// ALL should not send the state parameter
	if containsParam(receivedQuery, "state=") {
		t.Errorf("Expected no state param for ALL, got query: %s", receivedQuery)
	}
}

func TestBitbucketDCDiscoverAuthHeader(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests" {
			authHeader = r.Header.Get("Authorization")
			page := bitbucketPage{IsLastPage: true}
			page.Values, _ = json.Marshal([]bitbucketPR{})
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	// With token
	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
		Token:   "test-token",
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if authHeader != "Bearer test-token" {
		t.Errorf("Expected 'Bearer test-token', got %q", authHeader)
	}

	// Without token
	authHeader = ""
	s.Token = ""
	_, err = s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if authHeader != "" {
		t.Errorf("Expected no auth header, got %q", authHeader)
	}
}

func TestBitbucketDCDiscoverPagination(t *testing.T) {
	page1PRs := []bitbucketPR{{ID: 1, Title: "PR 1", Description: "Body 1"}}
	page2PRs := []bitbucketPR{{ID: 2, Title: "PR 2", Description: "Body 2"}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests":
			start := r.URL.Query().Get("start")
			if start == "1" {
				raw, _ := json.Marshal(page2PRs)
				page := bitbucketPage{IsLastPage: true, Size: 1, Values: raw}
				json.NewEncoder(w).Encode(page)
				return
			}
			raw, _ := json.Marshal(page1PRs)
			page := bitbucketPage{IsLastPage: false, Size: 1, NextPageStart: 1, Values: raw}
			json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests/1/activities" ||
			r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests/2/activities":
			page := bitbucketPage{IsLastPage: true}
			page.Values, _ = json.Marshal([]bitbucketActivity{})
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(items))
	}
	if items[0].Number != 1 || items[1].Number != 2 {
		t.Errorf("Unexpected items: %+v", items)
	}
}

func TestBitbucketDCDiscoverAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
	}

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestBitbucketDCDiscoverEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := bitbucketPage{IsLastPage: true}
		page.Values, _ = json.Marshal([]bitbucketPR{})
		json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("Expected 0 items, got %d", len(items))
	}
}

func TestBitbucketDCDiscoverComments(t *testing.T) {
	prs := []bitbucketPR{
		{ID: 42, Title: "Fix bug", Description: "Details"},
	}

	activities := []bitbucketActivity{
		{Action: "COMMENTED", Comment: &bitbucketPRNote{Text: "First comment"}},
		{Action: "APPROVED"},
		{Action: "COMMENTED", Comment: &bitbucketPRNote{Text: "Second comment"}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests":
			raw, _ := json.Marshal(prs)
			page := bitbucketPage{IsLastPage: true, Size: 1, Values: raw}
			json.NewEncoder(w).Encode(page)
		case fmt.Sprintf("/rest/api/1.0/projects/PROJ/repos/repo/pull-requests/%d/activities", 42):
			raw, _ := json.Marshal(activities)
			page := bitbucketPage{IsLastPage: true, Size: len(activities), Values: raw}
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(items))
	}
	expected := "First comment\n---\nSecond comment"
	if items[0].Comments != expected {
		t.Errorf("Expected comments %q, got %q", expected, items[0].Comments)
	}
}

func TestBitbucketDCDiscoverDefaultState(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests" {
			receivedQuery = r.URL.RawQuery
			page := bitbucketPage{IsLastPage: true}
			page.Values, _ = json.Marshal([]bitbucketPR{})
			json.NewEncoder(w).Encode(page)
		}
	}))
	defer server.Close()

	s := &BitbucketDataCenterSource{
		BaseURL: server.URL,
		Project: "PROJ",
		Repo:    "repo",
		// State not set — should default to OPEN
	}

	_, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !containsParam(receivedQuery, "state=OPEN") {
		t.Errorf("Expected state=OPEN in query: %s", receivedQuery)
	}
}
