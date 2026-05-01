package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCreateCheckRun(t *testing.T) {
	var (
		mu         sync.Mutex
		gotMethod  string
		gotPath    string
		gotBody    createCheckRunRequest
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
		json.NewEncoder(w).Encode(checkRunResponse{ID: 99001})
	}))
	defer server.Close()

	reporter := &ChecksReporter{
		Owner:   "test-owner",
		Repo:    "test-repo",
		Token:   "test-token",
		BaseURL: server.URL,
	}

	id, err := reporter.CreateCheckRun(context.Background(), "Kelos: my-spawner", "abc123", "in_progress", "", &checkRunOutput{
		Title:   "Kelos Review — In Progress",
		Summary: "Agent is working on PR #42",
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if id != 99001 {
		t.Errorf("Expected check run ID 99001, got %d", id)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("Expected POST, got %s", gotMethod)
	}
	if gotPath != "/repos/test-owner/test-repo/check-runs" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if gotBody.Name != "Kelos: my-spawner" {
		t.Errorf("Expected name %q, got %q", "Kelos: my-spawner", gotBody.Name)
	}
	if gotBody.HeadSHA != "abc123" {
		t.Errorf("Expected head_sha %q, got %q", "abc123", gotBody.HeadSHA)
	}
	if gotBody.Status != "in_progress" {
		t.Errorf("Expected status %q, got %q", "in_progress", gotBody.Status)
	}
	if gotBody.Output == nil || gotBody.Output.Title != "Kelos Review — In Progress" {
		t.Errorf("Expected output title %q, got %+v", "Kelos Review — In Progress", gotBody.Output)
	}
	if gotAuth != "token test-token" {
		t.Errorf("Expected auth %q, got %q", "token test-token", gotAuth)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Expected accept %q, got %q", "application/vnd.github+json", gotAccept)
	}
	if gotContent != "application/json" {
		t.Errorf("Expected content-type %q, got %q", "application/json", gotContent)
	}
}

func TestCreateCheckRunError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer server.Close()

	reporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	_, err := reporter.CreateCheckRun(context.Background(), "test", "abc123", "in_progress", "", nil)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestUpdateCheckRun(t *testing.T) {
	var (
		mu        sync.Mutex
		gotMethod string
		gotPath   string
		gotBody   updateCheckRunRequest
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(checkRunResponse{ID: 99001})
	}))
	defer server.Close()

	reporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	err := reporter.UpdateCheckRun(context.Background(), 99001, "completed", "success", &checkRunOutput{
		Title:   "Kelos Review — Succeeded",
		Summary: "Agent reviewed PR #42",
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("Expected PATCH, got %s", gotMethod)
	}
	if gotPath != "/repos/owner/repo/check-runs/99001" {
		t.Errorf("Unexpected path: %s", gotPath)
	}
	if gotBody.Status != "completed" {
		t.Errorf("Expected status %q, got %q", "completed", gotBody.Status)
	}
	if gotBody.Conclusion != "success" {
		t.Errorf("Expected conclusion %q, got %q", "success", gotBody.Conclusion)
	}
	if gotBody.Output == nil || gotBody.Output.Title != "Kelos Review — Succeeded" {
		t.Errorf("Expected output title %q, got %+v", "Kelos Review — Succeeded", gotBody.Output)
	}
	if gotBody.CompletedAt == nil {
		t.Error("Expected completed_at to be set for completed status")
	}
}

func TestUpdateCheckRunError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()

	reporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	err := reporter.UpdateCheckRun(context.Background(), 99999, "completed", "failure", nil)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestUpdateCheckRun_InProgressNoCompletedAt(t *testing.T) {
	var gotBody updateCheckRunRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(checkRunResponse{ID: 1})
	}))
	defer server.Close()

	reporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	err := reporter.UpdateCheckRun(context.Background(), 1, "in_progress", "", nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if gotBody.CompletedAt != nil {
		t.Error("Expected completed_at to be nil for in_progress status")
	}
}

func TestChecksReporter_UsesTokenFunc(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(checkRunResponse{ID: 1})
	}))
	defer server.Close()

	reporter := &ChecksReporter{
		Owner:     "owner",
		Repo:      "repo",
		Token:     "static-token",
		TokenFunc: func() string { return "dynamic-token" },
		BaseURL:   server.URL,
	}

	_, err := reporter.CreateCheckRun(context.Background(), "test", "sha", "in_progress", "", nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if gotAuth != "token dynamic-token" {
		t.Errorf("Expected auth %q, got %q", "token dynamic-token", gotAuth)
	}
}

func TestChecksReporter_DefaultBaseURL(t *testing.T) {
	r := &ChecksReporter{}
	if r.baseURL() != defaultBaseURL {
		t.Errorf("Expected %q, got %q", defaultBaseURL, r.baseURL())
	}
}

func TestChecksReporter_ResolveToken(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		r := &ChecksReporter{Token: "static"}
		if got := r.resolveToken(); got != "static" {
			t.Errorf("Expected %q, got %q", "static", got)
		}
	})

	t.Run("func takes precedence", func(t *testing.T) {
		r := &ChecksReporter{Token: "static", TokenFunc: func() string { return "dynamic" }}
		if got := r.resolveToken(); got != "dynamic" {
			t.Errorf("Expected %q, got %q", "dynamic", got)
		}
	})
}
