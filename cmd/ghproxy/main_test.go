package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kelos-dev/kelos/internal/source"
)

func TestProxy_ServesFreshCacheWithoutRevalidating(t *testing.T) {
	var calls atomic.Int32
	now := time.Unix(1700000000, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			t.Error("Did not expect upstream revalidation for a fresh cache entry")
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"repos":["a"]}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	p.now = func() time.Time { return now }
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() string {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	// First request hits upstream.
	body := doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected body: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls.Load())
	}

	now = now.Add(10 * time.Second)

	// Second request should be served directly from cache.
	body = doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected cached body: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected still 1 upstream call after fresh cache hit, got %d", calls.Load())
	}
}

func TestProxy_RevalidatesStaleGETWithETag(t *testing.T) {
	var calls atomic.Int32
	var bodyHits atomic.Int32
	now := time.Unix(1700000000, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		bodyHits.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"repos":["a"]}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Second, nil)
	p.now = func() time.Time { return now }
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() string {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	body := doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected body: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls.Load())
	}

	now = now.Add(2 * time.Second)

	body = doGET()
	if body != `{"repos":["a"]}` {
		t.Fatalf("unexpected cached body after revalidation: %s", body)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 upstream calls after stale revalidation, got %d", calls.Load())
	}
	if bodyHits.Load() != 1 {
		t.Fatalf("expected body to be fetched once, got %d", bodyHits.Load())
	}
}

func TestProxy_SeparatesCacheByUpstream(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hit":1}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, func(context.Context) string { return "" })
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, "https://ignored.example.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	doGET()
	doGET()
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit with fixed configured upstream, got %d", hits.Load())
	}
}

func TestProxy_PassesThroughNonGET(t *testing.T) {
	var method string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("POST", proxyServer.URL+"/repos/owner/repo/issues", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if method != "POST" {
		t.Fatalf("expected POST, got %s", method)
	}
}

func TestProxy_CacheKeyFormat(t *testing.T) {
	key := cacheKey("/repos/owner/repo", "")
	if key != "/repos/owner/repo|" {
		t.Fatalf("unexpected cache key: %s", key)
	}
}

func TestProxy_RewritesLinkHeader(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		// Simulate GitHub pagination with absolute upstream URLs.
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next", <%s/repos/owner/repo/issues?page=5>; rel="last"`, upstream.URL, upstream.URL))
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
	req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	link := resp.Header.Get("Link")
	wantLink := fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next", <%s/repos/owner/repo/issues?page=5>; rel="last"`, proxyServer.URL, proxyServer.URL)
	if link != wantLink {
		t.Errorf("Link header not rewritten:\n  got:  %s\n  want: %s", link, wantLink)
	}
}

func TestProxy_CachedResponsePreservesLinkHeader(t *testing.T) {
	var hits atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		hits.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next"`, upstream.URL))
		w.Write([]byte(`[{"number":1}]`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, 0, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() *http.Response {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// First request — populates cache.
	resp1 := doGET()
	resp1.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits.Load())
	}

	// Second request — served from cache via 304.
	resp2 := doGET()
	defer resp2.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("expected still 1 upstream hit, got %d", hits.Load())
	}

	link := resp2.Header.Get("Link")
	wantLink := fmt.Sprintf(`<%s/repos/owner/repo/issues?page=2>; rel="next"`, proxyServer.URL)
	if link != wantLink {
		t.Errorf("Cached Link header not rewritten:\n  got:  %s\n  want: %s", link, wantLink)
	}
}

func TestProxy_UsesConfiguredStaticToken(t *testing.T) {
	var authHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, func(context.Context) string { return "static-token" })
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if authHeader != "token static-token" {
		t.Fatalf("expected static token auth header, got %q", authHeader)
	}
}

func TestCacheKeyVariesByAccept(t *testing.T) {
	key1 := cacheKey("/repos/o/r/issues", "application/json")
	key2 := cacheKey("/repos/o/r/issues", "application/vnd.github.raw+json")

	if key1 == key2 {
		t.Fatal("expected cache key to vary by Accept header")
	}
	if key1 != cacheKey("/repos/o/r/issues", "application/json") {
		t.Fatal("expected cache key to be stable for identical inputs")
	}
}

func TestProxy_LogsCacheMiss(t *testing.T) {
	var buf bytes.Buffer
	ctrl.SetLogger(zap.New(zap.WriteTo(&buf), zap.UseDevMode(true)))

	now := time.Unix(1700000000, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	p.now = func() time.Time { return now }
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	doGET := func() {
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo/issues", nil)
		req.Header.Set(source.UpstreamBaseURLHeader, upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	// First request is a cache miss — should log.
	doGET()
	logOutput := buf.String()
	if !strings.Contains(logOutput, "Cache miss") {
		t.Errorf("expected cache miss log on first request, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "/repos/owner/repo/issues") {
		t.Errorf("expected log to contain request path, got: %s", logOutput)
	}

	// Second request within TTL is a fresh hit — no additional miss log.
	buf.Reset()
	now = now.Add(10 * time.Second)
	doGET()
	if strings.Contains(buf.String(), "Cache miss") {
		t.Error("unexpected cache miss log for fresh cache hit")
	}
}

func TestRewriteLinkHeader(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		upstream string
		proxy    string
		want     string
	}{
		{
			name:     "single next link",
			header:   `<https://api.github.com/repos/owner/repo/issues?page=2>; rel="next"`,
			upstream: "https://api.github.com",
			proxy:    "http://ghproxy:8888",
			want:     `<http://ghproxy:8888/repos/owner/repo/issues?page=2>; rel="next"`,
		},
		{
			name:     "next and last links",
			header:   `<https://api.github.com/repos/o/r?page=2>; rel="next", <https://api.github.com/repos/o/r?page=5>; rel="last"`,
			upstream: "https://api.github.com",
			proxy:    "http://ghproxy:8888",
			want:     `<http://ghproxy:8888/repos/o/r?page=2>; rel="next", <http://ghproxy:8888/repos/o/r?page=5>; rel="last"`,
		},
		{
			name:     "enterprise upstream",
			header:   `<https://github.example.com/api/v3/repos/o/r?page=2>; rel="next"`,
			upstream: "https://github.example.com/api/v3",
			proxy:    "http://ghproxy:8888",
			want:     `<http://ghproxy:8888/repos/o/r?page=2>; rel="next"`,
		},
		{
			name:     "no matching upstream — unchanged",
			header:   `<https://other.host/repos/o/r?page=2>; rel="next"`,
			upstream: "https://api.github.com",
			proxy:    "http://ghproxy:8888",
			want:     `<https://other.host/repos/o/r?page=2>; rel="next"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteLinkHeader(tt.header, tt.upstream, tt.proxy)
			if got != tt.want {
				t.Errorf("rewriteLinkHeader():\n  got:  %s\n  want: %s", got, tt.want)
			}
		})
	}
}

func TestProxy_CoalescesConcurrentGETRequests(t *testing.T) {
	var calls atomic.Int32
	// gate blocks the upstream handler so all concurrent requests queue up.
	gate := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-gate
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"repos":["a"]}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	const concurrency = 10
	var wg sync.WaitGroup
	bodies := make([]string, concurrency)
	statuses := make([]int, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("request %d failed: %v", idx, err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			bodies[idx] = string(body)
			statuses[idx] = resp.StatusCode
		}(i)
	}

	// Wait briefly for all requests to reach the proxy, then unblock upstream.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 upstream call (singleflight coalescing), got %d", got)
	}
	for i, body := range bodies {
		if body != `{"repos":["a"]}` {
			t.Errorf("request %d got unexpected body: %s", i, body)
		}
		if statuses[i] != http.StatusOK {
			t.Errorf("request %d got status %d, want 200", i, statuses[i])
		}
	}
}

func TestProxy_DoesNotCoalesceNonGET(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	for range 3 {
		req, _ := http.NewRequest("POST", proxyServer.URL+"/repos/owner/repo/issues", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
	}

	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 upstream calls for POST (no coalescing), got %d", got)
	}
}

func TestProxy_SingleflightSurvivesCallerCancellation(t *testing.T) {
	var calls atomic.Int32
	// gate blocks the upstream handler until we signal it.
	gate := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-gate
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := newProxy(upstream.URL, time.Minute, nil)
	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	var wg sync.WaitGroup

	// First request: will be cancelled before upstream responds.
	cancelCtx, cancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		req, _ := http.NewRequestWithContext(cancelCtx, "GET", proxyServer.URL+"/repos/owner/repo", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		// We expect this request to either error or succeed — either is fine.
	}()

	// Wait for the request to reach upstream, then cancel the first caller.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Second request: should still succeed because the upstream call uses
	// a detached context that is not affected by the first caller's cancel.
	var body string
	var status int
	wg.Add(1)
	go func() {
		defer wg.Done()
		req, _ := http.NewRequest("GET", proxyServer.URL+"/repos/owner/repo", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("second request failed: %v", err)
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		status = resp.StatusCode
	}()

	// Let upstream respond.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if body != `{"ok":true}` {
		t.Errorf("second request got unexpected body: %s", body)
	}
	if status != http.StatusOK {
		t.Errorf("second request got status %d, want 200", status)
	}
}
