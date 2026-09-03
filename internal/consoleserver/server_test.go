package consoleserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionreset"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
)

type fakeSessionAttachmentTransfer struct {
	uploadedName string
	uploadedData []byte
	attachment   sessionruntime.Attachment
	downloadData []byte
}

type patchErrorClient struct {
	client.Client
	err error
}

func (c *patchErrorClient) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return c.err
}

func (f *fakeSessionAttachmentTransfer) Upload(_ context.Context, _, _, name string, source io.Reader) (sessionruntime.Attachment, error) {
	f.uploadedName = name
	f.uploadedData, _ = io.ReadAll(source)
	return f.attachment, nil
}

func (f *fakeSessionAttachmentTransfer) Download(_ context.Context, _, _, _ string) (sessionruntime.Attachment, []byte, error) {
	return f.attachment, f.downloadData, nil
}

func TestAuthenticationProtectsApplicationAndAPI(t *testing.T) {
	server := testServer(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login" {
		t.Fatalf("GET / status = %d location = %q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"wrong"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid login status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid login status = %d body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("authentication cookie = %#v", cookies)
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Kelos Console") {
		t.Fatalf("authenticated application status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestConsoleApplicationIncludesResourceViews(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		`id="console-overview"`,
		`id="console-sessions"`,
		`id="console-resources"`,
		`id="overview-view"`,
		`id="resources-view"`,
		`id="resource-diagram-tab"`,
		`id="resource-diagram"`,
		`id="resource-relationship-focus"`,
		`id="resource-inventory-panel"`,
		`id="resource-type-list"`,
		`id="resource-search"`,
		`id="resource-detail-dialog"`,
		`id="resource-detail-logs-tab"`,
		`id="refresh-resource-logs"`,
		`id="resource-detail-logs"`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("Console application does not contain %s", expected)
		}
	}
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"async function loadResources",
		"function renderOverview",
		"function renderResourceDiagram",
		"function renderResources",
		"function setResourceDetailView",
		"function handleResourceDetailTabKeydown",
		"async function loadResourceTaskLogs",
		"async function openResourceDetail",
		"elements.resourceDetailTabs.addEventListener('keydown', handleResourceDetailTabKeydown)",
		"/api/resources?namespace=",
		"/api/resources/tasks/",
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Console JavaScript does not contain %q", expected)
		}
	}
	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{".resource-summary-grid", ".resource-summary-card", ".resource-diagram", ".resource-relationship-node", ".resource-browser", ".resource-type-list", ".resource-table", ".resource-detail-dialog", ".resource-detail-tabs[hidden]"} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Console styles do not contain %q", expected)
		}
	}
}

func TestConsoleResourcesListAndGet(t *testing.T) {
	server := testServer(t)
	objects := []client.Object{
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "running-task", Namespace: "team-a", CreationTimestamp: metav1.NewTime(time.Unix(20, 0))},
			Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseRunning, Message: "Agent is working"},
		},
		&kelos.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "repository", Namespace: "team-a", CreationTimestamp: metav1.NewTime(time.Unix(10, 0))}},
		&kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "other-task", Namespace: "team-b"}},
	}
	for _, object := range objects {
		if err := server.client.Create(t.Context(), object); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/resources?namespace=team-a", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("resource inventory status = %d body = %s", response.Code, response.Body.String())
	}
	var inventory struct {
		Namespace string                 `json:"namespace"`
		Groups    []consoleResourceGroup `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Namespace != "team-a" || len(inventory.Groups) != 4 {
		t.Fatalf("resource inventory = %#v", inventory)
	}
	collections := make(map[string]consoleResourceCollection)
	for _, group := range inventory.Groups {
		for _, collection := range group.Resources {
			collections[collection.Resource] = collection
		}
	}
	if len(collections) != len(consoleResourceDefinitions) {
		t.Fatalf("resource collections = %d, want %d", len(collections), len(consoleResourceDefinitions))
	}
	tasks := collections["tasks"].Items
	if len(tasks) != 1 || tasks[0].Name != "running-task" || tasks[0].Phase != "Running" || tasks[0].Message != "Agent is working" {
		t.Fatalf("Task summaries = %#v", tasks)
	}
	workspaces := collections["workspaces"].Items
	if len(workspaces) != 1 || workspaces[0].Name != "repository" {
		t.Fatalf("Workspace summaries = %#v", workspaces)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/resources/tasks/team-a/running-task", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("resource detail status = %d body = %s", response.Code, response.Body.String())
	}
	var detail consoleResourceDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"apiVersion: kelos.dev/v1alpha2", "kind: Task", "phase: Running"} {
		if !strings.Contains(detail.YAML, expected) {
			t.Errorf("resource detail YAML does not contain %q:\n%s", expected, detail.YAML)
		}
	}
}

func TestConsoleResourceRelationships(t *testing.T) {
	server := testServer(t)
	controller := true
	objects := []client.Object{
		&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "issues", Namespace: "team-a", UID: "spawner-uid"},
			Spec: kelos.TaskSpawnerSpec{TaskTemplate: kelos.TaskTemplate{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"},
			}},
		},
		&kelos.TaskPipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "release", Namespace: "team-a", UID: "pipeline-uid"},
			Spec: kelos.TaskPipelineSpec{Stages: []kelos.PipelineStage{{
				Name: "verify",
				TaskTemplate: kelos.PipelineTaskTemplate{Worker: &kelos.WorkerSpec{
					WorkspaceRef:    &kelos.WorkspaceReference{Name: "repository"},
					AgentConfigRefs: []kelos.AgentConfigReference{{Name: "reviewer"}},
				}},
			}}},
		},
		&kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "automation", Namespace: "team-a"},
			Spec: kelos.TaskSpawnerSpec{TaskTemplate: kelos.TaskTemplate{
				WorkspaceRef:    &kelos.WorkspaceReference{Name: "repository"},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "reviewer"}},
				DependsOn:       []string{"prerequisite"},
			}},
		},
		&kelos.SessionSpawner{ObjectMeta: metav1.ObjectMeta{Name: "webhook", Namespace: "team-a"}},
		&kelos.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "repository", Namespace: "team-a"}},
		&kelos.AgentConfig{ObjectMeta: metav1.ObjectMeta{Name: "reviewer", Namespace: "team-a"}},
		&kelos.WorkerPool{
			ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "team-a"},
			Spec: kelos.WorkerPoolSpec{Worker: kelos.WorkerSpec{
				WorkspaceRef:    &kelos.WorkspaceReference{Name: "repository"},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "reviewer"}},
			}},
		},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fix-console",
				Namespace: "team-a",
				Labels:    map[string]string{"team": "console"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: kelos.GroupVersion.String(),
					Kind:       "TaskSpawner",
					Name:       "issues",
					UID:        "spawner-uid",
					Controller: &controller,
				}},
			},
			Spec: kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}},
		},
		&kelos.Task{ObjectMeta: metav1.ObjectMeta{Name: "prerequisite", Namespace: "team-a"}},
		&kelos.Task{ObjectMeta: metav1.ObjectMeta{
			Name:      "pipeline-task",
			Namespace: "team-a",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: kelos.GroupVersion.String(),
				Kind:       "TaskPipeline",
				Name:       "release",
				UID:        "pipeline-uid",
				Controller: &controller,
			}},
		}},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-task",
				Namespace: "team-a",
				Labels:    map[string]string{"kelos.dev/taskspawner": "issues"},
			},
			Spec: kelos.TaskSpec{
				WorkspaceRef:    &kelos.WorkspaceReference{Name: "repository"},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "reviewer"}},
				DependsOn:       []string{"prerequisite"},
			},
		},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "manual-task",
				Namespace:   "team-a",
				Annotations: map[string]string{"kelos.dev/created-from-taskspawner": "issues"},
			},
		},
		&kelos.Session{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "console-session",
				Namespace:   "team-a",
				Annotations: map[string]string{"kelos.dev/sessionspawner-name": "webhook"},
			},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				WorkspaceRef:    &kelos.WorkspaceReference{Name: "repository"},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "reviewer"}},
			}},
		},
		&kelos.TaskRecord{
			ObjectMeta: metav1.ObjectMeta{Name: "fix-console-record", Namespace: "team-a", Labels: map[string]string{"team": "console"}},
			Spec:       kelos.TaskRecordSpec{TaskRef: kelos.TaskReference{Name: "fix-console"}},
		},
		&kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "console-budget", Namespace: "team-a"},
			Spec:       kelos.TaskBudgetSpec{TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "console"}}},
		},
	}
	for _, object := range objects {
		if err := server.client.Create(t.Context(), object); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/resources?namespace=team-a", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("resource inventory status = %d body = %s", response.Code, response.Body.String())
	}
	var inventory struct {
		Relationships []consoleResourceRelationship `json:"relationships"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}

	actual := make(map[string]bool, len(inventory.Relationships))
	for _, relationship := range inventory.Relationships {
		key := fmt.Sprintf("%s/%s %s %s/%s", relationship.Source.Resource, relationship.Source.Name, relationship.Relationship, relationship.Target.Resource, relationship.Target.Name)
		actual[key] = relationship.Inferred
	}
	expected := map[string]bool{
		"taskpipelines/release creates tasks/pipeline-task":                      false,
		"taskpipelines/release uses agentconfigs/reviewer":                       false,
		"taskpipelines/release uses workspaces/repository":                       false,
		"taskspawners/automation spawned Tasks depend on tasks/prerequisite":     false,
		"taskspawners/automation uses agentconfigs/reviewer":                     false,
		"taskspawners/automation uses workspaces/repository":                     false,
		"taskspawners/issues creates tasks/fix-console":                          false,
		"taskspawners/issues creates tasks/legacy-task":                          true,
		"taskspawners/issues runs Tasks on workerpools/pool":                     false,
		"tasks/fix-console runs on workerpools/pool":                             false,
		"tasks/legacy-task depends on tasks/prerequisite":                        false,
		"tasks/legacy-task uses agentconfigs/reviewer":                           false,
		"tasks/legacy-task uses workspaces/repository":                           false,
		"workerpools/pool uses workspaces/repository":                            false,
		"workerpools/pool uses agentconfigs/reviewer":                            false,
		"sessions/console-session uses workspaces/repository":                    false,
		"sessions/console-session uses agentconfigs/reviewer":                    false,
		"sessionspawners/webhook creates sessions/console-session":               false,
		"tasks/fix-console recorded as taskrecords/fix-console-record":           false,
		"taskbudgets/console-budget limits tasks/fix-console":                    true,
		"taskbudgets/console-budget accounts for taskrecords/fix-console-record": true,
		"taskspawners/issues creates tasks/manual-task":                          false,
	}
	if !maps.Equal(actual, expected) {
		t.Fatalf("resource relationships = %#v, want %#v", actual, expected)
	}
}

func TestConsoleTaskLogs(t *testing.T) {
	server := testServer(t)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "running-task", Namespace: "team-a"},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseRunning, PodName: "running-task-pod"},
	}
	if err := server.client.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	server.taskLogStream = func(_ context.Context, got *kelos.Task, tailLines int64) (io.ReadCloser, error) {
		if got.Name != task.Name || got.Status.PodName != task.Status.PodName {
			t.Fatalf("Task passed to log stream = %#v", got)
		}
		if tailLines != taskLogTailLineLimit {
			t.Fatalf("Task log tail lines = %d, want %d", tailLines, taskLogTailLineLimit)
		}
		return io.NopCloser(strings.NewReader("agent output\n")), nil
	}

	request := httptest.NewRequest(http.MethodGet, "/api/resources/tasks/team-a/running-task/logs", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("Task logs status = %d body = %s", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Errorf("Task logs Content-Type = %q, want %q", got, want)
	}
	if got, want := response.Body.String(), "agent output\n"; got != want {
		t.Errorf("Task logs = %q, want %q", got, want)
	}
}

func TestConsoleWorkerPoolTaskLogsOnlyIncludeSelectedTask(t *testing.T) {
	server := testServer(t)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-b", Namespace: "team-a"},
		Spec:       kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseSucceeded, PodName: "pool-0"},
	}
	if err := server.client.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	server.taskLogStream = func(_ context.Context, _ *kelos.Task, tailLines int64) (io.ReadCloser, error) {
		if tailLines != workerTaskLogLineLimit {
			t.Fatalf("WorkerPool Task log tail lines = %d, want %d", tailLines, workerTaskLogLineLimit)
		}
		return io.NopCloser(strings.NewReader(strings.Join([]string{
			"worker startup",
			"---KELOS_TASK_START--- task-a",
			"task-a output",
			"---KELOS_TASK_END--- task-a",
			"---KELOS_TASK_START--- task-b",
			"task-b output",
			"---KELOS_TASK_END--- task-b",
			"worker idle",
		}, "\n"))), nil
	}

	request := httptest.NewRequest(http.MethodGet, "/api/resources/tasks/team-a/task-b/logs", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("WorkerPool Task logs status = %d body = %s", response.Code, response.Body.String())
	}
	if got, want := response.Body.String(), "task-b output\n"; got != want {
		t.Errorf("WorkerPool Task logs = %q, want %q", got, want)
	}
}

func TestTaskPodLogOptionsBoundLogRetrieval(t *testing.T) {
	task := &kelos.Task{}
	options := taskPodLogOptions(task, taskLogTailLineLimit)
	if options.Container != kelos.AgentContainerName {
		t.Errorf("Pod log container = %q, want %q", options.Container, kelos.AgentContainerName)
	}
	if options.TailLines == nil || *options.TailLines != taskLogTailLineLimit {
		t.Errorf("Pod log tail lines = %v, want %d", options.TailLines, taskLogTailLineLimit)
	}
	if options.SinceTime != nil {
		t.Errorf("Pod log since time = %v, want nil", options.SinceTime)
	}

	startedAt := metav1.NewTime(time.Unix(20, 0))
	task.Spec.WorkerPoolRef = &kelos.WorkerPoolReference{Name: "pool"}
	task.Status.StartTime = &startedAt
	options = taskPodLogOptions(task, workerTaskLogLineLimit)
	if options.TailLines == nil || *options.TailLines != workerTaskLogLineLimit {
		t.Errorf("WorkerPool Pod log tail lines = %v, want %d", options.TailLines, workerTaskLogLineLimit)
	}
	wantSinceTime := startedAt.Add(-workerTaskLogSinceMargin)
	if options.SinceTime == nil || !options.SinceTime.Time.Equal(wantSinceTime) {
		t.Errorf("WorkerPool Pod log since time = %v, want %v", options.SinceTime, wantSinceTime)
	}

	createdAt := metav1.NewTime(time.Unix(18, 0))
	task.CreationTimestamp = createdAt
	options = taskPodLogOptions(task, workerTaskLogLineLimit)
	if options.SinceTime == nil || !options.SinceTime.Time.Equal(createdAt.Time) {
		t.Errorf("WorkerPool Pod log clamped since time = %v, want %v", options.SinceTime, createdAt)
	}

	task.Status.StartTime = nil
	options = taskPodLogOptions(task, workerTaskLogLineLimit)
	if options.SinceTime == nil || !options.SinceTime.Time.Equal(createdAt.Time) {
		t.Errorf("WorkerPool Pod log since time without start time = %v, want %v", options.SinceTime, createdAt)
	}
}

func TestReadTaskLogsBoundsHistoricalWorkerPoolTaskRetrieval(t *testing.T) {
	server := testServer(t)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-b", Namespace: "team-a"},
		Spec:       kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseSucceeded, PodName: "pool-0"},
	}
	var requestedTailLines []int64
	server.taskLogStream = func(_ context.Context, _ *kelos.Task, tailLines int64) (io.ReadCloser, error) {
		requestedTailLines = append(requestedTailLines, tailLines)
		return io.NopCloser(strings.NewReader("recent unrelated worker output\n")), nil
	}

	logs, err := server.readTaskLogs(t.Context(), task)
	if !errors.Is(err, errTaskLogSegmentUnavailable) {
		t.Fatalf("historical WorkerPool Task log error = %v, want %v", err, errTaskLogSegmentUnavailable)
	}
	if logs != "" {
		t.Errorf("historical WorkerPool Task logs = %q, want empty", logs)
	}
	if got, want := fmt.Sprint(requestedTailLines), fmt.Sprint([]int64{workerTaskLogLineLimit}); got != want {
		t.Errorf("WorkerPool Task log requests = %s, want %s", got, want)
	}
}

func TestReadTaskLogsRequiresMarkerForRunningWorkerPoolTask(t *testing.T) {
	server := testServer(t)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-b", Namespace: "team-a"},
		Spec:       kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseRunning, PodName: "pool-0"},
	}
	var requestedTailLines []int64
	server.taskLogStream = func(_ context.Context, _ *kelos.Task, tailLines int64) (io.ReadCloser, error) {
		requestedTailLines = append(requestedTailLines, tailLines)
		return io.NopCloser(strings.NewReader("recent output without the start marker\n")), nil
	}

	logs, err := server.readTaskLogs(t.Context(), task)
	if !errors.Is(err, errTaskLogSegmentUnavailable) {
		t.Fatalf("running WorkerPool Task log error = %v, want %v", err, errTaskLogSegmentUnavailable)
	}
	if logs != "" {
		t.Errorf("running WorkerPool Task logs = %q, want empty", logs)
	}
	if got, want := fmt.Sprint(requestedTailLines), fmt.Sprint([]int64{workerTaskLogLineLimit}); got != want {
		t.Errorf("running WorkerPool Task log requests = %s, want %s", got, want)
	}
}

func TestConsoleWorkerPoolTaskLogsReportUnavailableSegment(t *testing.T) {
	server := testServer(t)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-b", Namespace: "team-a"},
		Spec:       kelos.TaskSpec{WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"}},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseSucceeded, PodName: "pool-0"},
	}
	if err := server.client.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	server.taskLogStream = func(_ context.Context, _ *kelos.Task, _ int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("recent unrelated worker output\n")), nil
	}

	request := httptest.NewRequest(http.MethodGet, "/api/resources/tasks/team-a/task-b/logs", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `Task \"task-b\" log segment is unavailable`) {
		t.Fatalf("unavailable WorkerPool Task logs status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestTailLogLinesAppliesLineAndByteLimits(t *testing.T) {
	logs, err := tailLogLines(strings.NewReader("one\ntwo\nthree\nfour\n"), 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if want := taskLogOmittedMessage + "three\nfour\n"; logs != want {
		t.Errorf("line-limited logs = %q, want %q", logs, want)
	}

	maxBytes := len(taskLogOmittedMessage) + 10
	logs, err = tailLogLines(strings.NewReader(strings.Repeat("x", 100)+"\n"), 10, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) > maxBytes {
		t.Errorf("byte-limited logs length = %d, want at most %d", len(logs), maxBytes)
	}
	if want := taskLogOmittedMessage + strings.Repeat("x", 9) + "\n"; logs != want {
		t.Errorf("byte-limited logs = %q, want %q", logs, want)
	}

	maxBytes = len(taskLogOmittedMessage) + 6
	logs, err = tailLogLines(strings.NewReader("한글\n"), 10, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(logs) {
		t.Errorf("byte-limited logs are not valid UTF-8: %q", logs)
	}
	if want := taskLogOmittedMessage + "글\n"; logs != want {
		t.Errorf("UTF-8 byte-limited logs = %q, want %q", logs, want)
	}
}

func TestTailWorkerTaskLogSegmentStopsAtTaskEnd(t *testing.T) {
	logs, found, err := tailWorkerTaskLogSegment(strings.NewReader(strings.Join([]string{
		"worker startup",
		"---KELOS_TASK_START--- task-b",
		"first",
		"second",
		"third",
		"---KELOS_TASK_END--- task-b",
		"later worker output",
	}, "\n")), "task-b", 2, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("WorkerPool Task log segment was not found")
	}
	if want := taskLogOmittedMessage + "second\nthird\n"; logs != want {
		t.Errorf("WorkerPool Task log tail = %q, want %q", logs, want)
	}
}

func TestTailWorkerTaskLogSegmentAcceptsTailWithoutStartMarker(t *testing.T) {
	logs, found, err := tailWorkerTaskLogSegment(strings.NewReader(strings.Join([]string{
		"second-to-last",
		"last",
		"---KELOS_TASK_END--- task-b",
		"later worker output",
	}, "\n")), "task-b", 2, 1024, true)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("WorkerPool Task log tail was not found")
	}
	if want := "second-to-last\nlast\n"; logs != want {
		t.Errorf("WorkerPool Task partial log tail = %q, want %q", logs, want)
	}
}

func TestConsoleTaskLogsReportMissingPod(t *testing.T) {
	server := testServer(t)
	if err := server.client.Create(t.Context(), &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-task", Namespace: "team-a"},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhasePending},
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/resources/tasks/team-a/pending-task/logs", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `Task \"pending-task\" has no pod yet`) {
		t.Fatalf("Task logs without Pod status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionFormUsesResourceSelectors(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		`id="namespace-form"`,
		`id="active-namespace"`,
		`id="session-source"`,
		`id="session-source-status"`,
		`name="namespace" required value="default" autocomplete="off" readonly>`,
		`id="credential-secret"`,
		`id="workspace-select"`,
		`name="initialBranch" id="session-initial-branch"`,
		`name="initialPrompt" id="session-initial-prompt"`,
		`With emptyDir, Pod replacement loses history and may submit this prompt again; use a persistent volume to prevent replay.`,
		`id="agent-config-select"`,
		`id="selected-agent-configs"`,
		`id="session-mode-yaml"`,
		`id="session-yaml"`,
		`id="volume-claim-enabled"`,
		`<option value="opencode">OpenCode</option>`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("new Session form does not contain %s", expected)
		}
	}
}

func TestSessionSourceJavaScriptPreservesSelectedSource(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for description, expected := range map[string]string{
		"shared loading and submission guard":     "function updateCreationBusyState() {\n        const busy = state.sourceLoading || state.creatingSession;\n        elements.sessionSource.disabled = busy;\n        elements.createButton.disabled = busy;\n    }",
		"submit loading guard":                    "if (state.sourceLoading || state.creatingSession)\n            return;",
		"namespace invalidation reset":            "state.sourceGeneration += 1;\n        setSourceLoading(false);",
		"explicit StorageClass tracking":          "state.sourceStorageClassNamePresent = Boolean(claim && 'storageClassName' in claim);",
		"explicit empty StorageClass copy":        "if (storageClassName || state.sourceStorageClassNamePresent) {\n                        payload.volumeClaimTemplate.storageClassName = storageClassName;\n                    }",
		"advanced reference warning":              "in YAML for additional namespace-scoped references.",
		"unsupported spec field YAML requirement": "const allowedSpecFields = new Set(['worker', 'suspend', 'initialBranch', 'initialPrompt', 'volumeClaimTemplate']);\n        if (Object.keys(manifest.spec).some(key => !allowedSpecFields.has(key)))\n            return false;",
		"suspended source YAML requirement":       "if (manifest.spec.suspend === true)\n            return false;",
		"source initial branch population":        "elements.form.elements.initialBranch.value = manifest.spec.initialBranch || '';",
		"source initial prompt population":        "elements.form.elements.initialPrompt.value = manifest.spec.initialPrompt || '';",
		"initial branch form submission":          "const initialBranch = formValue(values, 'initialBranch').trim();\n                if (initialBranch)\n                    payload.initialBranch = initialBranch;",
		"initial prompt form submission":          "const initialPrompt = formValue(values, 'initialPrompt');\n                if (initialPrompt.trim())\n                    payload.initialPrompt = initialPrompt;",
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session source JavaScript is missing %s: %s", description, expected)
		}
	}
	if strings.Contains(javascript, "No namespace-scoped references.") {
		t.Error("Session source JavaScript claims there are no namespace-scoped references without checking advanced settings")
	}
}

func TestSessionJavaScriptCachesIncrementalHistory(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for description, expected := range map[string]string{
		"bounded Session view cache":        "const maxCachedSessionViews = 5;",
		"Session incarnation cache key":     "function sessionViewKey(session) {",
		"recreated Session invalidation":    "state.selected.uid !== current.uid",
		"view state preservation":           "function saveCurrentSessionView() {",
		"bounded history item subscription": "historyItems: sessionHistoryItemLimit",
		"bounded history byte subscription": "historyBytes: sessionHistoryByteLimit",
		"history cursor preservation":       "view.historyCursor = state.historyCursor",
		"reconnect high-water tracking":     "state.lastEventID = Math.max(state.lastEventID, state.historyLastEventID)",
		"older history request guard":       "if (state.historyPageLoading || !state.historyCursor",
		"older history isolation":           "function replayOlderHistoryPage(events) {",
		"replaced journal reset":            "state.currentView.journalID !== event.journalId",
		"deferred history rendering":        "function finishHistoryReplay(historyState) {",
		"selection bottom pin":              "state.pinHistoryToBottom = true;",
		"frame-batched bottom anchor":       "function scheduleBottomAnchor() {",
		"instant bottom positioning":        "elements.messages.scrollTop = elements.messages.scrollHeight;",
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session JavaScript is missing %s: %s", description, expected)
		}
	}
}

func TestSessionSummaryIncludesUID(t *testing.T) {
	summary := summarize(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Name: "chat", Namespace: "team-a", UID: types.UID("session-incarnation"),
	}})
	if summary.UID != "session-incarnation" {
		t.Fatalf("Session summary UID = %q, want session-incarnation", summary.UID)
	}
}

func TestSessionSummaryReportsIdleSuspension(t *testing.T) {
	summary := summarize(&kelos.Session{Status: kelos.SessionStatus{
		Phase: kelos.SessionPhaseSuspended,
		Conditions: []metav1.Condition{{
			Type:   kelos.SessionConditionReady,
			Status: metav1.ConditionFalse,
			Reason: sessionsuspend.IdlePolicyReason,
		}},
	}})
	if !summary.IdleSuspended {
		t.Fatal("Session summary idleSuspended = false, want true")
	}
	if summary.UserSuspended {
		t.Fatal("Session summary userSuspended = true for idle suspension")
	}
}

func TestSessionSummaryReportsUserSuspension(t *testing.T) {
	summary := summarize(&kelos.Session{Spec: kelos.SessionSpec{Suspend: ptr.To(true)}})
	if !summary.UserSuspended {
		t.Fatal("Session summary userSuspended = false, want true")
	}
	if summary.IdleSuspended {
		t.Fatal("Session summary idleSuspended = true for user suspension")
	}
}

func TestSessionJavaScriptResumesIdleSuspension(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for _, expected := range []string{
		"selectSession(session, true)",
		"if (resumeIdle && session.idleSuspended)\n            resumeIdleSession(session);",
		"await requestSessionLifecycleAction(session, 'resume');",
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session JavaScript is missing idle resume behavior %q", expected)
		}
	}
}

func TestSessionPageOffersUserSuspensionControls(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`id="suspend-session" aria-label="Suspend session"`,
		`id="resume-session" aria-label="Resume session"`,
		`id="session-action-lifecycle" type="button" role="menuitem"`,
	} {
		if !strings.Contains(string(index), expected) {
			t.Errorf("Session page is missing suspension control %q", expected)
		}
	}

	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"return session?.userSuspended || session?.idleSuspended ? 'resume' : 'suspend';",
		"elements.sessionActionLifecycle.textContent = action === 'resume' ? 'Resume' : 'Suspend';",
		"elements.sessionActionLifecycle.addEventListener('click', event => {",
		"elements.suspendButton.hidden = !session || Boolean(session.userSuspended);",
		"elements.suspendButton.addEventListener('click', suspendSelectedSession);",
		"elements.resumeButton.hidden = !Boolean(session?.userSuspended);",
		"elements.resumeButton.addEventListener('click', resumeSelectedSession);",
	} {
		if !strings.Contains(string(source), expected) {
			t.Errorf("Session JavaScript is missing user suspension behavior %q", expected)
		}
	}
}

func TestApplicationIncludesFileChangesView(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}

	for _, expected := range []string{
		`id="conversation-tab" type="button" role="tab" aria-selected="true" aria-controls="messages" tabindex="0"`,
		`id="changes-tab" type="button" role="tab" aria-selected="false" aria-controls="changes-view" tabindex="-1"`,
		`id="changes-count"`,
		`id="changes-view"`,
		`id="changes-list"`,
		`No file changes yet.`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("Console Sessions view does not contain %s", expected)
		}
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`state.fileChanges.set(file.name, file.diff)`,
		`state.diffs.set(key, block)`,
		`renderFileChangeList(block.list, block.files, openFiles)`,
		`const path = normalizeDiffPath(header.slice(prefix.length))`,
		`const rawPath = value.split('\t', 1)[0]`,
		`return new TextDecoder().decode(new Uint8Array(bytes))`,
		`!line.startsWith('+++ ')`,
		`!line.startsWith('--- ')`,
		`elements.viewTabs.addEventListener('keydown', handleViewTabKeydown)`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("file changes JavaScript does not contain %q", expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`.diff-card .file-change`,
		`.diff-line.added`,
		`.diff-line.removed`,
		`--diff-added-bg:#173424`,
		`--diff-removed-bg:#3a2020`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("file changes styles do not contain %q", expected)
		}
	}
}

func TestApplicationRendersMarkdownSafely(t *testing.T) {
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`const block = document.createElement('div')`,
		`block.className = 'code-block'`,
		`const pre = document.createElement('pre')`,
		`const code = document.createElement('code')`,
		`copyButton.addEventListener('click', () => copyCodeBlock(copyButton, content))`,
		`await globalThis.navigator.clipboard.writeText(text)`,
		`document.execCommand('copy')`,
		`if (/^[a-z0-9_+-]+$/i.test(language))`,
		`code.textContent = content`,
		"code.className = `language-${language.toLowerCase()}`",
		`const paragraph = document.createElement('p')`,
		"const element = document.createElement(`h${heading[1].length}`)",
		`const list = document.createElement(firstItem.ordered ? 'ol' : 'ul')`,
		`const blockquote = document.createElement('blockquote')`,
		`const table = document.createElement('table')`,
		`const header = document.createElement('th')`,
		`const cell = document.createElement('td')`,
		`const element = document.createElement(tags[0])`,
		`url.protocol !== 'http:' && url.protocol !== 'https:'`,
		`appendInlineMarkdown(link, label, depth + 1, false, scanBudget)`,
		`completedAssistantText(event.text, state.assistantTextByTurn.get(key))`,
		`state.assistantTextByTurn.set(key, text)`,
		`renderMessageMarkdown(bubble, state.assistantTextByTurn.get(key) || '')`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Markdown rendering JavaScript does not contain %q", expected)
		}
	}
	for _, unsafe := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML"} {
		if strings.Contains(string(javascript), unsafe) {
			t.Errorf("Markdown rendering JavaScript contains unsafe DOM API %q", unsafe)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`.message-bubble h1, .message-bubble h2`,
		`.message-bubble blockquote {`,
		`.message-bubble .markdown-table-container {`,
		`.message-bubble table {`,
		`.message-bubble th, .message-bubble td {`,
		`.message-bubble .table-align-right {`,
		`.message-bubble .inline-code {`,
		`.message-bubble .task-list-item { list-style: none;`,
		`display: grid; grid-template-columns: auto minmax(0, 1fr)`,
		`.message-bubble .code-block {`,
		`.message-bubble .code-block-toolbar {`,
		`.message-bubble .code-copy-button {`,
		`.message-bubble pre {`,
		`overflow-x: auto`,
		`--code-bg:#171717`,
		`--code-ink:#ececec`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Markdown rendering styles do not contain %q", expected)
		}
	}
	if strings.Contains(string(styles), `.message-bubble .task-list {`) {
		t.Error("Markdown rendering styles suppress markers for ordinary items in mixed task lists")
	}
}

func TestConsoleJavaScriptSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	command := exec.Command(node, "--check", "web/app.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("checking Console JavaScript: %v\n%s", err, output)
	}
}

func TestApplicationConsoleResourcesBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/console_resources_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Console resources tests: %v\n%s", err, output)
	}
}

func TestApplicationMarkdownBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/markdown_renderer_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Markdown renderer tests: %v\n%s", err, output)
	}
}

func TestApplicationSessionHistoryBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/session_history_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Session history tests: %v\n%s", err, output)
	}
}

func TestApplicationSectionChooserBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/section_chooser_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running section chooser tests: %v\n%s", err, output)
	}
}

func TestApplicationDisplayNameBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/display_name_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running display name tests: %v\n%s", err, output)
	}
}

func TestApplicationSessionSuspensionBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/session_resume_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Session resume tests: %v\n%s", err, output)
	}
}

func TestApplicationInputRequestBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/input_request_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running input request tests: %v\n%s", err, output)
	}
}

func TestApplicationSessionActionsBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	command := exec.Command(node, "testdata/session_actions_test.js")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("running Session actions tests: %v\n%s", err, output)
	}
}

func TestSessionFormAPICreatesPersistentSession(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"persistent-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace"}},
		"initialBranch":"feature/persistent-chat",
		"initialPrompt":"Investigate the issue interactively",
		"volumeClaimTemplate":{
			"accessModes":["ReadWriteOnce"],
			"storageClassName":"fast",
			"resources":{"requests":{"storage":"20Gi"}}
		}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "persistent-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	claim := session.Spec.VolumeClaimTemplate
	if claim == nil {
		t.Fatal("volumeClaimTemplate is nil")
	}
	if session.Spec.InitialBranch != "feature/persistent-chat" {
		t.Fatalf("initialBranch = %q, want %q", session.Spec.InitialBranch, "feature/persistent-chat")
	}
	if session.Spec.InitialPrompt != "Investigate the issue interactively" {
		t.Fatalf("initialPrompt = %q", session.Spec.InitialPrompt)
	}
	if len(claim.AccessModes) != 1 || claim.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("accessModes = %v", claim.AccessModes)
	}
	if claim.StorageClassName == nil || *claim.StorageClassName != "fast" {
		t.Fatalf("storageClassName = %v", claim.StorageClassName)
	}
	wantStorage := resource.MustParse("20Gi")
	if storage := claim.Resources.Requests[corev1.ResourceStorage]; storage.Cmp(wantStorage) != 0 {
		t.Fatalf("storage request = %s, want %s", storage.String(), wantStorage.String())
	}
}

func TestSessionFormAPISetsIdlePolicy(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"idle-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace"}},
		"idlePolicy":{"suspendAfterSeconds":1800,"deleteAfterSeconds":604800}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "idle-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	policy := session.Spec.IdlePolicy
	if policy == nil {
		t.Fatal("idlePolicy is nil")
	}
	if policy.SuspendAfterSeconds == nil || *policy.SuspendAfterSeconds != 1800 {
		t.Fatalf("suspendAfterSeconds = %v, want 1800", policy.SuspendAfterSeconds)
	}
	if policy.DeleteAfterSeconds == nil || *policy.DeleteAfterSeconds != 604800 {
		t.Fatalf("deleteAfterSeconds = %v, want 604800", policy.DeleteAfterSeconds)
	}
}

func TestSessionFormAPIWithoutIdlePolicy(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"no-idle-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace"}}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "no-idle-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Spec.IdlePolicy != nil {
		t.Fatalf("idlePolicy = %#v, want nil", session.Spec.IdlePolicy)
	}
}

func TestSessionFormAPIPartialIdlePolicy(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"delete-only-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace"}},
		"idlePolicy":{"deleteAfterSeconds":3600}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "delete-only-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	policy := session.Spec.IdlePolicy
	if policy == nil {
		t.Fatal("idlePolicy is nil")
	}
	if policy.SuspendAfterSeconds != nil {
		t.Fatalf("suspendAfterSeconds = %v, want nil", *policy.SuspendAfterSeconds)
	}
	if policy.DeleteAfterSeconds == nil || *policy.DeleteAfterSeconds != 3600 {
		t.Fatalf("deleteAfterSeconds = %v, want 3600", policy.DeleteAfterSeconds)
	}
}

func TestSessionFormAPISetsSuspend(t *testing.T) {
	server := testServer(t)
	payload := `{
		"name":"suspended-chat",
		"namespace":"default",
		"worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace"}},
		"suspend":true
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "suspended-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Spec.Suspend == nil || !*session.Spec.Suspend {
		t.Fatalf("suspend = %v, want true", session.Spec.Suspend)
	}
}

func TestSessionYAMLApplyAPI(t *testing.T) {
	server := testServer(t)
	manifest := `apiVersion: kelos.dev/v1alpha2
kind: Session
metadata:
  name: yaml-chat
  labels:
    source: web
spec:
  initialBranch: feature/yaml-chat
  initialPrompt: Investigate the issue interactively
  volumeClaimTemplate:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 5Gi
  worker:
    type: codex
    credentials:
      type: none
    model: gpt-5
    effort: high
    image: example.com/codex:latest
    workspaceRef:
      name: workspace
    podOverrides:
      serviceAccountName: kelos-controller
`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/apply?namespace=team-a", strings.NewReader(manifest))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Content-Type", "application/yaml")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "yaml-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Labels["source"] != "web" {
		t.Fatalf("Session labels = %v", session.Labels)
	}
	if session.Spec.Worker.Model != "gpt-5" || session.Spec.Worker.Effort != "high" || session.Spec.Worker.Image != "example.com/codex:latest" {
		t.Fatalf("worker settings = %#v", session.Spec.Worker)
	}
	if session.Spec.Worker.PodOverrides == nil || session.Spec.Worker.PodOverrides.ServiceAccountName != "kelos-controller" {
		t.Fatalf("worker Pod overrides = %#v", session.Spec.Worker.PodOverrides)
	}
	if session.Spec.InitialBranch != "feature/yaml-chat" {
		t.Fatalf("initialBranch = %q, want %q", session.Spec.InitialBranch, "feature/yaml-chat")
	}
	if session.Spec.InitialPrompt != "Investigate the issue interactively" {
		t.Fatalf("initialPrompt = %q", session.Spec.InitialPrompt)
	}
	if session.Spec.VolumeClaimTemplate == nil {
		t.Fatal("volumeClaimTemplate is nil")
	}
	wantStorage := resource.MustParse("5Gi")
	if storage := session.Spec.VolumeClaimTemplate.Resources.Requests[corev1.ResourceStorage]; storage.Cmp(wantStorage) != 0 {
		t.Fatalf("storage request = %s, want %s", storage.String(), wantStorage.String())
	}

	updated := strings.Replace(manifest, "source: web", "source: yaml", 1)
	request = httptest.NewRequest(http.MethodPost, "/api/sessions/apply?namespace=team-a", strings.NewReader(updated))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("reapply status = %d body = %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "yaml-chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Labels["source"] != "yaml" {
		t.Fatalf("Session labels after reapply = %v", session.Labels)
	}
}

func TestSessionYAMLApplyAPIRejectsInvalidManifests(t *testing.T) {
	server := testServer(t)
	for _, test := range []struct {
		name       string
		manifest   string
		wantStatus int
	}{
		{
			name:       "wrong kind",
			manifest:   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "other namespace",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\n  namespace: team-a\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unknown field",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: chat\nspec:\n  unknown: value\n  worker:\n    type: codex\n    credentials:\n      type: none\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multiple documents",
			manifest:   "apiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: one\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n---\napiVersion: kelos.dev/v1alpha2\nkind: Session\nmetadata:\n  name: two\nspec:\n  worker:\n    type: codex\n    credentials:\n      type: none\n",
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/sessions/apply", strings.NewReader(test.manifest))
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestSessionComposerUsesOneSendAndInterruptAction(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `id="send-message" type="submit" aria-label="Send message" data-action="send"`) {
		t.Error("Session composer does not contain the send action")
	}
	if !strings.Contains(body, `id="pending-message"`) {
		t.Error("Session composer does not contain the pending message region")
	}
	if !strings.Contains(body, `id="session-progress"`) {
		t.Error("Session composer does not contain the agent progress region")
	}
	for _, expected := range []string{`id="attachment-input" type="file" multiple`, `id="attach-files"`, `id="pending-attachments"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("Session composer does not contain attachment control %s", expected)
		}
	}
	if strings.Contains(body, `id="stop-session"`) {
		t.Error("Session header contains a separate interrupt action")
	}
}

func TestSessionPendingMessagePreservesLineBreaks(t *testing.T) {
	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	rules := regexp.MustCompile(`(?s)\.pending-message-text\s*\{([^}]*)\}`).FindAllStringSubmatch(string(styles), -1)
	if len(rules) == 0 {
		t.Fatal("Session pending message style is missing")
	}
	hasDeclaration := func(property, value string) bool {
		pattern := regexp.MustCompile(`(?:^|;)\s*` + regexp.QuoteMeta(property) + `\s*:\s*` + regexp.QuoteMeta(value) + `\s*(?:;|$)`)
		for _, rule := range rules {
			if pattern.MatchString(rule[1]) {
				return true
			}
		}
		return false
	}
	for _, declaration := range []struct {
		property string
		value    string
		want     bool
	}{
		{property: "overflow-wrap", value: "anywhere", want: true},
		{property: "white-space", value: "pre-wrap", want: true},
		{property: "text-overflow", value: "ellipsis", want: false},
		{property: "white-space", value: "nowrap", want: false},
	} {
		if got := hasDeclaration(declaration.property, declaration.value); got != declaration.want {
			t.Errorf("Session pending message style contains %s: %s = %t, want %t", declaration.property, declaration.value, got, declaration.want)
		}
	}
}

func TestSessionResetControlWarnsAndUsesResetEndpoint(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `id="reset-session" aria-label="Reset session"`) {
		t.Fatal("Session header does not contain the reset action")
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"destructive warning":        "This permanently deletes its conversation history and all workspace changes.",
		"reset API request":          "/reset`, { method: 'POST' }",
		"reset history clearing":     "resetCurrentSessionView();",
		"reset cached view clearing": "discardSessionView(session);",
		"reset connection blocking":  "state.selected.resetting",
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session reset control is missing %s: %s", description, expected)
		}
	}
}

func TestSessionComposerKeepsDraftsPerSession(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"draft storage":           `promptDrafts: new Map()`,
		"draft save key":          `state.promptDrafts.set(sessionKey(session), elements.input.value)`,
		"draft clear key":         `state.promptDrafts.delete(sessionKey(session))`,
		"Session selection save":  "function selectSession(session, resumeIdle = false) {\n        savePromptDraft(state.selected);",
		"Session draft restore":   `state.promptDrafts.get(sessionKey(session))`,
		"prompt submission clear": "state.socket.send(JSON.stringify({ type: 'message', text, attachmentIds: attachments.map(attachment => attachment.id) }));\n            clearPromptDraft(session);",
		"Session deletion clear":  "{ method: 'DELETE' });\n            discardSessionView(session);\n            clearPromptDraft(session);\n            clearAttachmentDraft(session);",
		"composer input save":     "elements.input.addEventListener('input', () => {\n        savePromptDraft(state.selected);",
	} {
		if !strings.Contains(string(source), expected) {
			t.Errorf("Session composer is missing %s: %s", description, expected)
		}
	}
}

func TestSessionComposerUploadsDroppedAttachments(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"drop handling":       "dragEvent.dataTransfer?.files?.length",
		"multipart upload":    "const body = new FormData();",
		"attachment endpoint": "/attachments`, {",
		"message references":  "attachmentIds: attachments.map(attachment => attachment.id)",
		"history rendering":   "appendMessageAttachments(bubble, event.attachments || [])",
	} {
		if !strings.Contains(string(source), expected) {
			t.Errorf("Session composer is missing %s: %s", description, expected)
		}
	}
}

func TestSessionComposerAllowsMultilinePromptsOnTouchDevices(t *testing.T) {
	source, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(source)
	for description, expected := range map[string]string{
		"touch device detection":  `window.matchMedia('(pointer: coarse)').matches`,
		"touch composer hint":     "? `Tap ${actionSymbol} to ${action} · Return for a new line · !COMMAND · /goal`",
		"desktop composer hint":   "`Enter to ${action} · Shift+Enter for a new line · !COMMAND · /goal`",
		"disabled interrupt hint": "`Click ${actionSymbol} to interrupt`",
		"desktop-only Enter send": `!event.isComposing && !usesTouchComposer()`,
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("Session composer is missing %s: %s", description, expected)
		}
	}
}

func TestSessionUIAdaptsToPhoneViewport(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}

	for description, expected := range map[string]string{
		"edge-to-edge viewport support": `viewport-fit=cover`,
		"Android keyboard resizing":     `interactive-widget=resizes-content`,
		"dismissible sidebar backdrop":  `id="sidebar-scrim"`,
		"compact session list header":   `class="session-sidebar-header"`,
	} {
		if !strings.Contains(string(index), expected) {
			t.Errorf("Session page is missing %s: %s", description, expected)
		}
	}
	scrollStart := bytes.Index(index, []byte(`<div class="sidebar-scroll">`))
	scrollEnd := bytes.Index(index, []byte(`<div class="sidebar-footer">`))
	if scrollStart < 0 || scrollEnd <= scrollStart {
		t.Fatal("Session page does not define sidebar scrolling before the fixed footer")
	}
	scrollContent := string(index[scrollStart:scrollEnd])
	for description, expected := range map[string]string{
		"new Session action":    `id="new-session"`,
		"namespace switcher":    `id="namespace-form"`,
		"console navigation":    `class="console-nav"`,
		"Session conversations": `id="session-list"`,
	} {
		if !strings.Contains(scrollContent, expected) {
			t.Errorf("Scrollable sidebar is missing %s: %s", description, expected)
		}
	}
	for description, expected := range map[string]string{
		"dynamic viewport height":            `height: 100dvh`,
		"desktop sidebar width":              `grid-template-columns: 260px minmax(0, 1fr)`,
		"landscape phone breakpoint":         `(max-height: 500px) and (pointer: coarse)`,
		"phone sidebar width":                `width: min(88vw, 320px)`,
		"phone navigation touch target":      `.console-nav button { min-height: 44px; padding: 8px 10px; font-size: 14px; }`,
		"compact phone session row":          `.session-item-select { min-height: 60px; padding: 7px 48px 7px 8px; }`,
		"phone create touch target":          `.new-session-button { min-height: 44px; margin-top: 4px; padding: 8px 10px; }`,
		"section reorder touch target":       `.session-section-order-button { width: 44px; height: 44px; }`,
		"single-row phone header":            `grid-template-areas: "menu heading actions"`,
		"phone lifecycle actions in sidebar": `.session-lifecycle-action, #reset-session, #delete-session { display: none !important; }`,
		"44-pixel touch targets":             `.icon-button { width: 44px; height: 44px; }`,
		"44-pixel view tabs":                 `.view-tabs button { min-height: 44px; padding: 5px 8px; }`,
		"Session action touch target":        `.session-item-actions { top: 5px; right: 1px; width: 44px; height: 44px; }`,
		"phone safe-area padding":            `env(safe-area-inset-bottom)`,
		"non-zooming form fields":            `.composer textarea, .pending-message-input, .yaml-panel textarea, .form-grid input`,
		"desktop composer alignment":         `.composer textarea { flex: 1; min-height: 36px;`,
		"mobile composer alignment":          `.composer textarea { min-height: 44px; padding: 10px 2px; line-height: 24px; }`,
		"phone-sized dialog":                 `max-height: calc(100dvh - 16px`,
		"scrolling sidebar controls":         `.sidebar-scroll { flex: 1; min-height: 0; overflow-y: auto;`,
		"shrinking composer content":         `z-index: 2; min-width: 0;`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session styles are missing %s: %s", description, expected)
		}
	}
	for description, expected := range map[string]string{
		"sidebar backdrop action":       `elements.sidebarScrim.addEventListener('click', () => setSidebarOpen(false))`,
		"sidebar Escape action":         `event.key === 'Escape' && elements.sidebar.classList.contains('open')`,
		"sidebar scroll menu dismissal": `elements.sidebarScroll.addEventListener('scroll', () => closeSessionActionsMenu())`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session behavior is missing %s: %s", description, expected)
		}
	}
}

func TestSessionAPIHappyPath(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"defaultNamespace":"default"`) {
		t.Fatalf("config status = %d body = %s", response.Code, response.Body.String())
	}

	payload := map[string]any{
		"name":      "chat",
		"namespace": "team-a",
		"section":   "Planning",
		"worker": map[string]any{
			"type":        "codex",
			"credentials": map[string]string{"type": "none"},
		},
	}
	body, _ := json.Marshal(payload)
	request = httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions?namespace=team-a", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
	}
	var sessions []sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "chat" || sessions[0].Namespace != "team-a" || sessions[0].Provider != "codex" || sessions[0].Section != "Planning" {
		t.Fatalf("listed Sessions = %#v", sessions)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/reset", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("reset status = %d body = %s", response.Code, response.Body.String())
	}
	var resetSummary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &resetSummary); err != nil {
		t.Fatal(err)
	}
	if !resetSummary.Resetting {
		t.Fatalf("reset Session summary = %#v", resetSummary)
	}
	var resetSession kelos.Session
	if err := server.client.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &resetSession); err != nil {
		t.Fatal(err)
	}
	if resetSession.Annotations[sessionreset.RequestAnnotation] == "" {
		t.Fatal("reset endpoint did not request a Session reset")
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/sessions/team-a/chat", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionSectionAPI(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "chat",
			Namespace:   "team-a",
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPatch, "/api/sessions/team-a/chat/section", strings.NewReader(`{"section":"  In progress  "}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("set section status = %d body = %s", response.Code, response.Body.String())
	}
	var summary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Section != "In progress" {
		t.Fatalf("set section summary = %#v", summary)
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[sessionSectionAnnotation] != "In progress" || updated.Annotations["owner"] != "platform" {
		t.Fatalf("Session annotations after setting section = %v", updated.Annotations)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/sessions/team-a/chat/section", strings.NewReader(`{"section":""}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("clear section status = %d body = %s", response.Code, response.Body.String())
	}
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &updated); err != nil {
		t.Fatal(err)
	}
	if _, exists := updated.Annotations[sessionSectionAnnotation]; exists || updated.Annotations["owner"] != "platform" {
		t.Fatalf("Session annotations after clearing section = %v", updated.Annotations)
	}
}

func TestSessionDisplayNameAPI(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "chat",
			Namespace:   "team-a",
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPatch, "/api/sessions/team-a/chat/display-name", strings.NewReader(`{"displayName":"  Investigate flaky CI  "}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("set display name status = %d body = %s", response.Code, response.Body.String())
	}
	var summary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Name != "chat" || summary.DisplayName != "Investigate flaky CI" {
		t.Fatalf("set display name summary = %#v", summary)
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[sessionDisplayNameAnnotation] != "Investigate flaky CI" || updated.Annotations["owner"] != "platform" {
		t.Fatalf("Session annotations after setting display name = %v", updated.Annotations)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/sessions/team-a/chat/display-name", strings.NewReader(`{"displayName":""}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("clear display name status = %d body = %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Name != "chat" || summary.DisplayName != "chat" {
		t.Fatalf("cleared display name summary = %#v", summary)
	}
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "chat"}, &updated); err != nil {
		t.Fatal(err)
	}
	if _, exists := updated.Annotations[sessionDisplayNameAnnotation]; exists || updated.Annotations["owner"] != "platform" {
		t.Fatalf("Session annotations after clearing display name = %v", updated.Annotations)
	}
}

func TestSessionSuspendAPISuspendsSession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "chat",
			Namespace:   "team-a",
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Model:       "gpt-5",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/suspend", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("suspend status = %d body = %s", response.Code, response.Body.String())
	}
	var summary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.UserSuspended {
		t.Fatal("suspend response userSuspended = false, want true")
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Suspend == nil || !*updated.Spec.Suspend {
		t.Fatalf("suspended Session suspend = %v, want true", updated.Spec.Suspend)
	}
	if updated.Spec.Worker.Model != "gpt-5" || updated.Annotations["owner"] != "platform" {
		t.Fatalf("suspended Session lost unrelated fields: %#v", updated)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/suspend", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("repeated suspend status = %d body = %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.UserSuspended {
		t.Fatal("repeated suspend response userSuspended = false, want true")
	}
}

func TestSessionSuspendAPIReturnsNotFoundWhenPatchTargetDisappears(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	server.client = &patchErrorClient{
		Client: server.client,
		err: apierrors.NewNotFound(
			schema.GroupResource{Group: kelos.GroupVersion.Group, Resource: "sessions"},
			session.Name,
		),
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/suspend", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("suspend status = %d body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `suspending Session \"chat\"`) {
		t.Fatalf("suspend body = %s", response.Body.String())
	}
}

func TestSessionResumeAPIRequestsIdleResume(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{
			Phase: kelos.SessionPhaseSuspended,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionReady,
				Status: metav1.ConditionFalse,
				Reason: sessionsuspend.IdlePolicyReason,
			}},
		},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/resume", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body = %s", response.Code, response.Body.String())
	}
	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&updated) {
		t.Fatalf("resume endpoint did not record a wake request: %#v", updated.Annotations)
	}
}

func TestSessionResumeAPIUnsuspendsUserSuspendedSession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "chat",
			Namespace:   "team-a",
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{
			Worker: kelos.WorkerSpec{
				Type:        "codex",
				Model:       "gpt-5",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			},
			Suspend: ptr.To(true),
		},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseSuspended},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/resume", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body = %s", response.Code, response.Body.String())
	}
	var summary sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.UserSuspended {
		t.Fatal("resume response userSuspended = true, want false")
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Suspend == nil || *updated.Spec.Suspend {
		t.Fatalf("resumed Session suspend = %v, want false", updated.Spec.Suspend)
	}
	if updated.Spec.Worker.Model != "gpt-5" || updated.Annotations["owner"] != "platform" {
		t.Fatalf("resumed Session lost unrelated fields: %#v", updated)
	}
}

func TestSessionResumeAPIRequestsIdleResumeAfterUserSuspension(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{
			Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			},
			Suspend: ptr.To(true),
		},
		Status: kelos.SessionStatus{
			Phase: kelos.SessionPhaseSuspended,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionReady,
				Status: metav1.ConditionFalse,
				Reason: sessionsuspend.IdlePolicyReason,
			}},
		},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/resume", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body = %s", response.Code, response.Body.String())
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Suspend == nil || *updated.Spec.Suspend {
		t.Fatalf("resumed Session suspend = %v, want false", updated.Spec.Suspend)
	}
	if !sessionsuspend.ResumeRequested(&updated) {
		t.Fatalf("resume endpoint did not record an idle wake request: %#v", updated.Annotations)
	}
}

func TestNormalizeSessionSection(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		want      string
		wantError bool
	}{
		{name: "trimmed", value: "  Customer work  ", want: "Customer work"},
		{name: "empty", value: "  ", want: ""},
		{name: "unicode", value: strings.Repeat("界", maxSessionSectionLength), want: strings.Repeat("界", maxSessionSectionLength)},
		{name: "too long", value: strings.Repeat("a", maxSessionSectionLength+1), wantError: true},
		{name: "control character", value: "one\ntwo", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSessionSection(test.value)
			if (err != nil) != test.wantError {
				t.Fatalf("normalizeSessionSection(%q) error = %v, wantError %t", test.value, err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("normalizeSessionSection(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestNormalizeSessionDisplayName(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		want      string
		wantError bool
	}{
		{name: "trimmed", value: "  Customer work  ", want: "Customer work"},
		{name: "empty", value: "  ", want: ""},
		{name: "unicode", value: strings.Repeat("界", maxSessionDisplayNameLength), want: strings.Repeat("界", maxSessionDisplayNameLength)},
		{name: "too long", value: strings.Repeat("a", maxSessionDisplayNameLength+1), wantError: true},
		{name: "control character", value: "one\ntwo", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSessionDisplayName(test.value)
			if (err != nil) != test.wantError {
				t.Fatalf("normalizeSessionDisplayName(%q) error = %v, wantError %t", test.value, err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("normalizeSessionDisplayName(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestSummarizeIncludesRuntimeStatus(t *testing.T) {
	createdAt := metav1.NewTime(time.Now().Add(-time.Hour))
	lastActivityAt := metav1.NewTime(time.Now().Add(-time.Minute))
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "chat",
			Namespace:         "default",
			CreationTimestamp: createdAt,
			Annotations:       map[string]string{sessionSectionAnnotation: "Reviews"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
		Status: kelos.SessionStatus{
			Phase:            kelos.SessionPhaseReady,
			LastActivityTime: &lastActivityAt,
			Model:            "gpt-5.6-sol",
			Branch:           "feature/session-status",
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionActive,
				Status: metav1.ConditionTrue,
				Reason: "WaitingForInput",
			}},
			PullRequest: &kelos.SessionPullRequest{
				URL:   "https://github.com/kelos-dev/kelos/pull/42",
				State: kelos.SessionPullRequestStateOpen,
				Checks: &kelos.SessionPullRequestChecks{
					State:     kelos.SessionPullRequestChecksStatePending,
					Completed: 2,
					Total:     3,
				},
			},
		},
	}

	summary := summarize(session)
	if summary.DisplayName != "chat" || summary.Active == nil || !*summary.Active || !summary.WaitingForInput || summary.Model != session.Status.Model || summary.Branch != session.Status.Branch || summary.PullRequest == nil || *summary.PullRequest != *session.Status.PullRequest || summary.Section != "Reviews" {
		t.Fatalf("summarize() = %#v", summary)
	}
	if summary.CreatedAt == nil || !summary.CreatedAt.Equal(&createdAt) {
		t.Fatalf("summarize() CreatedAt = %v, want %v", summary.CreatedAt, createdAt)
	}
	if summary.LastActivityAt == nil || !summary.LastActivityAt.Equal(&lastActivityAt) {
		t.Fatalf("summarize() LastActivityAt = %v, want %v", summary.LastActivityAt, lastActivityAt)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"model":"gpt-5.6-sol"`) {
		t.Fatalf("Session summary JSON = %s, want runtime model", data)
	}
	if !strings.Contains(string(data), `"waitingForInput":true`) {
		t.Fatalf("Session summary JSON = %s, want waiting-for-input state", data)
	}
}

func TestListSessionsOrdersByRecentActivityAcrossPodReplacement(t *testing.T) {
	server := testServer(t)
	now := time.Now()
	recentlyIdleTime := metav1.NewTime(now.Add(-30 * time.Minute))
	activeTime := metav1.NewTime(now.Add(-time.Hour))
	recreatedActivityTime := metav1.NewTime(now.Add(-90 * time.Minute))
	for _, session := range []*kelos.Session{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "newly-created", Namespace: "default", CreationTimestamp: metav1.NewTime(now)},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "recently-idle", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-3 * time.Hour))},
			Status: kelos.SessionStatus{LastActivityTime: &recentlyIdleTime, Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionFalse,
				Reason:             "Idle",
				LastTransitionTime: recentlyIdleTime,
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-4 * time.Hour))},
			Status: kelos.SessionStatus{LastActivityTime: &activeTime, Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionTrue,
				Reason:             "TurnActive",
				LastTransitionTime: activeTime,
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "recreated", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Hour))},
			Status: kelos.SessionStatus{LastActivityTime: &recreatedActivityTime, Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionFalse,
				Reason:             "Idle",
				LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
			}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
			Status: kelos.SessionStatus{Conditions: []metav1.Condition{{
				Type:               kelos.SessionConditionActive,
				Status:             metav1.ConditionUnknown,
				Reason:             "Unavailable",
				LastTransitionTime: metav1.NewTime(now.Add(time.Hour)),
			}}},
		},
	} {
		if err := server.client.Create(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
	}
	var sessions []sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	want := []string{"newly-created", "recently-idle", "active", "recreated", "unknown"}
	if len(sessions) != len(want) {
		t.Fatalf("listed Sessions = %d, want %d", len(sessions), len(want))
	}
	for i := range want {
		if sessions[i].Name != want[i] {
			t.Fatalf("Session order = %#v, want %v", sessions, want)
		}
	}
}

func TestSessionViewsIncludeRuntimeStatus(t *testing.T) {
	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"waiting display status":     "if (session.waitingForInput)\n            return 'Waiting for input';",
		"active display status":      "if (session.active === true)\n            return 'Active';",
		"idle display status":        "if (session.active === false)\n            return 'Idle';",
		"activity status in sidebar": "activity.textContent = `· ${displayStatus}`;",
		"activity status class":      `activity.className = 'session-item-activity';`,
		"activity status in header":  `sessionDisplayStatus(session)`,
		"namespace metadata class":   `namespace.className = 'session-item-namespace';`,
		"model in sidebar":           "model.textContent = `· ${session.model}`;",
		"model in header":            "if (session.model)\n            details.push(session.model);",
		"branch text":                `branch.textContent = session.branch;`,
		"validated pull request URL": `const url = safeHTTPURL(pullRequest?.url);`,
		"pull request state label":   "link.textContent = state ? `${pullRequestLabel(url)} · ${state}` : pullRequestLabel(url);",
		"pull request state color":   "if (state)\n            link.dataset.state = state.toLowerCase();",
		"pull request checks label":  "Pending: `Checks ${completed}/${checks.total}`",
		"pull request checks state":  "checkStatus.dataset.state = checks.state;",
		"pull request link target":   `link.target = '_blank';`,
		"sidebar pull request link":  `createPullRequestLink(session.pullRequest, 'session-item-pull-request');`,
		"header pull request link":   `createPullRequestLink(session.pullRequest, 'session-meta-pull-request');`,
		"sidebar activity timestamp": `createSessionTimestamp(session, true, 'session-item-time');`,
		"header activity timestamp":  `createSessionTimestamp(session, false, 'session-meta-time');`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session views are missing %s: %s", description, expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(styles), `.session-item-branch { margin-top: 3px;`) {
		t.Error("Session sidebar does not style branch information")
	}
	for description, expected := range map[string]string{
		"truncated namespace metadata": `.session-item-namespace { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }`,
		"reserved activity status":     `.session-item-activity { flex: none; }`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session sidebar is missing %s: %s", description, expected)
		}
	}
	for state, expected := range map[string]string{
		"idle":              `.phase-dot.ready, .phase-dot.idle {`,
		"active":            `.phase-dot.active {`,
		"waiting for input": `.phase-dot.waiting-for-input {`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session views do not style %s activity", state)
		}
	}
	for state, expected := range map[string]string{
		"draft":  `.pull-request-link[data-state="draft"] { color: var(--faint); }`,
		"open":   `.pull-request-link[data-state="open"] { color: var(--pr-open); }`,
		"queued": `.pull-request-link[data-state="queued"] { color: var(--warning); }`,
		"merged": `.pull-request-link[data-state="merged"] { color: var(--pr-merged); }`,
		"closed": `.pull-request-link[data-state="closed"] { color: var(--pr-closed); }`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session views do not style %s pull requests", state)
		}
	}
	for state, expected := range map[string]string{
		"pending": `.pull-request-checks[data-state="pending"] { color: var(--warning); }`,
		"success": `.pull-request-checks[data-state="success"] { color: var(--pr-open); }`,
		"failure": `.pull-request-checks[data-state="failure"] { color: var(--danger); }`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session views do not style %s pull request checks", state)
		}
	}
}

func TestSessionUISections(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"selected Session section control":       `id="session-section-form"`,
		"selected Session section chooser":       `id="session-section-choice" aria-label="Section"`,
		"selected Session new section field":     `id="session-section-choice-custom" maxlength="64"`,
		"selected Session section menu action":   `id="session-action-section" type="button" role="menuitem"`,
		"selected Session section editor close":  `id="cancel-session-section" type="button"`,
		"new Session section chooser":            `name="section" id="session-section-select"`,
		"new Session new section creation field": `name="sectionCustom" id="session-section-custom" maxlength="64"`,
	} {
		if !strings.Contains(string(index), expected) {
			t.Errorf("Session page is missing %s: %s", description, expected)
		}
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"section grouping":      `const sessionsBySection = new Map();`,
		"unsectioned group":     `name.textContent = section || 'Unsectioned';`,
		"Session drag handling": `configureSessionDrag(item, button, session);`,
		"section drag handling": `configureSectionDrag(group, heading, title, section);`,
		"unsectioned ordering":  "if (!available.includes(''))\n            available.push('');",
		"browser order storage": `window.localStorage.setItem(sectionOrderStorageKey(namespace), JSON.stringify(normalized));`,
		"order focus restore":   "if (focusDirection)\n            focusSectionOrderControl(section, focusDirection);",
		"mobile section editor": `function openSessionSectionEditor(session)`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session behavior is missing %s: %s", description, expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"section headings":       `.session-section-heading {`,
		"inline section control": `.session-section-control {`,
		"mobile section control": `.session-section-control.mobile-open {`,
		"Session drop target":    `.session-section-group.session-drop-target {`,
		"section order controls": `.session-section-order-button { width: 28px; height: 28px;`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session styles are missing %s: %s", description, expected)
		}
	}
}

func TestSessionUIDisplayNames(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"rename control":      `id="session-display-name" type="button"`,
		"display name dialog": `id="display-name-dialog"`,
		"display name input":  `id="session-display-name-input" maxlength="64"`,
	} {
		if !strings.Contains(string(index), expected) {
			t.Errorf("Session page is missing %s: %s", description, expected)
		}
	}

	javascript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"display name fallback": `return session?.displayName || session?.name || '';`,
		"sidebar display name":  `name.textContent = sessionDisplayName(session);`,
		"header display name":   `elements.title.textContent = sessionDisplayName(session);`,
		"runtime display name":  `sessionRuntimeStatusText(state.runtimeStatus, sessionDisplayName(state.selected))`,
		"canonical API route":   `/api/sessions/${encodeURIComponent(session.namespace)}/${encodeURIComponent(session.name)}/display-name`,
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Errorf("Session display name behavior is missing %s: %s", description, expected)
		}
	}

	styles, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for description, expected := range map[string]string{
		"rename control": `.session-display-name-button {`,
		"rename dialog":  `.display-name-dialog {`,
	} {
		if !strings.Contains(string(styles), expected) {
			t.Errorf("Session display name styles are missing %s: %s", description, expected)
		}
	}
}

func TestSessionAPIAcceptsFullWorkerSpec(t *testing.T) {
	server := testServer(t)
	payload := `{
  "name": "full-worker",
  "namespace": "default",
  "worker": {
    "type": "codex",
    "credentials": {"type": "none"},
    "model": "gpt-5",
    "effort": "high",
    "image": "example.com/codex:latest",
    "podOverrides": {
      "serviceAccountName": "kelos-controller"
    }
  }
}`
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	var session kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "full-worker"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Spec.Worker.Effort != "high" || session.Spec.Worker.Image != "example.com/codex:latest" {
		t.Fatalf("worker settings = %#v", session.Spec.Worker)
	}
	if session.Spec.Worker.PodOverrides == nil || session.Spec.Worker.PodOverrides.ServiceAccountName != "kelos-controller" {
		t.Fatalf("worker Pod overrides = %#v", session.Spec.Worker.PodOverrides)
	}
}

func TestSessionAPIListsRequestedNamespace(t *testing.T) {
	server := testServer(t)
	for _, namespace := range []string{"default", "team-a"} {
		session := &kelos.Session{
			ObjectMeta: metav1.ObjectMeta{Name: "chat-" + namespace, Namespace: namespace},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			}},
		}
		if err := server.client.Create(t.Context(), session); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name          string
		path          string
		wantNamespace string
	}{
		{name: "default namespace", path: "/api/sessions", wantNamespace: "default"},
		{name: "requested namespace", path: "/api/sessions?namespace=team-a", wantNamespace: "team-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
			}
			var sessions []sessionSummary
			if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 1 || sessions[0].Namespace != test.wantNamespace {
				t.Fatalf("listed Sessions = %#v", sessions)
			}
		})
	}
}

func TestSessionOptionsAPI(t *testing.T) {
	server := testServer(t)
	for _, workspace := range []kelos.Workspace{
		{ObjectMeta: metav1.ObjectMeta{Name: "zeta", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"}},
	} {
		if err := server.client.Create(t.Context(), &workspace); err != nil {
			t.Fatal(err)
		}
	}
	for _, agentConfig := range []kelos.AgentConfig{
		{ObjectMeta: metav1.ObjectMeta{Name: "tools", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"}},
	} {
		if err := server.client.Create(t.Context(), &agentConfig); err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []kelos.Session{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "codex", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "codex-duplicate", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeAPIKey,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "none", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "other-credentials"},
				},
			}},
		},
	} {
		if err := server.client.Create(t.Context(), &session); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/options", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("options status = %d body = %s", response.Code, response.Body.String())
	}
	var options sessionOptions
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	wantCredentials := []credentialOption{
		{Name: "claude-credentials", Type: kelos.CredentialTypeAPIKey, Provider: "claude-code"},
		{Name: "codex-credentials", Type: kelos.CredentialTypeOAuth, Provider: "codex"},
	}
	if len(options.Credentials) != len(wantCredentials) {
		t.Fatalf("credential options = %#v, want %#v", options.Credentials, wantCredentials)
	}
	for i := range wantCredentials {
		if options.Credentials[i] != wantCredentials[i] {
			t.Errorf("credential option %d = %#v, want %#v", i, options.Credentials[i], wantCredentials[i])
		}
	}
	if got := strings.Join(options.Workspaces, ","); got != "alpha,zeta" {
		t.Errorf("workspace options = %q, want %q", got, "alpha,zeta")
	}
	if got := strings.Join(options.AgentConfigs, ","); got != "defaults,tools" {
		t.Errorf("AgentConfig options = %q, want %q", got, "defaults,tools")
	}
	if got := strings.Join(options.Sessions, ","); got != "claude,codex,codex-duplicate,none" {
		t.Errorf("Session options = %q, want %q", got, "claude,codex,codex-duplicate,none")
	}

	request = httptest.NewRequest(http.MethodGet, "/api/options?namespace=team-a", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("team-a options status = %d body = %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Credentials) != 1 || options.Credentials[0].Name != "other-credentials" {
		t.Errorf("team-a credential options = %#v", options.Credentials)
	}
	if got := strings.Join(options.Workspaces, ","); got != "other" {
		t.Errorf("team-a workspace options = %q, want %q", got, "other")
	}
	if got := strings.Join(options.AgentConfigs, ","); got != "other" {
		t.Errorf("team-a AgentConfig options = %q, want %q", got, "other")
	}
	if got := strings.Join(options.Sessions, ","); got != "other" {
		t.Errorf("team-a Session options = %q, want %q", got, "other")
	}
}

func TestSessionSourceAPIReturnsReusableSpec(t *testing.T) {
	storageClassName := ""
	server := testServer(t)
	source := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "codex-review",
			Namespace:   "team-a",
			Labels:      map[string]string{"purpose": "review"},
			Annotations: map[string]string{"owner": "platform"},
		},
		Spec: kelos.SessionSpec{
			InitialBranch: "feature/codex-review",
			InitialPrompt: "Investigate the issue interactively",
			IdlePolicy: &kelos.SessionIdlePolicy{
				SuspendAfterSeconds: ptr.To(int32(900)),
			},
			Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
				Model:           "gpt-5",
				Effort:          "high",
				Image:           "example.com/codex:latest",
				WorkspaceRef:    &kelos.WorkspaceReference{Name: "kelos"},
				AgentConfigRefs: []kelos.AgentConfigReference{{Name: "defaults"}, {Name: "review-tools"}},
				PodOverrides: &kelos.PodOverrides{Env: []corev1.EnvVar{{
					Name:  "REVIEW_MODE",
					Value: "strict",
				}}},
			},
			VolumeClaimTemplate: &corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &storageClassName,
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("20Gi"),
				}},
			},
		},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "codex-review-0"},
	}
	if err := server.client.Create(t.Context(), source); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/codex-review", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("source Session status = %d body = %s", response.Code, response.Body.String())
	}
	var detail sessionSourceDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "codex-review" || detail.Namespace != "team-a" {
		t.Fatalf("source Session identity = %s/%s", detail.Namespace, detail.Name)
	}
	manifest := detail.Manifest
	if manifest.Metadata.Name != "" || manifest.Metadata.Namespace != "team-a" {
		t.Fatalf("Session manifest identity = %s/%s", manifest.Metadata.Namespace, manifest.Metadata.Name)
	}
	if len(manifest.Metadata.Labels) != 0 || len(manifest.Metadata.Annotations) != 0 {
		t.Fatalf("Session manifest copied source metadata: labels %v annotations %v", manifest.Metadata.Labels, manifest.Metadata.Annotations)
	}
	worker := manifest.Spec.Worker
	if manifest.Spec.InitialBranch != "feature/codex-review" || manifest.Spec.InitialPrompt != "Investigate the issue interactively" {
		t.Fatalf("Session manifest initialBranch/prompt = %q/%q", manifest.Spec.InitialBranch, manifest.Spec.InitialPrompt)
	}
	if manifest.Spec.IdlePolicy == nil || manifest.Spec.IdlePolicy.SuspendAfterSeconds == nil || *manifest.Spec.IdlePolicy.SuspendAfterSeconds != 900 {
		t.Fatalf("Session manifest idlePolicy = %#v", manifest.Spec.IdlePolicy)
	}
	if worker.Type != "codex" || worker.Model != "gpt-5" || worker.Effort != "high" || worker.Image != "example.com/codex:latest" || worker.Credentials == nil || worker.Credentials.SecretRef.Name != "codex-credentials" {
		t.Fatalf("Session manifest worker = %#v", worker)
	}
	if worker.WorkspaceRef == nil || worker.WorkspaceRef.Name != "kelos" || len(worker.AgentConfigRefs) != 2 {
		t.Fatalf("Session manifest references = workspace %#v AgentConfigs %#v", worker.WorkspaceRef, worker.AgentConfigRefs)
	}
	if worker.PodOverrides == nil || len(worker.PodOverrides.Env) != 1 || worker.PodOverrides.Env[0].Name != "REVIEW_MODE" {
		t.Fatalf("Session manifest Pod overrides = %#v", worker.PodOverrides)
	}
	if manifest.Spec.VolumeClaimTemplate == nil {
		t.Fatal("Session manifest volumeClaimTemplate is nil")
	}
	if manifest.Spec.VolumeClaimTemplate.StorageClassName == nil || *manifest.Spec.VolumeClaimTemplate.StorageClassName != "" {
		t.Fatalf("storageClassName = %v, want explicit empty string", manifest.Spec.VolumeClaimTemplate.StorageClassName)
	}
	wantStorage := resource.MustParse("20Gi")
	if storage := manifest.Spec.VolumeClaimTemplate.Resources.Requests[corev1.ResourceStorage]; storage.Cmp(wantStorage) != 0 {
		t.Fatalf("storage request = %s, want %s", storage.String(), wantStorage.String())
	}
	for _, expected := range []string{
		"apiVersion: kelos.dev/v1alpha2",
		"kind: Session",
		`name: ""`,
		"namespace: team-a",
		"name: codex-credentials",
		"effort: high",
		"image: example.com/codex:latest",
		"name: REVIEW_MODE",
		"initialBranch: feature/codex-review",
		"initialPrompt: Investigate the issue interactively",
		"suspendAfterSeconds: 900",
		`storageClassName: ""`,
		"storage: 20Gi",
	} {
		if !strings.Contains(detail.YAML, expected) {
			t.Errorf("generated Session YAML does not contain %q:\n%s", expected, detail.YAML)
		}
	}
	for _, unexpected := range []string{"codex-review-0", "purpose: review", "owner: platform", "status:"} {
		if strings.Contains(detail.YAML, unexpected) {
			t.Errorf("generated Session YAML contains source runtime data %q:\n%s", unexpected, detail.YAML)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/missing", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing source Session status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	for _, token := range []string{"", " \t"} {
		_, err := New(Config{Token: token})
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("New() token %q error = %v", token, err)
		}
	}
}

func TestNewRejectsEmptyDefaultNamespace(t *testing.T) {
	_, err := New(Config{Token: "secret-token"})
	if err == nil || !strings.Contains(err.Error(), "namespace must not be empty") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestConnectSessionBridgesAndAcknowledgesResumedSession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "chat",
			Namespace:   "team-a",
			Annotations: map[string]string{sessionsuspend.ResumeRequestAnnotation: "resume-request"},
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "chat-pod"},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	bridged := make(chan struct{})
	server.bridge = func(_ context.Context, connection *sessionSocket, namespace, podName string, acknowledgeResume func() error) error {
		defer close(bridged)
		if namespace != "team-a" || podName != "chat-pod" {
			t.Errorf("bridge target = %s/%s, want team-a/chat-pod", namespace, podName)
		}
		var request map[string]any
		if err := connection.ReadJSON(&request); err != nil {
			return err
		}
		if request["type"] != "subscribe" {
			t.Errorf("bridge request type = %v, want subscribe", request["type"])
		}
		if err := connection.WriteJSON(map[string]any{"type": "history.end"}); err != nil {
			return err
		}
		if acknowledgeResume == nil {
			return errors.New("resume acknowledgement is not configured")
		}
		return acknowledgeResume()
	}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	header := http.Header{"Authorization": []string{"Bearer secret-token"}}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/sessions/team-a/chat/connect", header)
	if err != nil {
		if response != nil {
			t.Fatalf("connecting WebSocket: %v (status %d)", err, response.StatusCode)
		}
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "subscribe", "since": 0}); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "history.end" {
		t.Fatalf("event type = %v, want history.end", event["type"])
	}
	select {
	case <-bridged:
	case <-time.After(time.Second):
		t.Fatal("bridge did not complete")
	}
	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if !sessionsuspend.ResumeRequested(&updated) {
		t.Fatalf("resume request was cleared before the controller observed its acknowledgement: %#v", updated.Annotations)
	}
	if !sessionsuspend.ResumeAcknowledged(&updated) {
		t.Fatalf("resume request was not acknowledged: %#v", updated.Annotations)
	}
}

func TestSessionAttachmentUploadAndDownload(t *testing.T) {
	server := testServer(t)
	if err := server.client.Create(t.Context(), &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "chat-pod"},
	}); err != nil {
		t.Fatal(err)
	}
	transfer := &fakeSessionAttachmentTransfer{
		attachment:   sessionruntime.Attachment{ID: "attachment-id", Name: "screen.png", MediaType: "image/png", SizeBytes: 7},
		downloadData: []byte("content"),
	}
	server.attachments = transfer

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, err := multipartWriter.CreateFormFile("file", "screen.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("content"))
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/team-a/chat/attachments", &body)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || transfer.uploadedName != "screen.png" || string(transfer.uploadedData) != "content" {
		t.Fatalf("upload status = %d name = %q data = %q body = %s", response.Code, transfer.uploadedName, transfer.uploadedData, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat/attachments/attachment-id", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Body.String() != "content" {
		t.Fatalf("download status = %d content-type = %q body = %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "inline") || !strings.Contains(response.Header().Get("Content-Disposition"), "screen.png") {
		t.Fatalf("download content disposition = %q", response.Header().Get("Content-Disposition"))
	}
}

func TestConnectSessionRejectsSuspendedSession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseSuspended},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat/connect", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("suspended Session status = %d body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "is suspended") {
		t.Fatalf("suspended Session body = %q", response.Body.String())
	}
}

func TestConnectSessionDoesNotResumeIdleSession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "team-a"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{
			Phase: kelos.SessionPhaseSuspended,
			Conditions: []metav1.Condition{{
				Type:   kelos.SessionConditionReady,
				Status: metav1.ConditionFalse,
				Reason: sessionsuspend.IdlePolicyReason,
			}},
		},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat/connect", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "is suspended") {
		t.Fatalf("idle-suspended Session response = %d body = %s", response.Code, response.Body.String())
	}

	var updated kelos.Session
	if err := server.client.Get(t.Context(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if sessionsuspend.ResumeRequested(&updated) {
		t.Fatal("connect endpoint requested an idle Session resume")
	}
}

func TestSessionSocketSerializesWrites(t *testing.T) {
	const writes = 32
	serverDone := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			serverDone <- err
			return
		}
		socket := &sessionSocket{Conn: connection}
		defer socket.Close()
		var wait sync.WaitGroup
		errors := make(chan error, writes)
		for i := 0; i < writes; i++ {
			wait.Add(1)
			go func(value int) {
				defer wait.Done()
				errors <- socket.WriteJSON(map[string]int{"value": value})
			}(i)
		}
		wait.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}))
	defer httpServer.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for i := 0; i < writes; i++ {
		var message map[string]int
		if err := connection.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("concurrent writes failed: %v", err)
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	controllerClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	server, err := New(Config{
		Token:            "secret-token",
		Client:           controllerClient,
		Clientset:        &kubernetes.Clientset{},
		RESTConfig:       &rest.Config{Host: "https://kubernetes.invalid"},
		DefaultNamespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}
