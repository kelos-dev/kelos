package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"sync"
	"testing"

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
