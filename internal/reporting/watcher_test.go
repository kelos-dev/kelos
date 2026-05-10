package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
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

type updateCountingClient struct {
	client.Client
	mu      sync.Mutex
	updates int
}

func (c *updateCountingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.mu.Lock()
	c.updates++
	c.mu.Unlock()
	return c.Client.Update(ctx, obj, opts...)
}

func (c *updateCountingClient) updateCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updates
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
				id:     nextID,
				body:   body.Body,
			})
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(commentResponse{ID: nextID})
		case http.MethodPatch:
			id, _ := strconv.ParseInt(path.Base(r.URL.Path), 10, 64)
			records = append(records, commentRecord{
				method: "update",
				id:     id,
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

func TestReportTaskStatus_CachePopulatedAfterCreate(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})
	task.UID = types.UID("uid-create")

	cache := NewReportStateCache()
	tr := &TaskReporter{
		Client: fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter: &GitHubReporter{
			Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL,
		},
		Cache: cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got, ok := cache.load(task.UID)
	if !ok {
		t.Fatal("Expected cache entry after successful report")
	}
	if got.phase != "accepted" {
		t.Errorf("Expected cached phase 'accepted', got %q", got.phase)
	}
	if got.commentID == 0 {
		t.Error("Expected non-zero cached comment ID")
	}
}

// TestReportTaskStatus_CacheFallbackUpdatesExistingComment exercises the
// cache-stale read race: the in-memory Task lacks the comment-ID annotation
// (because the previous reconcile's annotation Update has not propagated to
// the cached read yet) but the in-memory cache still has it, so the reporter
// must update the existing comment instead of creating a new one.
func TestReportTaskStatus_CacheFallbackUpdatesExistingComment(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})
	task.UID = types.UID("uid-fallback")

	cache := NewReportStateCache()
	cache.store(task.UID, 7777, "accepted")

	tr := &TaskReporter{
		Client: fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter: &GitHubReporter{
			Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL,
		},
		Cache: cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].method != "update" {
		t.Errorf("Expected update via cached comment ID, got %s", (*records)[0].method)
	}
	if (*records)[0].id != 7777 {
		t.Errorf("Expected cached comment ID 7777 to be patched, got %d", (*records)[0].id)
	}
}

// TestReportTaskStatus_CacheShortCircuitsDuplicateReport simulates two
// reconciles firing for the same phase before the annotation Update has
// propagated to the cached read. The first call posts the comment; the second
// must not post a duplicate even though the Task object it sees still has no
// AnnotationGitHubReportPhase.
func TestReportTaskStatus_CacheShortCircuitsDuplicateReport(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	annotations := map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	}

	first := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, annotations)
	first.UID = types.UID("uid-shortcircuit")
	first.ResourceVersion = ""

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(first).Build()
	cache := NewReportStateCache()
	tr := &TaskReporter{
		Client: cl,
		Reporter: &GitHubReporter{
			Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL,
		},
		Cache: cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), first); err != nil {
		t.Fatalf("First report failed: %v", err)
	}

	// Simulate a stale cached read: a second copy of the Task that has not yet
	// observed the annotation Update from the first reconcile.
	stale := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})
	stale.UID = types.UID("uid-shortcircuit")

	if err := tr.ReportTaskStatus(context.Background(), stale); err != nil {
		t.Fatalf("Second report failed: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected exactly 1 GitHub API call, got %d (%+v)", len(*records), *records)
	}
	if (*records)[0].method != "create" {
		t.Errorf("Expected the single call to be a create, got %s", (*records)[0].method)
	}
}

// TestReportTaskStatus_SkipsRepeatedNoOpPersist verifies that when both the
// cache and the annotation already record the desired phase + comment ID, no
// GitHub API call and no Task Update is issued — guarding against reconcile
// churn on informer resync where the same phase is observed repeatedly.
func TestReportTaskStatus_SkipsRepeatedNoOpPersist(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting:   "enabled",
		AnnotationSourceNumber:      "42",
		AnnotationSourceKind:        "issue",
		AnnotationGitHubCommentID:   "9999",
		AnnotationGitHubReportPhase: "accepted",
	})
	task.UID = types.UID("uid-noop")

	cache := NewReportStateCache()
	cache.store(task.UID, 9999, "accepted")

	counted := &updateCountingClient{
		Client: fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
	}
	tr := &TaskReporter{
		Client: counted,
		Reporter: &GitHubReporter{
			Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL,
		},
		Cache: cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 0 {
		t.Errorf("Expected no GitHub API calls, got %d", len(*records))
	}
	if counted.updateCount() != 0 {
		t.Errorf("Expected no Task Updates, got %d", counted.updateCount())
	}
}

func TestReportTaskStatus_NilCache(t *testing.T) {
	server, records := newTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "issue",
	})

	tr := &TaskReporter{
		Client: fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter: &GitHubReporter{
			Owner: "owner", Repo: "repo", Token: "token", BaseURL: server.URL,
		},
		// Cache intentionally nil — callers without race exposure (poll-driven
		// spawner) should keep working.
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(*records) != 1 || (*records)[0].method != "create" {
		t.Errorf("Expected one create call with nil cache, got %+v", *records)
	}
}

// --- Check Run reporting tests ---

type checkRunRecord struct {
	method      string
	name        string
	headSHA     string
	status      string
	conclusion  string
	outputTitle string
}

func newTestChecksServer(t *testing.T) (*httptest.Server, *[]checkRunRecord) {
	t.Helper()
	var (
		mu      sync.Mutex
		records []checkRunRecord
		nextID  int64 = 5000
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch r.Method {
		case http.MethodPost:
			var body createCheckRunRequest
			json.NewDecoder(r.Body).Decode(&body)
			nextID++
			records = append(records, checkRunRecord{
				method:  "create",
				name:    body.Name,
				headSHA: body.HeadSHA,
				status:  body.Status,
			})
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(checkRunResponse{ID: nextID})
		case http.MethodPatch:
			var body updateCheckRunRequest
			json.NewDecoder(r.Body).Decode(&body)
			outputTitle := ""
			if body.Output != nil {
				outputTitle = body.Output.Title
			}
			records = append(records, checkRunRecord{
				method:      "update",
				status:      body.Status,
				conclusion:  body.Conclusion,
				outputTitle: outputTitle,
			})
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(checkRunResponse{})
		}
	}))

	return server, &records
}

func TestReportTaskStatus_CreatesCheckRunOnPending(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubChecks:    "enabled",
		AnnotationSourceSHA:       "abc123def",
		AnnotationGitHubCheckName: "Kelos: my-spawner",
	})
	task.Labels = map[string]string{"kelos.dev/taskspawner": "my-spawner"}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	checksReporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: checksReporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 check run API call, got %d", len(*records))
	}
	if (*records)[0].method != "create" {
		t.Errorf("Expected create, got %s", (*records)[0].method)
	}
	if (*records)[0].name != "Kelos: my-spawner" {
		t.Errorf("Expected name %q, got %q", "Kelos: my-spawner", (*records)[0].name)
	}
	if (*records)[0].headSHA != "abc123def" {
		t.Errorf("Expected headSHA %q, got %q", "abc123def", (*records)[0].headSHA)
	}
	if (*records)[0].status != "in_progress" {
		t.Errorf("Expected status %q, got %q", "in_progress", (*records)[0].status)
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubCheckReportPhase] != "in_progress" {
		t.Errorf("Expected check report phase 'in_progress', got %q", updated.Annotations[AnnotationGitHubCheckReportPhase])
	}
	if updated.Annotations[AnnotationGitHubCheckRunID] == "" {
		t.Error("Expected check run ID to be set")
	}
}

func TestReportTaskStatus_UpdatesCheckRunOnSucceeded(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubChecks:           "enabled",
		AnnotationSourceSHA:              "abc123def",
		AnnotationGitHubCheckName:        "Kelos: my-spawner",
		AnnotationGitHubCheckRunID:       "5001",
		AnnotationGitHubCheckReportPhase: "in_progress",
	})
	task.Labels = map[string]string{"kelos.dev/taskspawner": "my-spawner"}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	checksReporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: checksReporter,
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
	if (*records)[0].status != "completed" {
		t.Errorf("Expected status 'completed', got %q", (*records)[0].status)
	}
	if (*records)[0].conclusion != "success" {
		t.Errorf("Expected conclusion 'success', got %q", (*records)[0].conclusion)
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubCheckReportPhase] != "succeeded" {
		t.Errorf("Expected check report phase 'succeeded', got %q", updated.Annotations[AnnotationGitHubCheckReportPhase])
	}
}

func TestReportTaskStatus_UpdatesCheckRunOnFailed(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseFailed, map[string]string{
		AnnotationGitHubChecks:           "enabled",
		AnnotationSourceSHA:              "abc123def",
		AnnotationGitHubCheckName:        "Kelos: my-spawner",
		AnnotationGitHubCheckRunID:       "5001",
		AnnotationGitHubCheckReportPhase: "in_progress",
	})
	task.Labels = map[string]string{"kelos.dev/taskspawner": "my-spawner"}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	checksReporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: checksReporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].conclusion != "failure" {
		t.Errorf("Expected conclusion 'failure', got %q", (*records)[0].conclusion)
	}

	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("Getting updated task: %v", err)
	}
	if updated.Annotations[AnnotationGitHubCheckReportPhase] != "failed" {
		t.Errorf("Expected check report phase 'failed', got %q", updated.Annotations[AnnotationGitHubCheckReportPhase])
	}
}

func TestReportTaskStatus_SkipsDuplicateCheckReport(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubChecks:           "enabled",
		AnnotationSourceSHA:              "abc123def",
		AnnotationGitHubCheckRunID:       "5001",
		AnnotationGitHubCheckReportPhase: "in_progress",
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	checksReporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: checksReporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 0 {
		t.Errorf("Expected 0 API calls (already reported), got %d", len(*records))
	}
}

func TestReportTaskStatus_ChecksSkipsWithoutSHA(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubChecks: "enabled",
		// No AnnotationSourceSHA
	})

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	checksReporter := &ChecksReporter{
		Owner:   "owner",
		Repo:    "repo",
		Token:   "token",
		BaseURL: server.URL,
	}

	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: checksReporter,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 0 {
		t.Errorf("Expected 0 API calls (no SHA), got %d", len(*records))
	}
}

func TestReportTaskStatus_BothCommentAndChecks(t *testing.T) {
	commentServer, commentRecords := newTestServer(t)
	defer commentServer.Close()

	checksServer, checksRecords := newTestChecksServer(t)
	defer checksServer.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationGitHubChecks:    "enabled",
		AnnotationSourceNumber:    "42",
		AnnotationSourceKind:      "pull-request",
		AnnotationSourceSHA:       "abc123def",
		AnnotationGitHubCheckName: "Kelos: my-spawner",
	})
	task.Labels = map[string]string{"kelos.dev/taskspawner": "my-spawner"}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	tr := &TaskReporter{
		Client: cl,
		Reporter: &GitHubReporter{
			Owner:   "owner",
			Repo:    "repo",
			Token:   "token",
			BaseURL: commentServer.URL,
		},
		ChecksReporter: &ChecksReporter{
			Owner:   "owner",
			Repo:    "repo",
			Token:   "token",
			BaseURL: checksServer.URL,
		},
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*commentRecords) != 1 {
		t.Errorf("Expected 1 comment API call, got %d", len(*commentRecords))
	}
	if len(*checksRecords) != 1 {
		t.Errorf("Expected 1 checks API call, got %d", len(*checksRecords))
	}
}

func TestReportTaskStatus_ChecksFallbackName(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubChecks: "enabled",
		AnnotationSourceSHA:    "abc123def",
		// No AnnotationGitHubCheckName — should fall back to label
	})
	task.Labels = map[string]string{"kelos.dev/taskspawner": "fallback-spawner"}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(task).
		Build()

	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: &ChecksReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL},
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].name != "Kelos: fallback-spawner" {
		t.Errorf("Expected fallback name %q, got %q", "Kelos: fallback-spawner", (*records)[0].name)
	}
}

func TestReportTaskStatus_CheckRunCachePopulatedAfterCreate(t *testing.T) {
	server, _ := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubChecks:    "enabled",
		AnnotationSourceSHA:       "abc123def",
		AnnotationGitHubCheckName: "Kelos: my-spawner",
	})
	task.UID = types.UID("uid-check-create")

	cache := NewReportStateCache()
	tr := &TaskReporter{
		Client:         fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: &ChecksReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL},
		Cache:          cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	got, ok := cache.load(task.UID)
	if !ok {
		t.Fatal("Expected cache entry after successful check run report")
	}
	if got.checkPhase != "in_progress" {
		t.Errorf("Expected cached check phase 'in_progress', got %q", got.checkPhase)
	}
	if got.checkRunID == 0 {
		t.Error("Expected non-zero cached check run ID")
	}
}

// TestReportTaskStatus_CheckRunCacheFallbackUpdatesExisting exercises the
// cache-stale read race for check runs: the in-memory Task lacks the
// check-run-ID annotation but the in-memory cache still has it, so the
// reporter must update the existing check run instead of creating a new one.
func TestReportTaskStatus_CheckRunCacheFallbackUpdatesExisting(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhaseSucceeded, map[string]string{
		AnnotationGitHubChecks:    "enabled",
		AnnotationSourceSHA:       "abc123def",
		AnnotationGitHubCheckName: "Kelos: my-spawner",
	})
	task.UID = types.UID("uid-check-fallback")

	cache := NewReportStateCache()
	cache.storeCheckRun(task.UID, 9001, "in_progress")

	tr := &TaskReporter{
		Client:         fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: &ChecksReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL},
		Cache:          cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected 1 API call, got %d", len(*records))
	}
	if (*records)[0].method != "update" {
		t.Errorf("Expected update via cached check run ID, got %s", (*records)[0].method)
	}
}

// TestReportTaskStatus_CheckRunCacheShortCircuitsDuplicate simulates two
// reconciles firing for the same check run phase before the annotation Update
// propagates. The first call creates the check run; the second must not create
// a duplicate.
func TestReportTaskStatus_CheckRunCacheShortCircuitsDuplicate(t *testing.T) {
	server, records := newTestChecksServer(t)
	defer server.Close()

	annotations := map[string]string{
		AnnotationGitHubChecks:    "enabled",
		AnnotationSourceSHA:       "abc123def",
		AnnotationGitHubCheckName: "Kelos: my-spawner",
	}

	first := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, annotations)
	first.UID = types.UID("uid-check-shortcircuit")
	first.ResourceVersion = ""

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(first).Build()
	cache := NewReportStateCache()
	tr := &TaskReporter{
		Client:         cl,
		Reporter:       &GitHubReporter{Owner: "o", Repo: "r", Token: "t"},
		ChecksReporter: &ChecksReporter{Owner: "o", Repo: "r", Token: "t", BaseURL: server.URL},
		Cache:          cache,
	}

	if err := tr.ReportTaskStatus(context.Background(), first); err != nil {
		t.Fatalf("First report failed: %v", err)
	}

	// Simulate a stale cached read: a second copy of the Task that has not yet
	// observed the annotation Update from the first reconcile.
	stale := newTaskWithAnnotations("test-task", "default", kelosv1alpha1.TaskPhasePending, map[string]string{
		AnnotationGitHubChecks:    "enabled",
		AnnotationSourceSHA:       "abc123def",
		AnnotationGitHubCheckName: "Kelos: my-spawner",
	})
	stale.UID = types.UID("uid-check-shortcircuit")

	if err := tr.ReportTaskStatus(context.Background(), stale); err != nil {
		t.Fatalf("Second report failed: %v", err)
	}

	if len(*records) != 1 {
		t.Fatalf("Expected exactly 1 GitHub API call, got %d (%+v)", len(*records), *records)
	}
	if (*records)[0].method != "create" {
		t.Errorf("Expected the single call to be a create, got %s", (*records)[0].method)
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
	wantMsgs := FormatSlackTransitionMessage("succeeded", task.Name, task.Status.Message, task.Status.Results)
	if posted[0].msg.Text != wantMsgs[0].Text {
		t.Errorf("text = %q, want %q", posted[0].msg.Text, wantMsgs[0].Text)
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
	// Progress messages should include Block Kit blocks so the activity
	// indicator loop can append a context element.
	if len(posted[0].msg.Blocks) == 0 {
		t.Error("expected progress message to include blocks for activity indicator support")
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

func TestSlackTaskReporter_EditsProgressOnNewText(t *testing.T) {
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
	var updatedTexts []string
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			postedTexts = append(postedTexts, msg.Text)
			return "1234567890.111111", nil
		},
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updatedTexts = append(updatedTexts, msg.Text)
			return nil
		},
	}

	pr := &fakeProgressReader{text: "First update"}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: pr,
	}

	// First call — posts a new message
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(postedTexts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(postedTexts))
	}
	if postedTexts[0] != "First update" {
		t.Errorf("first post = %q, want 'First update'", postedTexts[0])
	}

	// Change the text
	pr.text = "Second update"

	// Second call — should edit the existing message, not post a new one
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(postedTexts) != 1 {
		t.Errorf("expected still 1 post (edit should not create new), got %d", len(postedTexts))
	}
	if len(updatedTexts) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updatedTexts))
	}
	if updatedTexts[0] != "Second update" {
		t.Errorf("update text = %q, want 'Second update'", updatedTexts[0])
	}

	// Verify that the activity target's BaseMsg was updated to the new progress text.
	tr.mu.Lock()
	state := tr.activity[task.UID]
	tr.mu.Unlock()
	if state == nil {
		t.Fatal("expected activity state to be set after in-place edit")
	}
	if state.BaseMsg.Text != "Second update" {
		t.Errorf("activity BaseMsg.Text = %q, want 'Second update'", state.BaseMsg.Text)
	}
	if len(state.BaseMsg.Blocks) == 0 {
		t.Error("expected activity BaseMsg to have blocks after in-place edit")
	}
}

func TestSlackTaskReporter_ClearsProgressTSOnUpdateFailure(t *testing.T) {
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
			UID:       "uid-fail-update",
			Annotations: map[string]string{
				AnnotationSlackReporting:   "enabled",
				AnnotationSlackChannel:     "C123ABC",
				AnnotationSlackThreadTS:    "1234567890.123456",
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

	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			return "1234567890.111111", nil
		},
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			return fmt.Errorf("message_not_found")
		},
	}

	pr := &fakeProgressReader{text: "First update"}
	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: pr,
	}

	// First call posts a new progress message.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tr.mu.Lock()
	if tr.progressTS["uid-fail-update"] == "" {
		t.Fatal("expected progressTS to be set after first post")
	}
	tr.mu.Unlock()

	// Change text so dedup allows the update attempt.
	pr.text = "Second update"

	// Second call tries to edit but updateFn returns an error.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.progressTS["uid-fail-update"] != "" {
		t.Error("expected progressTS to be cleared after update failure")
	}
	if tr.lastProgress["uid-fail-update"] != "First update" {
		t.Errorf("lastProgress = %q, want 'First update' (should not update on failure)", tr.lastProgress["uid-fail-update"])
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

	// Verify caches are populated
	tr.mu.Lock()
	if _, ok := tr.lastProgress["uid-clear"]; !ok {
		t.Error("expected progress text cache to be populated")
	}
	if _, ok := tr.progressTS["uid-clear"]; !ok {
		t.Error("expected progress timestamp cache to be populated")
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

	// Verify both caches were cleared
	tr.mu.Lock()
	if _, ok := tr.lastProgress["uid-clear"]; ok {
		t.Error("expected progress text cache to be cleared after terminal phase")
	}
	if _, ok := tr.progressTS["uid-clear"]; ok {
		t.Error("expected progress timestamp cache to be cleared after terminal phase")
	}
	tr.mu.Unlock()
}

func TestSlackTaskReporter_EditsProgressMessageOnTerminalPhase(t *testing.T) {
	// When a progress message exists and the task completes, the final
	// result should be edited into the progress message instead of posting
	// a new reply, keeping the thread compact (ack + single response).
	task := newRunningTaskWithAnnotations("test-task", "uid-edit-terminal", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var posts []slackReplyRecord
	var updates []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			posts = append(posts, slackReplyRecord{method: "post", channel: channel, threadTS: threadTS, msg: msg})
			return "ts-progress", nil
		},
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updates = append(updates, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, msg: msg})
			return nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "Working on the analysis..."},
	}

	// Post a progress update (simulates the running phase).
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error posting progress: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 post (progress), got %d", len(posts))
	}

	// Transition to succeeded.
	succeededTask := task.DeepCopy()
	succeededTask.Status.Phase = kelosv1alpha1.TaskPhaseSucceeded
	succeededTask.Status.Message = "Here is the final answer."
	succeededTask.Status.Results = map[string]string{"response": "done"}

	cl2 := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(succeededTask).Build()
	tr.Client = cl2

	postsBefore := len(posts)
	if err := tr.ReportTaskStatus(context.Background(), succeededTask); err != nil {
		t.Fatalf("unexpected error on terminal phase: %v", err)
	}

	// Should NOT have posted a new reply.
	if len(posts) != postsBefore {
		t.Errorf("expected no new posts on terminal phase, got %d new", len(posts)-postsBefore)
	}

	// Should have updated the progress message with the final content.
	var terminalUpdate *slackReplyRecord
	for i := range updates {
		if updates[i].threadTS == "ts-progress" {
			terminalUpdate = &updates[i]
		}
	}
	if terminalUpdate == nil {
		t.Fatal("expected an update to the progress message with final content")
	}
	if len(terminalUpdate.msg.Blocks) == 0 {
		t.Error("expected final update to include blocks")
	}
}

func TestSlackTaskReporter_FallsBackToPostOnUpdateFailure(t *testing.T) {
	// If editing the progress message fails, we should fall back to posting
	// a new reply so the final result is never lost.
	task := newRunningTaskWithAnnotations("test-task", "uid-fallback-post", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var posts []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			posts = append(posts, slackReplyRecord{method: "post", channel: channel, threadTS: threadTS, msg: msg})
			return "ts-progress", nil
		},
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			return fmt.Errorf("slack API error")
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "Working on it..."},
	}

	// Post a progress update.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error posting progress: %v", err)
	}

	// Transition to succeeded — update will fail, should fall back to post.
	succeededTask := task.DeepCopy()
	succeededTask.Status.Phase = kelosv1alpha1.TaskPhaseSucceeded
	succeededTask.Status.Results = map[string]string{"response": "done"}

	cl2 := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(succeededTask).Build()
	tr.Client = cl2

	if err := tr.ReportTaskStatus(context.Background(), succeededTask); err != nil {
		t.Fatalf("unexpected error on terminal phase: %v", err)
	}

	// Should have fallen back to posting a new reply (2 total: progress + final).
	if len(posts) != 2 {
		t.Errorf("expected 2 posts (progress + fallback), got %d", len(posts))
	}
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

	// Seed caches with entries for two tasks
	tr.setLastProgress("uid-active", "some text")
	tr.setProgressTS("uid-active", "1234.5678")
	tr.setLastProgress("uid-deleted", "other text")
	tr.setProgressTS("uid-deleted", "1234.9999")

	// Sweep with only the active UID
	activeUIDs := map[types.UID]bool{
		"uid-active": true,
	}
	tr.SweepProgressCache(activeUIDs)

	tr.mu.Lock()
	defer tr.mu.Unlock()

	if _, ok := tr.lastProgress["uid-active"]; !ok {
		t.Error("expected active UID to remain in lastProgress cache")
	}
	if _, ok := tr.progressTS["uid-active"]; !ok {
		t.Error("expected active UID to remain in progressTS cache")
	}
	if _, ok := tr.lastProgress["uid-deleted"]; ok {
		t.Error("expected deleted UID to be swept from lastProgress cache")
	}
	if _, ok := tr.progressTS["uid-deleted"]; ok {
		t.Error("expected deleted UID to be swept from progressTS cache")
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

	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)[0]
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
	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)[0]
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

	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)[0]
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

func TestSlackTaskReporter_ActivityIndicatorWorksOnProgressMessage(t *testing.T) {
	// Regression test: the activity indicator must keep working after the
	// progress snapshot replaces the accepted message as the activity target.
	// Previously, progress messages were text-only (no blocks), causing
	// appendActivityContext to skip the update.
	task := newRunningTaskWithAnnotations("test-task", "uid-progress-activity", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var updates []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			return "ts-progress", nil
		},
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updates = append(updates, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, msg: msg})
			return nil
		},
	}

	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "I found the issue in the config."},
		ActivityReader: &fakeActivityReader{text: "Reading `config.yaml`..."},
	}
	// Seed initial activity target (from accepted post).
	tr.setActivityTarget(task.UID, "ts-accepted", FormatSlackTransitionMessage("accepted", task.Name, "", nil)[0])

	// Trigger a progress update — this should post a new progress message
	// and re-point the activity target at it.
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The first update should reset the old accepted message back to its
	// base content (strip the activity indicator).
	if len(updates) < 1 {
		t.Fatal("expected at least 1 update (reset of accepted message)")
	}
	if updates[0].threadTS != "ts-accepted" {
		t.Errorf("reset update targeted %q, want ts-accepted", updates[0].threadTS)
	}

	// Now call UpdateActivityIndicator — it should update the progress
	// message (not the old accepted message) with the activity context.
	tr.UpdateActivityIndicator(context.Background(), task)

	if len(updates) != 2 {
		t.Fatalf("expected 2 updates (reset + activity), got %d", len(updates))
	}
	if updates[1].threadTS != "ts-progress" {
		t.Errorf("activity update targeted %q, want ts-progress", updates[1].threadTS)
	}
	if len(updates[1].msg.Blocks) == 0 {
		t.Error("expected activity update to include blocks")
	}
}

func TestSlackTaskReporter_ResetsOldActivityIndicatorOnTargetSwitch(t *testing.T) {
	// When a progress message is posted and becomes the new activity target,
	// the old target (the accepted ack) should be updated back to its base
	// content so there is only one active indicator in the thread.
	task := newRunningTaskWithAnnotations("test-task", "uid-reset-indicator", map[string]string{
		AnnotationSlackReporting:   "enabled",
		AnnotationSlackChannel:     "C123ABC",
		AnnotationSlackThreadTS:    "1234567890.123456",
		AnnotationSlackReportPhase: "accepted",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()

	var updates []slackReplyRecord
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			return "ts-progress", nil
		},
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updates = append(updates, slackReplyRecord{method: "update", channel: channel, threadTS: messageTS, msg: msg})
			return nil
		},
	}

	acceptedMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)[0]
	tr := &SlackTaskReporter{
		Client:         cl,
		Reporter:       reporter,
		ProgressReader: &fakeProgressReader{text: "Investigating the logs..."},
	}
	tr.setActivityTarget(task.UID, "ts-accepted", acceptedMsg)

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First update resets the accepted message to its base content.
	if len(updates) < 1 {
		t.Fatal("expected at least 1 update for the reset call")
	}
	resetUpdate := updates[0]
	if resetUpdate.threadTS != "ts-accepted" {
		t.Errorf("reset targeted %q, want ts-accepted", resetUpdate.threadTS)
	}
	// The reset message should match the base accepted message (no activity element).
	if resetUpdate.msg.Text != acceptedMsg.Text {
		t.Errorf("reset text = %q, want %q", resetUpdate.msg.Text, acceptedMsg.Text)
	}
	if len(resetUpdate.msg.Blocks) != len(acceptedMsg.Blocks) {
		t.Errorf("reset blocks count = %d, want %d (base message blocks)", len(resetUpdate.msg.Blocks), len(acceptedMsg.Blocks))
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

	baseMsg := FormatSlackTransitionMessage("accepted", task.Name, "", nil)[0]
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
	baseMsg := FormatSlackTransitionMessage("accepted", "test-task", "", nil)[0]
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
	baseMsg := FormatSlackTransitionMessage("accepted", "test-task", "", nil)[0]
	originalBlockCount := len(baseMsg.Blocks)

	_ = appendActivityContext(baseMsg, "Reading...")

	if len(baseMsg.Blocks) != originalBlockCount {
		t.Errorf("base message mutated: blocks went from %d to %d", originalBlockCount, len(baseMsg.Blocks))
	}
}
