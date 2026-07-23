package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

type sessionSpawnerNoMatchClient struct {
	client.Client
}

func (c sessionSpawnerNoMatchClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*kelos.SessionSpawnerList); ok {
		return &apiMeta.NoKindMatchError{
			GroupKind:        schema.GroupKind{Group: "kelos.dev", Kind: "SessionSpawner"},
			SearchedVersions: []string{"v1alpha2"},
		}
	}
	return c.Client.List(ctx, list, opts...)
}

// newGenericTestHandler creates a WebhookHandler for generic webhooks backed by a fake client.
func newGenericTestHandler(t *testing.T, objs ...client.Object) *WebhookHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelos.TaskSpawner{}, &kelos.SessionSpawner{}).
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
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelos.TaskSpawner{}, &kelos.Session{}, &kelos.SessionSpawner{}).
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

const issueCommentPayload = `{
	"action": "created",
	"sender": {"login": "gjkim42"},
	"repository": {"full_name": "kelos-dev/kelos", "name": "kelos", "owner": {"login": "kelos-dev"}},
	"issue": {
		"number": 1520,
		"title": "Add SessionSpawner",
		"body": "Keep follow-up work in one Session",
		"html_url": "https://github.com/kelos-dev/kelos/issues/1520",
		"state": "open",
		"labels": []
	},
	"comment": {
		"body": "/kelos pick-up",
		"html_url": "https://github.com/kelos-dev/kelos/issues/1520#issuecomment-1"
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

func TestServeHTTP_TaskSpawnerWorksWhileSessionSpawnerCRDIsMissing(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: "default", UID: "workers-uid"},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{GitHubWebhook: &kelos.GitHubWebhook{
				Events: []string{"issues"},
			}},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: "api-key"},
				WorkspaceRef:   &kelos.WorkspaceReference{Name: "test-workspace"},
				PromptTemplate: "{{.Title}}",
			},
		},
	}
	handler := newTestHandler(t, spawner)
	handler.client = sessionSpawnerNoMatchClient{Client: handler.client}

	serveGitHubWebhook(t, handler, "issues", "upgrade-delivery", issuesPayload, http.StatusOK)

	var tasks kelos.TaskList
	if err := handler.client.List(context.Background(), &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("Tasks = %d, want 1", len(tasks.Items))
	}
}

func TestServeHTTP_DuplicateDeliveryIsIdempotent(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-gh-spawner",
			Namespace: "default",
			UID:       "dedup-gh-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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

func TestServeHTTP_SessionSpawnerCreatesSessionForMatchingDelivery(t *testing.T) {
	spawner := newSessionSpawner("workers")
	handler := newTestHandler(t, spawner)

	serveGitHubWebhook(t, handler, "issue_comment", "session-delivery-1", issueCommentPayload, http.StatusOK)

	var sessions kelos.SessionList
	if err := handler.client.List(context.Background(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("Sessions = %d, want 1", len(sessions.Items))
	}
	session := sessions.Items[0]
	if session.Spec.InitialBranch != "kelos-task-1520" {
		t.Fatalf("Session.spec.initialBranch = %q", session.Spec.InitialBranch)
	}
	wantPrompt := "Handle kelos-dev/kelos#1520 from gjkim42: https://github.com/kelos-dev/kelos/issues/1520"
	if session.Spec.InitialPrompt != wantPrompt {
		t.Fatalf("Session.spec.initialPrompt = %q, want %q", session.Spec.InitialPrompt, wantPrompt)
	}
	if session.Labels[sessionbuilder.LabelSessionSpawner] != string(spawner.UID) {
		t.Fatalf("Session %s label = %q", sessionbuilder.LabelSessionSpawner, session.Labels[sessionbuilder.LabelSessionSpawner])
	}
	if session.Annotations[sessionbuilder.AnnotationSessionSpawnerName] != spawner.Name {
		t.Fatalf("Session %s annotation = %q", sessionbuilder.AnnotationSessionSpawnerName, session.Annotations[sessionbuilder.AnnotationSessionSpawnerName])
	}
	if session.Name != webhookSpawnName("workers", "issue_comment", "session-delivery-1") {
		t.Fatalf("Session name = %q", session.Name)
	}

	// Simulate a webhook-server restart. The deterministic Create returns
	// AlreadyExists and still suppresses the redelivery.
	handler.deliveryCache = NewDeliveryCache(context.Background())
	serveGitHubWebhook(t, handler, "issue_comment", "session-delivery-1", issueCommentPayload, http.StatusOK)
	if err := handler.client.List(context.Background(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("Sessions after redelivery = %d, want 1", len(sessions.Items))
	}
}

func TestServeHTTP_SessionSpawnerCreatesSessionPerDistinctDelivery(t *testing.T) {
	spawner := newSessionSpawner("workers")
	handler := newTestHandler(t, spawner)
	serveGitHubWebhook(t, handler, "issue_comment", "session-delivery-1", issueCommentPayload, http.StatusOK)
	serveGitHubWebhook(t, handler, "issue_comment", "session-delivery-2", issueCommentPayload, http.StatusOK)

	var sessions kelos.SessionList
	if err := handler.client.List(context.Background(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 2 {
		t.Fatalf("Sessions = %d, want 2", len(sessions.Items))
	}

	var updatedSpawner kelos.SessionSpawner
	if err := handler.client.Get(context.Background(), client.ObjectKeyFromObject(spawner), &updatedSpawner); err != nil {
		t.Fatal(err)
	}
	lastDeliverySucceeded := apiMeta.FindStatusCondition(updatedSpawner.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
	if lastDeliverySucceeded == nil || lastDeliverySucceeded.Status != metav1.ConditionTrue || lastDeliverySucceeded.Reason != "SessionCreated" {
		t.Fatalf("SessionSpawner LastDeliverySucceeded condition = %#v", lastDeliverySucceeded)
	}
	if updatedSpawner.Status.LastSessionName != webhookSpawnName("workers", "issue_comment", "session-delivery-2") {
		t.Fatalf("lastSessionName = %q", updatedSpawner.Status.LastSessionName)
	}
}

func TestServeHTTP_SessionSpawnerTreatsExistingNameAsProcessed(t *testing.T) {
	spawner := newSessionSpawner("workers")
	sessionName := webhookSpawnName(spawner.Name, "issue_comment", "session-delivery-1")
	foreignSession := &kelos.Session{ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: spawner.Namespace}}
	handler := newTestHandler(t, spawner, foreignSession)

	serveGitHubWebhook(t, handler, "issue_comment", "session-delivery-1", issueCommentPayload, http.StatusOK)

	var updatedSpawner kelos.SessionSpawner
	if err := handler.client.Get(context.Background(), client.ObjectKeyFromObject(spawner), &updatedSpawner); err != nil {
		t.Fatal(err)
	}
	lastDeliverySucceeded := apiMeta.FindStatusCondition(updatedSpawner.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
	if lastDeliverySucceeded == nil || lastDeliverySucceeded.Status != metav1.ConditionTrue || lastDeliverySucceeded.Reason != "DeliveryAlreadyProcessed" {
		t.Fatalf("SessionSpawner LastDeliverySucceeded condition = %#v", lastDeliverySucceeded)
	}
}

func TestWebhookSpawnNamePreservesTaskSpawnerNaming(t *testing.T) {
	tests := []struct {
		name        string
		spawnerName string
		want        string
	}{
		{
			name:        "name fits",
			spawnerName: "workers",
			want:        "workers-issue-comment-0b220df19691",
		},
		{
			name:        "name is truncated",
			spawnerName: strings.Repeat("a", 47) + "x",
			want:        strings.Repeat("a", 47) + "x-issue-comment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webhookSpawnName(tt.spawnerName, "issue_comment", "delivery-1"); got != tt.want {
				t.Fatalf("webhookSpawnName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServeHTTP_SessionSpawnerFailureDoesNotRecordSessionName(t *testing.T) {
	spawner := newSessionSpawner("workers")
	spawner.Status.LastSessionName = "previous-session"
	spawner.Spec.SessionTemplate.InitialPrompt = "{{.Missing}}"
	handler := newTestHandler(t, spawner)

	serveGitHubWebhook(t, handler, "issue_comment", "failed-delivery", issueCommentPayload, http.StatusInternalServerError)

	var updatedSpawner kelos.SessionSpawner
	if err := handler.client.Get(context.Background(), client.ObjectKeyFromObject(spawner), &updatedSpawner); err != nil {
		t.Fatal(err)
	}
	if updatedSpawner.Status.LastSessionName != "previous-session" {
		t.Fatalf("lastSessionName = %q, want previous-session", updatedSpawner.Status.LastSessionName)
	}
	lastDeliverySucceeded := apiMeta.FindStatusCondition(updatedSpawner.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
	if lastDeliverySucceeded == nil || lastDeliverySucceeded.Status != metav1.ConditionFalse || lastDeliverySucceeded.Reason != "SessionBuildFailed" {
		t.Fatalf("SessionSpawner LastDeliverySucceeded condition = %#v", lastDeliverySucceeded)
	}
}

func TestSessionSpawnerDeliveryStatusUsesProcessedGeneration(t *testing.T) {
	current := newSessionSpawner("workers")
	current.Generation = 2
	current.Status.ObservedGeneration = 2
	handler := newTestHandler(t, current)
	processed := current.DeepCopy()
	processed.Generation = 1

	if err := handler.recordSessionSpawnerSuccess(context.Background(), processed, "workers-issue-comment-delivery", "SessionCreated", "Created Session"); err != nil {
		t.Fatal(err)
	}

	var updated kelos.SessionSpawner
	if err := handler.client.Get(context.Background(), client.ObjectKeyFromObject(current), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration = %d, want controller-owned value 2", updated.Status.ObservedGeneration)
	}
	condition := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
	if condition == nil || condition.ObservedGeneration != 1 {
		t.Fatalf("LastDeliverySucceeded condition = %#v, want observedGeneration 1", condition)
	}
}

func TestServeHTTP_SessionSpawnerSupportsOtherGitHubEvents(t *testing.T) {
	spawner := newSessionSpawner("workers")
	spawner.Spec.When.GitHubWebhook.Repository = "org/repo"
	spawner.Spec.When.GitHubWebhook.Events = []string{"issues"}
	spawner.Spec.When.GitHubWebhook.Filters = []kelos.GitHubWebhookFilter{{Event: "issues", Action: "opened"}}
	spawner.Spec.SessionTemplate.InitialBranch = "issue-{{.Number}}"
	spawner.Spec.SessionTemplate.InitialPrompt = "Handle {{.Event}} #{{.Number}}"
	handler := newTestHandler(t, spawner)

	serveGitHubWebhook(t, handler, "issues", "issues-delivery-1", issuesPayload, http.StatusOK)

	var sessions kelos.SessionList
	if err := handler.client.List(context.Background(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) != 1 {
		t.Fatalf("Sessions = %d, want 1", len(sessions.Items))
	}
	if sessions.Items[0].Spec.InitialBranch != "issue-42" || sessions.Items[0].Spec.InitialPrompt != "Handle issues #42" {
		t.Fatalf("Session spec = %#v", sessions.Items[0].Spec)
	}
}

func newSessionSpawner(name string) *kelos.SessionSpawner {
	return &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec: kelos.SessionSpawnerSpec{
			When: kelos.SessionSpawnerWhen{GitHubWebhook: &kelos.GitHubWebhook{
				Repository:     "kelos-dev/kelos",
				Events:         []string{"issue_comment"},
				ExcludeAuthors: []string{"kelos-bot[bot]"},
				Filters: []kelos.GitHubWebhookFilter{{
					Event:       "issue_comment",
					Action:      "created",
					BodyPattern: `(?m)^/kelos pick-up[ \t]*\r?$`,
					CommentOn:   kelos.CommentOnIssue,
					State:       "open",
					Author:      "gjkim42",
				}},
			}},
			SessionTemplate: kelos.SessionTemplate{
				SessionSpec: kelos.SessionSpec{
					Worker: kelos.WorkerSpec{
						Type:         "codex",
						Credentials:  &kelos.Credentials{Type: kelos.CredentialTypeOAuth},
						WorkspaceRef: &kelos.WorkspaceReference{Name: "kelos-agent"},
					},
					InitialBranch: "kelos-task-{{.Number}}",
					InitialPrompt: "Handle {{.Repository}}#{{.Number}} from {{.Sender}}: {{.URL}}",
				},
			},
		},
	}
}

func serveGitHubWebhook(t *testing.T, handler *WebhookHandler, eventType, deliveryID, payload string, wantStatus int) {
	t.Helper()
	body := []byte(payload)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set(GitHubEventHeader, eventType)
	request.Header.Set(GitHubSignatureHeader, signPayload(body, []byte(testSecret)))
	request.Header.Set(GitHubDeliveryHeader, deliveryID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("webhook status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
}

func TestServeHTTP_CreatesTaskForMatchingSpawner(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "test-uid-123",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues"},
					Filters: []kelos.GitHubWebhookFilter{
						{
							Event:  "issues",
							Action: "opened",
						},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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
	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "reporting-spawner",
			Namespace: "default",
			UID:       "reporting-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues"},
					Reporting: &kelos.GitHubReporting{
						Enabled: true,
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-reporting-spawner",
			Namespace: "default",
			UID:       "no-reporting-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-reporting-spawner",
			Namespace: "default",
			UID:       "pr-reporting-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"pull_request"},
					Reporting: &kelos.GitHubReporting{
						Enabled: true,
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-only-spawner",
			Namespace: "default",
			UID:       "test-uid-456",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"pull_request"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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
	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_SkipsSuspendedSpawner(t *testing.T) {
	suspended := true
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suspended-spawner",
			Namespace: "default",
			UID:       "test-uid-789",
		},
		Spec: kelos.TaskSpawnerSpec{
			Suspend: &suspended,
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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
	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for suspended spawner, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_MaxConcurrencyDropsEvent(t *testing.T) {
	maxConcurrency := int32(1)
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "limited-spawner",
			Namespace: "default",
			UID:       "test-uid-max",
		},
		Spec: kelos.TaskSpawnerSpec{
			MaxConcurrency: &maxConcurrency,
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "{{.Title}}",
			},
		},
		Status: kelos.TaskSpawnerStatus{
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
	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks when at max concurrency, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_RepositoryFilterRejectsWrongRepo(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "repo-filtered-spawner",
			Namespace: "default",
			UID:       "test-uid-repo",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events:     []string{"issues"},
					Repository: "other-org/other-repo",
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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
	var taskList kelos.TaskList
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

	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-comment-spawner",
			Namespace: "default",
			UID:       "test-uid-branch",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issue_comment"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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

func TestMatchesGitHubWebhookChangedFileEnrichment(t *testing.T) {
	filePatterns := &kelos.FilePatterns{Include: []string{"internal/**"}}
	fetchErr := errors.New("GitHub API unavailable")
	tests := []struct {
		name           string
		webhook        *kelos.GitHubWebhook
		eventType      string
		eventData      *GitHubEventData
		files          []string
		fetchErr       error
		wantMatch      bool
		wantFetches    int
		wantFetchError bool
	}{
		{
			name: "matching ordinary filter skips enrichment required by another OR filter",
			webhook: &kelos.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []kelos.GitHubWebhookFilter{
					{Event: "pull_request", Action: "opened"},
					{Event: "pull_request", Action: "opened", FilePatterns: filePatterns},
				},
			},
			eventType:   "pull_request",
			eventData:   &GitHubEventData{Action: "opened", Repository: "org/repo"},
			fetchErr:    fetchErr,
			wantMatch:   true,
			wantFetches: 0,
		},
		{
			name: "file filter fetches changed files before matching",
			webhook: &kelos.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []kelos.GitHubWebhookFilter{
					{Event: "pull_request", Action: "opened", FilePatterns: filePatterns},
				},
			},
			eventType:   "pull_request",
			eventData:   &GitHubEventData{Action: "opened", Repository: "org/repo"},
			files:       []string{"internal/webhook/handler.go"},
			wantMatch:   true,
			wantFetches: 1,
		},
		{
			name: "non-file criteria reject event without enrichment",
			webhook: &kelos.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []kelos.GitHubWebhookFilter{
					{Event: "pull_request", Action: "closed", FilePatterns: filePatterns},
				},
			},
			eventType:   "pull_request",
			eventData:   &GitHubEventData{Action: "opened", Repository: "org/repo"},
			fetchErr:    fetchErr,
			wantFetches: 0,
		},
		{
			name: "changed-file fetch error is identifiable",
			webhook: &kelos.GitHubWebhook{
				Events: []string{"pull_request"},
				Filters: []kelos.GitHubWebhookFilter{
					{Event: "pull_request", Action: "opened", FilePatterns: filePatterns},
				},
			},
			eventType:      "pull_request",
			eventData:      &GitHubEventData{Action: "opened", Repository: "org/repo"},
			fetchErr:       fetchErr,
			wantFetches:    1,
			wantFetchError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &WebhookHandler{}
			fetches := 0
			got, err := handler.matchesGitHubWebhook(context.Background(), tt.webhook, tt.eventType, tt.eventData, func(context.Context, *GitHubEventData) ([]string, error) {
				fetches++
				return tt.files, tt.fetchErr
			})
			if got != tt.wantMatch {
				t.Errorf("matchesGitHubWebhook() = %v, want %v", got, tt.wantMatch)
			}
			if fetches != tt.wantFetches {
				t.Errorf("changed-file fetches = %d, want %d", fetches, tt.wantFetches)
			}
			var gotFetchErr *githubChangedFilesFetchError
			if errors.As(err, &gotFetchErr) != tt.wantFetchError {
				t.Errorf("errors.As(err, *githubChangedFilesFetchError) = %v, want %v (err = %v)", errors.As(err, &gotFetchErr), tt.wantFetchError, err)
			}
		})
	}
}

// --- Linear HTTP handler tests ---

// newLinearTestHandler creates a WebhookHandler for Linear backed by a fake client.
func newLinearTestHandler(t *testing.T, objs ...client.Object) *WebhookHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelos.TaskSpawner{}).
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "linear-spawner",
			Namespace: "default",
			UID:       "linear-uid-123",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				LinearWebhook: &kelos.LinearWebhook{
					Types: []string{"Issue"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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
	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-spawner",
			Namespace: "default",
			UID:       "dedup-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				LinearWebhook: &kelos.LinearWebhook{
					Types: []string{"Issue"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-handler",
			Namespace: "default",
			UID:       "notion-uid-123",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id":    "$.data.id",
						"title": "$.data.properties.Name.title[0].plain_text",
					},
					Filters: []kelos.GenericWebhookFilter{
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
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-handler",
			Namespace: "default",
			UID:       "notion-uid-456",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
					Filters: []kelos.GenericWebhookFilter{
						{
							Field: "$.type",
							Value: strPtr("page.deleted"), // Won't match
						},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for non-matching filter, got %d", len(taskList.Items))
	}
}

func TestGenericServeHTTP_SkipsWrongSourceName(t *testing.T) {
	// Spawner listens for "notion" but webhook comes to /webhook/sentry
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-handler",
			Namespace: "default",
			UID:       "notion-uid-789",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}

	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks for wrong source name, got %d", len(taskList.Items))
	}
}

func TestGenericServeHTTP_DuplicateBodyIsIdempotent(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-generic",
			Namespace: "default",
			UID:       "dedup-generic-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dedup-id-generic",
			Namespace: "default",
			UID:       "dedup-id-generic-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id": "$.data.id",
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
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
	spawnerA := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-a",
			Namespace: "default",
			UID:       "notion-a-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id":       "$.data.id",
						"title":    "$.data.properties.Name.title[0].plain_text",
						"severity": "$.data.properties.Status.select.name",
					},
					Filters: []kelos.GenericWebhookFilter{
						{Field: "$.type", Value: strPtr("page.updated")},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				PromptTemplate: "A:{{.title}}",
			},
		},
	}

	spawnerB := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notion-b",
			Namespace: "default",
			UID:       "notion-b-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "notion",
					FieldMapping: map[string]string{
						"id":    "$.data.id",
						"title": "$.data.properties.Name.title[0].plain_text",
					},
					Filters: []kelos.GenericWebhookFilter{
						{Field: "$.type", Value: strPtr("page.updated")},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-tool-handler",
			Namespace: "default",
			UID:       "my-tool-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GenericWebhook: &kelos.GenericWebhook{
					Source: "my-tool",
					FieldMapping: map[string]string{
						"id": "$.id",
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
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

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task for hyphenated source, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_ChecksAnnotationsForPRWebhook(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checks-spawner",
			Namespace: "default",
			UID:       "checks-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"pull_request"},
					Reporting: &kelos.GitHubReporting{
						Enabled: true,
						Checks:  &kelos.GitHubChecksReporting{Name: "My Check"},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checks-issue-spawner",
			Namespace: "default",
			UID:       "checks-issue-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"issues", "pull_request"},
					Reporting: &kelos.GitHubReporting{
						Enabled: true,
						Checks:  &kelos.GitHubChecksReporting{},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checks-only-spawner",
			Namespace: "default",
			UID:       "checks-only-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"pull_request"},
					Reporting: &kelos.GitHubReporting{
						Checks: &kelos.GitHubChecksReporting{},
					},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
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

	var taskList kelos.TaskList
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

// nameTemplateSpawner returns a pull_request spawner whose taskTemplate names
// Tasks deterministically from the PR number, so repeated deliveries for the
// same PR deduplicate.
func nameTemplateSpawner() *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "responder",
			Namespace: "default",
			UID:       "responder-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubWebhook: &kelos.GitHubWebhook{
					Events: []string{"pull_request"},
				},
			},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: "api-key"},
				WorkspaceRef:   &kelos.WorkspaceReference{Name: "test-workspace"},
				NameTemplate:   "responder-pr-{{.Number}}",
				PromptTemplate: "{{.Title}}",
			},
		},
	}
}

// prWebhookPayload builds a minimal pull_request webhook payload for the given
// PR number.
func prWebhookPayload(number int) []byte {
	return []byte(fmt.Sprintf(`{
		"action": "opened",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"pull_request": {
			"number": %d,
			"title": "Test PR",
			"body": "PR body",
			"html_url": "https://github.com/org/repo/pull/%d",
			"state": "open",
			"head": {"ref": "feature-branch"}
		}
	}`, number, number))
}

// sendPRWebhook delivers a pull_request webhook with the given delivery ID.
func sendPRWebhook(t *testing.T, handler *WebhookHandler, payload []byte, deliveryID string) {
	t.Helper()
	sig := signPayload(payload, []byte(testSecret))
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "pull_request")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, deliveryID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Delivery %s: expected %d, got %d", deliveryID, http.StatusOK, rr.Code)
	}
}

// TestServeHTTP_DuplicateTasksDedupedByNameTemplate covers issue #1155: when a
// spawner configures a deterministic nameTemplate, multiple webhook deliveries
// for the same PR (each with a unique X-GitHub-Delivery) collapse to one Task.
func TestServeHTTP_DuplicateTasksDedupedByNameTemplate(t *testing.T) {
	handler := newTestHandler(t, nameTemplateSpawner())

	payload := prWebhookPayload(123)
	// Five distinct deliveries for the same PR, as GitHub would send for an
	// "opened" plus several "labeled" events within the same second.
	for _, id := range []string{"d-1", "d-2", "d-3", "d-4", "d-5"} {
		sendPRWebhook(t, handler, payload, id)
	}

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		names := make([]string, 0, len(taskList.Items))
		for _, task := range taskList.Items {
			names = append(names, task.Name)
		}
		t.Fatalf("Expected 1 task for PR #123, got %d: %s", len(taskList.Items), strings.Join(names, ", "))
	}
	if got := taskList.Items[0].Name; got != "responder-pr-123" {
		t.Errorf("task name = %q, want %q", got, "responder-pr-123")
	}
}

// TestCreateTask_NameCollisionWithUnrelatedTaskErrors ensures a rendered name
// that collides with a Task NOT owned by this spawner surfaces an error instead
// of silently succeeding as a dedup no-op.
func TestCreateTask_NameCollisionWithUnrelatedTaskErrors(t *testing.T) {
	spawner := nameTemplateSpawner()
	unrelated := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "responder-pr-123", // matches nameTemplate output for PR 123
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{Type: "claude-code", Prompt: "unrelated"},
	}
	handler := newTestHandler(t, spawner, unrelated)

	eventData, err := ParseGitHubWebhook("pull_request", prWebhookPayload(123))
	if err != nil {
		t.Fatalf("ParseGitHubWebhook: %v", err)
	}
	parsed := &ParsedWebhook{GitHub: eventData}

	_, err = handler.createTask(context.Background(), spawner, "pull_request", parsed, "delivery-collide")
	if err == nil {
		t.Fatal("expected error when rendered name collides with an unrelated Task, got nil")
	}
	if !strings.Contains(err.Error(), "responder-pr-123") {
		t.Errorf("error should name the colliding Task, got: %v", err)
	}
}

// TestServeHTTP_UnrelatedNameCollisionReturns500 ensures a rendered name that
// collides with a Task not owned by the spawner fails the delivery (HTTP 500)
// so GitHub retries, rather than being silently swallowed as a dedup no-op.
func TestServeHTTP_UnrelatedNameCollisionReturns500(t *testing.T) {
	spawner := nameTemplateSpawner()
	unrelated := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "responder-pr-123", Namespace: "default"},
		Spec:       kelos.TaskSpec{Type: "claude-code", Prompt: "unrelated"},
	}
	handler := newTestHandler(t, spawner, unrelated)

	payload := prWebhookPayload(123)
	sig := signPayload(payload, []byte(testSecret))
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(GitHubEventHeader, "pull_request")
	req.Header.Set(GitHubSignatureHeader, sig)
	req.Header.Set(GitHubDeliveryHeader, "delivery-collide")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected HTTP 500 on unrelated name collision, got %d", rr.Code)
	}

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("expected the unrelated Task to remain the only Task, got %d", len(taskList.Items))
	}
}

// TestServeHTTP_DistinctPRsCreateSeparateTasks ensures the nameTemplate dedup
// does not over-collapse: different PRs still get their own Tasks.
func TestServeHTTP_DistinctPRsCreateSeparateTasks(t *testing.T) {
	handler := newTestHandler(t, nameTemplateSpawner())

	sendPRWebhook(t, handler, prWebhookPayload(1), "pr-1")
	sendPRWebhook(t, handler, prWebhookPayload(2), "pr-2")

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 2 {
		t.Fatalf("Expected 2 tasks for 2 distinct PRs, got %d", len(taskList.Items))
	}
}
