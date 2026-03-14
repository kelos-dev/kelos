package source

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
)

func TestETagTransport_CachesOnSecondRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc123"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		hits.Add(1)
		w.Header().Set("ETag", `"abc123"`)
		w.Write([]byte(`{"data":"hello"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewETagTransport(http.DefaultTransport, logr.Discard())}

	// First request — should hit the server and cache.
	resp1, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if resp1.StatusCode != 200 {
		t.Fatalf("Expected 200 on first request, got %d", resp1.StatusCode)
	}
	if string(body1) != `{"data":"hello"}` {
		t.Fatalf("Unexpected body: %s", body1)
	}
	if hits.Load() != 1 {
		t.Fatalf("Expected 1 server hit, got %d", hits.Load())
	}

	// Second request — should get 304 from server, cached body from transport.
	resp2, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != 200 {
		t.Fatalf("Expected 200 on cached request, got %d", resp2.StatusCode)
	}
	if string(body2) != `{"data":"hello"}` {
		t.Fatalf("Unexpected cached body: %s", body2)
	}
	if hits.Load() != 1 {
		t.Fatalf("Expected server to still have 1 hit after cache, got %d", hits.Load())
	}
}

func TestETagTransport_SkipsNonGET(t *testing.T) {
	var hits atomic.Int32
	var gotIfNoneMatch bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") != "" {
			gotIfNoneMatch = true
		}
		w.Header().Set("ETag", `"xyz"`)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewETagTransport(http.DefaultTransport, logr.Discard())}

	// POST request — should not be cached.
	resp, err := client.Post(srv.URL+"/api", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Second POST — should hit the server again, not be served from cache.
	resp, err = client.Post(srv.URL+"/api", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotIfNoneMatch {
		t.Fatal("POST requests should not use ETag caching")
	}
	if hits.Load() != 2 {
		t.Fatalf("Expected 2 server hits for POST requests, got %d", hits.Load())
	}
}

func TestETagTransport_PassesThroughErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewETagTransport(http.DefaultTransport, logr.Discard())}

	resp, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Fatalf("Expected 403, got %d", resp.StatusCode)
	}
	if string(body) != "rate limited" {
		t.Fatalf("Unexpected body: %s", body)
	}

	// Second request should hit the server again (error responses are not cached).
	resp2, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != 403 {
		t.Fatalf("Expected 403 on second request, got %d", resp2.StatusCode)
	}
	if hits.Load() != 2 {
		t.Fatalf("Expected 2 server hits for error responses, got %d", hits.Load())
	}
}

func TestETagTransport_UpdatesCacheOnNewETag(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		switch n {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			w.Write([]byte(`{"version":1}`))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != `"v1"` {
				t.Errorf("Request 2: expected If-None-Match=\"v1\", got %q", got)
				return
			}
			// Data changed — return new body with new ETag.
			w.Header().Set("ETag", `"v2"`)
			w.Write([]byte(`{"version":2}`))
		case 3:
			if got := r.Header.Get("If-None-Match"); got != `"v2"` {
				t.Errorf("Request 3: expected If-None-Match=\"v2\", got %q", got)
				return
			}
			// Data unchanged — return 304.
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Errorf("Unexpected request %d with If-None-Match=%q", n, r.Header.Get("If-None-Match"))
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewETagTransport(http.DefaultTransport, logr.Discard())}
	url := srv.URL + "/repos/owner/repo/issues"

	// First request — cache v1.
	resp1, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if string(body1) != `{"version":1}` {
		t.Fatalf("Expected v1 body, got %s", body1)
	}

	// Second request — server returns 200 with new ETag, cache should update to v2.
	resp2, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != `{"version":2}` {
		t.Fatalf("Expected v2 body, got %s", body2)
	}

	// Third request — server returns 304, should get cached v2 body.
	resp3, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()

	if string(body3) != `{"version":2}` {
		t.Fatalf("Expected cached v2 body, got %s", body3)
	}
	if reqCount.Load() != 3 {
		t.Fatalf("Expected 3 server requests, got %d", reqCount.Load())
	}
}

func TestETagTransport_NoETagHeader(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("no etag"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewETagTransport(http.DefaultTransport, logr.Discard())}

	resp, err := client.Get(srv.URL + "/data")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "no etag" {
		t.Fatalf("Unexpected body: %s", body)
	}

	// Second request should hit server again since no ETag was returned.
	resp2, err := client.Get(srv.URL + "/data")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	if hits.Load() != 2 {
		t.Fatalf("Expected 2 server hits without ETag caching, got %d", hits.Load())
	}
}
