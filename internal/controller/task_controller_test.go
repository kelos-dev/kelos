package controller

import (
	"context"
	"testing"
	"time"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
		task            *axonv1alpha1.Task
		wantExpired     bool
		wantRequeueMin  time.Duration
		wantRequeueMax  time.Duration
		wantZeroRequeue bool
	}{
		{
			name: "No TTL set",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: nil,
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "Not in terminal phase",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase: axonv1alpha1.TaskPhaseRunning,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "CompletionTime not set",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: nil,
				},
			},
			wantExpired:     false,
			wantZeroRequeue: true,
		},
		{
			name: "TTL=0 and completed",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(0),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-1 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for succeeded task",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now().Add(-20 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL expired for failed task",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(5),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseFailed,
					CompletionTime: timePtr(time.Now().Add(-10 * time.Second)),
				},
			},
			wantExpired:     true,
			wantZeroRequeue: true,
		},
		{
			name: "TTL not yet expired",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(60),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase:          axonv1alpha1.TaskPhaseSucceeded,
					CompletionTime: timePtr(time.Now()),
				},
			},
			wantExpired:    false,
			wantRequeueMin: 50 * time.Second,
			wantRequeueMax: 61 * time.Second,
		},
		{
			name: "Pending phase with TTL",
			task: &axonv1alpha1.Task{
				Spec: axonv1alpha1.TaskSpec{
					TTLSecondsAfterFinished: int32Ptr(10),
				},
				Status: axonv1alpha1.TaskStatus{
					Phase: axonv1alpha1.TaskPhasePending,
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

func newReconcilerWithFakeClient(objs ...runtime.Object) *TaskReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(axonv1alpha1.AddToScheme(scheme))

	clientObjs := make([]runtime.Object, len(objs))
	copy(clientObjs, objs)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(clientObjs...).
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
			"X-Custom":      []byte("custom-value"),
		},
	}

	r := newReconcilerWithFakeClient(secret)

	servers := []axonv1alpha1.MCPServerSpec{
		{
			Name: "github",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			Headers: map[string]string{
				"X-Static": "static-value",
			},
			HeadersFrom: &axonv1alpha1.SecretReference{Name: "mcp-headers"},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() returned error: %v", err)
	}

	if len(resolved) != 1 {
		t.Fatalf("Expected 1 resolved server, got %d", len(resolved))
	}

	s := resolved[0]
	if s.HeadersFrom != nil {
		t.Error("Expected HeadersFrom to be nil after resolution")
	}
	if s.Headers["Authorization"] != "Bearer secret-token" {
		t.Errorf("Expected Authorization header from secret, got %q", s.Headers["Authorization"])
	}
	if s.Headers["X-Custom"] != "custom-value" {
		t.Errorf("Expected X-Custom header from secret, got %q", s.Headers["X-Custom"])
	}
	if s.Headers["X-Static"] != "static-value" {
		t.Errorf("Expected X-Static inline header preserved, got %q", s.Headers["X-Static"])
	}
}

func TestResolveMCPServerSecrets_HeadersFromPrecedence(t *testing.T) {
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

	servers := []axonv1alpha1.MCPServerSpec{
		{
			Name: "api",
			Type: "http",
			URL:  "https://api.example.com/mcp/",
			Headers: map[string]string{
				"Authorization": "Bearer inline-token",
			},
			HeadersFrom: &axonv1alpha1.SecretReference{Name: "mcp-headers"},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() returned error: %v", err)
	}

	// Secret value should take precedence over inline value
	if resolved[0].Headers["Authorization"] != "Bearer from-secret" {
		t.Errorf("Expected headersFrom to take precedence, got %q", resolved[0].Headers["Authorization"])
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
		},
	}

	r := newReconcilerWithFakeClient(secret)

	servers := []axonv1alpha1.MCPServerSpec{
		{
			Name:    "local-db",
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "dbhub"},
			Env: map[string]string{
				"DSN": "postgres://localhost/db",
			},
			EnvFrom: &axonv1alpha1.SecretReference{Name: "mcp-env"},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() returned error: %v", err)
	}

	s := resolved[0]
	if s.EnvFrom != nil {
		t.Error("Expected EnvFrom to be nil after resolution")
	}
	if s.Env["DB_PASSWORD"] != "secret-pass" {
		t.Errorf("Expected DB_PASSWORD from secret, got %q", s.Env["DB_PASSWORD"])
	}
	if s.Env["DSN"] != "postgres://localhost/db" {
		t.Errorf("Expected DSN inline env preserved, got %q", s.Env["DSN"])
	}
}

func TestResolveMCPServerSecrets_MissingSecret(t *testing.T) {
	r := newReconcilerWithFakeClient() // No secrets in the cluster

	servers := []axonv1alpha1.MCPServerSpec{
		{
			Name:        "api",
			Type:        "http",
			URL:         "https://api.example.com/mcp/",
			HeadersFrom: &axonv1alpha1.SecretReference{Name: "nonexistent"},
		},
	}

	_, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err == nil {
		t.Fatal("Expected error for missing secret, got nil")
	}
}

func TestResolveMCPServerSecrets_NoSecretRefs(t *testing.T) {
	r := newReconcilerWithFakeClient()

	servers := []axonv1alpha1.MCPServerSpec{
		{
			Name:    "github",
			Type:    "http",
			URL:     "https://api.example.com/mcp/",
			Headers: map[string]string{"X-Static": "value"},
		},
	}

	resolved, err := r.resolveMCPServerSecrets(context.Background(), "default", servers)
	if err != nil {
		t.Fatalf("resolveMCPServerSecrets() returned error: %v", err)
	}

	if resolved[0].Headers["X-Static"] != "value" {
		t.Errorf("Expected inline headers preserved, got %v", resolved[0].Headers)
	}
}
