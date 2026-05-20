package main

import (
	"bytes"
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

func TestAtlassianStartupDiagnosticsDoNotLogAuthorization(t *testing.T) {
	upstream := mockMCPServer(t, []any{
		map[string]any{"url": "https://wgen4.atlassian.net"},
	})
	defer upstream.Close()

	var logs bytes.Buffer
	err := validateAtlassianAccess(context.Background(), upstream.Client(), config{
		upstreamURL:   upstream.URL + "?private=query",
		authorization: "Basic token",
		expectedSite:  "https://wgen4.atlassian.net",
	}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	logText := logs.String()
	if strings.Contains(logText, "Basic token") || strings.Contains(logText, "private=query") {
		t.Fatalf("logs contain sensitive detail: %s", logText)
	}
	for _, want := range []string{
		`"auth_scheme":"Basic"`,
		`"session_id_received":true`,
		`"tool_count":1`,
		`"host_count":1`,
		`"wgen4.atlassian.net"`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs missing %s: %s", want, logText)
		}
	}
}

func TestAtlassianStartupDiagnosticsLogsSanitizedStringPayloadPreview(t *testing.T) {
	upstream := mockMCPServer(t, "Authorization: Basic very-secret-token for user.name@example.com failed; api_token=secret-value")
	defer upstream.Close()

	var logs bytes.Buffer
	err := validateAtlassianAccess(context.Background(), upstream.Client(), config{
		upstreamURL:   upstream.URL,
		authorization: "Basic token",
		expectedSite:  "https://wgen4.atlassian.net",
	}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err == nil {
		t.Fatal("expected validation error")
	}
	logText := logs.String()
	for _, sensitive := range []string{"very-secret-token", "user.name@example.com", "secret-value"} {
		if strings.Contains(logText, sensitive) {
			t.Fatalf("logs contain sensitive detail %q: %s", sensitive, logText)
		}
	}
	for _, want := range []string{
		`"payload_shape":"string"`,
		`"payload_preview":`,
		`Basic [REDACTED]`,
		`[EMAIL]`,
		`api_token=[REDACTED]`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs missing %s: %s", want, logText)
		}
	}
}

func TestPayloadPreviewTruncates(t *testing.T) {
	preview := payloadPreview(strings.Repeat("x", payloadPreviewLen+10))
	if len(preview) != payloadPreviewLen {
		t.Fatalf("preview len = %d, want %d", len(preview), payloadPreviewLen)
	}
	if !strings.HasSuffix(preview, "...") {
		t.Fatalf("preview should end with ellipsis: %q", preview)
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
