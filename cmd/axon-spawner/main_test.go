package main

import (
	"context"
	"testing"

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

	src, err := buildSource(ts, "my-org", "my-repo", "https://github.example.com/api/v3")
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

	src, err := buildSource(ts, "axon-core", "axon", "")
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

func TestRunCycleWithSource_PluginsArePropagated(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	ts.Spec.TaskTemplate.Plugins = []axonv1alpha1.Plugin{
		{
			Name: "my-plugin",
			ConfigMapRef: axonv1alpha1.ConfigMapReference{
				Name: "my-plugin-cm",
			},
		},
		{
			Name: "another-plugin",
			ConfigMapRef: axonv1alpha1.ConfigMapReference{
				Name: "another-plugin-cm",
			},
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

	var taskList axonv1alpha1.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("Listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}

	task := taskList.Items[0]
	if len(task.Spec.Plugins) != 2 {
		t.Fatalf("Expected 2 plugins, got %d", len(task.Spec.Plugins))
	}
	if task.Spec.Plugins[0].Name != "my-plugin" {
		t.Errorf("Expected plugin name %q, got %q", "my-plugin", task.Spec.Plugins[0].Name)
	}
	if task.Spec.Plugins[0].ConfigMapRef.Name != "my-plugin-cm" {
		t.Errorf("Expected ConfigMap name %q, got %q", "my-plugin-cm", task.Spec.Plugins[0].ConfigMapRef.Name)
	}
	if task.Spec.Plugins[1].Name != "another-plugin" {
		t.Errorf("Expected plugin name %q, got %q", "another-plugin", task.Spec.Plugins[1].Name)
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
