package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		postFn: func(ctx context.Context, channel, threadTS, text string) (string, error) {
			posted = append(posted, slackReplyRecord{method: "post", channel: channel, threadTS: threadTS, text: text})
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
	if updated.Annotations[AnnotationSlackReplyTS] != "1234567890.999999" {
		t.Errorf("reply ts = %q, want 1234567890.999999", updated.Annotations[AnnotationSlackReplyTS])
	}
}

func TestSlackTaskReporter_UpdatesExistingReply(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "C123ABC",
				AnnotationSlackThreadTS:    "1234567890.123456",
				AnnotationSlackReplyTS:     "1234567890.999999",
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

	var updated []slackReplyRecord
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS, text string) error {
			updated = append(updated, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, text: text})
			return nil
		},
	}

	tr := &SlackTaskReporter{Client: cl, Reporter: reporter}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(updated) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updated))
	}
	if updated[0].channel != "C123ABC" {
		t.Errorf("channel = %q, want C123ABC", updated[0].channel)
	}
	// Verify the message includes the PR URL
	wantText := FormatSlackSucceeded(task.Name, task.Status.Results)
	if updated[0].text != wantText {
		t.Errorf("text = %q, want %q", updated[0].text, wantText)
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
				postFn: func(ctx context.Context, channel, threadTS, text string) (string, error) {
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
	text     string
}

type fakeSlackReporter struct {
	postFn   func(ctx context.Context, channel, threadTS, text string) (string, error)
	updateFn func(ctx context.Context, channel, messageTS, text string) error
}

func (f *fakeSlackReporter) PostThreadReply(ctx context.Context, channel, threadTS, text string) (string, error) {
	if f.postFn != nil {
		return f.postFn(ctx, channel, threadTS, text)
	}
	return "fake-reply-ts", nil
}

func (f *fakeSlackReporter) UpdateMessage(ctx context.Context, channel, messageTS, text string) error {
	if f.updateFn != nil {
		return f.updateFn(ctx, channel, messageTS, text)
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
