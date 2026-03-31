package main

import (
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

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
	upstream        *http.Client
	upstreamBaseURL string
	cacheTTL        time.Duration
	now             func() time.Time
	tokenResolver   func() string
}

func newProxy(upstreamBaseURL string, cacheTTL time.Duration, tokenResolver func() string) *proxy {
	if upstreamBaseURL == "" {
		upstreamBaseURL = defaultUpstream
	}
	if tokenResolver == nil {
		tokenResolver = func() string { return "" }
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

func (p *proxy) fetchResponse(log logr.Logger, upstream string, key string, r *http.Request) (*responsePayload, error) {
	var entry *cacheEntry
	if r.Method == http.MethodGet {
		p.mu.RLock()
		entry = p.cache[key]
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
	}

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
	if token := p.tokenResolver(); token != "" {
		outReq.Header.Set("Authorization", "token "+token)
	}
	if r.Method == http.MethodGet && entry != nil {
		outReq.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := p.upstream.Do(outReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

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

	if r.Method == http.MethodGet && resp.StatusCode >= 200 && resp.StatusCode < 300 {
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

	cacheResult := "skip"
	if r.Method == http.MethodGet {
		cacheResult = "miss"
		log.Info("Cache miss", "key", key, "status", resp.StatusCode, "resource", source.ClassifyResource(r.URL.Path))
	}
	return &responsePayload{
		statusCode:  resp.StatusCode,
		cacheResult: cacheResult,
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

func newTokenResolver(staticToken, tokenFile string) func() string {
	var (
		mu          sync.Mutex
		cached      string
		cachedAt    time.Time
		refreshRate = 30 * time.Second
	)
	return func() string {
		if tokenFile == "" {
			return strings.TrimSpace(staticToken)
		}
		mu.Lock()
		defer mu.Unlock()
		if cached != "" && time.Since(cachedAt) < refreshRate {
			return cached
		}
		data, err := os.ReadFile(tokenFile)
		if err == nil {
			if token := strings.TrimSpace(string(data)); token != "" {
				cached = token
				cachedAt = time.Now()
				return token
			}
		}
		return strings.TrimSpace(staticToken)
	}
}

func main() {
	var listenAddr string
	var metricsAddr string
	var upstreamBaseURL string
	var githubTokenFile string
	var cacheTTL time.Duration
	flag.StringVar(&listenAddr, "listen-address", ":8888", "Address to listen on")
	flag.StringVar(&metricsAddr, "metrics-address", ":9090", "Address to serve Prometheus metrics on")
	flag.StringVar(&upstreamBaseURL, "upstream-base-url", defaultUpstream, "GitHub API base URL to proxy")
	flag.StringVar(&githubTokenFile, "github-token-file", "", "Path to file containing GitHub token")
	flag.DurationVar(&cacheTTL, "cache-ttl", defaultCacheTTL, "Duration to serve cached GET responses without upstream revalidation")

	opts, applyVerbosity := logging.SetupZapOptions(flag.CommandLine)
	flag.Parse()

	if err := applyVerbosity(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(opts)))
	log := ctrl.Log.WithName("ghproxy")

	p := newProxy(upstreamBaseURL, cacheTTL, newTokenResolver(os.Getenv("GITHUB_TOKEN"), githubTokenFile))

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
