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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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
			name: "CompletionTime not set requeues to wait for status to settle",
			task: &kelos.Task{
				Spec: kelos.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelos.TaskStatus{
					Phase:          kelos.TaskPhaseSucceeded,
					CompletionTime: nil,
				},
			},
			wantExpired:    false,
			wantRequeueMin: 4 * time.Second,
			wantRequeueMax: 6 * time.Second,
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

func TestReconcile_SkipsTerminalTaskWithoutJob(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-task",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelos.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "test",
			AgentConfigRefs: []kelos.AgentConfigReference{
				{Name: "missing-agent-config"},
			},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseFailed,
			Message: "Failed to validate skills auth secret: missing secret",
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task).
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
	if result != (ctrl.Result{}) {
		t.Fatalf("Reconcile() result = %+v, want empty result", result)
	}

	var jobs batchv1.JobList
	if err := cl.List(context.Background(), &jobs, client.InNamespace(task.Namespace)); err != nil {
		t.Fatalf("listing Jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("Jobs = %d, want none", len(jobs.Items))
	}
}

func TestReconcile_RequeuesTerminalTaskWithoutJobUntilTTLExpires(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	ttlSeconds := int32(60)
	completionTime := metav1.NewTime(time.Now())
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-task",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelos.TaskSpec{
			Type:                    AgentTypeCodex,
			Prompt:                  "test",
			TTLSecondsAfterFinished: &ttlSeconds,
			AgentConfigRefs: []kelos.AgentConfigReference{
				{Name: "missing-agent-config"},
			},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseFailed,
			Message:        "Failed to validate skills auth secret: missing secret",
			CompletionTime: &completionTime,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task).
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
	if result.RequeueAfter < 50*time.Second || result.RequeueAfter > 61*time.Second {
		t.Fatalf("RequeueAfter = %v, want near task TTL", result.RequeueAfter)
	}

	var jobs batchv1.JobList
	if err := cl.List(context.Background(), &jobs, client.InNamespace(task.Namespace)); err != nil {
		t.Fatalf("listing Jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("Jobs = %d, want none", len(jobs.Items))
	}
}

func TestReconcile_RequeuesTerminalTaskWithTTLButNoCompletionTime(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	ttlSeconds := int32(60)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-task",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelos.TaskSpec{
			Type:                    AgentTypeCodex,
			Prompt:                  "test",
			TTLSecondsAfterFinished: &ttlSeconds,
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseFailed,
			Message: "Something went wrong",
			// CompletionTime intentionally nil
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task).
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
	if result.RequeueAfter == 0 {
		t.Fatal("RequeueAfter = 0, want non-zero requeue for terminal task with TTL but no CompletionTime")
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

func TestValidateSkillsAuthSecrets(t *testing.T) {
	tests := []struct {
		name       string
		objects    []runtime.Object
		skills     []kelos.SkillsShSpec
		wantErrStr string
	}{
		{
			name: "valid secret",
			objects: []runtime.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "skills-token", Namespace: "default"},
				Data:       map[string][]byte{GitHubTokenSecretKey: []byte("ghp_test")},
			}},
			skills: []kelos.SkillsShSpec{{
				Source:    "org/private-skills",
				SecretRef: &kelos.SecretReference{Name: "skills-token"},
			}},
		},
		{
			name: "public skills do not require secret",
			skills: []kelos.SkillsShSpec{{
				Source: "org/public-skills",
			}},
		},
		{
			name: "missing secret",
			skills: []kelos.SkillsShSpec{{
				Source:    "org/private-skills",
				SecretRef: &kelos.SecretReference{Name: "missing-token"},
			}},
			wantErrStr: "missing-token",
		},
		{
			name: "missing token key",
			objects: []runtime.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "skills-token", Namespace: "default"},
				Data:       map[string][]byte{"OTHER": []byte("value")},
			}},
			skills: []kelos.SkillsShSpec{{
				Source:    "org/private-skills",
				SecretRef: &kelos.SecretReference{Name: "skills-token"},
			}},
			wantErrStr: GitHubTokenSecretKey,
		},
		{
			name: "empty token",
			objects: []runtime.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "skills-token", Namespace: "default"},
				Data:       map[string][]byte{GitHubTokenSecretKey: []byte("")},
			}},
			skills: []kelos.SkillsShSpec{{
				Source:    "org/private-skills",
				SecretRef: &kelos.SecretReference{Name: "skills-token"},
			}},
			wantErrStr: "empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReconcilerWithFakeClient(tt.objects...)
			err := r.validateSkillsAuthSecrets(context.Background(), "default", tt.skills)
			if tt.wantErrStr == "" {
				if err != nil {
					t.Fatalf("validateSkillsAuthSecrets() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSkillsAuthSecrets() error = nil, want %q", tt.wantErrStr)
			}
			if !strings.Contains(err.Error(), tt.wantErrStr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErrStr)
			}
			if !strings.Contains(err.Error(), tt.skills[0].Source) {
				t.Errorf("error = %q, want it to mention source %q", err, tt.skills[0].Source)
			}
		})
	}
}

func TestFailTaskBeforeJobReleasesBranchLock(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Branch: "feature-1",
			WorkspaceRef: &kelos.WorkspaceReference{
				Name: "workspace-1",
			},
		},
	}
	locker := NewBranchLocker()
	lockKey := branchLockKey(task)
	if ok, holder := locker.TryAcquire(lockKey, task.Name); !ok {
		t.Fatalf("TryAcquire() failed, held by %q", holder)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task).
		Build()

	r := &TaskReconciler{
		Client:       cl,
		Scheme:       scheme,
		BranchLocker: locker,
	}
	const message = "Failed to validate skills auth secret: missing token"
	if err := r.failTaskBeforeJob(context.Background(), task, message); err != nil {
		t.Fatalf("failTaskBeforeJob() error = %v", err)
	}

	if holder := locker.Holder(lockKey); holder != "" {
		t.Fatalf("branch lock holder = %q, want released", holder)
	}
	updated := &kelos.Task{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if updated.Status.Phase != kelos.TaskPhaseFailed {
		t.Fatalf("task phase = %q, want %q", updated.Status.Phase, kelos.TaskPhaseFailed)
	}
	if updated.Status.Message != message {
		t.Fatalf("task message = %q, want %q", updated.Status.Message, message)
	}
	if updated.Status.CompletionTime == nil {
		t.Fatal("task completionTime is nil, want set")
	}
}

func TestReconcile_PoolBackedTaskDeletionRequestsCancellation(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	now := metav1.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pool-task-1",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{taskFinalizer},
		},
		Spec: kelos.TaskSpec{
			Prompt:        "test",
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "my-pool"},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "my-pool-0",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pool-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask:   "pool-task-1",
				kelos.AnnotationWorkerTaskStatus:     "running",
				kelos.AnnotationWorkerTasksCompleted: "2",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(task).
		WithObjects(task, pod).
		Build()

	r := &TaskReconciler{
		Client:       cl,
		Scheme:       scheme,
		BranchLocker: NewBranchLocker(),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(task),
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("expected reconcile to requeue while waiting for worker release")
	}

	updatedPod := &corev1.Pod{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), updatedPod); err != nil {
		t.Fatalf("getting updated pod: %v", err)
	}
	if got := updatedPod.Annotations[kelos.AnnotationWorkerAssignedTask]; got != "pool-task-1" {
		t.Errorf("assigned-task = %q, want %q", got, "pool-task-1")
	}
	if got := updatedPod.Annotations[kelos.AnnotationWorkerTaskStatus]; got != "running" {
		t.Errorf("task-status = %q, want %q", got, "running")
	}
	if got := updatedPod.Annotations[kelos.AnnotationWorkerCancelTask]; got != "pool-task-1" {
		t.Errorf("cancel-task = %q, want %q", got, "pool-task-1")
	}
	if v := updatedPod.Annotations[kelos.AnnotationWorkerTasksCompleted]; v != "2" {
		t.Errorf("tasks-completed = %q, want %q", v, "2")
	}

	updatedTask := &kelos.Task{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), updatedTask); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if !controllerutil.ContainsFinalizer(updatedTask, taskFinalizer) {
		t.Error("expected finalizer to remain until worker releases the task")
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
			Credentials: &kelos.Credentials{
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
			Credentials: &kelos.Credentials{
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

func TestUpdateStatusRetriesTaskRecordForUnchangedTerminalTask(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	costUSD := resource.MustParse("1.25")
	completionTime := metav1.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
			UID:       "task-uid",
		},
		Spec: kelos.TaskSpec{
			Type:   "codex",
			Prompt: "test",
			Credentials: &kelos.Credentials{
				Type: kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
			Results:        map[string]string{"cost-usd": "1.25"},
			Usage:          &kelos.TaskUsage{CostUSD: &costUSD},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}
	if _, err := r.updateStatus(context.Background(), task, &batchv1.Job{}); err != nil {
		t.Fatalf("updateStatus() error: %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "task-uid"}, &record); err != nil {
		t.Fatalf("getting TaskRecord: %v", err)
	}
	if record.Spec.TaskRef.Name != "task-1" {
		t.Fatalf("record taskRef.name = %q, want task-1", record.Spec.TaskRef.Name)
	}
}

func TestEnsurePluginConfigMap_CreateAndUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "test-uid",
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}

	plugins := []kelos.PluginSpec{
		{
			Name: "team-tools",
			Skills: []kelos.SkillDefinition{
				{Name: "deploy", Content: "Deploy instructions here"},
			},
		},
	}

	built, err := buildPluginConfigMap(task, plugins)
	if err != nil {
		t.Fatalf("buildPluginConfigMap() error: %v", err)
	}
	if err := r.ensurePluginConfigMap(context.Background(), task, built); err != nil {
		t.Fatalf("ensurePluginConfigMap() error: %v", err)
	}

	configMap := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: "test-task-plugins", Namespace: "default"}
	if err := cl.Get(context.Background(), key, configMap); err != nil {
		t.Fatalf("getting plugin ConfigMap: %v", err)
	}
	if got := configMap.Data["p0-s0"]; got != "Deploy instructions here" {
		t.Errorf("data[p0-s0] = %q, want %q", got, "Deploy instructions here")
	}

	// The Task must own the ConfigMap so garbage collection removes it.
	if len(configMap.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(configMap.OwnerReferences))
	}
	owner := configMap.OwnerReferences[0]
	if owner.Kind != "Task" || owner.Name != "test-task" || owner.UID != "test-uid" {
		t.Errorf("owner reference = %+v, want Task/test-task", owner)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("expected owner reference to be a controller reference")
	}

	// A second reconcile with changed content must update in place.
	plugins[0].Skills[0].Content = "Updated deploy instructions"
	rebuilt, err := buildPluginConfigMap(task, plugins)
	if err != nil {
		t.Fatalf("buildPluginConfigMap() on update error: %v", err)
	}
	if err := r.ensurePluginConfigMap(context.Background(), task, rebuilt); err != nil {
		t.Fatalf("ensurePluginConfigMap() on existing ConfigMap error: %v", err)
	}
	if err := cl.Get(context.Background(), key, configMap); err != nil {
		t.Fatalf("getting updated plugin ConfigMap: %v", err)
	}
	if got := configMap.Data["p0-s0"]; got != "Updated deploy instructions" {
		t.Errorf("data[p0-s0] after update = %q, want %q", got, "Updated deploy instructions")
	}
}

// TestEnsurePluginConfigMap_NotAdoptedWhenUnowned verifies that a pre-existing
// ConfigMap sharing the generated name but not controlled by the Task is left
// untouched and a name-collision error is returned, so a user-created
// ConfigMap can never be overwritten or garbage-collected with the Task.
func TestEnsurePluginConfigMap_NotAdoptedWhenUnowned(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "test-uid",
		},
	}

	// A user-owned ConfigMap that happens to share the generated name and
	// carries unrelated content the controller must not clobber.
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-plugins",
			Namespace: "default",
		},
		Data: map[string]string{"user-key": "user-value"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, existing).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}

	plugins := []kelos.PluginSpec{
		{
			Name: "team-tools",
			Skills: []kelos.SkillDefinition{
				{Name: "deploy", Content: "Deploy instructions here"},
			},
		},
	}

	built, err := buildPluginConfigMap(task, plugins)
	if err != nil {
		t.Fatalf("buildPluginConfigMap() error: %v", err)
	}
	if err := r.ensurePluginConfigMap(context.Background(), task, built); err == nil {
		t.Fatal("ensurePluginConfigMap() succeeded, want name-collision error")
	}

	// The pre-existing ConfigMap must be untouched: data preserved and no
	// owner reference pointing at the Task.
	got := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: "test-task-plugins", Namespace: "default"}
	if err := cl.Get(context.Background(), key, got); err != nil {
		t.Fatalf("getting plugin ConfigMap: %v", err)
	}
	if got.Data["user-key"] != "user-value" {
		t.Errorf("data[user-key] = %q, want %q (must not be overwritten)", got.Data["user-key"], "user-value")
	}
	if _, ok := got.Data["p0-s0"]; ok {
		t.Error("plugin content was written into an unowned ConfigMap")
	}
	if len(got.OwnerReferences) != 0 {
		t.Errorf("expected no owner references on unowned ConfigMap, got %d", len(got.OwnerReferences))
	}
}

func TestCreateTaskRecord(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	int64Ptr := func(v int64) *int64 { return &v }
	timePtr := func(t time.Time) *metav1.Time {
		mt := metav1.NewTime(t)
		return &mt
	}

	costUSD := resource.MustParse("2.50")
	startTime := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	completionTime := time.Date(2024, 6, 15, 10, 5, 0, 0, time.UTC)

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "abc-123-def",
			Labels: map[string]string{
				"team": "platform",
			},
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Model:  "opus",
			Prompt: "test prompt",
			Credentials: &kelos.Credentials{
				Type: kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			StartTime:      timePtr(startTime),
			CompletionTime: timePtr(completionTime),
			Usage: &kelos.TaskUsage{
				CostUSD:      &costUSD,
				InputTokens:  int64Ptr(5000),
				OutputTokens: int64Ptr(2000),
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}

	// First call should create the TaskRecord
	if err := r.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord: %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKey{
		Namespace: "default",
		Name:      "abc-123-def",
	}, &record); err != nil {
		t.Fatalf("getting TaskRecord: %v", err)
	}

	// Verify TaskRecord fields
	if record.Spec.TaskRef.Name != "test-task" {
		t.Errorf("TaskRef.Name = %q, want %q", record.Spec.TaskRef.Name, "test-task")
	}
	if record.Spec.TaskRef.UID != "abc-123-def" {
		t.Errorf("TaskRef.UID = %q, want %q", record.Spec.TaskRef.UID, "abc-123-def")
	}
	if record.Spec.Type != "claude-code" {
		t.Errorf("Type = %q, want %q", record.Spec.Type, "claude-code")
	}
	if record.Spec.Model != "opus" {
		t.Errorf("Model = %q, want %q", record.Spec.Model, "opus")
	}
	if record.Spec.Phase != kelos.TaskPhaseSucceeded {
		t.Errorf("Phase = %q, want %q", record.Spec.Phase, kelos.TaskPhaseSucceeded)
	}
	if record.Spec.Usage == nil {
		t.Fatal("Usage = nil, want non-nil")
	}
	if record.Spec.Usage.CostUSD == nil || record.Spec.Usage.CostUSD.Cmp(costUSD) != 0 {
		t.Errorf("Usage.CostUSD = %v, want %s", record.Spec.Usage.CostUSD, costUSD.String())
	}
	if record.Spec.Usage.InputTokens == nil || *record.Spec.Usage.InputTokens != 5000 {
		t.Errorf("Usage.InputTokens = %v, want 5000", record.Spec.Usage.InputTokens)
	}
	if record.Spec.Usage.OutputTokens == nil || *record.Spec.Usage.OutputTokens != 2000 {
		t.Errorf("Usage.OutputTokens = %v, want 2000", record.Spec.Usage.OutputTokens)
	}
	if record.Labels["team"] != "platform" {
		t.Errorf("Labels[team] = %q, want %q", record.Labels["team"], "platform")
	}

	// Second call (idempotency) should not error
	if err := r.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord (idempotent): %v", err)
	}

	// Verify still only one record exists
	var recordList kelos.TaskRecordList
	if err := cl.List(context.Background(), &recordList, client.InNamespace("default")); err != nil {
		t.Fatalf("listing TaskRecords: %v", err)
	}
	if len(recordList.Items) != 1 {
		t.Errorf("TaskRecord count = %d, want 1", len(recordList.Items))
	}
}

func TestCreateTaskRecord_NilUsageSkips(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-usage-task",
			Namespace: "default",
			UID:       "uid-no-usage",
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: &kelos.Credentials{
				Type: kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelos.TaskStatus{
			Phase: kelos.TaskPhaseSucceeded,
			Usage: nil,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task).
		Build()

	r := &TaskReconciler{Client: cl, Scheme: scheme}

	// Should not create a record when Usage is nil
	if err := r.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord: %v", err)
	}

	var recordList kelos.TaskRecordList
	if err := cl.List(context.Background(), &recordList, client.InNamespace("default")); err != nil {
		t.Fatalf("listing TaskRecords: %v", err)
	}
	if len(recordList.Items) != 0 {
		t.Errorf("TaskRecord count = %d, want 0 (no record should be created for nil usage)", len(recordList.Items))
	}
}
