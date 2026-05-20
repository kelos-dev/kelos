package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultAddr        = ":8080"
	defaultUpstreamURL = "https://mcp.atlassian.com/v1/mcp"
	defaultSiteURL     = "https://wgen4.atlassian.net"
	payloadPreviewLen  = 300

	envAddr          = "CODY_TOOLS_ADDR"
	envUpstreamURL   = "CODY_TOOLS_ATLASSIAN_UPSTREAM_URL"
	envAuthorization = "CODY_TOOLS_ATLASSIAN_AUTHORIZATION"
	envExpectedSite  = "CODY_TOOLS_ATLASSIAN_EXPECTED_SITE_URL"
)

var (
	authHeaderPattern     = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)(basic|bearer)\s+[A-Za-z0-9._~+/=-]+`)
	genericAuthPattern    = regexp.MustCompile(`(?i)\b(basic|bearer)\s+[A-Za-z0-9._~+/=-]{8,}`)
	secretKeyValuePattern = regexp.MustCompile(`(?i)\b(api[_-]?token|access[_-]?token|refresh[_-]?token|password|secret|private[_-]?key)(["']?\s*[:=]\s*["']?)[^"',\s}]+`)
	emailPattern          = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	whitespacePattern     = regexp.MustCompile(`\s+`)
)

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"TE":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

type config struct {
	addr          string
	upstreamURL   string
	authorization string
	expectedSite  string
}

type server struct {
	cfg        config
	httpClient *http.Client
	logger     *slog.Logger
	ready      bool
}

type rpcRequestLogFields struct {
	Method string
	Tool   string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(2)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := validateAtlassianAccess(ctx, client, cfg, logger); err != nil {
		logger.Error("atlassian startup validation failed", "error", err)
		os.Exit(1)
	}

	s := &server{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 0,
		},
		logger: logger,
		ready:  true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/mcp/atlassian", s.handleAtlassian)
	mux.HandleFunc("/mcp/atlassian/", s.handleAtlassian)

	logger.Info("cody-tools listening", "addr", cfg.addr, "route", "/mcp/atlassian")
	if err := http.ListenAndServe(cfg.addr, mux); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		addr:          strings.TrimSpace(os.Getenv(envAddr)),
		upstreamURL:   strings.TrimSpace(os.Getenv(envUpstreamURL)),
		authorization: strings.TrimSpace(os.Getenv(envAuthorization)),
		expectedSite:  strings.TrimSpace(os.Getenv(envExpectedSite)),
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.upstreamURL == "" {
		cfg.upstreamURL = defaultUpstreamURL
	}
	if cfg.expectedSite == "" {
		cfg.expectedSite = defaultSiteURL
	}
	if cfg.authorization == "" {
		return config{}, fmt.Errorf("%s is required", envAuthorization)
	}
	if _, err := url.ParseRequestURI(cfg.upstreamURL); err != nil {
		return config{}, fmt.Errorf("%s is invalid: %w", envUpstreamURL, err)
	}
	if _, err := url.ParseRequestURI(cfg.expectedSite); err != nil {
		return config{}, fmt.Errorf("%s is invalid: %w", envExpectedSite, err)
	}
	return cfg, nil
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if !s.ready {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleAtlassian(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp/atlassian" && r.URL.Path != "/mcp/atlassian/" {
		http.NotFound(w, r)
		return
	}

	body, err := readBody(r)
	if err != nil {
		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}
	fields := parseRPCRequestLogFields(body)
	start := time.Now()

	status, err := s.forwardAtlassian(w, r, body)
	duration := time.Since(start)
	logArgs := []any{
		"adapter", "atlassian",
		"route", "/mcp/atlassian",
		"method", fields.Method,
		"tool", fields.Tool,
		"http_method", r.Method,
		"status", status,
		"duration_ms", duration.Milliseconds(),
	}
	if err != nil {
		logArgs = append(logArgs, "error", err)
		s.logger.Error("mcp_request_failed", logArgs...)
		return
	}
	s.logger.Info("mcp_request", logArgs...)
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func (s *server) forwardAtlassian(w http.ResponseWriter, inbound *http.Request, body []byte) (int, error) {
	upstreamURL, err := url.Parse(s.cfg.upstreamURL)
	if err != nil {
		http.Error(w, "invalid upstream", http.StatusInternalServerError)
		return http.StatusInternalServerError, err
	}
	if inbound.URL.RawQuery != "" {
		upstreamURL.RawQuery = inbound.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(inbound.Context(), inbound.Method, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build upstream request", http.StatusInternalServerError)
		return http.StatusInternalServerError, err
	}
	copyRequestHeaders(req.Header, inbound.Header)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	req.Header.Set("Authorization", s.cfg.authorization)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func copyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(key)]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(key)]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func parseRPCRequestLogFields(body []byte) rpcRequestLogFields {
	if len(bytes.TrimSpace(body)) == 0 {
		return rpcRequestLogFields{}
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return rpcRequestLogFields{}
	}
	if list, ok := payload.([]any); ok && len(list) > 0 {
		payload = list[0]
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return rpcRequestLogFields{}
	}
	fields := rpcRequestLogFields{}
	if method, ok := obj["method"].(string); ok {
		fields.Method = method
	}
	params, _ := obj["params"].(map[string]any)
	if fields.Method == "tools/call" && params != nil {
		if tool, ok := params["name"].(string); ok {
			fields.Tool = tool
		}
	}
	return fields
}

func validateAtlassianAccess(ctx context.Context, client *http.Client, cfg config, logger *slog.Logger) error {
	mcp := atlassianMCPClient{
		client:        client,
		upstreamURL:   cfg.upstreamURL,
		authorization: cfg.authorization,
	}

	logger.Info("atlassian startup validation started",
		"upstream_url", sanitizedURL(cfg.upstreamURL),
		"expected_site", cfg.expectedSite,
		"auth_scheme", authorizationScheme(cfg.authorization),
	)
	if err := mcp.initialize(ctx); err != nil {
		return err
	}
	logger.Info("atlassian initialize succeeded", "session_id_received", mcp.sessionID != "")

	if err := mcp.initialized(ctx); err != nil {
		return err
	}
	logger.Info("atlassian initialized notification sent")

	tools, err := mcp.listTools(ctx)
	if err != nil {
		logger.Warn("atlassian tools list failed", "error", err)
	} else {
		names := toolNames(tools)
		logger.Info("atlassian tools listed", "tool_count", len(names), "tools", names, "payload_shape", payloadShape(tools))
	}

	resources, err := mcp.callAccessibleResources(ctx)
	if err != nil {
		return err
	}
	expectedHost, err := normalizedHost(cfg.expectedSite)
	if err != nil {
		return err
	}
	hosts := atlassianHosts(resources)
	logArgs := []any{
		"payload_shape", payloadShape(resources),
		"host_count", len(hosts),
		"hosts", sortedHosts(hosts),
	}
	if preview := payloadPreview(resources); preview != "" {
		logArgs = append(logArgs, "payload_preview", preview)
	}
	logger.Info("atlassian accessible resources received", logArgs...)
	if len(hosts) == 0 {
		return errors.New("upstream returned no accessible Atlassian site URLs")
	}
	if len(hosts) != 1 {
		return fmt.Errorf("configured token can access %d Atlassian sites; expected only %s", len(hosts), expectedHost)
	}
	for host := range hosts {
		if host != expectedHost {
			return fmt.Errorf("configured token can access %s; expected only %s", host, expectedHost)
		}
	}
	logger.Info("atlassian startup validation passed", "site", expectedHost)
	return nil
}

func sanitizedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid-url"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func authorizationScheme(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return "unknown"
	}
	return fields[0]
}

func normalizedHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host in %q", raw)
	}
	return strings.ToLower(u.Host), nil
}

func atlassianHosts(value any) map[string]struct{} {
	hosts := make(map[string]struct{})
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			if host, ok := atlassianHostFromString(typed); ok {
				hosts[host] = struct{}{}
			}
		}
	}
	walk(value)
	return hosts
}

func sortedHosts(hosts map[string]struct{}) []string {
	out := make([]string, 0, len(hosts))
	for host := range hosts {
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

func toolNames(value any) []string {
	var tools []any
	switch typed := value.(type) {
	case map[string]any:
		tools, _ = typed["tools"].([]any)
	case []any:
		tools = typed
	}

	names := make([]string, 0, len(tools))
	for _, item := range tools {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := obj["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func payloadShape(value any) string {
	return valueShape(value, 0)
}

func payloadPreview(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(whitespacePattern.ReplaceAllString(text, " "))
	if text == "" {
		return ""
	}
	text = sanitizeDiagnosticText(text)
	return truncateRunes(text, payloadPreviewLen)
}

func sanitizeDiagnosticText(text string) string {
	text = authHeaderPattern.ReplaceAllString(text, `${1}${2} [REDACTED]`)
	text = genericAuthPattern.ReplaceAllString(text, `${1} [REDACTED]`)
	text = secretKeyValuePattern.ReplaceAllString(text, `${1}${2}[REDACTED]`)
	text = emailPattern.ReplaceAllString(text, `[EMAIL]`)
	return text
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func valueShape(value any, depth int) string {
	if depth >= 3 {
		return valueType(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 10 {
			keys = append(keys[:10], "...")
		}
		return fmt.Sprintf("object(keys=%v)", keys)
	case []any:
		if len(typed) == 0 {
			return "array(len=0)"
		}
		return fmt.Sprintf("array(len=%d, first=%s)", len(typed), valueShape(typed[0], depth+1))
	case nil:
		return "null"
	default:
		return valueType(value)
	}
}

func valueType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64, float32, int, int64, int32:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func atlassianHostFromString(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	candidates := []string{raw}
	if !strings.Contains(raw, "://") {
		candidates = append(candidates, "https://"+raw)
	}
	for _, candidate := range candidates {
		u, err := url.Parse(candidate)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.ToLower(u.Host)
		if strings.HasSuffix(host, ".atlassian.net") {
			return host, true
		}
	}
	return "", false
}

type atlassianMCPClient struct {
	client        *http.Client
	upstreamURL   string
	authorization string
	sessionID     string
	nextID        int
}

func (c *atlassianMCPClient) accessibleResources(ctx context.Context) (any, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	if err := c.initialized(ctx); err != nil {
		return nil, err
	}
	return c.callAccessibleResources(ctx)
}

func (c *atlassianMCPClient) listTools(ctx context.Context) (any, error) {
	return c.call(ctx, "tools/list", map[string]any{})
}

func (c *atlassianMCPClient) callAccessibleResources(ctx context.Context) (any, error) {
	result, err := c.call(ctx, "tools/call", map[string]any{
		"name":      "getAccessibleAtlassianResources",
		"arguments": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	return extractToolPayload(result)
}

func (c *atlassianMCPClient) initialize(ctx context.Context) error {
	result, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "cody-tools",
			"version": "0.1.0",
		},
	})
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("initialize returned no result")
	}
	return nil
}

func (c *atlassianMCPClient) initialized(ctx context.Context) error {
	_, err := c.post(ctx, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	return err
}

func (c *atlassianMCPClient) call(ctx context.Context, method string, params any) (any, error) {
	c.nextID++
	return c.post(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  method,
		"params":  params,
	})
}

func (c *atlassianMCPClient) post(ctx context.Context, payload map[string]any) (any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		c.sessionID = session
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream MCP HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(limited)))
	}

	message, err := decodeMCPResponse(resp)
	if err != nil {
		return nil, err
	}
	if rpcErr, ok := message["error"]; ok && rpcErr != nil {
		encoded, _ := json.Marshal(rpcErr)
		return nil, fmt.Errorf("upstream MCP error: %s", encoded)
	}
	return message["result"], nil
}

func decodeMCPResponse(resp *http.Response) (map[string]any, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return decodeSSEMessage(body)
	}
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, err
	}
	return message, nil
}

func decodeSSEMessage(body []byte) (map[string]any, error) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(data), &message); err != nil {
			return nil, err
		}
		return message, nil
	}
	return nil, errors.New("SSE response did not contain a JSON data event")
}

func extractToolPayload(result any) (any, error) {
	obj, ok := result.(map[string]any)
	if !ok {
		return nil, errors.New("tool result was not an object")
	}
	if structured, ok := obj["structuredContent"]; ok {
		return structured, nil
	}
	content, ok := obj["content"].([]any)
	if !ok || len(content) == 0 {
		return nil, errors.New("tool result contained no content")
	}
	for _, item := range content {
		contentObj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, ok := contentObj["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed, nil
		}
		return text, nil
	}
	return nil, errors.New("tool result content had no text payload")
}
