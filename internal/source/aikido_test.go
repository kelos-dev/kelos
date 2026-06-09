package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAikidoDiscoverBuildsQueriesFiltersSeverityAndMapsMetadata(t *testing.T) {
	var issueGroupQuery urlValues
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/aikido/repositories/code":
			if got := r.URL.Query().Get("filter_name"); got != "notification-service" {
				t.Errorf("filter_name = %q, want notification-service", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"name": "notification-service", "active": true}},
			})
		case "/aikido/open-issue-groups":
			issueGroupQuery = urlValues(r.URL.Query())
			if r.URL.Query().Get("filter_issue_type") != "" {
				t.Errorf("filter_issue_type should not be sent, got %q", r.URL.Query().Get("filter_issue_type"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":                   123,
						"title":                "Upgrade vulnerable package",
						"description":          "lodash vulnerable",
						"severity":             "high",
						"status":               "open",
						"issue_type":           "open_source",
						"code_repository_name": "notification-service",
						"package_name":         "lodash",
						"fixed_version":        "4.17.21",
						"url":                  "https://app.aikido.dev/issues/groups/123",
					},
					{
						"id":                   124,
						"title":                "Low severity issue",
						"severity":             "low",
						"status":               "open",
						"issue_type":           "sast",
						"code_repository_name": "notification-service",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &AikidoSource{
		ProxyBaseURL: server.URL + "/aikido",
		Repositories: []string{"notification-service"},
		Statuses:     []string{"open"},
		Severities:   []string{"high"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if issueGroupQuery.Get("filter_code_repo_name") != "notification-service" {
		t.Errorf("filter_code_repo_name = %q", issueGroupQuery.Get("filter_code_repo_name"))
	}
	if issueGroupQuery.Get("filter_status") != "open" {
		t.Errorf("filter_status = %q", issueGroupQuery.Get("filter_status"))
	}

	item := items[0]
	if item.ID != "aikido-group-123" {
		t.Errorf("ID = %q", item.ID)
	}
	if item.Kind != "AikidoIssueGroup" {
		t.Errorf("Kind = %q", item.Kind)
	}
	if item.Metadata[AikidoMetadataIssueGroupID] != "123" {
		t.Errorf("issue group metadata = %q", item.Metadata[AikidoMetadataIssueGroupID])
	}
	if item.Metadata[AikidoMetadataSeverity] != "high" {
		t.Errorf("severity metadata = %q", item.Metadata[AikidoMetadataSeverity])
	}
	if item.Metadata[AikidoMetadataRepositories] != "notification-service" {
		t.Errorf("repositories metadata = %q", item.Metadata[AikidoMetadataRepositories])
	}
	if !strings.Contains(item.Body, "Fixed version: 4.17.21") {
		t.Errorf("Body missing fixed version hint:\n%s", item.Body)
	}
}

func TestAikidoDiscoverIssueExportScopesToMainBranchAndBuildsPromptRows(t *testing.T) {
	var repoQuery urlValues
	var exportQuery urlValues
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(aikidoHeaderClient); got != aikidoClientDiscovery {
			t.Errorf("Aikido client header = %q, want %q", got, aikidoClientDiscovery)
		}
		if got := r.Header.Get(aikidoHeaderTaskSpawner); got != "cody-aikido-security-main" {
			t.Errorf("TaskSpawner header = %q", got)
		}
		switch r.URL.Path {
		case "/aikido/repositories/code":
			repoQuery = urlValues(r.URL.Query())
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":     1443254,
				"name":   "template-nestjs-be",
				"branch": "main",
				"active": true,
			}})
		case "/aikido/issues/export":
			exportQuery = urlValues(r.URL.Query())
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":                299565698,
					"group_id":          17997122,
					"status":            "open",
					"type":              "open_source",
					"severity":          "critical",
					"code_repo_id":      1443254,
					"code_repo_name":    "template-nestjs-be",
					"affected_package":  "golang.org/x/crypto",
					"installed_version": "v0.49.0",
					"patched_versions":  []string{"0.52.0"},
					"cve_id":            "AIKIDO-2026-11022",
					"affected_file":     "go.mod",
					"start_line":        12,
					"end_line":          12,
				},
				{
					"id":             299565699,
					"group_id":       17997123,
					"status":         "open",
					"type":           "open_source",
					"severity":       "low",
					"code_repo_id":   1443254,
					"code_repo_name": "template-nestjs-be",
				},
			})
		case "/aikido/issues/groups/17997122":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":           17997122,
				"title":        "Upgrade golang.org/x/crypto",
				"description":  "A vulnerable package is installed",
				"how_to_fix":   "Upgrade to 0.52.0",
				"severity":     "critical",
				"group_status": "open",
				"type":         "open_source",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &AikidoSource{
		ProxyBaseURL:    server.URL + "/aikido",
		Branch:          "main",
		Repositories:    []string{"template-nestjs-be"},
		Statuses:        []string{"open"},
		Severities:      []string{"critical"},
		IssueTypes:      []string{"open_source"},
		TaskSpawnerName: "cody-aikido-security-main",
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if repoQuery.Get("filter_branch") != "main" {
		t.Errorf("filter_branch = %q, want main", repoQuery.Get("filter_branch"))
	}
	if repoQuery.Get("filter_name") != "template-nestjs-be" {
		t.Errorf("filter_name = %q, want template-nestjs-be", repoQuery.Get("filter_name"))
	}
	if exportQuery.Get("filter_code_repo_id") != "1443254" {
		t.Errorf("filter_code_repo_id = %q, want 1443254", exportQuery.Get("filter_code_repo_id"))
	}
	if exportQuery.Get("filter_issue_type") != "open_source" {
		t.Errorf("filter_issue_type = %q, want open_source", exportQuery.Get("filter_issue_type"))
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	item := items[0]
	if item.Branch != "main" {
		t.Errorf("Branch = %q, want main", item.Branch)
	}
	if item.Metadata[AikidoMetadataIssueGroupID] != "17997122" {
		t.Errorf("issue group metadata = %q", item.Metadata[AikidoMetadataIssueGroupID])
	}
	if item.Metadata[AikidoMetadataBranch] != "main" {
		t.Errorf("branch metadata = %q", item.Metadata[AikidoMetadataBranch])
	}
	if item.Metadata[AikidoMetadataAffectedPackages] != "golang.org/x/crypto" {
		t.Errorf("affected packages metadata = %q", item.Metadata[AikidoMetadataAffectedPackages])
	}
	if item.Metadata["aikido.kelos.dev/issue-ids"] != "" {
		t.Fatalf("issue IDs should stay out of annotations, got %q", item.Metadata["aikido.kelos.dev/issue-ids"])
	}
	for _, want := range []string{
		"These rows are scoped to active Aikido code repositories on the branch above.",
		"issue_id=299565698",
		"patched=0.52.0",
		"file=go.mod",
		"Work only against latest main",
	} {
		if !strings.Contains(item.Body, want) {
			t.Fatalf("Body missing %q:\n%s", want, item.Body)
		}
	}
}

func TestAikidoDiscoverRetriesProxyRetryAfter(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-issue-groups" {
			http.NotFound(w, r)
			return
		}
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "limited", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "24589148",
			"title":      "Upgrade vulnerable package",
			"severity":   "high",
			"status":     "open",
			"issue_type": "open_source",
		}})
	}))
	defer server.Close()

	s := &AikidoSource{ProxyBaseURL: server.URL}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(items) != 1 || items[0].ID != "aikido-group-24589148" {
		t.Fatalf("items = %#v", items)
	}
}

func TestAikidoDiscoverDefaultsStatusToOpen(t *testing.T) {
	var status string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-issue-groups":
			status = r.URL.Query().Get("filter_status")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &AikidoSource{ProxyBaseURL: server.URL}
	if _, err := s.Discover(context.Background()); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if status != "open" {
		t.Errorf("filter_status = %q, want open", status)
	}
}

func TestAikidoDiscoverRepositoryValidationFailsOnNoExactMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"name": "notification-service-old", "active": true}},
		})
	}))
	defer server.Close()

	s := &AikidoSource{
		ProxyBaseURL: server.URL,
		Repositories: []string{"notification-service"},
	}

	_, err := s.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Discover() error = %v, want repository not found", err)
	}
}

func TestAikidoDiscoverDeduplicatesIssueGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-issue-groups" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "abc",
			"title":      "Shared issue",
			"severity":   "critical",
			"status":     r.URL.Query().Get("filter_status"),
			"issue_type": "sast",
		}})
	}))
	defer server.Close()

	s := &AikidoSource{
		ProxyBaseURL: server.URL,
		Statuses:     []string{"open", "snoozed"},
	}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
}

func TestAikidoDiscoverErrorsOnMissingIssueGroupID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-issue-groups" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"title": "No ID", "severity": "high"}})
	}))
	defer server.Close()

	s := &AikidoSource{ProxyBaseURL: server.URL}
	_, err := s.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing issue group ID") {
		t.Fatalf("Discover() error = %v, want missing issue group ID", err)
	}
}

func TestAikidoDiscoverRedactsLeakedSecretBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-issue-groups" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":          "secret-1",
			"title":       "Leaked secret",
			"description": "token=ghp_abcdefghijklmnopqrstuvwxyz123456",
			"severity":    "critical",
			"status":      "open",
			"issue_type":  "leaked_secret",
		}})
	}))
	defer server.Close()

	s := &AikidoSource{ProxyBaseURL: server.URL}
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if strings.Contains(items[0].Body, "ghp_abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("Body leaked secret: %s", items[0].Body)
	}
}

func TestAikidoWorkItemIDShortensLongIDs(t *testing.T) {
	id := aikidoWorkItemID("550e8400-e29b-41d4-a716-446655440000")
	if len(id) > 30 {
		t.Fatalf("len(id) = %d, want <= 30: %s", len(id), id)
	}
	if !strings.HasPrefix(id, "aikido-group-550e8400-") {
		t.Fatalf("ID = %q, want stable shortened Aikido group ID", id)
	}
}

type urlValues map[string][]string

func (v urlValues) Get(key string) string {
	if values := v[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}
