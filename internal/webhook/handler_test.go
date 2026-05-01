package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// newGenericTestHandler creates a WebhookHandler for generic webhooks backed by a fake client.
func newGenericTestHandler(t *testing.T, objs ...client.Object) *WebhookHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelosv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelosv1alpha1.TaskSpawner{}).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(fakeClient)
	if err != nil {
		t.Fatal(err)
	}

	return &WebhookHandler{
		client:        fakeClient,
		source:        GenericSource,
		log:           logr.Discard(),
		taskBuilder:   tb,
		secret:        nil, // Generic source uses per-source secrets
		deliveryCache: NewDeliveryCache(context.Background()),
	}
}

const testSecret = "test-webhook-secret"

// signPayload computes the GitHub-style HMAC-SHA256 signature for a payload.
func signPayload(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// newTestHandler creates a WebhookHandler backed by a fake client with the given objects.
func newTestHandler(t *testing.T, objs ...client.Object) *WebhookHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelosv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelosv1alpha1.TaskSpawner{}).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(fakeClient)
	if err != nil {
		t.Fatal(err)
	}

	return &WebhookHandler{
		client:        fakeClient,
		source:        GitHubSource,
		log:           logr.Discard(),
		taskBuilder:   tb,
		secret:        []byte(testSecret),
		deliveryCache: NewDeliveryCache(context.Background()),
	}
}

// issuesPayload is a minimal valid GitHub issues webhook payload.
const issuesPayload = `{
	"action": "opened",
	"sender": {"login": "testuser"},
	"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
	"issue": {
		"number": 42,
		"title": "Test Issue",
		"body": "Test body",
		"html_url": "https://github.com/org/repo/issues/42",
		"state": "open",
		"labels": []
	}
}`

func TestServeHTTP_RejectsNonPOST(t *testing.T) {
	handler := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestServeHTTP_RejectsInvalidSignature(t *testing.T) {
	handler := newTestHandler(t)

	payload := []byte(issuesPayload)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, "sha256=invalid")
	req.Header.Set(GitHubDeliveryHeader, "test-delivery-1")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestServeHTTP_AcceptsValidSignature(t *testing.T) {
	handler := newTestHandler(t)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "test-delivery-2")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestServeHTTP_DuplicateDeliveryIsIdempotent(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-gh-spawner",
			Namespace: "default",
			UID:       "dedup-gh-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))
	deliveryID := "duplicate-delivery-id"

	// First request — should create a task
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, deliveryID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("First request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task after first request, got %d", len(taskList.Items))
	}

	// Second request with same delivery ID — dedup should prevent a second task
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, deliveryID)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Duplicate request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Errorf("Expected still 1 task after duplicate request, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_CreatesTaskForMatchingSpawner(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "test-uid-123",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues"},
					Filters: []kelosv1alpha1.GitHubWebhookFilter{
						{
							Event:  "issues",
							Action: "opened",
						},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "create-task-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify the task was created
	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Namespace != "default" {
		t.Errorf("Expected task namespace 'default', got %q", task.Namespace)
	}
	if task.Labels["kelos.dev/taskspawner"] != "test-spawner" {
		t.Errorf("Expected taskspawner label 'test-spawner', got %q", task.Labels["kelos.dev/taskspawner"])
	}
	if task.Spec.Prompt != "Test Issue" {
		t.Errorf("Expected prompt 'Test Issue', got %q", task.Spec.Prompt)
	}
	// Verify owner reference was set by BuildTask
	if len(task.OwnerReferences) != 1 {
		t.Fatalf("Expected 1 owner reference, got %d", len(task.OwnerReferences))
	}
	if task.OwnerReferences[0].Name != "test-spawner" {
		t.Errorf("Expected owner ref name 'test-spawner', got %q", task.OwnerReferences[0].Name)
	}
	if task.OwnerReferences[0].Kind != "TaskSpawner" {
		t.Errorf("Expected owner ref kind 'TaskSpawner', got %q", task.OwnerReferences[0].Kind)
	}
}

func TestServeHTTP_StampsReportingAnnotationsWhenEnabled(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "reporting-spawner",
			Namespace: "default",
			UID:       "reporting-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues"},
					Reporting: &kelosv1alpha1.GitHubReporting{
						Enabled: true,
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "reporting-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" {
		t.Errorf("Expected github-reporting 'enabled', got %q", task.Annotations[reporting.AnnotationGitHubReporting])
	}
	if task.Annotations[reporting.AnnotationSourceKind] != "issue" {
		t.Errorf("Expected source-kind 'issue', got %q", task.Annotations[reporting.AnnotationSourceKind])
	}
	if task.Annotations[reporting.AnnotationSourceNumber] != "42" {
		t.Errorf("Expected source-number '42', got %q", task.Annotations[reporting.AnnotationSourceNumber])
	}
	if task.Annotations[reporting.AnnotationSourceOwner] != "org" {
		t.Errorf("Expected source-owner 'org', got %q", task.Annotations[reporting.AnnotationSourceOwner])
	}
	if task.Annotations[reporting.AnnotationSourceRepo] != "repo" {
		t.Errorf("Expected source-repo 'repo', got %q", task.Annotations[reporting.AnnotationSourceRepo])
	}
}

func TestServeHTTP_NoReportingAnnotationsWhenDisabled(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-reporting-spawner",
			Namespace: "default",
			UID:       "no-reporting-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "no-reporting-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if _, ok := task.Annotations[reporting.AnnotationGitHubReporting]; ok {
		t.Error("Expected no github-reporting annotation when reporting is not enabled")
	}
}

func TestServeHTTP_ReportingAnnotationsPullRequest(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-reporting-spawner",
			Namespace: "default",
			UID:       "pr-reporting-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"pull_request"},
					Reporting: &kelosv1alpha1.GitHubReporting{
						Enabled: true,
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(`{
		"action": "opened",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"pull_request": {
			"number": 99,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/org/repo/pull/99",
			"state": "open",
			"head": {"ref": "feature-branch"}
		}
	}`)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "pull_request")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "pr-reporting-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" {
		t.Errorf("Expected github-reporting 'enabled', got %q", task.Annotations[reporting.AnnotationGitHubReporting])
	}
	if task.Annotations[reporting.AnnotationSourceKind] != "pull-request" {
		t.Errorf("Expected source-kind 'pull-request', got %q", task.Annotations[reporting.AnnotationSourceKind])
	}
	if task.Annotations[reporting.AnnotationSourceNumber] != "99" {
		t.Errorf("Expected source-number '99', got %q", task.Annotations[reporting.AnnotationSourceNumber])
	}
	if task.Annotations[reporting.AnnotationSourceOwner] != "org" {
		t.Errorf("Expected source-owner 'org', got %q", task.Annotations[reporting.AnnotationSourceOwner])
	}
	if task.Annotations[reporting.AnnotationSourceRepo] != "repo" {
		t.Errorf("Expected source-repo 'repo', got %q", task.Annotations[reporting.AnnotationSourceRepo])
	}
}

func TestWebhookSourceKind(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		eventData *GitHubEventData
		want      string
	}{
		{
			name:      "issues event",
			eventType: "issues",
			eventData: &GitHubEventData{Event: "issues"},
			want:      "issue",
		},
		{
			name:      "pull_request event",
			eventType: "pull_request",
			eventData: &GitHubEventData{Event: "pull_request"},
			want:      "pull-request",
		},
		{
			name:      "pull_request_review event",
			eventType: "pull_request_review",
			eventData: &GitHubEventData{Event: "pull_request_review"},
			want:      "pull-request",
		},
		{
			name:      "issue_comment on issue",
			eventType: "issue_comment",
			eventData: &GitHubEventData{Event: "issue_comment"},
			want:      "issue",
		},
		{
			name:      "issue_comment on PR",
			eventType: "issue_comment",
			eventData: &GitHubEventData{Event: "issue_comment", PullRequestAPIURL: "https://api.github.com/repos/o/r/pulls/1"},
			want:      "pull-request",
		},
		{
			name:      "push event",
			eventType: "push",
			eventData: &GitHubEventData{Event: "push"},
			want:      "issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := webhookSourceKind(tt.eventType, tt.eventData)
			if got != tt.want {
				t.Errorf("webhookSourceKind(%q) = %q, want %q", tt.eventType, got, tt.want)
			}
		})
	}
}

func TestServeHTTP_SkipsNonMatchingSpawner(t *testing.T) {
	// Spawner only listens for pull_request events, not issues
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-only-spawner",
			Namespace: "default",
			UID:       "test-uid-456",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"pull_request"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "no-match-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify no task was created
	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_SkipsSuspendedSpawner(t *testing.T) {
	suspended := true
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suspended-spawner",
			Namespace: "default",
			UID:       "test-uid-789",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			Suspend: &suspended,
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "suspended-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify no task was created
	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for suspended spawner, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_MaxConcurrencyDropsEvent(t *testing.T) {
	maxConcurrency := int32(1)
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "limited-spawner",
			Namespace: "default",
			UID:       "test-uid-max",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			MaxConcurrency: &maxConcurrency,
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
		Status: kelosv1alpha1.TaskSpawnerStatus{
			ActiveTasks: 1, // Already at max
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "max-concurrency-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify no task was created
	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks when at max concurrency, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_RepositoryFilterRejectsWrongRepo(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "repo-filtered-spawner",
			Namespace: "default",
			UID:       "test-uid-repo",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events:     []string{"issues"},
					Repository: "other-org/other-repo",
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	// issuesPayload has repository "org/repo", spawner expects "other-org/other-repo"
	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "repo-filter-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify no task was created
	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for wrong repo, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_IssueCommentOnPR_EnrichesBranch(t *testing.T) {
	// Swap the fetcher to return a known branch
	orig := githubPRBranchFetcher
	defer func() { githubPRBranchFetcher = orig }()
	githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (githubPRHeadInfo, error) {
		return githubPRHeadInfo{Branch: "feature-branch", SHA: "enriched-sha-456"}, nil
	}

	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-comment-spawner",
			Namespace: "default",
			UID:       "test-uid-branch",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issue_comment"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "Review PR on branch {{.Branch}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(`{
		"action": "created",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"issue": {
			"number": 42,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/org/repo/pull/42",
			"state": "open",
			"pull_request": {
				"url": "https://api.github.com/repos/org/repo/pulls/42",
				"html_url": "https://github.com/org/repo/pull/42"
			}
		},
		"comment": {"body": "/review"}
	}`)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issue_comment")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "branch-enrich-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Spec.Prompt != "Review PR on branch feature-branch" {
		t.Errorf("Expected prompt with enriched branch, got %q", task.Spec.Prompt)
	}
}

func TestSpawnerNeedsChangedFiles(t *testing.T) {
	tests := []struct {
		name    string
		spawner *kelosv1alpha1.TaskSpawner
		want    bool
	}{
		{
			name: "filePatterns in filter",
			spawner: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"push"},
							Filters: []kelosv1alpha1.GitHubWebhookFilter{
								{
									Event: "push",
									FilePatterns: &kelosv1alpha1.FilePatterns{
										Include: []string{"*.go"},
									},
								},
							},
						},
					},
					TaskTemplate: kelosv1alpha1.TaskTemplate{},
				},
			},
			want: true,
		},
		{
			name: "no filePatterns in filters",
			spawner: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"push"},
						},
					},
					TaskTemplate: kelosv1alpha1.TaskTemplate{
						PromptTemplate: "{{.Title}}",
					},
				},
			},
			want: false,
		},
		{
			name: "nil GitHubWebhook",
			spawner: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When:         kelosv1alpha1.When{},
					TaskTemplate: kelosv1alpha1.TaskTemplate{},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spawnerNeedsChangedFiles(tt.spawner)
			if got != tt.want {
				t.Errorf("spawnerNeedsChangedFiles() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Linear HTTP handler tests ---

// newLinearTestHandler creates a WebhookHandler for Linear backed by a fake client.
func newLinearTestHandler(t *testing.T, objs ...client.Object) *WebhookHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelosv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelosv1alpha1.TaskSpawner{}).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(fakeClient)
	if err != nil {
		t.Fatal(err)
	}

	return &WebhookHandler{
		client:        fakeClient,
		source:        LinearSource,
		log:           logr.Discard(),
		taskBuilder:   tb,
		secret:        []byte(testSecret),
		deliveryCache: NewDeliveryCache(context.Background()),
	}
}

const linearIssuePayload = `{
	"type": "Issue",
	"action": "create",
	"data": {
		"id": "LIN-42",
		"title": "Linear Test Issue",
		"state": {"name": "Todo"},
		"labels": [{"name": "agent-task"}]
	}
}`

func TestLinearServeHTTP_RejectsInvalidSignature(t *testing.T) {
	handler := newLinearTestHandler(t)

	payload := []byte(linearIssuePayload)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(LinearSignatureHeader, "invalid")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestLinearServeHTTP_AcceptsValidSignature(t *testing.T) {
	handler := newLinearTestHandler(t)

	payload := []byte(linearIssuePayload)
	sig := computeHMAC(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(LinearSignatureHeader, sig)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestLinearServeHTTP_CreatesTaskForMatchingSpawner(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "linear-spawner",
			Namespace: "default",
			UID:       "linear-uid-123",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				LinearWebhook: &kelosv1alpha1.LinearWebhook{
					Types: []string{"Issue"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newLinearTestHandler(t, spawner)

	payload := []byte(linearIssuePayload)
	sig := computeHMAC(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(LinearSignatureHeader, sig)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify the task was created
	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Labels["kelos.dev/taskspawner"] != "linear-spawner" {
		t.Errorf("Expected taskspawner label 'linear-spawner', got %q", task.Labels["kelos.dev/taskspawner"])
	}
	if task.Spec.Prompt != "Linear Test Issue" {
		t.Errorf("Expected prompt 'Linear Test Issue', got %q", task.Spec.Prompt)
	}
	// Task name should use the parsed type "issue" not the generic "linear"
	if !strings.Contains(task.Name, "issue") {
		t.Errorf("Expected task name to contain 'issue', got %q", task.Name)
	}
}

func TestLinearServeHTTP_DuplicateBodyIsIdempotent(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-spawner",
			Namespace: "default",
			UID:       "dedup-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				LinearWebhook: &kelosv1alpha1.LinearWebhook{
					Types: []string{"Issue"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newLinearTestHandler(t, spawner)

	payload := []byte(linearIssuePayload)
	sig := computeHMAC(payload, []byte(testSecret))

	// First request — should create a task
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(LinearSignatureHeader, sig)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("First request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task after first request, got %d", len(taskList.Items))
	}

	// Second request with identical body — dedup via body hash, no new task
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(LinearSignatureHeader, sig)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Duplicate request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Errorf("Expected still 1 task after duplicate request, got %d", len(taskList.Items))
	}
}

// --- Generic webhook HTTP handler tests ---

const genericNotionPayload = `{
	"type": "page.updated",
	"data": {
		"id": "page-abc-123",
		"properties": {
			"Name": {"title": [{"plain_text": "Fix login bug"}]},
			"Status": {"select": {"name": "Ready for AI"}},
			"Description": {"rich_text": [{"plain_text": "Users report login failures"}]}
		}
	}
}`

func TestGenericServeHTTP_RejectsMissingSourcePath(t *testing.T) {
	handler := newGenericTestHandler(t)

	payload := []byte(genericNotionPayload)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected %d for missing source path, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestGenericServeHTTP_AcceptsUnknownSource(t *testing.T) {
	handler := newGenericTestHandler(t)

	payload := []byte(genericNotionPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/unknown", bytes.NewReader(payload))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected %d for unknown source with no matching spawners, got %d", http.StatusOK, rr.Code)
	}
}

func TestGenericServeHTTP_CreatesTaskForMatchingSpawner(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-handler",
			Namespace: "default",
			UID:       "notion-uid-123",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id":    "$.data.id",
						"title": "$.data.properties.Name.title[0].plain_text",
					},
					Filters: []kelosv1alpha1.GenericWebhookFilter{
						{
							Field: "$.type",
							Value: strPtr("page.updated"),
						},
						{
							Field: "$.data.properties.Status.select.name",
							Value: strPtr("Ready for AI"),
						},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "{{.title}}",
			},
		},
	}

	handler := newGenericTestHandler(t, spawner)

	payload := []byte(genericNotionPayload)

	req := httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Labels["kelos.dev/taskspawner"] != "notion-handler" {
		t.Errorf("Expected taskspawner label 'notion-handler', got %q", task.Labels["kelos.dev/taskspawner"])
	}
	if task.Spec.Prompt != "Fix login bug" {
		t.Errorf("Expected prompt 'Fix login bug', got %q", task.Spec.Prompt)
	}
}

func TestGenericServeHTTP_SkipsNonMatchingFilters(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-handler",
			Namespace: "default",
			UID:       "notion-uid-456",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
					Filters: []kelosv1alpha1.GenericWebhookFilter{
						{
							Field: "$.type",
							Value: strPtr("page.deleted"), // Won't match
						},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
			},
		},
	}

	handler := newGenericTestHandler(t, spawner)

	payload := []byte(genericNotionPayload) // type is "page.updated"

	req := httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for non-matching filter, got %d", len(taskList.Items))
	}
}

func TestGenericServeHTTP_SkipsWrongSourceName(t *testing.T) {
	// Spawner listens for "notion" but webhook comes to /webhook/sentry
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-handler",
			Namespace: "default",
			UID:       "notion-uid-789",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
			},
		},
	}

	handler := newGenericTestHandler(t, spawner)

	payload := []byte(`{"action":"created"}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook/sentry", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for wrong source name, got %d", len(taskList.Items))
	}
}

func TestGenericServeHTTP_DuplicateBodyIsIdempotent(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-generic",
			Namespace: "default",
			UID:       "dedup-generic-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "test",
			},
		},
	}

	handler := newGenericTestHandler(t, spawner)

	payload := []byte(genericNotionPayload)

	// First request
	req := httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("First request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task after first request, got %d", len(taskList.Items))
	}

	// Second request with identical body — should be deduped
	req = httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Duplicate request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Errorf("Expected still 1 task after duplicate request, got %d", len(taskList.Items))
	}
}

func TestGenericServeHTTP_DuplicateIDDifferentBodyIsIdempotent(t *testing.T) {
	// Same logical event (same mapped id) but different JSON encoding should
	// still deduplicate via the id-based delivery ID, not the body hash.
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-id-generic",
			Namespace: "default",
			UID:       "dedup-id-generic-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "test",
			},
		},
	}

	handler := newGenericTestHandler(t, spawner)

	// First request
	payload1 := []byte(`{"data":{"id":"page-abc-123"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload1))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("First request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task after first request, got %d", len(taskList.Items))
	}

	// Second request — same id but different JSON (extra field, different whitespace)
	payload2 := []byte(`{ "data" : { "id" : "page-abc-123" , "extra": true } }`)
	req = httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload2))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Duplicate request: expected %d, got %d", http.StatusOK, rr.Code)
	}

	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Errorf("Expected still 1 task after retry with same id, got %d", len(taskList.Items))
	}
}

func TestGenericServeHTTP_MultipleSpawnersNoFieldLeak(t *testing.T) {
	// Spawner A maps "severity" from the payload; Spawner B does not.
	// Before the fix, Fields were never reset between spawner iterations,
	// so Spawner B's task template would see Spawner A's "severity" field.
	spawnerA := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-a",
			Namespace: "default",
			UID:       "notion-a-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id":       "$.data.id",
						"title":    "$.data.properties.Name.title[0].plain_text",
						"severity": "$.data.properties.Status.select.name",
					},
					Filters: []kelosv1alpha1.GenericWebhookFilter{
						{Field: "$.type", Value: strPtr("page.updated")},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "A:{{.title}}",
			},
		},
	}

	spawnerB := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-b",
			Namespace: "default",
			UID:       "notion-b-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id":    "$.data.id",
						"title": "$.data.properties.Name.title[0].plain_text",
					},
					Filters: []kelosv1alpha1.GenericWebhookFilter{
						{Field: "$.type", Value: strPtr("page.updated")},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "B:{{.title}}",
			},
		},
	}

	handler := newGenericTestHandler(t, spawnerA, spawnerB)

	payload := []byte(genericNotionPayload)
	req := httptest.NewRequest(http.MethodPost, "/webhook/notion", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(taskList.Items))
	}

	// Verify Spawner B's task does not contain Spawner A's "severity" field
	// by checking that parsed.Generic.Fields only has keys from B's fieldMapping.
	// We verify indirectly: both tasks should have correct prompts and the
	// GenericEventData should have been reset between calls.
	for _, task := range taskList.Items {
		if task.Labels["kelos.dev/taskspawner"] == "notion-b" {
			if task.Spec.Prompt != "B:Fix login bug" {
				t.Errorf("Expected prompt 'B:Fix login bug', got %q", task.Spec.Prompt)
			}
		}
		if task.Labels["kelos.dev/taskspawner"] == "notion-a" {
			if task.Spec.Prompt != "A:Fix login bug" {
				t.Errorf("Expected prompt 'A:Fix login bug', got %q", task.Spec.Prompt)
			}
		}
	}
}

func TestGenericServeHTTP_HyphenatedSourceName(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tool-handler",
			Namespace: "default",
			UID:       "my-tool-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GenericWebhook: &kelosv1alpha1.GenericWebhook{
					Source: "my-tool",
					FieldMapping: map[string]string{
						"id": "$.id",
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "test",
			},
		},
	}

	handler := newGenericTestHandler(t, spawner)

	payload := []byte(`{"id":"123"}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook/my-tool", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task for hyphenated source, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_ChecksAnnotationsForPRWebhook(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checks-spawner",
			Namespace: "default",
			UID:       "checks-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"pull_request"},
					Reporting: &kelosv1alpha1.GitHubReporting{
						Enabled: true,
						Checks:  &kelosv1alpha1.GitHubChecksReporting{Name: "My Check"},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(`{
		"action": "opened",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"pull_request": {
			"number": 42,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/org/repo/pull/42",
			"state": "open",
			"head": {"ref": "feature-branch", "sha": "deadbeef123"}
		}
	}`)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "pull_request")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "checks-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" {
		t.Errorf("Expected github-reporting 'enabled', got %q", task.Annotations[reporting.AnnotationGitHubReporting])
	}
	if task.Annotations[reporting.AnnotationGitHubChecks] != "enabled" {
		t.Errorf("Expected github-checks 'enabled', got %q", task.Annotations[reporting.AnnotationGitHubChecks])
	}
	if task.Annotations[reporting.AnnotationSourceSHA] != "deadbeef123" {
		t.Errorf("Expected source-sha 'deadbeef123', got %q", task.Annotations[reporting.AnnotationSourceSHA])
	}
	if task.Annotations[reporting.AnnotationGitHubCheckName] != "My Check" {
		t.Errorf("Expected check name 'My Check', got %q", task.Annotations[reporting.AnnotationGitHubCheckName])
	}
	if task.Annotations[reporting.AnnotationSourceKind] != "pull-request" {
		t.Errorf("Expected source-kind 'pull-request', got %q", task.Annotations[reporting.AnnotationSourceKind])
	}
}

func TestServeHTTP_ChecksAnnotationsSkippedForNonPRWebhook(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checks-issue-spawner",
			Namespace: "default",
			UID:       "checks-issue-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"issues", "pull_request"},
					Reporting: &kelosv1alpha1.GitHubReporting{
						Enabled: true,
						Checks:  &kelosv1alpha1.GitHubChecksReporting{},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(issuesPayload)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "issues")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "checks-issue-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	// Comment reporting should be enabled
	if task.Annotations[reporting.AnnotationGitHubReporting] != "enabled" {
		t.Errorf("Expected github-reporting 'enabled', got %q", task.Annotations[reporting.AnnotationGitHubReporting])
	}
	// Checks should NOT be stamped for issue events even when checks is configured
	if _, ok := task.Annotations[reporting.AnnotationGitHubChecks]; ok {
		t.Error("Expected no github-checks annotation for issue event")
	}
	if _, ok := task.Annotations[reporting.AnnotationSourceSHA]; ok {
		t.Error("Expected no source-sha annotation for issue event")
	}
}

func TestServeHTTP_ChecksOnlyWithoutCommentReporting(t *testing.T) {
	spawner := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checks-only-spawner",
			Namespace: "default",
			UID:       "checks-only-uid",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			When: kelosv1alpha1.When{
				GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
					Events: []string{"pull_request"},
					Reporting: &kelosv1alpha1.GitHubReporting{
						Checks: &kelosv1alpha1.GitHubChecksReporting{},
					},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: kelosv1alpha1.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
	}

	handler := newTestHandler(t, spawner)

	payload := []byte(`{
		"action": "opened",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"pull_request": {
			"number": 10,
			"title": "Checks Only PR",
			"body": "",
			"html_url": "https://github.com/org/repo/pull/10",
			"state": "open",
			"head": {"ref": "feature", "sha": "aaa111bbb222"}
		}
	}`)
	sig := signPayload(payload, []byte(testSecret))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "pull_request")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "checks-only-delivery")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var taskList kelosv1alpha1.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	// Comment reporting should NOT be set
	if _, ok := task.Annotations[reporting.AnnotationGitHubReporting]; ok {
		t.Error("Expected no github-reporting annotation when Enabled is false")
	}
	// Checks should be set
	if task.Annotations[reporting.AnnotationGitHubChecks] != "enabled" {
		t.Errorf("Expected github-checks 'enabled', got %q", task.Annotations[reporting.AnnotationGitHubChecks])
	}
	if task.Annotations[reporting.AnnotationSourceSHA] != "aaa111bbb222" {
		t.Errorf("Expected source-sha 'aaa111bbb222', got %q", task.Annotations[reporting.AnnotationSourceSHA])
	}
}
