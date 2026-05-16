package contextfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/PaesslerAG/jsonpath"
	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	defaultTimeoutSeconds   = 10
	defaultMaxResponseBytes = 32768
	defaultMethod           = "GET"
)

// Fetcher fetches external context sources and returns their results as
// template variables.
type Fetcher struct {
	Client     client.Client
	HTTPClient *http.Client
	Namespace  string
	Logger     logr.Logger
}

// FetchAll fetches all context sources in parallel and returns a map
// suitable for injection as templateVars["Context"]. The templateVars
// parameter provides work item variables for rendering URL and header
// templates.
func (f *Fetcher) FetchAll(ctx context.Context, sources []v1alpha1.ContextSource, templateVars map[string]interface{}) (map[string]interface{}, error) {
	var mu sync.Mutex
	result := make(map[string]interface{}, len(sources))

	g, gctx := errgroup.WithContext(ctx)
	for _, src := range sources {
		g.Go(func() error {
			val, err := f.fetchOne(gctx, src, templateVars)
			if err != nil {
				if src.FailurePolicy != v1alpha1.ContextSourceFailurePolicyIgnore {
					return fmt.Errorf("context source %q: %w", src.Name, err)
				}
				if gctx.Err() != nil {
					// Context was cancelled (likely by a failing source);
					// skip logging to avoid misleading per-source error noise.
					mu.Lock()
					result[src.Name] = ""
					mu.Unlock()
					return nil
				}
				f.Logger.Error(err, "Context source fetch failed, using empty value", "source", src.Name)
				mu.Lock()
				result[src.Name] = ""
				mu.Unlock()
				return nil
			}
			mu.Lock()
			result[src.Name] = val
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

func (f *Fetcher) fetchOne(ctx context.Context, src v1alpha1.ContextSource, templateVars map[string]interface{}) (string, error) {
	if src.HTTP == nil {
		return "", fmt.Errorf("no source kind configured (http is required)")
	}
	httpSrc := src.HTTP

	// Render URL template
	renderedURL, err := renderTemplateStr("url", httpSrc.URL, templateVars)
	if err != nil {
		return "", fmt.Errorf("rendering URL template: %w", err)
	}

	// Validate URL scheme
	if err := validateURLScheme(renderedURL, httpSrc.AllowInsecure); err != nil {
		return "", err
	}

	// Resolve headers
	headers, err := f.resolveHeaders(ctx, httpSrc, templateVars)
	if err != nil {
		return "", fmt.Errorf("resolving headers: %w", err)
	}

	// Build request body for POST
	var bodyReader io.Reader
	if httpSrc.Body != "" {
		rendered, err := renderTemplateStr("body", httpSrc.Body, templateVars)
		if err != nil {
			return "", fmt.Errorf("rendering body template: %w", err)
		}
		bodyReader = strings.NewReader(rendered)
	}

	method := httpSrc.Method
	if method == "" {
		method = defaultMethod
	}

	timeoutSec := defaultTimeoutSeconds
	if httpSrc.TimeoutSeconds != nil {
		timeoutSec = int(*httpSrc.TimeoutSeconds)
	}

	maxBytes := int64(defaultMaxResponseBytes)
	if httpSrc.MaxResponseBytes != nil {
		maxBytes = int64(*httpSrc.MaxResponseBytes)
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, renderedURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("creating HTTP request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if method == "POST" && bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, renderedURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return "", fmt.Errorf("response body exceeds maxResponseBytes (%d)", maxBytes)
	}

	// Apply response filter if configured
	if httpSrc.ResponseFilter != nil {
		return applyJSONPathFilter(body, httpSrc.ResponseFilter.Expression)
	}

	return string(body), nil
}

func (f *Fetcher) resolveHeaders(ctx context.Context, httpSrc *v1alpha1.HTTPContextSource, templateVars map[string]interface{}) (map[string]string, error) {
	headers := make(map[string]string, len(httpSrc.Headers)+len(httpSrc.HeadersFrom))

	// Render static headers (support template variables)
	for k, v := range httpSrc.Headers {
		rendered, err := renderTemplateStr("header-"+k, v, templateVars)
		if err != nil {
			return nil, fmt.Errorf("rendering header %q: %w", k, err)
		}
		headers[k] = rendered
	}

	// Resolve headers from Secrets
	for _, hfs := range httpSrc.HeadersFrom {
		secret := &corev1.Secret{}
		key := client.ObjectKey{Name: hfs.SecretName, Namespace: f.Namespace}
		if err := f.Client.Get(ctx, key, secret); err != nil {
			return nil, fmt.Errorf("reading Secret %q for header %q: %w", hfs.SecretName, hfs.Header, err)
		}
		val, ok := secret.Data[hfs.SecretKey]
		if !ok {
			return nil, fmt.Errorf("key %q not found in Secret %q", hfs.SecretKey, hfs.SecretName)
		}
		headers[hfs.Header] = string(val)
	}

	return headers, nil
}

func validateURLScheme(rawURL string, allowInsecure bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure {
			return nil
		}
		return fmt.Errorf("HTTP URLs are not allowed without allowInsecure: %s", rawURL)
	default:
		return fmt.Errorf("unsupported URL scheme %q (only http/https are allowed)", parsed.Scheme)
	}
}

func applyJSONPathFilter(body []byte, expr string) (string, error) {
	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parsing JSON response for JSONPath filter: %w", err)
	}

	val, err := jsonpath.Get(expr, parsed)
	if err != nil {
		return "", fmt.Errorf("evaluating JSONPath %q: %w", expr, err)
	}

	// Marshal complex values back to JSON; scalars use fmt.
	switch v := val.(type) {
	case string:
		return v, nil
	case float64, bool, nil:
		return fmt.Sprintf("%v", v), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshaling JSONPath result: %w", err)
		}
		return string(b), nil
	}
}

func renderTemplateStr(name, tmplStr string, vars map[string]interface{}) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}
