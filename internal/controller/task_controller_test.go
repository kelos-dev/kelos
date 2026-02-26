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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/githubapp"
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
			repoURL:     "https://github.com/axon-core/axon.git",
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
			utilruntime.Must(axonv1alpha1.AddToScheme(scheme))

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

			task := &axonv1alpha1.Task{
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

			workspace := &axonv1alpha1.WorkspaceSpec{
				Repo: repoURL,
				SecretRef: &axonv1alpha1.SecretReference{
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
	utilruntime.Must(axonv1alpha1.AddToScheme(scheme))

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

	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
	}
	workspace := &axonv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/axon-core/axon.git",
		SecretRef: &axonv1alpha1.SecretReference{
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
