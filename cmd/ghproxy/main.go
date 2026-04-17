package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/singleflight"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/logging"
	"github.com/kelos-dev/kelos/internal/source"
)

const (
	// defaultUpstream is used when no upstream is explicitly configured.
	defaultUpstream = "https://api.github.com"

	// defaultCacheTTL is the default freshness window for cached GET responses.
	defaultCacheTTL = 15 * time.Second
)

// cacheEntry stores a cached response body together with the ETag returned
// by the upstream so it can be served directly while fresh and revalidated
// later with conditional requests.
type cacheEntry struct {
	etag        string
	body        []byte
	contentType string
	link        string
	status      int
	freshUntil  time.Time
}

type proxy struct {
	mu              sync.RWMutex
	cache           map[string]*cacheEntry
	inflight        singleflight.Group
	upstream        *http.Client
	upstreamBaseURL string
	cacheTTL        time.Duration
	now             func() time.Time
	tokenResolver   func(ctx context.Context) string
}

func newProxy(upstreamBaseURL string, cacheTTL time.Duration, tokenResolver func(ctx context.Context) string) *proxy {
	if upstreamBaseURL == "" {
		upstreamBaseURL = defaultUpstream
	}
	if tokenResolver == nil {
		tokenResolver = func(context.Context) string { return "" }
	}
	return &proxy{
		cache: make(map[string]*cacheEntry),
		upstream: &http.Client{
			Timeout: 30 * time.Second,
		},
		upstreamBaseURL: strings.TrimSuffix(strings.TrimSpace(upstreamBaseURL), "/"),
		cacheTTL:        cacheTTL,
		now:             time.Now,
		tokenResolver:   tokenResolver,
	}
}

// cacheKey returns a key that includes the request path+query and Accept
// header so that the same path with different content types is cached
// separately. The upstream is fixed per proxy instance.
func cacheKey(pathAndQuery, accept string) string {
	return pathAndQuery + "|" + accept
}

// rewriteLinkHeader rewrites absolute URLs in a Link header, replacing the
// upstream base with the proxy's own base so clients follow pagination
// links back through the proxy.
func rewriteLinkHeader(header, upstreamBase, proxyBase string) string {
	return strings.ReplaceAll(header, upstreamBase, proxyBase)
}

func (p *proxy) nextFreshUntil() time.Time {
	if p.cacheTTL <= 0 {
		return time.Time{}
	}
	return p.now().Add(p.cacheTTL)
}

func (p *proxy) isFresh(entry *cacheEntry) bool {
	return entry != nil && !entry.freshUntil.IsZero() && p.now().Before(entry.freshUntil)
}

func writeCachedResponse(w http.ResponseWriter, proxyBase, upstream string, entry *cacheEntry) {
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("ETag", entry.etag)
	if entry.link != "" {
		w.Header().Set("Link", rewriteLinkHeader(entry.link, upstream, proxyBase))
	}
	w.WriteHeader(entry.status)
	w.Write(entry.body)
}

type responsePayload struct {
	statusCode  int
	cacheResult string
	contentType string
	etag        string
	link        string
	body        []byte
	headers     map[string]string
}

func recordUpstreamRequest(method, statusCode, resource, reason string) {
	githubAPIUpstreamRequestsTotal.WithLabelValues(method, statusCode, resource, reason).Inc()
}

func (p *proxy) fetchResponse(log logr.Logger, upstream string, key string, r *http.Request) (*responsePayload, error) {
	if r.Method == http.MethodGet {
		p.mu.RLock()
		entry := p.cache[key]
		p.mu.RUnlock()
		if p.isFresh(entry) {
			return &responsePayload{
				statusCode:  entry.status,
				cacheResult: "fresh_hit",
				contentType: entry.contentType,
				etag:        entry.etag,
				link:        entry.link,
				body:        entry.body,
				headers:     map[string]string{},
			}, nil
		}

		// Coalesce concurrent GET requests for the same cache key into
		// a single upstream call. A detached context is used so that one
		// caller's cancellation does not abort the shared request.
		v, err, _ := p.inflight.Do(key, func() (interface{}, error) {
			return p.doGETUpstream(log, upstream, key, r.URL.RequestURI(), r.Header)
		})
		if err != nil {
			return nil, err
		}
		return v.(*responsePayload), nil
	}

	return p.doNonGET(upstream, r)
}

// doNonGET handles non-GET requests, forwarding the original request body
// and context directly to upstream without singleflight coalescing.
func (p *proxy) doNonGET(upstream string, r *http.Request) (*responsePayload, error) {
	resource := source.ClassifyResource(r.URL.Path)
	target, err := url.Parse(upstream + r.URL.RequestURI())
	if err != nil {
		return nil, fmt.Errorf("parsing upstream URL: %w", err)
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, fmt.Errorf("creating upstream request: %w", err)
	}

	for _, h := range []string{"Accept", "User-Agent"} {
		if v := r.Header.Get(h); v != "" {
			outReq.Header.Set(h, v)
		}
	}
	if token := p.tokenResolver(r.Context()); token != "" {
		outReq.Header.Set("Authorization", "token "+token)
	}

	resp, err := p.upstream.Do(outReq)
	if err != nil {
		recordUpstreamRequest(r.Method, "error", resource, "skip")
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()
	recordUpstreamRequest(r.Method, strconv.Itoa(resp.StatusCode), resource, "skip")

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading upstream response body: %w", err)
	}

	return &responsePayload{
		statusCode:  resp.StatusCode,
		cacheResult: "skip",
		contentType: resp.Header.Get("Content-Type"),
		etag:        resp.Header.Get("ETag"),
		link:        resp.Header.Get("Link"),
		body:        body,
		headers: map[string]string{
			"X-RateLimit-Limit":     resp.Header.Get("X-RateLimit-Limit"),
			"X-RateLimit-Remaining": resp.Header.Get("X-RateLimit-Remaining"),
			"X-RateLimit-Reset":     resp.Header.Get("X-RateLimit-Reset"),
		},
	}, nil
}

// doGETUpstream performs a GET request to upstream, coalescing concurrent
// requests via singleflight. Uses a detached context so that one caller's
// cancellation does not abort the shared request.
func (p *proxy) doGETUpstream(log logr.Logger, upstream, key, requestURI string, hdr http.Header) (*responsePayload, error) {
	p.mu.RLock()
	entry := p.cache[key]
	p.mu.RUnlock()
	resource := source.ClassifyResource(requestURI)
	reason := "miss"
	if entry != nil {
		reason = "revalidate"
	}

	target, err := url.Parse(upstream + requestURI)
	if err != nil {
		return nil, fmt.Errorf("parsing upstream URL: %w", err)
	}

	// Use a detached context with a timeout so that a single caller's
	// cancellation does not cancel the coalesced upstream request.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating upstream request: %w", err)
	}

	for _, h := range []string{"Accept", "User-Agent"} {
		if v := hdr.Get(h); v != "" {
			outReq.Header.Set(h, v)
		}
	}
	if token := p.tokenResolver(ctx); token != "" {
		outReq.Header.Set("Authorization", "token "+token)
	}
	if entry != nil {
		outReq.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := p.upstream.Do(outReq)
	if err != nil {
		recordUpstreamRequest(http.MethodGet, "error", resource, reason)
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()
	recordUpstreamRequest(http.MethodGet, strconv.Itoa(resp.StatusCode), resource, reason)

	if resp.StatusCode == http.StatusNotModified && entry != nil {
		refreshed := *entry
		refreshed.freshUntil = p.nextFreshUntil()
		p.mu.Lock()
		p.cache[key] = &refreshed
		p.mu.Unlock()
		return &responsePayload{
			statusCode:  entry.status,
			cacheResult: "revalidated_hit",
			contentType: refreshed.contentType,
			etag:        refreshed.etag,
			link:        refreshed.link,
			body:        refreshed.body,
			headers: map[string]string{
				"X-RateLimit-Limit":     resp.Header.Get("X-RateLimit-Limit"),
				"X-RateLimit-Remaining": resp.Header.Get("X-RateLimit-Remaining"),
				"X-RateLimit-Reset":     resp.Header.Get("X-RateLimit-Reset"),
			},
		}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading upstream response body: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if etag := resp.Header.Get("ETag"); etag != "" {
			p.mu.Lock()
			p.cache[key] = &cacheEntry{
				etag:        etag,
				body:        body,
				contentType: resp.Header.Get("Content-Type"),
				link:        resp.Header.Get("Link"),
				status:      resp.StatusCode,
				freshUntil:  p.nextFreshUntil(),
			}
			p.mu.Unlock()
		}
	}

	log.Info("Cache miss", "key", key, "status", resp.StatusCode, "resource", resource)
	return &responsePayload{
		statusCode:  resp.StatusCode,
		cacheResult: "miss",
		contentType: resp.Header.Get("Content-Type"),
		etag:        resp.Header.Get("ETag"),
		link:        resp.Header.Get("Link"),
		body:        body,
		headers: map[string]string{
			"X-RateLimit-Limit":     resp.Header.Get("X-RateLimit-Limit"),
			"X-RateLimit-Remaining": resp.Header.Get("X-RateLimit-Remaining"),
			"X-RateLimit-Reset":     resp.Header.Get("X-RateLimit-Reset"),
		},
	}, nil
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := ctrl.Log.WithName("ghproxy")
	resource := source.ClassifyResource(r.URL.Path)
	statusCode := http.StatusBadGateway
	cacheResult := "skip"
	defer func() {
		githubAPIRequestsTotal.WithLabelValues(r.Method, strconv.Itoa(statusCode), resource, cacheResult).Inc()
	}()

	upstream := p.upstreamBaseURL
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}
	proxyBase := scheme + "://" + r.Host
	key := cacheKey(r.URL.RequestURI(), r.Header.Get("Accept"))

	payload, err := p.fetchResponse(log, upstream, key, r)
	if err != nil {
		http.Error(w, "Upstream request failed", http.StatusBadGateway)
		log.Error(err, "Upstream request failed", "upstream", upstream, "path", r.URL.RequestURI())
		return
	}

	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if v := payload.headers[h]; v != "" {
			w.Header().Set(h, v)
		}
	}
	if payload.contentType != "" {
		w.Header().Set("Content-Type", payload.contentType)
	}
	if payload.etag != "" {
		w.Header().Set("ETag", payload.etag)
	}
	if payload.link != "" {
		w.Header().Set("Link", rewriteLinkHeader(payload.link, upstream, proxyBase))
	}

	statusCode = payload.statusCode
	cacheResult = payload.cacheResult
	w.WriteHeader(payload.statusCode)
	w.Write(payload.body)
}

// newTokenResolver returns a function that resolves a GitHub API token.
// It prefers a static PAT, then falls back to GitHub App credentials.
func newTokenResolver(token, appID, installID, privateKey, apiBaseURL string) func(context.Context) string {
	if token != "" {
		return func(context.Context) string { return token }
	}
	if appID == "" || installID == "" || privateKey == "" {
		return func(context.Context) string { return "" }
	}

	creds, err := githubapp.ParseCredentials(map[string][]byte{
		"appID":          []byte(appID),
		"installationID": []byte(installID),
		"privateKey":     []byte(privateKey),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse GitHub App credentials: %v\n", err)
		os.Exit(1)
	}

	tc := githubapp.NewTokenClient()
	if apiBaseURL != "" {
		tc.BaseURL = apiBaseURL
	}
	provider := githubapp.NewTokenProvider(tc, creds)

	return func(ctx context.Context) string {
		t, err := provider.Token(ctx)
		if err != nil {
			ctrl.Log.WithName("ghproxy").Error(err, "Resolving GitHub App token")
			return ""
		}
		return t
	}
}

func main() {
	var listenAddr string
	var metricsAddr string
	var upstreamBaseURL string
	var cacheTTL time.Duration
	var githubToken string
	var githubAppID string
	var githubAppInstallationID string
	var githubAppPrivateKey string
	flag.StringVar(&listenAddr, "listen-address", ":8888", "Address to listen on")
	flag.StringVar(&metricsAddr, "metrics-address", ":9090", "Address to serve Prometheus metrics on")
	flag.StringVar(&upstreamBaseURL, "upstream-base-url", defaultUpstream, "GitHub API base URL to proxy")
	flag.DurationVar(&cacheTTL, "cache-ttl", defaultCacheTTL, "Duration to serve cached GET responses without upstream revalidation")
	flag.StringVar(&githubToken, "github-token", "", "GitHub personal access token (env: GITHUB_TOKEN)")
	flag.StringVar(&githubAppID, "github-app-id", "", "GitHub App ID for installation token generation (env: GITHUB_APP_ID)")
	flag.StringVar(&githubAppInstallationID, "github-app-installation-id", "", "GitHub App installation ID (env: GITHUB_APP_INSTALLATION_ID)")
	flag.StringVar(&githubAppPrivateKey, "github-app-private-key", "", "GitHub App private key in PEM format (env: GITHUB_APP_PRIVATE_KEY)")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))
	log := ctrl.Log.WithName("ghproxy")

	// Fall back to environment variables for credentials not passed via flags.
	if githubToken == "" {
		githubToken = os.Getenv("GITHUB_TOKEN")
	}
	if githubAppID == "" {
		githubAppID = os.Getenv("GITHUB_APP_ID")
	}
	if githubAppInstallationID == "" {
		githubAppInstallationID = os.Getenv("GITHUB_APP_INSTALLATION_ID")
	}
	if githubAppPrivateKey == "" {
		githubAppPrivateKey = os.Getenv("GITHUB_APP_PRIVATE_KEY")
	}

	p := newProxy(upstreamBaseURL, cacheTTL, newTokenResolver(githubToken, githubAppID, githubAppInstallationID, githubAppPrivateKey, upstreamBaseURL))

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      p,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:    metricsAddr,
		Handler: metricsMux,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "Metrics server failed")
			os.Exit(1)
		}
	}()

	log.Info("Starting ghproxy", "address", listenAddr, "metricsAddress", metricsAddr, "upstreamBaseURL", upstreamBaseURL, "cacheTTL", cacheTTL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "Server failed")
		os.Exit(1)
	}
}
