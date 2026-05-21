package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAtlassianHandlerInjectsServerSideAuth(t *testing.T) {
	var gotAuth string
	var gotCookie string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	s := &server{
		cfg: config{
			upstreamURL:   upstream.URL,
			authorization: "Basic server-secret",
		},
		httpClient: upstream.Client(),
		logger:     testLogger(),
		ready:      true,
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/atlassian", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer client-secret")
	req.Header.Set("Cookie", "session=client")
	rec := httptest.NewRecorder()

	s.handleAtlassian(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotAuth != "Basic server-secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotCookie != "" {
		t.Fatalf("Cookie forwarded = %q", gotCookie)
	}
	if gotBody["method"] != "tools/list" {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
}

func TestAtlassianHandlerRejectsUnknownSubroute(t *testing.T) {
	s := &server{logger: testLogger()}
	req := httptest.NewRequest(http.MethodPost, "/mcp/atlassian/extra", nil)
	rec := httptest.NewRecorder()

	s.handleAtlassian(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAikidoHandlerProxiesReadOnlyRequestsWithServerSideAuth(t *testing.T) {
	var gotAuth string
	var gotCookie string
	var gotPath string
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	s := &server{
		cfg: config{
			aikidoAPIBaseURL:    upstream.URL + "/api/public/v1",
			aikidoAuthorization: "Bearer server-secret",
		},
		httpClient: upstream.Client(),
		logger:     testLogger(),
		ready:      true,
	}
	req := httptest.NewRequest(http.MethodGet, "/aikido/open-issue-groups?filter_code_repo_name=payments-api&page=0", nil)
	req.Header.Set("Authorization", "Bearer client-secret")
	req.Header.Set("Cookie", "session=client")
	rec := httptest.NewRecorder()

	s.handleAikido(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotAuth != "Bearer server-secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotCookie != "" {
		t.Fatalf("Cookie forwarded = %q", gotCookie)
	}
	if gotPath != "/api/public/v1/open-issue-groups" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "filter_code_repo_name=payments-api&page=0" {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestAikidoHandlerRejectsWriteMethods(t *testing.T) {
	s := &server{
		cfg: config{
			aikidoAPIBaseURL:    "https://app.aikido.dev/api/public/v1",
			aikidoAuthorization: "Bearer server-secret",
		},
		httpClient: http.DefaultClient,
		logger:     testLogger(),
	}
	req := httptest.NewRequest(http.MethodPost, "/aikido/open-issue-groups", nil)
	rec := httptest.NewRecorder()

	s.handleAikido(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
	}
}

func TestAikidoHandlerRequiresServerSideAuth(t *testing.T) {
	s := &server{
		cfg: config{
			aikidoAPIBaseURL: "https://app.aikido.dev/api/public/v1",
		},
		httpClient: http.DefaultClient,
		logger:     testLogger(),
	}
	req := httptest.NewRequest(http.MethodGet, "/aikido/open-issue-groups", nil)
	rec := httptest.NewRecorder()

	s.handleAikido(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAikidoHandlerBlocksRedirectsToUnexpectedHosts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
	}))
	defer upstream.Close()

	s := &server{
		cfg: config{
			aikidoAPIBaseURL:    upstream.URL + "/api/public/v1",
			aikidoAuthorization: "Bearer server-secret",
		},
		httpClient: upstream.Client(),
		logger:     testLogger(),
	}
	req := httptest.NewRequest(http.MethodGet, "/aikido/open-issue-groups", nil)
	rec := httptest.NewRecorder()

	s.handleAikido(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAikidoAuthorizationFromEnv(t *testing.T) {
	if got := aikidoAuthorizationFromEnv("Bearer exact", "ignored"); got != "Bearer exact" {
		t.Fatalf("exact authorization = %q", got)
	}
	if got := aikidoAuthorizationFromEnv("", "raw-token"); got != "Bearer raw-token" {
		t.Fatalf("raw token authorization = %q", got)
	}
	if got := aikidoAuthorizationFromEnv("", "Basic encoded"); got != "Basic encoded" {
		t.Fatalf("preformatted authorization = %q", got)
	}
}

func TestParseRPCRequestLogFields(t *testing.T) {
	fields := parseRPCRequestLogFields([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"createJiraIssue","arguments":{}}}`))
	if fields.Method != "tools/call" {
		t.Fatalf("Method = %q", fields.Method)
	}
	if fields.Tool != "createJiraIssue" {
		t.Fatalf("Tool = %q", fields.Tool)
	}
}

func TestAtlassianHosts(t *testing.T) {
	hosts := atlassianHosts(map[string]any{
		"resources": []any{
			map[string]any{"url": "https://wgen4.atlassian.net"},
			map[string]any{"siteUrl": "alpheya.atlassian.net"},
		},
	})
	if _, ok := hosts["wgen4.atlassian.net"]; !ok {
		t.Fatalf("missing wgen4 host: %#v", hosts)
	}
	if _, ok := hosts["alpheya.atlassian.net"]; !ok {
		t.Fatalf("missing alpheya host: %#v", hosts)
	}
}

func TestRequiredToolsPresent(t *testing.T) {
	if !requiredToolsPresent([]string{"createJiraIssue", "getAccessibleAtlassianResources", "searchJiraIssuesUsingJql"}) {
		t.Fatal("expected required tools to be present")
	}
	if requiredToolsPresent([]string{"getTeamworkGraphContext", "getTeamworkGraphObject"}) {
		t.Fatal("expected required tools to be missing")
	}
}

func TestValidateAtlassianAccessFailsForMultipleSites(t *testing.T) {
	upstream := mockMCPServer(t, []any{
		map[string]any{"url": "https://wgen4.atlassian.net"},
		map[string]any{"url": "https://alpheya.atlassian.net"},
	})
	defer upstream.Close()

	err := validateAtlassianAccess(context.Background(), upstream.Client(), config{
		upstreamURL:   upstream.URL,
		authorization: "Basic token",
		expectedSite:  "https://wgen4.atlassian.net",
	}, testLogger())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "expected only wgen4.atlassian.net") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAtlassianAccessSucceedsForWgen4Only(t *testing.T) {
	upstream := mockMCPServer(t, []any{
		map[string]any{"url": "https://wgen4.atlassian.net"},
	})
	defer upstream.Close()

	err := validateAtlassianAccess(context.Background(), upstream.Client(), config{
		upstreamURL:   upstream.URL,
		authorization: "Basic token",
		expectedSite:  "https://wgen4.atlassian.net",
	}, testLogger())
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
}

func mockMCPServer(t *testing.T, resources any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if method, _ := req["method"].(string); method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		id := req["id"]
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
				},
			})
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []any{
						map[string]any{"name": "getAccessibleAtlassianResources"},
					},
				},
			})
		case "tools/call":
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"content": []any{
						map[string]any{
							"type": "text",
							"text": mustJSON(t, resources),
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected method %q", method)
		}
	}))
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
