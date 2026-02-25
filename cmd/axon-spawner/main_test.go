package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	ctrl "sigs.k8s.io/controller-runtime"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/source"
)

type fakeSource struct {
	items []source.WorkItem
}

func (f *fakeSource) Discover(_ context.Context) ([]source.WorkItem, error) {
	return f.items, nil
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(axonv1alpha1.AddToScheme(s))
	return s
}

func int32Ptr(v int32) *int32 { return &v }
func boolPtr(v bool) *bool    { return &v }

func setupTest(t *testing.T, ts *axonv1alpha1.TaskSpawner, existingTasks ...axonv1alpha1.Task) (client.Client, types.NamespacedName) {
	t.Helper()
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	objs := []client.Object{ts}
	for i := range existingTasks {
		objs = append(objs, &existingTasks[i])
	}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(objs...).
		WithStatusSubresource(ts).
		Build()

	key := types.NamespacedName{Name: ts.Name, Namespace: ts.Namespace}
	return cl, key
}

func newTaskSpawner(name, namespace string, maxConcurrency *int32) *axonv1alpha1.TaskSpawner {
	return &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
				},
				WorkspaceRef: &axonv1alpha1.WorkspaceReference{Name: "test-ws"},
			},
			MaxConcurrency: maxConcurrency,
		},
	}
}

func newTask(name, namespace, spawnerName string, phase axonv1alpha1.TaskPhase) axonv1alpha1.Task {
	return axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"axon.io/taskspawner": spawnerName,
			},
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeOAuth,
				SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
			},
		},
		Status: axonv1alpha1.TaskStatus{
			Phase: phase,
		},
	}
}

func TestBuildSource_GitHubIssuesWithBaseURL(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)

	src, err := buildSource(ts, "my-org", "my-repo", "https://github.example.com/api/v3", "", "", "", "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	ghSrc, ok := src.(*source.GitHubSource)
	if !ok {
		t.Fatalf("Expected *source.GitHubSource, got %T", src)
	}
	if ghSrc.BaseURL != "https://github.example.com/api/v3" {
		t.Errorf("BaseURL = %q, want %q", ghSrc.BaseURL, "https://github.example.com/api/v3")
	}
	if ghSrc.Owner != "my-org" {
		t.Errorf("Owner = %q, want %q", ghSrc.Owner, "my-org")
	}
	if ghSrc.Repo != "my-repo" {
		t.Errorf("Repo = %q, want %q", ghSrc.Repo, "my-repo")
	}
}

func TestBuildSource_GitHubIssuesDefaultBaseURL(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)

	src, err := buildSource(ts, "axon-core", "axon", "", "", "", "", "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	ghSrc, ok := src.(*source.GitHubSource)
	if !ok {
		t.Fatalf("Expected *source.GitHubSource, got %T", src)
	}
	if ghSrc.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (defaults to api.github.com)", ghSrc.BaseURL)
	}
}

func TestBuildSource_Jira(t *testing.T) {
	ts := &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				Jira: &axonv1alpha1.Jira{
					BaseURL:   "https://mycompany.atlassian.net",
					Project:   "PROJ",
					JQL:       "status = Open",
					SecretRef: axonv1alpha1.SecretReference{Name: "jira-creds"},
				},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
				},
			},
		},
	}

	t.Setenv("JIRA_USER", "user@example.com")
	t.Setenv("JIRA_TOKEN", "jira-api-token")

	src, err := buildSource(ts, "", "", "", "", "https://mycompany.atlassian.net", "PROJ", "status = Open")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	jiraSrc, ok := src.(*source.JiraSource)
	if !ok {
		t.Fatalf("Expected *source.JiraSource, got %T", src)
	}
	if jiraSrc.BaseURL != "https://mycompany.atlassian.net" {
		t.Errorf("BaseURL = %q, want %q", jiraSrc.BaseURL, "https://mycompany.atlassian.net")
	}
	if jiraSrc.Project != "PROJ" {
		t.Errorf("Project = %q, want %q", jiraSrc.Project, "PROJ")
	}
	if jiraSrc.JQL != "status = Open" {
		t.Errorf("JQL = %q, want %q", jiraSrc.JQL, "status = Open")
	}
	if jiraSrc.User != "user@example.com" {
		t.Errorf("User = %q, want %q", jiraSrc.User, "user@example.com")
	}
	if jiraSrc.Token != "jira-api-token" {
		t.Errorf("Token = %q, want %q", jiraSrc.Token, "jira-api-token")
	}
}

func TestRunCycleWithSource_NoMaxConcurrency(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
			{ID: "3", Title: "Item 3"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// All 3 tasks should be created
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 3 {
		t.Errorf("Expected 3 tasks, got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxConcurrencyLimitsCreation(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(2))
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
			{ID: "3", Title: "Item 3"},
			{ID: "4", Title: "Item 4"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Only 2 tasks should be created (maxConcurrency=2)
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 2 {
		t.Errorf("Expected 2 tasks (maxConcurrency=2), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxConcurrencyWithExistingActiveTasks(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(3))
	existingTasks := []axonv1alpha1.Task{
		newTask("spawner-existing1", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
		newTask("spawner-existing2", "default", "spawner", axonv1alpha1.TaskPhasePending),
	}
	cl, key := setupTest(t, ts, existingTasks...)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "existing1", Title: "Existing 1"},
			{ID: "existing2", Title: "Existing 2"},
			{ID: "3", Title: "Item 3"},
			{ID: "4", Title: "Item 4"},
			{ID: "5", Title: "Item 5"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// 2 active + 1 new = 3 (maxConcurrency), so only 1 new task should be created
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 3 {
		t.Errorf("Expected 3 tasks (2 existing + 1 new), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_CompletedTasksDontCountTowardsLimit(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(2))
	existingTasks := []axonv1alpha1.Task{
		newTask("spawner-done1", "default", "spawner", axonv1alpha1.TaskPhaseSucceeded),
		newTask("spawner-done2", "default", "spawner", axonv1alpha1.TaskPhaseFailed),
	}
	cl, key := setupTest(t, ts, existingTasks...)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "done1", Title: "Done 1"},
			{ID: "done2", Title: "Done 2"},
			{ID: "3", Title: "Item 3"},
			{ID: "4", Title: "Item 4"},
			{ID: "5", Title: "Item 5"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// 2 completed tasks don't count, so 2 new can be created (maxConcurrency=2)
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 4 {
		t.Errorf("Expected 4 tasks (2 completed + 2 new), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxConcurrencyZeroMeansNoLimit(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(0))
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
			{ID: "3", Title: "Item 3"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 3 {
		t.Errorf("Expected 3 tasks (no limit with maxConcurrency=0), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxConcurrencyAlreadyAtLimit(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(2))
	existingTasks := []axonv1alpha1.Task{
		newTask("spawner-active1", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
		newTask("spawner-active2", "default", "spawner", axonv1alpha1.TaskPhasePending),
	}
	cl, key := setupTest(t, ts, existingTasks...)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "active1", Title: "Active 1"},
			{ID: "active2", Title: "Active 2"},
			{ID: "3", Title: "New Item"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Already at limit (2 active), so no new tasks should be created
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 2 {
		t.Errorf("Expected 2 tasks (at limit, none created), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_ActiveTasksStatusUpdated(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(5))
	existingTasks := []axonv1alpha1.Task{
		newTask("spawner-running", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
		newTask("spawner-done", "default", "spawner", axonv1alpha1.TaskPhaseSucceeded),
	}
	cl, key := setupTest(t, ts, existingTasks...)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "running", Title: "Running"},
			{ID: "done", Title: "Done"},
			{ID: "3", Title: "New"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Check status was updated with activeTasks
	var updatedTS axonv1alpha1.TaskSpawner
	if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
		t.Fatalf("Getting TaskSpawner: %v", err)
	}
	// 1 existing running + 1 new = 2 active
	if updatedTS.Status.ActiveTasks != 2 {
		t.Errorf("Expected activeTasks=2, got %d", updatedTS.Status.ActiveTasks)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestRunCycleWithSource_AgentConfigRefForwarded(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.TaskTemplate.AgentConfigRef = &axonv1alpha1.AgentConfigReference{
		Name: "my-config",
	}
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Spec.AgentConfigRef == nil {
		t.Fatal("Expected AgentConfigRef to be forwarded to spawned Task")
	}
	if task.Spec.AgentConfigRef.Name != "my-config" {
		t.Errorf("Expected AgentConfigRef.Name %q, got %q", "my-config", task.Spec.AgentConfigRef.Name)
	}
}

func TestRunCycleWithSource_PodOverridesForwarded(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.TaskTemplate.PodOverrides = &axonv1alpha1.PodOverrides{
		ActiveDeadlineSeconds: int64Ptr(1800),
		Env: []corev1.EnvVar{
			{Name: "HTTP_PROXY", Value: "http://proxy:8080"},
		},
		NodeSelector: map[string]string{
			"pool": "agents",
		},
	}
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify the created Task has PodOverrides forwarded from the TaskTemplate.
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if task.Spec.PodOverrides == nil {
		t.Fatal("Expected PodOverrides to be forwarded to spawned Task")
	}
	if task.Spec.PodOverrides.ActiveDeadlineSeconds == nil || *task.Spec.PodOverrides.ActiveDeadlineSeconds != 1800 {
		t.Errorf("Expected ActiveDeadlineSeconds 1800, got %v", task.Spec.PodOverrides.ActiveDeadlineSeconds)
	}
	if len(task.Spec.PodOverrides.Env) != 1 || task.Spec.PodOverrides.Env[0].Name != "HTTP_PROXY" {
		t.Errorf("Expected env HTTP_PROXY to be forwarded, got %v", task.Spec.PodOverrides.Env)
	}
	if task.Spec.PodOverrides.NodeSelector["pool"] != "agents" {
		t.Errorf("Expected nodeSelector pool=agents, got %v", task.Spec.PodOverrides.NodeSelector)
	}
}

func TestRunCycleWithSource_Suspended(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.Suspend = boolPtr(true)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// No tasks should be created when suspended
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks when suspended, got %d", len(taskList.Items))
	}

	// Status should be Suspended
	var updatedTS axonv1alpha1.TaskSpawner
	if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
		t.Fatalf("Getting TaskSpawner: %v", err)
	}
	if updatedTS.Status.Phase != axonv1alpha1.TaskSpawnerPhaseSuspended {
		t.Errorf("Expected phase %q, got %q", axonv1alpha1.TaskSpawnerPhaseSuspended, updatedTS.Status.Phase)
	}
	if updatedTS.Status.Message != "Suspended by user" {
		t.Errorf("Expected message %q, got %q", "Suspended by user", updatedTS.Status.Message)
	}
}

func TestRunCycleWithSource_SuspendFalseRunsNormally(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.Suspend = boolPtr(false)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Tasks should be created normally when suspend=false
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 2 {
		t.Errorf("Expected 2 tasks when suspend=false, got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_SuspendedIdempotent(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.Suspend = boolPtr(true)
	// Pre-set the status to Suspended to test idempotency
	ts.Status.Phase = axonv1alpha1.TaskSpawnerPhaseSuspended
	ts.Status.Message = "Suspended by user"
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
		},
	}

	// Run twice - should not error on the second run
	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("First cycle error: %v", err)
	}
	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Second cycle error: %v", err)
	}

	// Still no tasks created
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks when suspended, got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxTotalTasksLimitsCreation(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.MaxTotalTasks = int32Ptr(2)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
			{ID: "3", Title: "Item 3"},
			{ID: "4", Title: "Item 4"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Only 2 tasks should be created (maxTotalTasks=2)
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 2 {
		t.Errorf("Expected 2 tasks (maxTotalTasks=2), got %d", len(taskList.Items))
	}

	// Check TaskBudgetExhausted condition
	var updatedTS axonv1alpha1.TaskSpawner
	if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
		t.Fatalf("Getting TaskSpawner: %v", err)
	}
	foundCondition := false
	for _, c := range updatedTS.Status.Conditions {
		if c.Type == "TaskBudgetExhausted" && c.Status == metav1.ConditionTrue {
			foundCondition = true
			break
		}
	}
	if !foundCondition {
		t.Error("Expected TaskBudgetExhausted condition to be True")
	}
}

func TestRunCycleWithSource_MaxTotalTasksWithExistingTasks(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.MaxTotalTasks = int32Ptr(3)
	ts.Status.TotalTasksCreated = 2 // Already created 2 tasks before
	existingTasks := []axonv1alpha1.Task{
		newTask("spawner-existing1", "default", "spawner", axonv1alpha1.TaskPhaseSucceeded),
		newTask("spawner-existing2", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
	}
	cl, key := setupTest(t, ts, existingTasks...)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "existing1", Title: "Existing 1"},
			{ID: "existing2", Title: "Existing 2"},
			{ID: "3", Title: "Item 3"},
			{ID: "4", Title: "Item 4"},
			{ID: "5", Title: "Item 5"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Only 1 new task should be created (totalCreated=2, max=3, budget left=1)
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 3 {
		t.Errorf("Expected 3 tasks (2 existing + 1 new), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxTotalTasksBudgetAlreadyExhausted(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.MaxTotalTasks = int32Ptr(5)
	ts.Status.TotalTasksCreated = 5 // Budget already used
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// No new tasks should be created
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 0 {
		t.Errorf("Expected 0 tasks (budget exhausted), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxTotalTasksZeroMeansNoLimit(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.MaxTotalTasks = int32Ptr(0)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
			{ID: "3", Title: "Item 3"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 3 {
		t.Errorf("Expected 3 tasks (no limit with maxTotalTasks=0), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_MaxTotalTasksAndMaxConcurrencyCombined(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(10))
	ts.Spec.MaxTotalTasks = int32Ptr(2)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
			{ID: "2", Title: "Item 2"},
			{ID: "3", Title: "Item 3"},
			{ID: "4", Title: "Item 4"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// maxTotalTasks=2 is more restrictive than maxConcurrency=10
	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 2 {
		t.Errorf("Expected 2 tasks (maxTotalTasks=2 more restrictive), got %d", len(taskList.Items))
	}
}

func TestRunCycleWithSource_SuspendedConditionSet(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.Suspend = boolPtr(true)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var updatedTS axonv1alpha1.TaskSpawner
	if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
		t.Fatalf("Getting TaskSpawner: %v", err)
	}

	foundSuspended := false
	for _, c := range updatedTS.Status.Conditions {
		if c.Type == "Suspended" && c.Status == metav1.ConditionTrue {
			foundSuspended = true
			break
		}
	}
	if !foundSuspended {
		t.Error("Expected Suspended condition to be True")
	}
}

func TestRunCycleWithSource_BranchTemplateRendered(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.TaskTemplate.Branch = "axon-task-{{.Number}}"
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "42", Number: 42, Title: "Fix login bug"},
			{ID: "99", Number: 99, Title: "Add feature"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(taskList.Items))
	}

	branches := make(map[string]string)
	for _, task := range taskList.Items {
		branches[task.Name] = task.Spec.Branch
	}
	if branches["spawner-42"] != "axon-task-42" {
		t.Errorf("Expected branch %q for spawner-42, got %q", "axon-task-42", branches["spawner-42"])
	}
	if branches["spawner-99"] != "axon-task-99" {
		t.Errorf("Expected branch %q for spawner-99, got %q", "axon-task-99", branches["spawner-99"])
	}
}

func TestRunCycleWithSource_BranchStaticPassedThrough(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.TaskTemplate.Branch = "feature/my-branch"
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Number: 1, Title: "Item 1"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}
	if taskList.Items[0].Spec.Branch != "feature/my-branch" {
		t.Errorf("Expected branch %q, got %q", "feature/my-branch", taskList.Items[0].Spec.Branch)
	}
}

func TestRunCycleWithSource_NotSuspendedConditionCleared(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.Suspend = boolPtr(false)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "1", Title: "Item 1"},
		},
	}

	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	var updatedTS axonv1alpha1.TaskSpawner
	if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
		t.Fatalf("Getting TaskSpawner: %v", err)
	}

	for _, c := range updatedTS.Status.Conditions {
		if c.Type == "Suspended" && c.Status == metav1.ConditionTrue {
			t.Error("Expected Suspended condition to be False when not suspended")
		}
	}
}

func TestRunCycleWithSource_CommentFieldsPassedToSource(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.When.GitHubIssues = &axonv1alpha1.GitHubIssues{
		TriggerComment:  "/axon pick-up",
		ExcludeComments: []string{"/axon needs-input"},
	}

	src, err := buildSource(ts, "owner", "repo", "", "", "", "", "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	ghSrc, ok := src.(*source.GitHubSource)
	if !ok {
		t.Fatalf("Expected *source.GitHubSource, got %T", src)
	}
	if ghSrc.TriggerComment != "/axon pick-up" {
		t.Errorf("TriggerComment = %q, want %q", ghSrc.TriggerComment, "/axon pick-up")
	}
	if len(ghSrc.ExcludeComments) != 1 || ghSrc.ExcludeComments[0] != "/axon needs-input" {
		t.Errorf("ExcludeComments = %v, want %v", ghSrc.ExcludeComments, []string{"/axon needs-input"})
	}
}
