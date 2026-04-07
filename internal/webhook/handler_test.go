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
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

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
	githubPRBranchFetcher = func(ctx context.Context, prAPIURL string) (string, error) {
		return "feature-branch", nil
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
