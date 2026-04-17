package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCreateComment(t *testing.T) {
	var (
		mu         sync.Mutex
		gotMethod  string
		gotPath    string
		gotBody    createCommentRequest
		gotAuth    string
		gotAccept  string
		gotContent string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotContent = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotBody)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(commentResponse{ID: 12345})
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:   "test-owner",
		Repo:    "test-repo",
		Token:   "test-token",
		BaseURL: server.URL,
	}

	commentID, err := reporter.CreateComment(context.Background(), 42, "Test comment body")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if commentID != 12345 {
		t.Errorf("Expected comment ID 12345, got %d", commentID)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("Expected POST, got %s", gotMethod)
	}
	if gotPath != "/repos/test-owner/test-repo/issues/42/comments" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if gotBody.Body != "Test comment body" {
		t.Errorf("Expected body %q, got %q", "Test comment body", gotBody.Body)
	}
	if gotAuth != "token test-token" {
		t.Errorf("Expected auth %q, got %q", "token test-token", gotAuth)
	}
	if gotAccept != "application/vnd.github.v3+json" {
		t.Errorf("Expected accept %q, got %q", "application/vnd.github.v3+json", gotAccept)
	}
	if gotContent != "application/json" {
		t.Errorf("Expected content-type %q, got %q", "application/json", gotContent)
	}
}

func TestCreateCommentError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"rate limit"}`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	_, err := reporter.CreateComment(context.Background(), 1, "body")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestUpdateComment(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotPath   string
		gotBody   createCommentRequest
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(commentResponse{ID: 12345})
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	err := reporter.UpdateComment(context.Background(), 12345, "Updated body")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("Expected PATCH, got %s", gotMethod)
	}
	if gotPath != "/repos/owner/repo/issues/comments/12345" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if gotBody.Body != "Updated body" {
		t.Errorf("Expected body %q, got %q", "Updated body", gotBody.Body)
	}
}

func TestUpdateCommentError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	err := reporter.UpdateComment(context.Background(), 99999, "body")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestCreateCommentNoToken(t *testing.T) {
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(commentResponse{ID: 1})
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		BaseURL: server.URL,
	}

	_, err := reporter.CreateComment(context.Background(), 1, "body")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Expected no auth header, got %q", gotAuth)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	r := &GitHubReporter{}
	if r.baseURL() != defaultBaseURL {
		t.Errorf("Expected %q, got %q", defaultBaseURL, r.baseURL())
	}
}

func TestResolveToken_StaticToken(t *testing.T) {
	r := &GitHubReporter{Token: "static-token"}
	if got := r.resolveToken(); got != "static-token" {
		t.Errorf("Expected %q, got %q", "static-token", got)
	}
}

func TestResolveToken_TokenFunc(t *testing.T) {
	r := &GitHubReporter{Token: "static-token", TokenFunc: func() string { return "func-token" }}
	if got := r.resolveToken(); got != "func-token" {
		t.Errorf("Expected %q, got %q", "func-token", got)
	}
}

func TestResolveToken_TokenFuncDynamic(t *testing.T) {
	current := "first-token"
	r := &GitHubReporter{TokenFunc: func() string { return current }}
	if got := r.resolveToken(); got != "first-token" {
		t.Errorf("Expected %q, got %q", "first-token", got)
	}

	current = "rotated-token"
	if got := r.resolveToken(); got != "rotated-token" {
		t.Errorf("Expected %q after rotation, got %q", "rotated-token", got)
	}
}

func TestResolveToken_NilTokenFunc(t *testing.T) {
	r := &GitHubReporter{Token: "fallback"}
	if got := r.resolveToken(); got != "fallback" {
		t.Errorf("Expected fallback %q, got %q", "fallback", got)
	}
}

func TestCreateComment_UsesTokenFunc(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(commentResponse{ID: 1})
	}))
	defer server.Close()

	reporter := &GitHubReporter{
		Owner:     "owner",
		Repo:      "repo",
		TokenFunc: func() string { return "func-based-token" },
		BaseURL:   server.URL,
	}

	_, err := reporter.CreateComment(context.Background(), 1, "body")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if gotAuth != "token func-based-token" {
		t.Errorf("Expected auth %q, got %q", "token func-based-token", gotAuth)
	}
}

func TestFormatComments(t *testing.T) {
	accepted := FormatAcceptedComment("test-task")
	if accepted == "" {
		t.Error("Expected non-empty accepted comment")
	}

	succeeded := FormatSucceededComment("test-task")
	if succeeded == "" {
		t.Error("Expected non-empty succeeded comment")
	}

	failed := FormatFailedComment("test-task")
	if failed == "" {
		t.Error("Expected non-empty failed comment")
	}
}
