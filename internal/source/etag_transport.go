package source

import (
	"bytes"
	"io"
	"net/http"
	"sync"

	"github.com/go-logr/logr"
)

type cachedEntry struct {
	etag   string
	body   []byte
	header http.Header
}

type etagTransport struct {
	base  http.RoundTripper
	log   logr.Logger
	mu    sync.Mutex
	cache map[string]*cachedEntry
}

// NewETagTransport wraps a base RoundTripper with transparent ETag caching.
// GET requests that return an ETag header are cached; subsequent requests for
// the same URL send If-None-Match and, on 304, return the cached body as a
// synthetic 200 response. Conditional requests returning 304 do not count
// against the GitHub API rate limit.
func NewETagTransport(base http.RoundTripper, log logr.Logger) http.RoundTripper {
	return &etagTransport{
		base:  base,
		log:   log.WithName("etag-cache"),
		cache: make(map[string]*cachedEntry),
	}
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}

	key := req.URL.String()

	t.mu.Lock()
	entry := t.cache[key]
	t.mu.Unlock()

	if entry != nil {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotModified && entry != nil {
		resp.Body.Close()
		t.log.V(1).Info("Cache hit", "url", key)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     entry.header.Clone(),
			Body:       io.NopCloser(bytes.NewReader(entry.body)),
			Request:    req,
		}, nil
	}

	etag := resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusOK && etag != "" {
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}

		t.mu.Lock()
		t.cache[key] = &cachedEntry{
			etag:   etag,
			body:   body,
			header: resp.Header.Clone(),
		}
		t.mu.Unlock()

		t.log.Info("Cached response", "url", key, "etag", etag)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}

	return resp, nil
}
