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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	defaultAddr                  = ":8080"
	defaultUpstreamURL           = "https://mcp.atlassian.com/v1/mcp"
	defaultSiteURL               = "https://wgen4.atlassian.net"
	defaultAikidoAPIBaseURL      = "https://app.aikido.dev/api/public/v1"
	defaultAikidoTokenURL        = "https://app.aikido.dev/api/oauth/token"
	defaultHindsightAllowedTools = "recall,list_memories,get_memory,list_tags,get_bank"
	defaultContextTimeout        = 10 * time.Second

	envAddr                       = "CODY_TOOLS_ADDR"
	envUpstreamURL                = "CODY_TOOLS_ATLASSIAN_UPSTREAM_URL"
	envAuthorization              = "CODY_TOOLS_ATLASSIAN_AUTHORIZATION"
	envExpectedSite               = "CODY_TOOLS_ATLASSIAN_EXPECTED_SITE_URL"
	envAikidoAPIBaseURL           = "CODY_TOOLS_AIKIDO_API_BASE_URL"
	envAikidoTokenURL             = "CODY_TOOLS_AIKIDO_TOKEN_URL"
	envAikidoAuthorization        = "CODY_TOOLS_AIKIDO_AUTHORIZATION"
	envAikidoAPIKey               = "AIKIDO_API_KEY"
	envAikidoClientCredentials    = "CODY_TOOLS_AIKIDO_CLIENT_CREDENTIALS"
	envAikidoClientID             = "CODY_TOOLS_AIKIDO_CLIENT_ID"
	envAikidoClientSecret         = "CODY_TOOLS_AIKIDO_CLIENT_SECRET"
	envHindsightMCPURL            = "HINDSIGHT_MCP_URL"
	envHindsightAuthorization     = "HINDSIGHT_AUTHORIZATION"
	envHindsightAllowedTools      = "HINDSIGHT_ALLOWED_TOOLS"
	envContextTimeout             = "CODY_TOOLS_CONTEXT_TIMEOUT"
	envGitHubAppClientID          = "CODY_TOOLS_GITHUB_APP_CLIENT_ID"
	envGitHubAppInstallationID    = "CODY_TOOLS_GITHUB_APP_INSTALLATION_ID"
	envGitHubAppPrivateKey        = "CODY_TOOLS_GITHUB_APP_PRIVATE_KEY"
	aikidoRoute                   = "/aikido"
	atlassianRoute                = "/mcp/atlassian"
	contextRoute                  = "/mcp/context"
	githubRoute                   = "/github"
	githubTokenRoute              = githubRoute + "/app/installations/token"
	githubCredentialRoute         = githubRoute + "/credential"
	aikidoAuthorizationError      = "aikido credentials are not configured"
	contextDisabledError          = "cody context is not configured"
	githubDisabledError           = "github app credentials are not configured"
	aikidoDefaultTokenTTL         = 5 * time.Minute
	aikidoTokenExpirySkew         = 1 * time.Minute
	aikidoTokenRequestContentType = "application/x-www-form-urlencoded"
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
	addr                string
	upstreamURL         string
	authorization       string
	expectedSite        string
	aikidoAPIBaseURL    string
	aikidoTokenURL      string
	aikidoAuthorization string
	aikidoClientID      string
	aikidoClientSecret  string
	hindsightMCPURL     string
	hindsightAuth       string
	hindsightTools      map[string]struct{}
	contextTimeout      time.Duration
	githubAppClientID   string
	githubInstallation  string
	githubPrivateKey    string
}

type server struct {
	cfg               config
	httpClient        *http.Client
	aikidoOAuth       *aikidoOAuthClientCredentials
	githubTokenClient *githubapp.TokenClient
	githubCredentials *githubapp.Credentials
	logger            *slog.Logger
	ready             bool
}

type rpcRequestLogFields struct {
	Method string
	Tool   string
}

type mcpRequestInfo struct {
	ID              any
	Method          string
	Tool            string
	HasBankOverride bool
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(2)
	}
	githubCredentials, err := githubCredentialsFromConfig(cfg)
	if err != nil {
		logger.Error("github config invalid", "error", err)
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
		githubTokenClient: githubapp.NewTokenClient(),
		githubCredentials: githubCredentials,
		logger:            logger,
		ready:             true,
	}
	s.aikidoOAuth = newAikidoOAuthClientCredentials(cfg, s.httpClient)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc(atlassianRoute, s.handleAtlassian)
	mux.HandleFunc(atlassianRoute+"/", s.handleAtlassian)
	mux.HandleFunc(aikidoRoute, s.handleAikido)
	mux.HandleFunc(aikidoRoute+"/", s.handleAikido)
	mux.HandleFunc(contextRoute, s.handleContext)
	mux.HandleFunc(contextRoute+"/", s.handleContext)
	mux.HandleFunc(githubRoute, s.handleGitHub)
	mux.HandleFunc(githubRoute+"/", s.handleGitHub)

	logger.Info("cody-tools listening", "addr", cfg.addr, "routes", []string{atlassianRoute, aikidoRoute, contextRoute, githubRoute})
	if err := http.ListenAndServe(cfg.addr, mux); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		addr:                strings.TrimSpace(os.Getenv(envAddr)),
		upstreamURL:         strings.TrimSpace(os.Getenv(envUpstreamURL)),
		authorization:       strings.TrimSpace(os.Getenv(envAuthorization)),
		expectedSite:        strings.TrimSpace(os.Getenv(envExpectedSite)),
		aikidoAPIBaseURL:    strings.TrimSpace(os.Getenv(envAikidoAPIBaseURL)),
		aikidoTokenURL:      strings.TrimSpace(os.Getenv(envAikidoTokenURL)),
		aikidoAuthorization: aikidoAuthorizationFromEnv(os.Getenv(envAikidoAuthorization), os.Getenv(envAikidoAPIKey)),
		hindsightMCPURL:     strings.TrimSpace(os.Getenv(envHindsightMCPURL)),
		hindsightAuth:       strings.TrimSpace(os.Getenv(envHindsightAuthorization)),
		contextTimeout:      defaultContextTimeout,
		githubAppClientID:   strings.TrimSpace(os.Getenv(envGitHubAppClientID)),
		githubInstallation:  strings.TrimSpace(os.Getenv(envGitHubAppInstallationID)),
		githubPrivateKey:    strings.TrimSpace(os.Getenv(envGitHubAppPrivateKey)),
	}
	clientID, clientSecret, err := aikidoClientCredentialsFromEnv(
		os.Getenv(envAikidoClientCredentials),
		os.Getenv(envAikidoClientID),
		os.Getenv(envAikidoClientSecret),
	)
	if err != nil {
		return config{}, err
	}
	tools, err := parseAllowedTools(os.Getenv(envHindsightAllowedTools))
	if err != nil {
		return config{}, err
	}
	cfg.hindsightTools = tools
	if rawTimeout := strings.TrimSpace(os.Getenv(envContextTimeout)); rawTimeout != "" {
		timeout, err := time.ParseDuration(rawTimeout)
		if err != nil {
			return config{}, fmt.Errorf("%s is invalid: %w", envContextTimeout, err)
		}
		if timeout <= 0 {
			return config{}, fmt.Errorf("%s must be greater than zero", envContextTimeout)
		}
		cfg.contextTimeout = timeout
	}
	cfg.aikidoClientID = clientID
	cfg.aikidoClientSecret = clientSecret

	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.upstreamURL == "" {
		cfg.upstreamURL = defaultUpstreamURL
	}
	if cfg.expectedSite == "" {
		cfg.expectedSite = defaultSiteURL
	}
	if cfg.aikidoAPIBaseURL == "" {
		cfg.aikidoAPIBaseURL = defaultAikidoAPIBaseURL
	}
	if cfg.aikidoTokenURL == "" {
		cfg.aikidoTokenURL = defaultAikidoTokenURL
	}
	if cfg.authorization == "" {
		return config{}, fmt.Errorf("%s is required", envAuthorization)
	}
	if err := validateURL(envUpstreamURL, cfg.upstreamURL); err != nil {
		return config{}, err
	}
	if err := validateURL(envExpectedSite, cfg.expectedSite); err != nil {
		return config{}, err
	}
	if err := validateURL(envAikidoAPIBaseURL, cfg.aikidoAPIBaseURL); err != nil {
		return config{}, err
	}
	if err := validateURL(envAikidoTokenURL, cfg.aikidoTokenURL); err != nil {
		return config{}, err
	}
	if cfg.hindsightMCPURL != "" {
		if cfg.hindsightAuth == "" {
			return config{}, fmt.Errorf("%s is required when %s is set", envHindsightAuthorization, envHindsightMCPURL)
		}
		if err := validateURL(envHindsightMCPURL, cfg.hindsightMCPURL); err != nil {
			return config{}, err
		}
	}
	if err := validateGitHubAppConfig(cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func validateGitHubAppConfig(cfg config) error {
	values := map[string]string{
		envGitHubAppClientID:       cfg.githubAppClientID,
		envGitHubAppInstallationID: cfg.githubInstallation,
		envGitHubAppPrivateKey:     cfg.githubPrivateKey,
	}
	present := 0
	for _, value := range values {
		if value != "" {
			present++
		}
	}
	if present == 0 || present == len(values) {
		return nil
	}
	var missing []string
	for name, value := range values {
		if value == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return fmt.Errorf("github app config is incomplete; missing %s", strings.Join(missing, ", "))
}

func githubCredentialsFromConfig(cfg config) (*githubapp.Credentials, error) {
	if cfg.githubAppClientID == "" && cfg.githubInstallation == "" && cfg.githubPrivateKey == "" {
		return nil, nil
	}
	return githubapp.ParseCredentialValues(cfg.githubAppClientID, cfg.githubInstallation, cfg.githubPrivateKey)
}

func validateURL(name, raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must include scheme and host", name)
	}
	return nil
}

func aikidoAuthorizationFromEnv(rawAuthorization, rawAPIKey string) string {
	if authorization := strings.TrimSpace(rawAuthorization); authorization != "" {
		return authorization
	}
	apiKey := strings.TrimSpace(rawAPIKey)
	if apiKey == "" {
		return ""
	}
	fields := strings.Fields(apiKey)
	if len(fields) > 1 && isAuthorizationScheme(fields[0]) {
		return apiKey
	}
	return "Bearer " + apiKey
}

func aikidoClientCredentialsFromEnv(rawCredentials, rawClientID, rawClientSecret string) (string, string, error) {
	credentials := strings.TrimSpace(rawCredentials)
	clientID := strings.TrimSpace(rawClientID)
	clientSecret := strings.TrimSpace(rawClientSecret)

	if credentials != "" {
		if clientID != "" || clientSecret != "" {
			return "", "", fmt.Errorf("%s cannot be combined with %s or %s", envAikidoClientCredentials, envAikidoClientID, envAikidoClientSecret)
		}
		parts := strings.SplitN(credentials, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return "", "", fmt.Errorf("%s must be formatted as <client_id>:<client_secret>", envAikidoClientCredentials)
		}
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}
	if clientID == "" && clientSecret == "" {
		return "", "", nil
	}
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("%s and %s must be set together", envAikidoClientID, envAikidoClientSecret)
	}
	return clientID, clientSecret, nil
}

func isAuthorizationScheme(value string) bool {
	switch strings.ToLower(value) {
	case "basic", "bearer":
		return true
	default:
		return false
	}
}

func parseAllowedTools(raw string) (map[string]struct{}, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultHindsightAllowedTools
	}
	allowed := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		tool := strings.TrimSpace(item)
		if tool == "" {
			continue
		}
		allowed[tool] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%s must include at least one tool", envHindsightAllowedTools)
	}
	return allowed, nil
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
	if r.URL.Path != atlassianRoute && r.URL.Path != atlassianRoute+"/" {
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
		"route", atlassianRoute,
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

func (s *server) handleAikido(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != aikidoRoute && !strings.HasPrefix(r.URL.Path, aikidoRoute+"/") {
		http.NotFound(w, r)
		return
	}

	start := time.Now()
	status, err := s.forwardAikido(w, r)
	duration := time.Since(start)
	logArgs := []any{
		"adapter", "aikido",
		"route", aikidoRoute,
		"path", strings.TrimPrefix(r.URL.Path, aikidoRoute),
		"http_method", r.Method,
		"status", status,
		"duration_ms", duration.Milliseconds(),
	}
	if err != nil {
		logArgs = append(logArgs, "error", err)
		s.logger.Error("api_proxy_request_failed", logArgs...)
		return
	}
	s.logger.Info("api_proxy_request", logArgs...)
}

func (s *server) handleContext(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != contextRoute && r.URL.Path != contextRoute+"/" {
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

	status, err := s.forwardContext(w, r, body)
	duration := time.Since(start)
	logArgs := []any{
		"adapter", "context",
		"route", contextRoute,
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

func (s *server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status, purpose, err := s.forwardGitHub(w, r)
	duration := time.Since(start)
	logArgs := []any{
		"adapter", "github",
		"route", githubRoute,
		"path", strings.TrimPrefix(r.URL.Path, githubRoute),
		"purpose", purpose,
		"http_method", r.Method,
		"status", status,
		"duration_ms", duration.Milliseconds(),
	}
	if err != nil {
		logArgs = append(logArgs, "error", err)
		s.logger.Error("github_request_failed", logArgs...)
		return
	}
	s.logger.Info("github_request", logArgs...)
}

type githubTokenRequest struct {
	Purpose       string          `json:"purpose"`
	Repositories  json.RawMessage `json:"repositories,omitempty"`
	RepositoryIDs json.RawMessage `json:"repository_ids,omitempty"`
	Permissions   json.RawMessage `json:"permissions,omitempty"`
}

type githubTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Source    string    `json:"source"`
}

type githubCredentialRequest struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Path     string `json:"path"`
}

type githubCredentialResponse struct {
	Username  string     `json:"username,omitempty"`
	Password  string     `json:"password,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *server) forwardGitHub(w http.ResponseWriter, r *http.Request) (int, string, error) {
	if r.URL.Path != githubTokenRoute && r.URL.Path != githubCredentialRoute {
		http.NotFound(w, r)
		return http.StatusNotFound, "", fmt.Errorf("unknown github route %s", r.URL.Path)
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return http.StatusMethodNotAllowed, "", fmt.Errorf("method %s is not allowed", r.Method)
	}
	switch r.URL.Path {
	case githubTokenRoute:
		return s.handleGitHubToken(w, r)
	case githubCredentialRoute:
		return s.handleGitHubCredential(w, r)
	default:
		http.NotFound(w, r)
		return http.StatusNotFound, "", fmt.Errorf("unknown github route %s", r.URL.Path)
	}
}

func (s *server) handleGitHubToken(w http.ResponseWriter, r *http.Request) (int, string, error) {
	var request githubTokenRequest
	if err := decodeOptionalJSON(r, &request); err != nil {
		http.Error(w, "invalid github token request", http.StatusBadRequest)
		return http.StatusBadRequest, "", err
	}
	purpose := strings.TrimSpace(request.Purpose)
	if purpose == "" {
		purpose = "token"
	}
	if len(request.Repositories) > 0 || len(request.RepositoryIDs) > 0 || len(request.Permissions) > 0 {
		http.Error(w, "repository and permission downscoping is not supported", http.StatusBadRequest)
		return http.StatusBadRequest, purpose, errors.New("github token request included unsupported downscoping fields")
	}
	token, err := s.generateGitHubInstallationToken(r.Context())
	if err != nil {
		http.Error(w, githubDisabledError, http.StatusServiceUnavailable)
		return http.StatusServiceUnavailable, purpose, err
	}
	writeJSON(w, githubTokenResponse{
		Token:     token.Token,
		ExpiresAt: token.ExpiresAt,
		Source:    "github_app_installation",
	})
	return http.StatusOK, purpose, nil
}

func (s *server) handleGitHubCredential(w http.ResponseWriter, r *http.Request) (int, string, error) {
	var request githubCredentialRequest
	if err := decodeOptionalJSON(r, &request); err != nil {
		http.Error(w, "invalid github credential request", http.StatusBadRequest)
		return http.StatusBadRequest, "", err
	}
	if request.Protocol != "https" || !githubCredentialHostAllowed(request.Host) {
		writeJSON(w, githubCredentialResponse{})
		return http.StatusOK, "credential", nil
	}
	token, err := s.generateGitHubInstallationToken(r.Context())
	if err != nil {
		http.Error(w, githubDisabledError, http.StatusServiceUnavailable)
		return http.StatusServiceUnavailable, "credential", err
	}
	writeJSON(w, githubCredentialResponse{
		Username:  "x-access-token",
		Password:  token.Token,
		ExpiresAt: &token.ExpiresAt,
	})
	return http.StatusOK, "credential", nil
}

func (s *server) generateGitHubInstallationToken(ctx context.Context) (*githubapp.TokenResponse, error) {
	if s.githubCredentials == nil {
		return nil, errors.New(githubDisabledError)
	}
	client := s.githubTokenClient
	if client == nil {
		client = githubapp.NewTokenClient()
	}
	return client.GenerateInstallationToken(ctx, s.githubCredentials)
}

func githubCredentialHostAllowed(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "github.com", "api.github.com":
		return true
	default:
		return false
	}
}

func decodeOptionalJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	return json.Unmarshal(body, target)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
	}
}

func (s *server) forwardAikido(w http.ResponseWriter, inbound *http.Request) (int, error) {
	if inbound.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return http.StatusMethodNotAllowed, fmt.Errorf("method %s is not allowed", inbound.Method)
	}
	upstreamURL, expectedHost, err := buildAikidoUpstreamURL(s.cfg.aikidoAPIBaseURL, inbound.URL)
	if err != nil {
		http.Error(w, "invalid upstream", http.StatusInternalServerError)
		return http.StatusInternalServerError, err
	}

	authorization, err := s.aikidoAuthorization(inbound.Context())
	if err != nil {
		http.Error(w, aikidoAuthorizationError, http.StatusServiceUnavailable)
		return http.StatusServiceUnavailable, err
	}

	req, err := newAikidoUpstreamRequest(inbound, upstreamURL, authorization)
	if err != nil {
		http.Error(w, "build upstream request", http.StatusInternalServerError)
		return http.StatusInternalServerError, err
	}

	resp, err := s.doAikido(req, expectedHost, authorization)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && s.aikidoOAuth != nil {
		resp.Body.Close()
		s.aikidoOAuth.invalidate()
		authorization, err = s.aikidoAuthorization(inbound.Context())
		if err == nil {
			req, err = newAikidoUpstreamRequest(inbound, upstreamURL, authorization)
		}
		if err == nil {
			resp, err = s.doAikido(req, expectedHost, authorization)
		}
	}
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

func (s *server) forwardContext(w http.ResponseWriter, inbound *http.Request, body []byte) (int, error) {
	if s.cfg.hindsightMCPURL == "" {
		http.Error(w, contextDisabledError, http.StatusServiceUnavailable)
		return http.StatusServiceUnavailable, errors.New(contextDisabledError)
	}
	if inbound.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return http.StatusMethodNotAllowed, fmt.Errorf("method %s is not allowed", inbound.Method)
	}

	info, err := parseMCPRequestInfo(body)
	if err != nil {
		http.Error(w, "invalid MCP request", http.StatusBadRequest)
		return http.StatusBadRequest, err
	}
	if info.Method == "tools/call" {
		if info.Tool == "" {
			writeMCPError(w, info.ID, -32602, "tools/call requires params.name")
			return http.StatusOK, nil
		}
		if info.HasBankOverride {
			writeMCPError(w, info.ID, -32602, "bank_id cannot be overridden in Cody context phase 1")
			return http.StatusOK, nil
		}
		if !s.contextToolAllowed(info.Tool) {
			writeMCPError(w, info.ID, -32601, fmt.Sprintf("tool %q is not available in Cody context phase 1", info.Tool))
			return http.StatusOK, nil
		}
	}

	reqCtx := inbound.Context()
	if s.cfg.contextTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(reqCtx, s.cfg.contextTimeout)
		defer cancel()
	}
	req, err := s.newHindsightRequest(reqCtx, inbound, body)
	if err != nil {
		http.Error(w, "build upstream request", http.StatusInternalServerError)
		return http.StatusInternalServerError, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	if info.Method == "tools/list" && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		filtered, err := filterMCPToolsList(resp.Body, s.contextToolAllowed)
		if err != nil {
			http.Error(w, "filter tools list", http.StatusBadGateway)
			return http.StatusBadGateway, err
		}
		copyResponseHeaders(w.Header(), resp.Header)
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if _, err := w.Write(filtered); err != nil {
			return resp.StatusCode, err
		}
		return resp.StatusCode, nil
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func (s *server) contextToolAllowed(name string) bool {
	_, ok := s.cfg.hindsightTools[name]
	return ok
}

func (s *server) newHindsightRequest(ctx context.Context, inbound *http.Request, body []byte) (*http.Request, error) {
	upstreamURL, err := url.Parse(s.cfg.hindsightMCPURL)
	if err != nil {
		return nil, err
	}
	if inbound.URL.RawQuery != "" {
		upstreamURL.RawQuery = inbound.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, inbound.Method, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyRequestHeaders(req.Header, inbound.Header)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	req.Header.Set("Authorization", s.cfg.hindsightAuth)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (s *server) aikidoAuthorization(ctx context.Context) (string, error) {
	if s.aikidoOAuth != nil {
		return s.aikidoOAuth.authorization(ctx)
	}
	if s.cfg.aikidoAuthorization != "" {
		return s.cfg.aikidoAuthorization, nil
	}
	return "", errors.New(aikidoAuthorizationError)
}

func newAikidoUpstreamRequest(inbound *http.Request, upstreamURL *url.URL, authorization string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(inbound.Context(), http.MethodGet, upstreamURL.String(), nil)
	if err != nil {
		return nil, err
	}
	copyRequestHeaders(req.Header, inbound.Header)
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	req.Header.Set("Authorization", authorization)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

func (s *server) doAikido(req *http.Request, expectedHost, authorization string) (*http.Response, error) {
	client := *s.httpClient
	client.CheckRedirect = func(redirectReq *http.Request, _ []*http.Request) error {
		if !sameHost(redirectReq.URL.Host, expectedHost) {
			return fmt.Errorf("blocked redirect to unexpected host %s", redirectReq.URL.Host)
		}
		redirectReq.Header.Del("Authorization")
		redirectReq.Header.Del("Cookie")
		redirectReq.Header.Set("Authorization", authorization)
		return nil
	}
	return client.Do(req)
}

type aikidoOAuthClientCredentials struct {
	client       *http.Client
	tokenURL     string
	clientID     string
	clientSecret string
	now          func() time.Time

	mu        sync.Mutex
	token     string
	tokenType string
	expiresAt time.Time
}

type aikidoOAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func newAikidoOAuthClientCredentials(cfg config, client *http.Client) *aikidoOAuthClientCredentials {
	if cfg.aikidoClientID == "" || cfg.aikidoClientSecret == "" {
		return nil
	}
	return &aikidoOAuthClientCredentials{
		client:       client,
		tokenURL:     cfg.aikidoTokenURL,
		clientID:     cfg.aikidoClientID,
		clientSecret: cfg.aikidoClientSecret,
		now:          time.Now,
	}
}

func (c *aikidoOAuthClientCredentials) authorization(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.token != "" && now.Before(c.expiresAt) {
		return c.authorizationHeader(), nil
	}
	if err := c.refreshLocked(ctx, now); err != nil {
		return "", err
	}
	return c.authorizationHeader(), nil
}

func (c *aikidoOAuthClientCredentials) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.expiresAt = time.Time{}
}

func (c *aikidoOAuthClientCredentials) authorizationHeader() string {
	tokenType := strings.TrimSpace(c.tokenType)
	if tokenType == "" || strings.EqualFold(tokenType, "bearer") {
		tokenType = "Bearer"
	}
	return tokenType + " " + c.token
}

func (c *aikidoOAuthClientCredentials) refreshLocked(ctx context.Context, now time.Time) error {
	body := url.Values{"grant_type": []string{"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", aikidoTokenRequestContentType)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("aikido token endpoint HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(limited)))
	}

	var tokenResponse aikidoOAuthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return err
	}
	if strings.TrimSpace(tokenResponse.AccessToken) == "" {
		return errors.New("aikido token endpoint returned no access_token")
	}

	c.token = strings.TrimSpace(tokenResponse.AccessToken)
	c.tokenType = strings.TrimSpace(tokenResponse.TokenType)
	if c.tokenType == "" {
		c.tokenType = "Bearer"
	}
	c.expiresAt = aikidoExpiresAt(now, tokenResponse.ExpiresIn)
	return nil
}

func aikidoExpiresAt(now time.Time, expiresInSeconds int64) time.Time {
	if expiresInSeconds <= 0 {
		return now.Add(aikidoDefaultTokenTTL)
	}
	ttl := time.Duration(expiresInSeconds) * time.Second
	skew := aikidoTokenExpirySkew
	if ttl <= 2*skew {
		skew = ttl / 4
	}
	return now.Add(ttl - skew)
}

func buildAikidoUpstreamURL(baseRaw string, inbound *url.URL) (*url.URL, string, error) {
	baseURL, err := url.Parse(baseRaw)
	if err != nil {
		return nil, "", err
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, "", fmt.Errorf("missing scheme or host in %q", baseRaw)
	}
	if inbound.Path != aikidoRoute && !strings.HasPrefix(inbound.Path, aikidoRoute+"/") {
		return nil, "", fmt.Errorf("path %q does not match %s route", inbound.Path, aikidoRoute)
	}

	upstreamURL := *baseURL
	suffix := strings.TrimPrefix(inbound.Path, aikidoRoute)
	upstreamURL.Path = joinURLPath(baseURL.Path, suffix)
	upstreamURL.RawQuery = inbound.RawQuery
	upstreamURL.Fragment = ""
	return &upstreamURL, strings.ToLower(baseURL.Host), nil
}

func joinURLPath(basePath, suffix string) string {
	basePath = strings.TrimRight(basePath, "/")
	suffix = strings.TrimLeft(suffix, "/")
	if suffix == "" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	if basePath == "" {
		return "/" + suffix
	}
	return basePath + "/" + suffix
}

func sameHost(actual, expected string) bool {
	return strings.EqualFold(actual, expected)
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

func parseMCPRequestInfo(body []byte) (mcpRequestInfo, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return mcpRequestInfo{}, err
	}
	if _, ok := payload.([]any); ok {
		return mcpRequestInfo{}, fmt.Errorf("JSON-RPC batch requests are not supported by %s", contextRoute)
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return mcpRequestInfo{}, errors.New("JSON-RPC request must be an object")
	}
	info := mcpRequestInfo{ID: obj["id"]}
	method, ok := obj["method"].(string)
	if !ok || method == "" {
		return mcpRequestInfo{}, errors.New("JSON-RPC method is required")
	}
	info.Method = method
	params, _ := obj["params"].(map[string]any)
	if method == "tools/call" && params != nil {
		info.Tool, _ = params["name"].(string)
		if _, ok := params["bank_id"]; ok {
			info.HasBankOverride = true
		}
		if arguments, _ := params["arguments"].(map[string]any); arguments != nil {
			if _, ok := arguments["bank_id"]; ok {
				info.HasBankOverride = true
			}
		}
	}
	return info, nil
}

func writeMCPError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func filterMCPToolsList(body io.Reader, allowed func(string) bool) ([]byte, error) {
	var message map[string]any
	if err := json.NewDecoder(body).Decode(&message); err != nil {
		return nil, err
	}
	result, _ := message["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	filtered := make([]any, 0, len(tools))
	for _, item := range tools {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := obj["name"].(string)
		if allowed(name) {
			filtered = append(filtered, item)
		}
	}
	if result != nil {
		result["tools"] = filtered
	}
	return json.Marshal(message)
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

	if err := mcp.initialized(ctx); err != nil {
		return err
	}

	tools, err := mcp.listTools(ctx)
	if err != nil {
		logger.Warn("atlassian tools list failed", "error", err)
	} else {
		names := toolNames(tools)
		logger.Info("atlassian tools listed", "tool_count", len(names), "required_tools_present", requiredToolsPresent(names))
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
	logger.Info("atlassian accessible resources received",
		"host_count", len(hosts),
		"hosts", sortedHosts(hosts),
	)
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

func requiredToolsPresent(names []string) bool {
	required := map[string]bool{
		"getAccessibleAtlassianResources": false,
		"createJiraIssue":                 false,
		"searchJiraIssuesUsingJql":        false,
	}
	for _, name := range names {
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for _, present := range required {
		if !present {
			return false
		}
	}
	return true
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
