package contextfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(kelos.AddToScheme(s))
	return s
}

func int32Ptr(v int32) *int32 { return &v }

func newFetcher(opts ...func(*Fetcher)) *Fetcher {
	f := &Fetcher{
		HTTPClient: http.DefaultClient,
		Namespace:  "default",
		Logger:     zap.New(zap.UseDevMode(true)),
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

func TestFetchAll_BasicGET(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "test",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL,
			AllowInsecure: true,
		},
	}}
	vars := map[string]interface{}{"Number": 42}

	result, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["test"]; got != `{"status":"ok"}` {
		t.Errorf("expected {\"status\":\"ok\"}, got %v", got)
	}
}

func TestFetchAll_POSTWithBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Write([]byte("created"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "post",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL,
			Method:        "POST",
			Body:          `{"id":{{.Number}}}`,
			AllowInsecure: true,
		},
	}}
	vars := map[string]interface{}{"Number": 42}

	result, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["post"]; got != "created" {
		t.Errorf("expected 'created', got %v", got)
	}
}

func TestFetchAll_URLTemplateRendering(t *testing.T) {
	var requestedPath string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "url",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL + "/items/{{.Number}}",
			AllowInsecure: true,
		},
	}}
	vars := map[string]interface{}{"Number": 99}

	_, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestedPath != "/items/99" {
		t.Errorf("expected path /items/99, got %s", requestedPath)
	}
}

func TestFetchAll_ResponseFilter(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"value": "extracted",
			},
		})
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "filtered",
		HTTP: &kelos.HTTPContextSource{
			URL: srv.URL,
			ResponseFilter: &kelos.ResponseFilter{
				Type:       kelos.ResponseFilterTypeJSONPath,
				Expression: "$.data.value",
			},
			AllowInsecure: true,
		},
	}}
	vars := map[string]interface{}{}

	result, err := f.FetchAll(context.Background(), sources, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["filtered"]; got != "extracted" {
		t.Errorf("expected 'extracted', got %v", got)
	}
}

func TestFetchAll_ResponseFilter_ComplexValue(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []interface{}{"a", "b"},
		})
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "arr",
		HTTP: &kelos.HTTPContextSource{
			URL: srv.URL,
			ResponseFilter: &kelos.ResponseFilter{
				Type:       kelos.ResponseFilterTypeJSONPath,
				Expression: "$.items",
			},
			AllowInsecure: true,
		},
	}}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["arr"]; got != `["a","b"]` {
		t.Errorf("expected [\"a\",\"b\"], got %v", got)
	}
}

func TestFetchAll_HeadersFromSecret(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("Bearer s3cret")},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(secret).Build()

	f := newFetcher(func(f *Fetcher) {
		f.HTTPClient = srv.Client()
		f.Client = cl
	})
	sources := []kelos.ContextSource{{
		Name: "auth",
		HTTP: &kelos.HTTPContextSource{
			URL: srv.URL,
			HeadersFrom: []kelos.HTTPHeaderSource{{
				Header:     "Authorization",
				SecretName: "my-secret",
				SecretKey:  "token",
			}},
			AllowInsecure: true,
		},
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("expected 'Bearer s3cret', got %q", gotAuth)
	}
}

func TestFetchAll_StaticHeaders(t *testing.T) {
	var gotAccept string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "hdrs",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL,
			Headers:       map[string]string{"Accept": "application/json"},
			AllowInsecure: true,
		},
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAccept != "application/json" {
		t.Errorf("expected application/json, got %q", gotAccept)
	}
}

func TestFetchAll_HTTPSRequired(t *testing.T) {
	f := newFetcher()
	sources := []kelos.ContextSource{{
		Name: "insecure",
		HTTP: &kelos.HTTPContextSource{
			URL: "http://example.com/data",
		},
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for HTTP URL without allowInsecure")
	}
	if !strings.Contains(err.Error(), "HTTP URLs are not allowed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_HTTPAllowInsecure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := newFetcher()
	sources := []kelos.ContextSource{{
		Name: "insecure",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL,
			AllowInsecure: true,
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyIgnore,
	}}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["insecure"]; got != "ok" {
		t.Errorf("expected 'ok', got %v", got)
	}
}

func TestFetchAll_FailurePolicyFail(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "req",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL,
			AllowInsecure: true,
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyFail,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for source with failurePolicy=Fail")
	}
	if !strings.Contains(err.Error(), "context source") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_FailurePolicyIgnore(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "opt",
		HTTP: &kelos.HTTPContextSource{
			URL:           srv.URL,
			AllowInsecure: true,
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyIgnore,
	}}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["opt"]; got != "" {
		t.Errorf("expected empty string for failed ignored source, got %v", got)
	}
}

func TestFetchAll_IgnoredSourceCancelledByFailing(t *testing.T) {
	// When a source with failurePolicy=Fail fails, errgroup cancels the context.
	// In-flight ignored sources should NOT log "Context source fetch failed" —
	// verify by capturing log output.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow enough that the failing source fails first.
		time.Sleep(2 * time.Second)
		w.Write([]byte("slow"))
	}))
	defer slowSrv.Close()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	var logBuf strings.Builder
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	f := newFetcher(func(f *Fetcher) { f.Logger = logger })
	sources := []kelos.ContextSource{
		{
			Name: "ignored-slow",
			HTTP: &kelos.HTTPContextSource{
				URL:            slowSrv.URL,
				AllowInsecure:  true,
				TimeoutSeconds: int32Ptr(5),
			},
			FailurePolicy: kelos.ContextSourceFailurePolicyIgnore,
		},
		{
			Name: "fail-fast",
			HTTP: &kelos.HTTPContextSource{
				URL:           failSrv.URL,
				AllowInsecure: true,
			},
			FailurePolicy: kelos.ContextSourceFailurePolicyFail,
		},
	}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("Expected error from source with failurePolicy=Fail")
	}
	if strings.Contains(logBuf.String(), "Context source fetch failed") {
		t.Error("Ignored source logged misleading 'fetch failed' when it was actually cancelled by failing source")
	}
}

func TestFetchAll_Timeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Write([]byte("late"))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "slow",
		HTTP: &kelos.HTTPContextSource{
			URL:            srv.URL,
			TimeoutSeconds: int32Ptr(1),
			AllowInsecure:  true,
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyFail,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestFetchAll_ResponseSizeLimit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write more than 64 bytes
		w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	f := newFetcher(func(f *Fetcher) { f.HTTPClient = srv.Client() })
	sources := []kelos.ContextSource{{
		Name: "big",
		HTTP: &kelos.HTTPContextSource{
			URL:              srv.URL,
			MaxResponseBytes: int32Ptr(64),
			AllowInsecure:    true,
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyFail,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "maxResponseBytes") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_MultipleSources(t *testing.T) {
	plainSrv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data1"))
	}))
	defer plainSrv1.Close()

	plainSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data2"))
	}))
	defer plainSrv2.Close()

	f := newFetcher()
	sources := []kelos.ContextSource{
		{
			Name: "src1",
			HTTP: &kelos.HTTPContextSource{
				URL:           plainSrv1.URL,
				AllowInsecure: true,
			},
		},
		{
			Name: "src2",
			HTTP: &kelos.HTTPContextSource{
				URL:           plainSrv2.URL,
				AllowInsecure: true,
			},
		},
	}

	result, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result["src1"]; got != "data1" {
		t.Errorf("src1: expected 'data1', got %v", got)
	}
	if got := result["src2"]; got != "data2" {
		t.Errorf("src2: expected 'data2', got %v", got)
	}
}

func TestFetchAll_SecretNotFound(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()

	f := newFetcher(func(f *Fetcher) { f.Client = cl })
	sources := []kelos.ContextSource{{
		Name: "missing",
		HTTP: &kelos.HTTPContextSource{
			URL: "https://example.com",
			HeadersFrom: []kelos.HTTPHeaderSource{{
				Header:     "Authorization",
				SecretName: "nonexistent",
				SecretKey:  "token",
			}},
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyFail,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing Secret")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_SecretKeyNotFound(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"other-key": []byte("val")},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(secret).Build()

	f := newFetcher(func(f *Fetcher) { f.Client = cl })
	sources := []kelos.ContextSource{{
		Name: "badkey",
		HTTP: &kelos.HTTPContextSource{
			URL: "https://example.com",
			HeadersFrom: []kelos.HTTPHeaderSource{{
				Header:     "Authorization",
				SecretName: "my-secret",
				SecretKey:  "token",
			}},
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyFail,
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing key in Secret")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchAll_MissingTemplateVariable(t *testing.T) {
	f := newFetcher()
	sources := []kelos.ContextSource{{
		Name: "bad",
		HTTP: &kelos.HTTPContextSource{
			URL:           "https://api.example.com/items/{{.MissingVar}}",
			AllowInsecure: true,
		},
		FailurePolicy: kelos.ContextSourceFailurePolicyFail,
	}}
	vars := map[string]interface{}{"Number": 42}

	_, err := f.FetchAll(context.Background(), sources, vars)
	if err == nil {
		t.Fatal("Expected error for missing template variable")
	}
	if !strings.Contains(err.Error(), "MissingVar") {
		t.Errorf("Expected error to mention missing variable, got: %v", err)
	}
}

func TestFetchAll_NilHTTP(t *testing.T) {
	f := newFetcher()
	sources := []kelos.ContextSource{{
		Name: "nohttp",
	}}

	_, err := f.FetchAll(context.Background(), sources, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error when http is nil")
	}
	if !strings.Contains(err.Error(), "no source kind configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateURLScheme(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"https allowed", "https://example.com", false, false},
		{"http blocked", "http://example.com", false, true},
		{"http allowed with flag", "http://example.com", true, false},
		{"ftp blocked", "ftp://example.com", false, true},
		{"ftp blocked even with flag", "ftp://example.com", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURLScheme(tt.url, tt.allowInsecure)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateURLScheme(%q, %v) error = %v, wantErr %v", tt.url, tt.allowInsecure, err, tt.wantErr)
			}
		})
	}
}

func TestApplyJSONPathFilter(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		expr    string
		want    string
		wantErr bool
	}{
		{
			name: "string value",
			body: `{"a":"b"}`,
			expr: "$.a",
			want: "b",
		},
		{
			name: "numeric value",
			body: `{"a":42}`,
			expr: "$.a",
			want: "42",
		},
		{
			name: "nested object",
			body: `{"a":{"b":"c"}}`,
			expr: "$.a",
			want: `{"b":"c"}`,
		},
		{
			name:    "missing field",
			body:    `{"a":"b"}`,
			expr:    "$.missing",
			wantErr: true,
		},
		{
			name:    "invalid json",
			body:    `not json`,
			expr:    "$.a",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyJSONPathFilter([]byte(tt.body), tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
