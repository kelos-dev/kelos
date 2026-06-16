package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

func TestTTLExpired(t *testing.T) {
	r := &TaskReconciler{}

	int32Ptr := func(v int32) *int32 { return &v }
	timePtr := func(t time.Time) *metav1.Time {
		mt := metav1.NewTime(t)
		return &mt
	}

	tests := []struct {
		name            string
		task            *kelos.Task
		wantExpired     bool
		wantRequeueMin  time.Duration
		wantRequeueMax  time.Duration
		wantZeroRequeue bool
	}{
		{
			name: "No TTL set",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: nil,
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "Not in terminal phase",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelos.TaskStatus{
					Phase: kelos.TaskPhaseRunning,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "CompletionTime not set",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseSucceeded,
					CompletionTime: nil,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "TTL=0 and completed",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(0),
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-1 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for succeeded task",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-20 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for failed task",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(5),
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseFailed,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL not yet expired",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now()),
				},
			},
			wantExpired:    false,
			wantRequeueMin: 50 * time.Second,
			wantRequeueMax: 61 * time.Second,
		},
		{
			name: "Pending phase with TTL",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: kelos.TaskStatus{
					Phase: kelos.TaskPhasePending,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, requeueAfter := r.ttlExpired(tt.task)
			if expired != tt.wantExpired {
				t.Errorf("ttlExpired() expired = %v, want %v", expired, tt.wantExpired)
			}
			if tt.wantZeroRequeue {
				if requeueAfter != 0 {
					t.Errorf("ttlExpired() requeueAfter = %v, want 0", requeueAfter)
				}
			} else {
				if requeueAfter < tt.wantRequeueMin || requeueAfter > tt.wantRequeueMax {
					t.Errorf("ttlExpired() requeueAfter = %v, want between %v and %v",
						requeueAfter, tt.wantRequeueMin, tt.wantRequeueMax)
				}
			}
		})
	}
}

func TestResolveGitHubAppToken_EnterpriseURL(t *testing.T) {
	// Generate a test RSA key for GitHub App credentials
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	tests := []struct {
		name        string
		repoURL     string
		enterprise  bool
		wantAPIPath string
	}{
		{
			name:        "github.com uses default API URL",
			repoURL:     "https://github.com/kelos-dev/kelos.git",
			wantAPIPath: "/app/installations/67890/access_tokens",
		},
		{
			name:        "enterprise host uses enterprise API URL",
			enterprise:  true,
			wantAPIPath: "/api/v3/app/installations/67890/access_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedPath string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedPath = r.URL.Path
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"token":      "ghs_test_token",
					"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
				})
			}))
			defer server.Close()

			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(kelos.AddToScheme(scheme))
			utilruntime.Must(kelos.AddToScheme(scheme))

			secretData := map[string][]byte{
				"appID":          []byte("12345"),
				"installationID": []byte("67890"),
				"privateKey":     keyPEM,
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "github-app-creds",
					Namespace: "default",
				},
				Data: secretData,
			}

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(secret).
				Build()

			tc := &githubapp.TokenClient{
				BaseURL: server.URL,
				Client:  server.Client(),
			}

			r := &TaskReconciler{
				Client:      cl,
				Scheme:      scheme,
				TokenClient: tc,
			}

			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task",
					Namespace: "default",
					UID:       "test-uid",
				},
			}

			repoURL := tt.repoURL
			if tt.enterprise {
				// Use a workspace repo URL with the TLS test server's host
				// so it is treated as a GitHub Enterprise host. Since
				// gitHubAPIBaseURL always produces https:// URLs, the TLS
				// server ensures the request succeeds.
				repoURL = server.URL + "/my-org/my-repo.git"
			}

			workspace := &kelos.WorkspaceSpec{
				Repo: repoURL,
				SecretRef: &kelos.SecretReference{
					Name: "github-app-creds",
				},
			}

			result, err := r.resolveGitHubAppToken(context.Background(), task, workspace)
			if err != nil {
				t.Fatalf("resolveGitHubAppToken() error: %v", err)
			}

			if result.SecretRef.Name != "test-task-github-token" {
				t.Errorf("secret name = %q, want %q", result.SecretRef.Name, "test-task-github-token")
			}

			if receivedPath != tt.wantAPIPath {
				t.Errorf("API path = %q, want %q", receivedPath, tt.wantAPIPath)
			}
		})
	}
}

func TestResolveGitHubAppToken_AnnotatesExpiryAndSource(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	expires := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_initial",
			"expires_at": expires.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app-creds",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     keyPEM,
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	r := &TaskReconciler{
		Client: cl,
		Scheme: scheme,
		TokenClient: &githubapp.TokenClient{
			BaseURL: server.URL,
			Client:  server.Client(),
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "test-task", Namespace: "default"},
	}
	workspace := &kelos.WorkspaceSpec{
		Repo:      "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelos.SecretReference{Name: "github-app-creds"},
	}

	if _, err := r.resolveGitHubAppToken(context.Background(), task, workspace); err != nil {
		t.Fatalf("resolveGitHubAppToken() error: %v", err)
	}

	var got corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "test-task-github-token"}, &got); err != nil {
		t.Fatalf("getting token secret: %v", err)
	}
	if src := got.Annotations[githubAppSecretAnnotation]; src != "github-app-creds" {
		t.Errorf("annotation %q = %q, want %q", githubAppSecretAnnotation, src, "github-app-creds")
	}
	gotExpiresStr := got.Annotations[tokenExpiresAtAnnotation]
	if gotExpiresStr == "" {
		t.Fatalf("missing %q annotation", tokenExpiresAtAnnotation)
	}
	gotExpires, err := time.Parse(time.RFC3339, gotExpiresStr)
	if err != nil {
		t.Fatalf("parsing %q annotation: %v", tokenExpiresAtAnnotation, err)
	}
	if !gotExpires.Equal(expires) {
		t.Errorf("annotation expiry = %v, want %v", gotExpires, expires)
	}
}

func TestRefreshGitHubAppTokenIfNeeded_NoTokenSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &TaskReconciler{Client: cl, Scheme: scheme}

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "test-task", Namespace: "default"},
	}
	next, err := r.refreshGitHubAppTokenIfNeeded(context.Background(), task)
	if err != nil {
		t.Fatalf("refreshGitHubAppTokenIfNeeded() error: %v", err)
	}
	if next != 0 {
		t.Errorf("next = %v, want 0 when there is no token secret", next)
	}
}

func TestRefreshGitHubAppTokenIfNeeded_SkipsWhenNotApp(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	// Token secret without the App annotation (PAT-derived).
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-github-token",
			Namespace: "default",
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("ghp_test")},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	r := &TaskReconciler{Client: cl, Scheme: scheme}

	task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "test-task", Namespace: "default"}}
	next, err := r.refreshGitHubAppTokenIfNeeded(context.Background(), task)
	if err != nil {
		t.Fatalf("refreshGitHubAppTokenIfNeeded() error: %v", err)
	}
	if next != 0 {
		t.Errorf("next = %v, want 0 when secret is not App-derived", next)
	}
}

func TestRefreshGitHubAppTokenIfNeeded_SkipsWhenStillFresh(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	expires := time.Now().Add(45 * time.Minute).UTC()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-github-token",
			Namespace: "default",
			Annotations: map[string]string{
				githubAppSecretAnnotation: "github-app-creds",
				tokenExpiresAtAnnotation:  expires.Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("ghs_existing")},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	r := &TaskReconciler{Client: cl, Scheme: scheme}

	task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "test-task", Namespace: "default"}}
	next, err := r.refreshGitHubAppTokenIfNeeded(context.Background(), task)
	if err != nil {
		t.Fatalf("refreshGitHubAppTokenIfNeeded() error: %v", err)
	}
	if next <= 0 {
		t.Errorf("next = %v, want >0 when token is still fresh", next)
	}

	// Secret data must not have been touched.
	var after corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "test-task-github-token"}, &after); err != nil {
		t.Fatalf("getting secret: %v", err)
	}
	if string(after.Data["GITHUB_TOKEN"]) != "ghs_existing" {
		t.Errorf("token mutated unexpectedly: got %q, want %q", after.Data["GITHUB_TOKEN"], "ghs_existing")
	}
}

func TestReconcile_RequeuesSoonAfterTokenRefreshFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	expires := time.Now().Add(20 * time.Second).UTC()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-task",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelos.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "test",
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Status: batchv1.JobStatus{Active: 1},
	}
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-github-token",
			Namespace: "default",
			Annotations: map[string]string{
				githubAppSecretAnnotation: "github-app-creds",
				tokenExpiresAtAnnotation:  expires.Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("ghs_old")},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task, job, tokenSecret).
		Build()

	r := &TaskReconciler{
		Client: cl,
		Scheme: scheme,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(task),
	})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %v, want positive refresh retry", result.RequeueAfter)
	}
	if result.RequeueAfter >= tokenRefreshRetryInterval {
		t.Errorf("RequeueAfter = %v, want less than retry interval %v for near-expiry token", result.RequeueAfter, tokenRefreshRetryInterval)
	}
	if result.RequeueAfter >= tokenRefreshMargin {
		t.Errorf("RequeueAfter = %v, want less than refresh margin %v", result.RequeueAfter, tokenRefreshMargin)
	}
	if result.RequeueAfter >= time.Until(expires) {
		t.Errorf("RequeueAfter = %v, want retry before token expiry %v", result.RequeueAfter, expires)
	}
}

func TestRefreshGitHubAppTokenIfNeeded_RemintsExpiringToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	newExpiry := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Second)
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_refreshed",
			"expires_at": newExpiry.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	// Token expires in 1 minute, which is inside the 5-minute refresh margin.
	expires := time.Now().Add(1 * time.Minute).UTC()
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-github-token",
			Namespace: "default",
			Annotations: map[string]string{
				githubAppSecretAnnotation: "github-app-creds",
				tokenExpiresAtAnnotation:  expires.Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{"GITHUB_TOKEN": []byte("ghs_old")},
	}
	appSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app-creds",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     keyPEM,
		},
	}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", Namespace: "default"},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelos.SecretReference{Name: "github-app-creds"},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tokenSecret, appSecret, workspace).
		Build()

	r := &TaskReconciler{
		Client: cl,
		Scheme: scheme,
		TokenClient: &githubapp.TokenClient{
			BaseURL: server.URL,
			Client:  server.Client(),
		},
	}

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "test-task", Namespace: "default"},
		Spec: kelos.TaskSpec{
			WorkspaceRef: &kelos.WorkspaceReference{Name: "ws"},
		},
	}

	next, err := r.refreshGitHubAppTokenIfNeeded(context.Background(), task)
	if err != nil {
		t.Fatalf("refreshGitHubAppTokenIfNeeded() error: %v", err)
	}
	if calls != 1 {
		t.Errorf("token endpoint calls = %d, want 1", calls)
	}
	if next <= 0 {
		t.Errorf("next = %v, want positive after refresh", next)
	}

	var after corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "test-task-github-token"}, &after); err != nil {
		t.Fatalf("getting refreshed secret: %v", err)
	}
	// Refresh writes via StringData (the apiserver merges into Data on
	// persistence). Check either field so the test does not depend on
	// fake-client merge behavior.
	gotToken := after.StringData["GITHUB_TOKEN"]
	if gotToken == "" {
		gotToken = string(after.Data["GITHUB_TOKEN"])
	}
	if gotToken != "ghs_refreshed" {
		t.Errorf("refreshed token = %q, want %q", gotToken, "ghs_refreshed")
	}
	gotExpiresStr := after.Annotations[tokenExpiresAtAnnotation]
	gotExpires, err := time.Parse(time.RFC3339, gotExpiresStr)
	if err != nil {
		t.Fatalf("parsing refreshed expiry: %v", err)
	}
	if !gotExpires.Equal(newExpiry) {
		t.Errorf("refreshed expiry = %v, want %v", gotExpires, newExpiry)
	}
}

func TestResolveGitHubAppToken_PATSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pat-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"GITHUB_TOKEN": []byte("ghp_test"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	r := &TaskReconciler{
		Client: cl,
		Scheme: scheme,
	}

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
	}
	workspace := &kelos.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelos.SecretReference{
			Name: "pat-secret",
		},
	}

	result, err := r.resolveGitHubAppToken(context.Background(), task, workspace)
	if err != nil {
		t.Fatalf("resolveGitHubAppToken() error: %v", err)
	}

	// PAT secrets should pass through unchanged
	if result.SecretRef.Name != "pat-secret" {
		t.Errorf("secret name = %q, want %q (should be unchanged for PAT)", result.SecretRef.Name, "pat-secret")
	}
}

func newReconcilerWithFakeClient(objs ...runtime.Object) *TaskReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()

	return &TaskReconciler{
		Client: cl,
		Scheme: scheme,
	}
}

func TestResolveMCPServerSecrets_HeadersFrom(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-headers",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"Authorization": []byte("Bearer secret-token"),
			"X-From-Secret": []byte("secret-value"),
		},
	}

	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			Headers: map[string]string{
				"X-Inline": "inline-value",
			},
			HeadersFrom: &kelos.SecretValuesSource{
				SecretRef: kelos.SecretReference{Name: "mcp-headers"},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}

	if got := resolved[0].Headers["Authorization"]; got != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer secret-token")
	}
	if got := resolved[0].Headers["X-From-Secret"]; got != "secret-value" {
		t.Errorf("X-From-Secret = %q, want %q", got, "secret-value")
	}
	if got := resolved[0].Headers["X-Inline"]; got != "inline-value" {
		t.Errorf("X-Inline = %q, want %q", got, "inline-value")
	}
	if resolved[0].HeadersFrom != nil {
		t.Fatal("HeadersFrom should be nil after resolution")
	}
}

func TestResolveMCPServerSecrets_EnvFrom(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-env",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"DB_PASSWORD": []byte("secret-pass"),
			"DB_HOST":     []byte("db.internal"),
		},
	}

	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name:    "local-db",
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "@bytebase/dbhub"},
			Env: []corev1.EnvVar{
				{Name: "DSN", Value: "postgres://localhost/db"},
			},
			EnvFrom: &kelos.SecretValuesSource{
				SecretRef: kelos.SecretReference{Name: "mcp-env"},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}

	got := envVarMap(resolved[0].Env)
	if got["DB_PASSWORD"] != "secret-pass" {
		t.Errorf("DB_PASSWORD = %q, want %q", got["DB_PASSWORD"], "secret-pass")
	}
	if got["DB_HOST"] != "db.internal" {
		t.Errorf("DB_HOST = %q, want %q", got["DB_HOST"], "db.internal")
	}
	if got["DSN"] != "postgres://localhost/db" {
		t.Errorf("DSN = %q, want %q", got["DSN"], "postgres://localhost/db")
	}
	if resolved[0].EnvFrom != nil {
		t.Fatal("EnvFrom should be nil after resolution")
	}
}

func TestResolveMCPServerSecrets_EnvValueFromSecretKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-secret", Namespace: "default"},
		Data: map[string][]byte{
			"db-password": []byte("hunter2"),
		},
	}

	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name:    "local-db",
			Type:    "stdio",
			Command: "dbhub",
			Env: []corev1.EnvVar{
				{Name: "DSN", Value: "postgres://localhost/db"},
				{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
						Key:                  "db-password",
					},
				}},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}

	got := envVarMap(resolved[0].Env)
	if got["DB_PASSWORD"] != "hunter2" {
		t.Errorf("DB_PASSWORD = %q, want %q", got["DB_PASSWORD"], "hunter2")
	}
	if got["DSN"] != "postgres://localhost/db" {
		t.Errorf("DSN = %q, want %q", got["DSN"], "postgres://localhost/db")
	}
	for _, e := range resolved[0].Env {
		if e.ValueFrom != nil {
			t.Errorf("ValueFrom for %q should be nil after resolution, got %+v", e.Name, e.ValueFrom)
		}
	}
}

func TestResolveMCPServerSecrets_EnvValueFromConfigMapKey(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-config", Namespace: "default"},
		Data: map[string]string{
			"host": "db.internal",
		},
	}

	r := newReconcilerWithFakeClient(cm)
	servers := []kelos.MCPServerSpec{
		{
			Name:    "local-db",
			Type:    "stdio",
			Command: "dbhub",
			Env: []corev1.EnvVar{
				{Name: "DB_HOST", ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-config"},
						Key:                  "host",
					},
				}},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}

	got := envVarMap(resolved[0].Env)
	if got["DB_HOST"] != "db.internal" {
		t.Errorf("DB_HOST = %q, want %q", got["DB_HOST"], "db.internal")
	}
}

func TestResolveMCPServerSecrets_EnvValueFromMissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-secret", Namespace: "default"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
						Key:                  "missing",
					},
				}},
			},
		},
	}

	if _, err := r.resolveMCPServerSecrets(context.Background(), "default", servers); err == nil {
		t.Fatal("expected error for missing secret key, got nil")
	}
}

func TestResolveMCPServerSecrets_EnvValueFromOptionalMissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-secret", Namespace: "default"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	optional := true
	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
						Key:                  "missing",
						Optional:             &optional,
					},
				}},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}
	got := envVarMap(resolved[0].Env)
	if _, ok := got["DB_PASSWORD"]; ok {
		t.Errorf("DB_PASSWORD present = %q, want it omitted for optional missing key", got["DB_PASSWORD"])
	}
}

func TestResolveMCPServerSecrets_EnvValueFromOptionalMissingConfigMapKey(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-config", Namespace: "default"},
		Data:       map[string]string{"other": "x"},
	}
	optional := true
	r := newReconcilerWithFakeClient(cm)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "DB_HOST", ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-config"},
						Key:                  "missing",
						Optional:             &optional,
					},
				}},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}
	got := envVarMap(resolved[0].Env)
	if _, ok := got["DB_HOST"]; ok {
		t.Errorf("DB_HOST present = %q, want it omitted for optional missing key", got["DB_HOST"])
	}
}

func TestResolveMCPServerSecrets_EnvValueFromUnsupportedFieldRef(t *testing.T) {
	r := newReconcilerWithFakeClient()
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "NODE", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
				}},
			},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("expected error for fieldRef, got nil")
	}
	if !strings.Contains(err.Error(), "secretKeyRef and configMapKeyRef") {
		t.Errorf("error = %q, want it to mention supported sources", err)
	}
}

// A SecretKeyRef combined with a pod-scoped source must be rejected rather
// than silently honoring the SecretKeyRef branch.
func TestResolveMCPServerSecrets_EnvValueFromSecretKeyWithFieldRef(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-secret", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("hunter2")},
	}
	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
						Key:                  "password",
					},
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
				}},
			},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("expected error for secretKeyRef combined with fieldRef, got nil")
	}
	if !strings.Contains(err.Error(), "secretKeyRef and configMapKeyRef") {
		t.Errorf("error = %q, want it to mention supported sources", err)
	}
}

// Setting both SecretKeyRef and ConfigMapKeyRef is ambiguous and must be
// rejected rather than silently preferring the secret.
func TestResolveMCPServerSecrets_EnvValueFromSecretKeyAndConfigMapKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-secret", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("hunter2")},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-config", Namespace: "default"},
		Data:       map[string]string{"password": "fromcm"},
	}
	r := newReconcilerWithFakeClient(secret, cm)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
						Key:                  "password",
					},
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-config"},
						Key:                  "password",
					},
				}},
			},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("expected error for secretKeyRef combined with configMapKeyRef, got nil")
	}
	if !strings.Contains(err.Error(), "only one of secretKeyRef or configMapKeyRef") {
		t.Errorf("error = %q, want it to mention only one source is allowed", err)
	}
}

func TestResolveMCPServerSecrets_EnvValueFromUnsupportedFileKeyRef(t *testing.T) {
	r := newReconcilerWithFakeClient()
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
					FileKeyRef: &corev1.FileKeySelector{Key: "token", Path: "env"},
				}},
			},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("expected error for fileKeyRef, got nil")
	}
	if !strings.Contains(err.Error(), "secretKeyRef and configMapKeyRef") {
		t.Errorf("error = %q, want it to mention supported sources", err)
	}
}

func TestResolveMCPServerSecrets_EnvValueAndValueFromMutuallyExclusive(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-secret", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("hunter2")},
	}
	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "DB_PASSWORD", Value: "literal", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
						Key:                  "password",
					},
				}},
			},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("expected error when both value and valueFrom are set, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want it to mention 'mutually exclusive'", err)
	}
}

func TestResolveMCPServerSecrets_EnvFromOverridesValueFrom(t *testing.T) {
	keySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "key-secret", Namespace: "default"},
		Data:       map[string][]byte{"value": []byte("from-key")},
	}
	bulkSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bulk-secret", Namespace: "default"},
		Data:       map[string][]byte{"SHARED": []byte("from-bulk")},
	}

	r := newReconcilerWithFakeClient(keySecret, bulkSecret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "local",
			Type: "stdio",
			Env: []corev1.EnvVar{
				{Name: "SHARED", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "key-secret"},
						Key:                  "value",
					},
				}},
			},
			EnvFrom: &kelos.SecretValuesSource{
				SecretRef: kelos.SecretReference{Name: "bulk-secret"},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}
	got := envVarMap(resolved[0].Env)
	if got["SHARED"] != "from-bulk" {
		t.Errorf("SHARED = %q, want %q (envFrom should win)", got["SHARED"], "from-bulk")
	}
}

func envVarMap(env []corev1.EnvVar) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}

func TestResolveMCPServerSecrets_SecretTakesPrecedence(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-headers",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"Authorization": []byte("Bearer from-secret"),
		},
	}

	r := newReconcilerWithFakeClient(secret)
	servers := []kelos.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			Headers: map[string]string{
				"Authorization": "Bearer inline-token",
			},
			HeadersFrom: &kelos.SecretValuesSource{
				SecretRef: kelos.SecretReference{Name: "mcp-headers"},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}

	if got := resolved[0].Headers["Authorization"]; got != "Bearer from-secret" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer from-secret")
	}
}

func TestResolveMCPServerSecrets_MissingSecret(t *testing.T) {
	r := newReconcilerWithFakeClient()
	servers := []kelos.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			HeadersFrom: &kelos.SecretValuesSource{
				SecretRef: kelos.SecretReference{Name: "missing-secret"},
			},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("resolveMCPServerSecrets() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "missing-secret") {
		t.Errorf("error = %q, want it to mention missing-secret", err)
	}
}

func TestIsJobFailed(t *testing.T) {
	tests := []struct {
		name       string
		conditions []batchv1.JobCondition
		want       bool
	}{
		{
			name:       "No conditions",
			conditions: nil,
			want:       false,
		},
		{
			name: "Job failed condition true",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionTrue,
				},
			},
			want: true,
		},
		{
			name: "Job failed condition false",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionFalse,
				},
			},
			want: false,
		},
		{
			name: "Job complete condition only",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			},
			want: false,
		},
		{
			name: "Multiple conditions with failed",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionFalse,
				},
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionTrue,
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: tt.conditions,
				},
			}
			if got := isJobFailed(job); got != tt.want {
				t.Errorf("isJobFailed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLatestTaskPodName(t *testing.T) {
	now := time.Now()
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "task-pod-old", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute))}},
		{ObjectMeta: metav1.ObjectMeta{Name: "task-pod-new", CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Minute))}},
		{ObjectMeta: metav1.ObjectMeta{Name: "task-pod-mid", CreationTimestamp: metav1.NewTime(now.Add(-90 * time.Second))}},
	}

	if got := latestTaskPodName(pods); got != "task-pod-new" {
		t.Fatalf("latestTaskPodName() = %q, want %q", got, "task-pod-new")
	}
}

func TestUpdateStatusRefreshesPodName(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	now := time.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   "codex",
			Prompt: "test",
			Credentials: kelos.Credentials{
				Type: kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "task-pod-old",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "task-pod-new",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"kelos.dev/task": "task-1",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task, pod).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}
	if _, err := r.updateStatus(context.Background(), task, &batchv1.Job{}); err != nil {
		t.Fatalf("updateStatus() error: %v", err)
	}

	updated := &kelos.Task{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if updated.Status.PodName != "task-pod-new" {
		t.Fatalf("task.Status.PodName = %q, want %q", updated.Status.PodName, "task-pod-new")
	}
}

func TestUpdateStatusClearsStalePodNameWhenNoLivePodsRemain(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   "codex",
			Prompt: "test",
			Credentials: kelos.Credentials{
				Type: kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseFailed,
			PodName: "task-pod-old",
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}
	if _, err := r.updateStatus(context.Background(), task, &batchv1.Job{}); err != nil {
		t.Fatalf("updateStatus() error: %v", err)
	}

	updated := &kelos.Task{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if updated.Status.PodName != "" {
		t.Fatalf("task.Status.PodName = %q, want empty", updated.Status.PodName)
	}
}
