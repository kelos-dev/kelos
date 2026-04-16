package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/slack-go/slack"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(kelosv1alpha1.AddToScheme(s))
	return s
}

type commentRecord struct {
	method string
	number int
	id     int64
	body   string
}

type conflictOnceClient struct {
	client.Client
	mu                 sync.Mutex
	remainingConflicts int
}

func (c *conflictOnceClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.remainingConflicts > 0 {
		c.remainingConflicts--
		return apierrors.NewConflict(
			schema.GroupResource{Group: "kelos.dev", Resource: "tasks"},
			obj.GetName(),
			errors.New("conflict"),
		)
	}

	return c.Client.Update(ctx, obj, opts...)
}

func newTestServer(t *testing.T) (*httptest.Server, *[]commentRecord) {
	t.Helper()
	var (
		mu      sync.Mutex
		records []commentRecord
		nextID  int64 = 1000
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		var body createCommentRequest
		json.NewDecoder(r.Body).Decode(&body)

		switch r.Method {
		case http.MethodPost:
			nextID++
			records = append(records, commentRecord{
				method: "create",
				body:   body.Body,
			})
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(commentResponse{ID: nextID})
		case http.MethodPatch:
			records = append(records, commentRecord{
				method: "update",
				body:   body.Body,
			})
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(commentResponse{})
		}
	}))

	return server, &records
}

func newTaskWithAnnotations(name, namespace string, phase kelosv1alpha1.TaskPhase, annotations map[string]string) *kelosv1alpha1.Task {
	return &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: phase,
		},
	}
}

func TestReportTaskStatus_CreatesCommentOnPending(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].method != "create" {
		t.Errorf("Expected create, got %s", (*records)[0].method)
	}

	// Verify annotations were persisted
	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubReportPhase] != "accepted" {
		t.Errorf("Expected report phase 'accepted', got %q", updated.Annotations[AnnotationGitHubReportPhase])
	}
	if updated.Annotations[AnnotationGitHubCommentID] == "" {
		t.Error("Expected comment ID to be set")
	}
}

func TestReportTaskStatus_UpdatesCommentOnSucceeded(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "42",
		AnnotationSourceKind:        "issue",
		AnnotationGitHubCommentID:   "5555",
		AnnotationGitHubReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].method != "update" {
		t.Errorf("Expected update, got %s", (*records)[0].method)
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubReportPhase] != "succeeded" {
		t.Errorf("Expected report phase 'succeeded', got %q", updated.Annotations[AnnotationGitHubReportPhase])
	}
}

func TestReportTaskStatus_UpdatesCommentOnFailed(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseFailed, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "42",
		AnnotationSourceKind:        "issue",
		AnnotationGitHubCommentID:   "5555",
		AnnotationGitHubReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubReportPhase] != "failed" {
		t.Errorf("Expected report phase 'failed', got %q", updated.Annotations[AnnotationGitHubReportPhase])
	}
}

func TestReportTaskStatus_SkipsDuplicateReport(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "42",
		AnnotationSourceKind:        "issue",
		AnnotationGitHubCommentID:   "5555",
		AnnotationGitHubReportPhase: "accepted", // Already reported
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// No API calls should have been made since it was already reported
	if len(*records) != 0 {
		t.Errorf("Expected 0 API calls (already reported), got %d", len(*records))
	}
}

func TestReportTaskStatus_SkipsWithoutReportingAnnotation(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationSourceNumber: "42",
		AnnotationSourceKind:   "issue",
		// No AnnotationGitHubReporting
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 0 {
		t.Errorf("Expected 0 API calls (reporting not enabled), got %d", len(*records))
	}
}

func TestReportTaskStatus_SkipsEmptyPhase(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", "", map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 0 {
		t.Errorf("Expected 0 API calls (empty phase), got %d", len(*records))
	}
}

func TestReportTaskStatus_RunningMapsToAccepted(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseRunning, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubReportPhase] != "accepted" {
		t.Errorf("Expected report phase 'accepted' for Running task, got %q", updated.Annotations[AnnotationGitHubReportPhase])
	}
}

func TestReportTaskStatus_CreatesNewCommentWhenNoCommentID(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	// Task with succeeded phase but no comment ID (e.g. short-lived task)
	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	// Should create, not update, since no comment ID exists
	if (*records)[0].method != "create" {
		t.Errorf("Expected create for task with no comment ID, got %s", (*records)[0].method)
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	commentID, err := strconv.ParseInt(updated.Annotations[AnnotationGitHubCommentID], 10, 64)
	if err != nil || commentID == 0 {
		t.Errorf("Expected valid comment ID, got %q", updated.Annotations[AnnotationGitHubCommentID])
	}
}

func TestReportTaskStatus_RetriesAnnotationPersistenceOnConflict(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})

	baseClient := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	cl := &conflictOnceClient{
		Client:             baseClient,
		remainingConflicts: 1,
	}

	reporter := &GitHubReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].method != "create" {
		t.Fatalf("Expected create, got %s", (*records)[0].method)
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubReportPhase] != "accepted" {
		t.Errorf("Expected report phase 'accepted', got %q", updated.Annotations[AnnotationGitHubReportPhase])
	}
	if updated.Annotations[AnnotationGitHubCommentID] == "" {
		t.Error("Expected comment ID to be set")
	}
}

func TestReportTaskStatus_CorruptedCommentIDReturnsError(t *testing.T) {
	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
		AnnotationGitHubCommentID: "not-a-number",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t"}
	tr := &TaskReporter{Client: cl, Reporter: reporter}

	err := tr.ReportTaskStatus(context.Background(), task)
	if err == nil {
		t.Fatal("Expected error for corrupted comment ID, got nil")
	}
}

func TestReportTaskStatus_NilAnnotations(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhasePending,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	reporter := &GitHubReporter{Owner: "o", Repo: "r", Token: "t"}
	tr := &TaskReporter{Client: cl, Reporter: reporter}

	// Should not error when annotations are nil
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestSlackTaskReporter_PostsThreadReply(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhasePending,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var posted []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			posted = append(posted, slackReplyRecord{method: "post", channel: channel, threadTS: threadTS, msg: msg})
			return "1234567890.999999", nil
		},
	}

	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posted) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posted))
	}
	if posted[0].channel != "C123ABC" {
		t.Errorf("channel = %q, want C123ABC", posted[0].channel)
	}
	if posted[0].threadTS != "1234567890.123456" {
		t.Errorf("threadTS = %q, want 1234567890.123456", posted[0].threadTS)
	}

	// Verify annotations were persisted
	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationSlackReportPhase] != "accepted" {
		t.Errorf("report phase = %q, want accepted", updated.Annotations[AnnotationSlackReportPhase])
	}
}

func TestSlackTaskReporter_PostsNewReplyOnPhaseChange(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",

				AnnotationSlackReportPhase: "accepted",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseSucceeded,
			Results: map[string]string{"pr": "https://github.com/org/repo/pull/42"},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var posted []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			posted = append(posted, slackReplyRecord{method: "post", channel: channel, threadTS: threadTS, msg: msg})
			return "1234567890.888888", nil
		},
	}

	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posted) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posted))
	}
	if posted[0].channel != "C123ABC" {
		t.Errorf("channel = %q, want C123ABC", posted[0].channel)
	}
	// Verify the message includes the PR URL
	wantMsg := FormatSlackTransitionMessage("succeeded", task.Name, task.Status.Message, task.Status.Results)
	if posted[0].msg.Text != wantMsg.Text {
		t.Errorf("text = %q, want %q", posted[0].msg.Text, wantMsg.Text)
	}

}

func TestSlackTaskReporter_SkipPaths(t *testing.T) {
	baseTask := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhasePending,
		},
	}

	tests := []struct {
		name   string
		mutate func(t *kelosv1alpha1.Task)
	}{
		{
			name: "no reporting annotation",
			mutate: func(t *kelosv1alpha1.Task) {
				delete(t.Annotations, AnnotationSlackReporting)
			},
		},
		{
			name: "already reported same phase",
			mutate: func(t *kelosv1alpha1.Task) {
				t.Annotations[AnnotationSlackReportPhase] = "accepted"
			},
		},
		{
			name: "nil annotations",
			mutate: func(t *kelosv1alpha1.Task) {
				t.Annotations = nil
			},
		},
		{
			name: "missing channel",
			mutate: func(t *kelosv1alpha1.Task) {
				delete(t.Annotations, AnnotationSlackChannel)
			},
		},
		{
			name: "empty phase",
			mutate: func(t *kelosv1alpha1.Task) {
				t.Status.Phase = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := baseTask.DeepCopy()
			tt.mutate(task)

			called := false
			reporter := &fakeSlackReporter{
				postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
					called = true
					return "", nil
				},
			}

			cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
			tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

			if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if called {
				t.Error("expected reporter to not be called")
			}
		})
	}
}

type slackReplyRecord struct {
	method   string
	channel  string
	threadTS string
	msg      SlackMessage
}

type fakeSlackReporter struct {
	postFn   func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error)
	updateFn func(ctx context.Context, channel, messageTS string, msg SlackMessage) error
}

func (f *fakeSlackReporter) PostThreadReply(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
	if f.postFn != nil {
		return f.postFn(ctx, channel, threadTS, msg)
	}
	return "fake-reply-ts", nil
}

func (f *fakeSlackReporter) UpdateMessage(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
	if f.updateFn != nil {
		return f.updateFn(ctx, channel, messageTS, msg)
	}
	return nil
}

func TestSlackTaskReporter_PhaseMapping(t *testing.T) {
	tests := []struct {
		name          string
		phase         kelosv1alpha1.TaskPhase
		wantDesired   string
		shouldProcess bool
	}{
		{"pending", kelosv1alpha1.TaskPhasePending, "accepted", true},
		{"running", kelosv1alpha1.TaskPhaseRunning, "accepted", true},
		{"waiting", kelosv1alpha1.TaskPhaseWaiting, "accepted", true},
		{"succeeded", kelosv1alpha1.TaskPhaseSucceeded, "succeeded", true},
		{"failed", kelosv1alpha1.TaskPhaseFailed, "failed", true},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationSlackReporting: "enabled",
						AnnotationSlackChannel:   "C123",
						AnnotationSlackThreadTS:  "1234.5678",
					},
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase: tt.phase,
				},
			}

			if tt.shouldProcess {
				// Mark as already reported to verify skip logic
				task.Annotations[AnnotationSlackReportPhase] = tt.wantDesired
			}

			cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
			tr := &SlackTaskReporter{Client: cl, Reporter: &SlackReporter{BotToken: "xoxb-test"}}

			// Should not error — either skips (empty phase) or skips (already reported)
			if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

type fakeProgressReader struct {
	text string
}

func (f *fakeProgressReader) ReadProgress(ctx context.Context, namespace, podName, container, agentType string) string {
	return f.text
}

func TestSlackTaskReporter_PostsProgressReply(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "uid-123",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",

				AnnotationSlackReportPhase: "accepted",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var posted []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			posted = append(posted, slackReplyRecord{method: "post", channel: channel, threadTS: threadTS, msg: msg})
			return "1234567890.111111", nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "Searching through release tags..."},
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(posted) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posted))
	}
	if posted[0].channel != "C123ABC" {
		t.Errorf("channel = %q, want C123ABC", posted[0].channel)
	}
	if posted[0].threadTS != "1234567890.123456" {
		t.Errorf("threadTS = %q, want original thread TS", posted[0].threadTS)
	}
	if posted[0].msg.Text != "Searching through release tags..." {
		t.Errorf("text = %q, want progress text", posted[0].msg.Text)
	}
}

func TestSlackTaskReporter_SkipsProgressWhenNoReader(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "C123ABC",
				AnnotationSlackThreadTS:    "1234567890.123456",
				AnnotationSlackReportPhase: "accepted",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	called := false
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			called = true
			return "", nil
		},
	}

	// No ProgressReader set
	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("expected no Slack API call when ProgressReader is nil")
	}
}

func TestSlackTaskReporter_SkipsProgressWhenNoPod(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "C123ABC",
				AnnotationSlackThreadTS:    "1234567890.123456",
				AnnotationSlackReportPhase: "accepted",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhasePending,
			PodName: "", // No pod yet
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	called := false
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			called = true
			return "", nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "something"},
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("expected no Slack API call when pod name is empty")
	}
}

func TestSlackTaskReporter_SkipsProgressWhenEmpty(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "C123ABC",
				AnnotationSlackThreadTS:    "1234567890.123456",
				AnnotationSlackReportPhase: "accepted",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	called := false
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			called = true
			return "", nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: ""}, // Empty text
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("expected no Slack API call when progress text is empty")
	}
}

func TestSlackTaskReporter_DeduplicatesProgress(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "uid-456",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",

				AnnotationSlackReportPhase: "accepted",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	postCount := 0
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			postCount++
			return "1234567890.111111", nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "Same progress text"},
	}

	// First call should post
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("expected 1 post on first call, got %d", postCount)
	}

	// Second call with same text should NOT post
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postCount != 1 {
		t.Errorf("expected still 1 post (deduplicated), got %d", postCount)
	}
}

func TestSlackTaskReporter_PostsOnNewText(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "uid-789",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",

				AnnotationSlackReportPhase: "accepted",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var postedTexts []string
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			postedTexts = append(postedTexts, msg.Text)
			return "1234567890.111111", nil
		},
	}

	pr := &fakeProgressReader{text: "First update"}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: pr,
	}

	// First call
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Change the text
	pr.text = "Second update"

	// Second call with different text should post again
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(postedTexts) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(postedTexts))
	}
	if postedTexts[0] != "First update" {
		t.Errorf("first post = %q, want 'First update'", postedTexts[0])
	}
	if postedTexts[1] != "Second update" {
		t.Errorf("second post = %q, want 'Second update'", postedTexts[1])
	}
}

func TestSlackTaskReporter_ClearsProgressCacheOnTerminal(t *testing.T) {
	// First, seed the progress cache via a running task
	runningTask := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "uid-clear",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",

				AnnotationSlackReportPhase: "accepted",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(runningTask).Build()

	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			return "1234567890.111111", nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "Working on it..."},
	}

	// Post a progress update to populate the cache
	if err := tr.ReportTaskStatus(context.Background(), runningTask); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify cache is populated
	tr.mu.Lock()
	if _, ok := tr.lastProgress["uid-clear"]; !ok {
		t.Error("expected progress cache to be populated")
	}
	tr.mu.Unlock()

	// Simulate task completing by creating a new task object with succeeded phase.
	// We rebuild the fake client with the succeeded task to allow annotation persistence.
	succeededTask := runningTask.DeepCopy()
	succeededTask.Status.Phase = kelosv1alpha1.TaskPhaseSucceeded
	succeededTask.Status.Results = map[string]string{"response": "done"}

	cl2 := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(succeededTask).Build()
	tr.Client = cl2

	if err := tr.ReportTaskStatus(context.Background(), succeededTask); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify cache was cleared
	tr.mu.Lock()
	if _, ok := tr.lastProgress["uid-clear"]; ok {
		t.Error("expected progress cache to be cleared after terminal phase")
	}
	tr.mu.Unlock()
}

func TestSlackTaskReporter_SweepsStaleProgressEntries(t *testing.T) {
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			return "fake-ts", nil
		},
	}

	tr := &SlackTaskReporter{
		Reporter: reporter,
	}

	// Seed cache with entries for two tasks
	tr.setLastProgress("uid-active", "some text")
	tr.setLastProgress("uid-deleted", "other text")

	// Sweep with only the active UID
	activeUIDs := map[types.UID]bool{
		"uid-active": true,
	}
	tr.SweepProgressCache(activeUIDs)

	tr.mu.Lock()
	defer tr.mu.Unlock()

	if _, ok := tr.lastProgress["uid-active"]; !ok {
		t.Error("expected active UID to remain in cache")
	}
	if _, ok := tr.lastProgress["uid-deleted"]; ok {
		t.Error("expected deleted UID to be swept from cache")
	}
}

// --- Activity indicator tests ---

type fakeActivityReader struct {
	text string
}

func (f *fakeActivityReader) ReadActivity(ctx context.Context, namespace, podName, container, agentType string) string {
	return f.text
}

func newRunningTaskWithAnnotations(name string, uid types.UID, annotations map[string]string) *kelosv1alpha1.Task {
	return &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			UID:         uid,
			Annotations: annotations,
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:   kelosv1alpha1.TaskPhaseRunning,
			PodName: "test-pod",
		},
	}
}

func TestSlackTaskReporter_UpdatesAcceptedMessageWithActivity(t *testing.T) {
	task := newRunningTaskWithAnnotations("test-task", "uid-act-1", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	var updates []slackReplyRecord
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updates = append(updates, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, msg: msg})
			return nil
		},
	}

	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)
	tr := &SlackTaskReporter{
		Reporter:       reporter,
		ActivityReader: &fakeActivityReader{text: "Reading `main.go`..."},
	}
	// Simulate the accepted message having been posted.
	tr.setActivityTarget(task.UID, "1234567890.accepted", baseMsg)

	tr.UpdateActivityIndicator(context.Background(), task)

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].channel != "C123ABC" {
		t.Errorf("channel = %q, want C123ABC", updates[0].channel)
	}
	if updates[0].threadTS != "1234567890.accepted" {
		t.Errorf("messageTS = %q, want accepted TS", updates[0].threadTS)
	}
	// The updated message should have the original blocks + activity in context.
	if updates[0].msg.Text != baseMsg.Text {
		t.Errorf("fallback text changed: got %q", updates[0].msg.Text)
	}
	// Should have more blocks than the base (activity appended to context).
	if len(updates[0].msg.Blocks) < len(baseMsg.Blocks) {
		t.Errorf("expected at least %d blocks, got %d", len(baseMsg.Blocks), len(updates[0].msg.Blocks))
	}
}

func TestSlackTaskReporter_UpdatesActivityInPlace(t *testing.T) {
	task := newRunningTaskWithAnnotations("test-task", "uid-act-2", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	var updates []slackReplyRecord
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updates = append(updates, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, msg: msg})
			return nil
		},
	}

	ar := &fakeActivityReader{text: "Reading `main.go`..."}
	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)
	tr := &SlackTaskReporter{
		Reporter:       reporter,
		ActivityReader: ar,
	}
	tr.setActivityTarget(task.UID, "1234567890.accepted", baseMsg)

	// First call updates the accepted message with activity.
	tr.UpdateActivityIndicator(context.Background(), task)

	// Change activity text and call again — should update in place again.
	ar.text = "Running `make test`..."
	tr.UpdateActivityIndicator(context.Background(), task)

	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[1].threadTS != "1234567890.accepted" {
		t.Errorf("update messageTS = %q, want accepted TS", updates[1].threadTS)
	}
}

func TestSlackTaskReporter_SkipsActivityWhenUnchanged(t *testing.T) {
	task := newRunningTaskWithAnnotations("test-task", "uid-act-3", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	updateCount := 0
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updateCount++
			return nil
		},
	}

	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)
	tr := &SlackTaskReporter{
		Reporter:       reporter,
		ActivityReader: &fakeActivityReader{text: "Thinking..."},
	}
	tr.setActivityTarget(task.UID, "1234567890.accepted", baseMsg)

	tr.UpdateActivityIndicator(context.Background(), task)
	tr.UpdateActivityIndicator(context.Background(), task) // Same text

	if updateCount != 1 {
		t.Errorf("expected 1 update (dedup second), got %d", updateCount)
	}
}

func TestSlackTaskReporter_SkipsActivityWhenNoTarget(t *testing.T) {
	task := newRunningTaskWithAnnotations("test-task", "uid-no-target", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	updateCalled := false
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updateCalled = true
			return nil
		},
	}

	// No activity target set — accepted message not yet posted.
	tr := &SlackTaskReporter{
		Reporter:       reporter,
		ActivityReader: &fakeActivityReader{text: "Reading..."},
	}

	tr.UpdateActivityIndicator(context.Background(), task)

	if updateCalled {
		t.Error("expected no update when no activity target is set")
	}
}

func TestSlackTaskReporter_ActivitySkipPaths(t *testing.T) {
	tests := []struct {
		name   string
		task   *kelosv1alpha1.Task
		reader ActivityReader
	}{
		{
			name: "no activity reader",
			task: newRunningTaskWithAnnotations("t", "uid-1", map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "C123",
				AnnotationSlackThreadTS:    "1234.5678",
				AnnotationSlackReportPhase: "accepted",
			}),
			reader: nil,
		},
		{
			name: "reporting not enabled",
			task: newRunningTaskWithAnnotations("t", "uid-2", map[string]string{
				AnnotationSlackChannel:     "C123",
				AnnotationSlackThreadTS:    "1234.5678",
				AnnotationSlackReportPhase: "accepted",
			}),
			reader: &fakeActivityReader{text: "something"},
		},
		{
			name: "not yet accepted",
			task: newRunningTaskWithAnnotations("t", "uid-3", map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123",
				AnnotationSlackThreadTS:  "1234.5678",
			}),
			reader: &fakeActivityReader{text: "something"},
		},
		{
			name: "no pod name",
			task: func() *kelosv1alpha1.Task {
				t := newRunningTaskWithAnnotations("t", "uid-4", map[string]string{
					AnnotationSlackReporting:   "enabled",
					AnnotationSlackChannel:     "C123",
					AnnotationSlackThreadTS:    "1234.5678",
					AnnotationSlackReportPhase: "accepted",
				})
				t.Status.PodName = ""
				return t
			}(),
			reader: &fakeActivityReader{text: "something"},
		},
		{
			name: "no channel",
			task: newRunningTaskWithAnnotations("t", "uid-5", map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "",
				AnnotationSlackThreadTS:    "1234.5678",
				AnnotationSlackReportPhase: "accepted",
			}),
			reader: &fakeActivityReader{text: "something"},
		},
		{
			name: "task not running",
			task: func() *kelosv1alpha1.Task {
				t := newRunningTaskWithAnnotations("t", "uid-6", map[string]string{
					AnnotationSlackReporting:   "enabled",
					AnnotationSlackChannel:     "C123",
					AnnotationSlackThreadTS:    "1234.5678",
					AnnotationSlackReportPhase: "accepted",
				})
				t.Status.Phase = kelosv1alpha1.TaskPhasePending
				return t
			}(),
			reader: &fakeActivityReader{text: "something"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateCalled := false
			reporter := &fakeSlackReporter{
				updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
					updateCalled = true
					return nil
				},
			}

			tr := &SlackTaskReporter{
				Reporter:       reporter,
				ActivityReader: tt.reader,
			}
			// Seed a target so skip is due to the test condition, not missing target.
			if tt.reader != nil {
				tr.setActivityTarget(tt.task.UID, "ts-seed", SlackMessage{Text: "base"})
			}

			tr.UpdateActivityIndicator(context.Background(), tt.task)

			if updateCalled {
				t.Error("expected no Slack API call")
			}
		})
	}
}

func TestSlackTaskReporter_AcceptedPostSetsActivityTarget(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "uid-accepted-target",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123ABC",
				AnnotationSlackThreadTS:  "1234567890.123456",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: &kelosv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase: kelosv1alpha1.TaskPhasePending,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			return "1234567890.accepted", nil
		},
	}

	tr := &SlackTaskReporter{
		Client:   cl,
		Reporter: reporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tr.mu.Lock()
	state := tr.activity[task.UID]
	tr.mu.Unlock()
	if state == nil {
		t.Fatal("expected activity state to be set after accepted post")
	}
	if state.MessageTS != "1234567890.accepted" {
		t.Errorf("messageTS = %q, want accepted TS", state.MessageTS)
	}
}

func TestSlackTaskReporter_ProgressPostUpdatesActivityTarget(t *testing.T) {
	task := newRunningTaskWithAnnotations("test-task", "uid-progress-target", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	postCount := 0
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			postCount++
			return fmt.Sprintf("ts-progress-%d", postCount), nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "I found the issue."},
	}
	// Seed initial target (from accepted post).
	tr.setActivityTarget(task.UID, "ts-accepted", SlackMessage{Text: "accepted"})

	// Trigger progress update.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Activity target should now point to the progress message.
	tr.mu.Lock()
	state := tr.activity[task.UID]
	tr.mu.Unlock()
	if state == nil {
		t.Fatal("expected activity state after progress post")
	}
	if state.MessageTS != "ts-progress-1" {
		t.Errorf("messageTS = %q, want progress TS", state.MessageTS)
	}
	// LastText should be reset so activity updates on the new message.
	if state.LastText != "" {
		t.Errorf("LastText = %q, want empty (reset for new target)", state.LastText)
	}
}

func TestSlackTaskReporter_SweepClearsActivityState(t *testing.T) {
	reporter := &fakeSlackReporter{}
	tr := &SlackTaskReporter{Reporter: reporter}

	// Seed activity state.
	tr.mu.Lock()
	tr.activity = map[types.UID]*activityState{
		"uid-active":  {MessageTS: "ts-1", LastText: "Working..."},
		"uid-deleted": {MessageTS: "ts-2", LastText: "Reading..."},
	}
	tr.mu.Unlock()

	tr.SweepProgressCache(map[types.UID]bool{"uid-active": true})

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if _, ok := tr.activity["uid-active"]; !ok {
		t.Error("expected active UID to remain in activity cache")
	}
	if _, ok := tr.activity["uid-deleted"]; ok {
		t.Error("expected deleted UID to be swept from activity cache")
	}
}

func TestSlackTaskReporter_EmptyActivityUsesIdlePhrase(t *testing.T) {
	task := newRunningTaskWithAnnotations("test-task", "uid-idle", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	var updates []slackReplyRecord
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updates = append(updates, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, msg: msg})
			return nil
		},
	}

	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)
	tr := &SlackTaskReporter{
		Reporter:       reporter,
		ActivityReader: &fakeActivityReader{text: ""}, // Empty — triggers idle phrase
	}
	tr.setActivityTarget(task.UID, "1234567890.accepted", baseMsg)

	// First call should post an idle phrase.
	tr.UpdateActivityIndicator(context.Background(), task)
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	// Second call should post a different idle phrase (tick incremented).
	tr.UpdateActivityIndicator(context.Background(), task)
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
}

func TestAppendActivityContext_AppendsToExistingContext(t *testing.T) {
	baseMsg := FormatSlackTransitionMessage("accepted", "test-task", "", nil)
	result := appendActivityContext(baseMsg, "Reading `main.go`...")

	// Should have same number of blocks (activity appended to existing context block).
	if len(result.Blocks) != len(baseMsg.Blocks) {
		t.Fatalf("block count = %d, want %d (appended to existing context)", len(result.Blocks), len(baseMsg.Blocks))
	}

	// Last block should be a context block with 2 elements (task name + activity).
	lastBlock := result.Blocks[len(result.Blocks)-1]
	ctx, ok := lastBlock.(*slack.ContextBlock)
	if !ok {
		t.Fatalf("last block: expected *ContextBlock, got %T", lastBlock)
	}
	if len(ctx.ContextElements.Elements) != 2 {
		t.Errorf("context elements = %d, want 2", len(ctx.ContextElements.Elements))
	}
}

func TestAppendActivityContext_AddsNewContextBlock(t *testing.T) {
	// Message with no context block at the end.
	baseMsg := SlackMessage{
		Text:   "Just text",
		Blocks: []slack.Block{slack.NewSectionBlock(slack.NewTextBlockObject(slack.PlainTextType, "hello", false, false), nil, nil)},
	}
	result := appendActivityContext(baseMsg, "Thinking...")

	if len(result.Blocks) != 2 {
		t.Fatalf("block count = %d, want 2 (section + new context)", len(result.Blocks))
	}

	ctx, ok := result.Blocks[1].(*slack.ContextBlock)
	if !ok {
		t.Fatalf("block 1: expected *ContextBlock, got %T", result.Blocks[1])
	}
	if len(ctx.ContextElements.Elements) != 1 {
		t.Errorf("context elements = %d, want 1", len(ctx.ContextElements.Elements))
	}
}

func TestAppendActivityContext_SkipsTextOnlyMessages(t *testing.T) {
	baseMsg := SlackMessage{Text: "I found the issue in the config.", Blocks: nil}
	result := appendActivityContext(baseMsg, "Reading `main.go`...")

	// Should return the base message unchanged — no blocks added.
	if len(result.Blocks) != 0 {
		t.Fatalf("block count = %d, want 0 (text-only message unchanged)", len(result.Blocks))
	}
	if result.Text != baseMsg.Text {
		t.Errorf("text changed: got %q", result.Text)
	}
}

func TestAppendActivityContext_DoesNotMutateBase(t *testing.T) {
	baseMsg := FormatSlackTransitionMessage("accepted", "test-task", "", nil)
	originalBlockCount := len(baseMsg.Blocks)

	_ = appendActivityContext(baseMsg, "Reading...")

	if len(baseMsg.Blocks) != originalBlockCount {
		t.Errorf("base message mutated: blocks went from %d to %d", originalBlockCount, len(baseMsg.Blocks))
	}
}
