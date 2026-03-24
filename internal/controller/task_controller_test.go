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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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
		task            *kelosv1alpha1.Task
		wantExpired     bool
		wantRequeueMin  time.Duration
		wantRequeueMax  time.Duration
		wantZeroRequeue bool
	}{
		{
			name: "No TTL set",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: nil,
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:          kelosv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "Not in terminal phase",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase: kelosv1alpha1.TaskPhaseRunning,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "CompletionTime not set",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:          kelosv1alpha1.TaskPhaseSucceeded,
					CompletionTime: nil,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "TTL=0 and completed",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(0),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:          kelosv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-1 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for succeeded task",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:          kelosv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-20 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for failed task",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(5),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:          kelosv1alpha1.TaskPhaseFailed,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL not yet expired",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:          kelosv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now()),
				},
			},
			wantExpired:    false,
			wantRequeueMin: 50 * time.Second,
			wantRequeueMax: 61 * time.Second,
		},
		{
			name: "Pending phase with TTL",
			task: &kelosv1alpha1.Task{
				Spec: kelosv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase: kelosv1alpha1.TaskPhasePending,
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
			utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

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

			task := &kelosv1alpha1.Task{
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

			workspace := &kelosv1alpha1.WorkspaceSpec{
				Repo: repoURL,
				SecretRef: &kelosv1alpha1.SecretReference{
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

func TestResolveGitHubAppToken_PATSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

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

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
	}
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/kelos-dev/kelos.git",
		SecretRef: &kelosv1alpha1.SecretReference{
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
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

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
	servers := []kelosv1alpha1.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			Headers: map[string]string{
				"X-Inline": "inline-value",
			},
			HeadersFrom: &kelosv1alpha1.SecretValuesSource{
				SecretRef: kelosv1alpha1.SecretReference{Name: "mcp-headers"},
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
	servers := []kelosv1alpha1.MCPServerSpec{
		{
			Name:    "local-db",
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "@bytebase/dbhub"},
			Env: map[string]string{
				"DSN": "postgres://localhost/db",
			},
			EnvFrom: &kelosv1alpha1.SecretValuesSource{
				SecretRef: kelosv1alpha1.SecretReference{Name: "mcp-env"},
			},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() error = %v", err)
	}

	if got := resolved[0].Env["DB_PASSWORD"]; got != "secret-pass" {
		t.Errorf("DB_PASSWORD = %q, want %q", got, "secret-pass")
	}
	if got := resolved[0].Env["DB_HOST"]; got != "db.internal" {
		t.Errorf("DB_HOST = %q, want %q", got, "db.internal")
	}
	if got := resolved[0].Env["DSN"]; got != "postgres://localhost/db" {
		t.Errorf("DSN = %q, want %q", got, "postgres://localhost/db")
	}
	if resolved[0].EnvFrom != nil {
		t.Fatal("EnvFrom should be nil after resolution")
	}
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
	servers := []kelosv1alpha1.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			Headers: map[string]string{
				"Authorization": "Bearer inline-token",
			},
			HeadersFrom: &kelosv1alpha1.SecretValuesSource{
				SecretRef: kelosv1alpha1.SecretReference{Name: "mcp-headers"},
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
	servers := []kelosv1alpha1.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			HeadersFrom: &kelosv1alpha1.SecretValuesSource{
				SecretRef: kelosv1alpha1.SecretReference{Name: "missing-secret"},
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
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	now := time.Now()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "codex",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type: kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: &kelosv1alpha1.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
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

	updated := &kelosv1alpha1.Task{}
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
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "codex",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type: kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: &kelosv1alpha1.SecretReference{
					Name: "creds",
				},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseFailed,
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

	updated := &kelosv1alpha1.Task{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if updated.Status.PodName != "" {
		t.Fatalf("task.Status.PodName = %q, want empty", updated.Status.PodName)
	}
}
