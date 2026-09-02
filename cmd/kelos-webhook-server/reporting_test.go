package main

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/webhook"
)

func newReportingTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding core API to scheme: %v", err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatalf("adding Kelos API to scheme: %v", err)
	}
	return scheme
}

func TestReportingReconcilerSkipsTasksOwnedByOtherServerMode(t *testing.T) {
	tests := []struct {
		name        string
		gatewayMode bool
		gatewayName string
		source      webhook.WebhookSource
		provider    string
		resolver    func(context.Context) (string, error)
	}{
		{name: "gateway server skips source-specific task", gatewayMode: true},
		{
			name:        "source-specific server skips gateway task",
			gatewayName: "github",
			resolver:    func(context.Context) (string, error) { return "token", nil },
		},
		// Per-source servers of different providers watch the same Tasks; each
		// must leave the other provider's Tasks alone instead of failing on
		// credentials it does not have.
		{name: "github server skips gitlab task", source: webhook.GitHubSource, provider: reporting.SourceProviderGitLab},
		{name: "gitlab server skips github task", source: webhook.GitLabSource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "task",
					Namespace: "default",
					Annotations: map[string]string{
						reporting.AnnotationGitHubReporting: "enabled",
						reporting.AnnotationSourceOwner:     "owner",
						reporting.AnnotationSourceRepo:      "repo",
						reporting.AnnotationWebhookGateway:  tt.gatewayName,
						reporting.AnnotationSourceProvider:  tt.provider,
					},
				},
			}
			reconciler := &reportingReconciler{
				Client: fake.NewClientBuilder().WithScheme(newReportingTestScheme(t)).WithObjects(task).Build(),
				config: reportingConfig{GatewayMode: tt.gatewayMode, Source: tt.source, TokenResolver: tt.resolver},
			}

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
			})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result != (ctrl.Result{}) {
				t.Errorf("Reconcile() result = %v, want empty result", result)
			}
		})
	}
}

func TestResolveReportingCredsFromGateway(t *testing.T) {
	gateway := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{GitHub: &kelos.GitHubGateway{
			SecretRef:      kelos.SecretReference{Name: "webhook-secret"},
			APIBaseURL:     "https://github.example/api/v3",
			CredentialsRef: &kelos.SecretReference{Name: "github-credentials"},
		}},
	}
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-credentials", Namespace: "default"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("token")},
	}
	reconciler := &reportingReconciler{Client: fake.NewClientBuilder().
		WithScheme(newReportingTestScheme(t)).
		WithObjects(gateway, credentials).
		Build()}
	task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{
		Namespace:   "default",
		Annotations: map[string]string{reporting.AnnotationWebhookGateway: gateway.Name},
	}}

	resolver, baseURL, appID, err := reconciler.resolveReportingCreds(context.Background(), task)
	if err != nil {
		t.Fatalf("resolveReportingCreds() error = %v", err)
	}
	if baseURL != gateway.Spec.GitHub.APIBaseURL {
		t.Errorf("resolveReportingCreds() base URL = %q, want %q", baseURL, gateway.Spec.GitHub.APIBaseURL)
	}
	if appID != "" {
		t.Errorf("resolveReportingCreds() app ID = %q, want empty for a personal access token", appID)
	}
	token, err := resolver(context.Background())
	if err != nil {
		t.Fatalf("resolved token error = %v", err)
	}
	if token != "token" {
		t.Errorf("resolved token = %q, want %q", token, "token")
	}
}

func TestReportingReconcilerPostsGitLabNote(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.EscapedPath()
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]int64{"id": 77})
	}))
	defer server.Close()

	tests := []struct {
		name        string
		gatewayMode bool
		objects     []client.Object
		annotations map[string]string
		config      reportingConfig
		wantToken   string
	}{
		{
			name: "per-source server uses the configured GitLab token and instance URL, never the payload's",
			annotations: map[string]string{
				reporting.AnnotationSourceBaseURL: "https://attacker.example",
			},
			config:    reportingConfig{Source: webhook.GitLabSource, GitLabToken: "server-token", GitLabBaseURL: server.URL},
			wantToken: "server-token",
		},
		{
			name:        "gateway server uses the gateway credentials and API base URL override",
			gatewayMode: true,
			objects: []client.Object{
				&kelos.WebhookGateway{
					ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
					Spec: kelos.WebhookGatewaySpec{GitLab: &kelos.GitLabGateway{
						SecretRef:      kelos.SecretReference{Name: "webhook-secret"},
						APIBaseURL:     server.URL,
						CredentialsRef: &kelos.SecretReference{Name: "gitlab-credentials"},
					}},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "gitlab-credentials", Namespace: "default"},
					Data:       map[string][]byte{"GITLAB_TOKEN": []byte("gateway-token")},
				},
			},
			annotations: map[string]string{
				reporting.AnnotationSourceBaseURL:  "https://attacker.example",
				reporting.AnnotationWebhookGateway: "gl",
			},
			config:    reportingConfig{GatewayMode: true},
			wantToken: "gateway-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			gotPath, gotToken = "", ""
			mu.Unlock()
			annotations := map[string]string{
				reporting.AnnotationGitHubReporting:   "enabled",
				reporting.AnnotationGitHubCommentMode: string(kelos.CommentModePerTask),
				reporting.AnnotationSourceProvider:    reporting.SourceProviderGitLab,
				reporting.AnnotationSourceKind:        reporting.SourceKindMergeRequest,
				reporting.AnnotationSourceNumber:      "7",
				reporting.AnnotationSourceRepo:        "group/sub/repo",
			}
			for k, v := range tt.annotations {
				annotations[k] = v
			}
			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task", Namespace: "default", Annotations: annotations},
				Status:     kelos.TaskStatus{Phase: kelos.TaskPhasePending},
			}
			objects := append([]client.Object{task}, tt.objects...)
			reconciler := &reportingReconciler{
				Client: fake.NewClientBuilder().WithScheme(newReportingTestScheme(t)).WithObjects(objects...).Build(),
				config: tt.config,
				cache:  reporting.NewReportStateCache(),
			}

			if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: "default", Name: "task"},
			}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			mu.Lock()
			path, token := gotPath, gotToken
			mu.Unlock()
			if path != "/api/v4/projects/group%2Fsub%2Frepo/merge_requests/7/notes" {
				t.Errorf("note posted to %q", path)
			}
			if token != tt.wantToken {
				t.Errorf("PRIVATE-TOKEN = %q, want %q", token, tt.wantToken)
			}

			var updated kelos.Task
			if err := reconciler.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "task"}, &updated); err != nil {
				t.Fatal(err)
			}
			if updated.Annotations[reporting.AnnotationGitHubCommentID] != "77" {
				t.Errorf("expected note id persisted, got %q", updated.Annotations[reporting.AnnotationGitHubCommentID])
			}
		})
	}
}

func TestResolveGitLabReportingCredsErrors(t *testing.T) {
	gateway := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
		Spec:       kelos.WebhookGatewaySpec{GitLab: &kelos.GitLabGateway{SecretRef: kelos.SecretReference{Name: "webhook-secret"}}},
	}
	reconciler := &reportingReconciler{Client: fake.NewClientBuilder().WithScheme(newReportingTestScheme(t)).WithObjects(gateway).Build()}

	noToken := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Annotations: map[string]string{}}}
	if _, _, err := reconciler.resolveGitLabReportingCreds(context.Background(), noToken); err == nil || !strings.Contains(err.Error(), "no GitLab token") {
		t.Errorf("expected missing server token error, got %v", err)
	}

	noCreds := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Annotations: map[string]string{reporting.AnnotationWebhookGateway: "gl"}}}
	if _, _, err := reconciler.resolveGitLabReportingCreds(context.Background(), noCreds); err == nil || !strings.Contains(err.Error(), "credentialsRef") {
		t.Errorf("expected missing credentialsRef error, got %v", err)
	}
}

func TestResolveGitLabReportingCredsRejectsGitHubTokenKey(t *testing.T) {
	gateway := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{GitLab: &kelos.GitLabGateway{
			SecretRef:      kelos.SecretReference{Name: "webhook-secret"},
			CredentialsRef: &kelos.SecretReference{Name: "gitlab-credentials"},
		}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gitlab-credentials", Namespace: "default"},
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("not-a-gitlab-key")},
	}
	reconciler := &reportingReconciler{Client: fake.NewClientBuilder().WithScheme(newReportingTestScheme(t)).WithObjects(gateway, secret).Build()}

	task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Annotations: map[string]string{reporting.AnnotationWebhookGateway: "gl"}}}
	_, _, err := reconciler.resolveGitLabReportingCreds(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "no GITLAB_TOKEN") {
		t.Fatalf("expected GITLAB_TOKEN-only credentials error, got %v", err)
	}
}

func TestReportingReconcilerUsesGatewayGitHubAppIdentityForStickyComments(t *testing.T) {
	var userRequests atomic.Int32
	var tokenRequests atomic.Int32
	var listRequests atomic.Int32
	var createRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations/456/access_tokens":
			tokenRequests.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"token":      "installation-token",
				"expires_at": time.Now().Add(time.Hour),
			})
		case r.URL.Path == "/user":
			userRequests.Add(1)
			http.Error(w, "installation tokens have no user context", http.StatusForbidden)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodGet:
			listRequests.Add(1)
			if got := r.Header.Get("Authorization"); got != "token installation-token" {
				t.Errorf("Authorization header = %q, want installation token", got)
			}
			_ = json.NewEncoder(w).Encode([]interface{}{})
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			createRequests.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 123})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generating test private key: %v", err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	gateway := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{GitHub: &kelos.GitHubGateway{
			SecretRef:      kelos.SecretReference{Name: "webhook-secret"},
			APIBaseURL:     server.URL,
			CredentialsRef: &kelos.SecretReference{Name: "github-credentials"},
		}},
	}
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-credentials", Namespace: "default"},
		Data: map[string][]byte{
			"appID":          []byte("123"),
			"installationID": []byte("456"),
			"privateKey":     privateKeyPEM,
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task",
			Namespace: "default",
			UID:       types.UID("task-uid"),
			Labels:    map[string]string{"kelos.dev/taskspawner": "reviewer"},
			Annotations: map[string]string{
				reporting.AnnotationGitHubReporting:   "enabled",
				reporting.AnnotationGitHubCommentMode: string(kelos.CommentModeSticky),
				reporting.AnnotationSourceOwner:       "owner",
				reporting.AnnotationSourceRepo:        "repo",
				reporting.AnnotationSourceNumber:      "42",
				reporting.AnnotationWebhookGateway:    gateway.Name,
			},
		},
		Status: kelos.TaskStatus{Phase: kelos.TaskPhasePending},
	}
	reconciler := &reportingReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(newReportingTestScheme(t)).
			WithObjects(gateway, credentials, task).
			Build(),
		config: reportingConfig{GatewayMode: true},
	}

	_, err = reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: task.Namespace, Name: task.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if userRequests.Load() != 0 {
		t.Errorf("authenticated user endpoint called %d times, want 0 for GitHub App reporting", userRequests.Load())
	}
	if tokenRequests.Load() != 1 || listRequests.Load() != 1 || createRequests.Load() != 1 {
		t.Errorf("requests = token:%d list:%d create:%d, want 1 each", tokenRequests.Load(), listRequests.Load(), createRequests.Load())
	}
}

func TestResolveReportingCredsErrors(t *testing.T) {
	tests := []struct {
		name        string
		gateway     *kelos.WebhookGateway
		credentials *corev1.Secret
		gatewayName string
		wantError   string
	}{
		{
			name:      "no gateway and no resolver",
			wantError: "no GitHub token resolver configured",
		},
		{
			name: "gateway without credentials reference",
			gateway: &kelos.WebhookGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "missing-ref", Namespace: "default"},
				Spec: kelos.WebhookGatewaySpec{GitHub: &kelos.GitHubGateway{
					SecretRef: kelos.SecretReference{Name: "webhook-secret"},
				}},
			},
			gatewayName: "missing-ref",
			wantError:   "has no github.credentialsRef",
		},
		{
			name: "credentials without usable token",
			gateway: &kelos.WebhookGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-credentials", Namespace: "default"},
				Spec: kelos.WebhookGatewaySpec{GitHub: &kelos.GitHubGateway{
					SecretRef:      kelos.SecretReference{Name: "webhook-secret"},
					CredentialsRef: &kelos.SecretReference{Name: "credentials"},
				}},
			},
			credentials: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "default"}},
			gatewayName: "empty-credentials",
			wantError:   "credentials contain no usable token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]client.Object, 0, 2)
			if tt.gateway != nil {
				objects = append(objects, tt.gateway)
			}
			if tt.credentials != nil {
				objects = append(objects, tt.credentials)
			}
			reconciler := &reportingReconciler{Client: fake.NewClientBuilder().
				WithScheme(newReportingTestScheme(t)).
				WithObjects(objects...).
				Build()}
			task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{
				Namespace:   "default",
				Annotations: map[string]string{reporting.AnnotationWebhookGateway: tt.gatewayName},
			}}

			_, _, _, err := reconciler.resolveReportingCreds(context.Background(), task)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("resolveReportingCreds() error = %v, want error containing %q", err, tt.wantError)
			}
		})
	}
}

func TestReportingAnnotationPredicate_Create(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{name: "reporting enabled", annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"}, want: true},
		{name: "checks enabled", annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"}, want: true},
		{name: "both enabled", annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled", reporting.AnnotationGitHubChecks: "enabled"}, want: true},
		{name: "reporting disabled value", annotations: map[string]string{reporting.AnnotationGitHubReporting: "disabled"}, want: false},
		{name: "missing annotation", annotations: nil, want: false},
		{name: "unrelated annotations only", annotations: map[string]string{"other": "value"}, want: false},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			if got := pred.Create(event.CreateEvent{Object: task}); got != tt.want {
				t.Errorf("Create(%v) = %v, want %v", tt.annotations, got, tt.want)
			}
		})
	}
}

func TestReportingAnnotationPredicate_Update(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		oldPhase    kelos.TaskPhase
		newPhase    kelos.TaskPhase
		want        bool
	}{
		{
			name:        "enabled, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "enabled, phase unchanged",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelos.TaskPhaseRunning,
			newPhase:    kelos.TaskPhaseRunning,
			want:        false,
		},
		{
			name:        "checks only, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"},
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "missing annotation, phase changed",
			annotations: nil,
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseSucceeded,
			want:        false,
		},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldTask := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelos.TaskStatus{Phase: tt.oldPhase},
			}
			newTask := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelos.TaskStatus{Phase: tt.newPhase},
			}
			if got := pred.Update(event.UpdateEvent{ObjectOld: oldTask, ObjectNew: newTask}); got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}
