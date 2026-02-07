package main

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
	"github.com/gjkim42/axon/internal/source"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(axonv1alpha1.AddToScheme(s))
	return s
}

type fakeSource struct {
	items []source.WorkItem
}

func (f *fakeSource) Discover(_ context.Context) ([]source.WorkItem, error) {
	return f.items, nil
}

func int32Ptr(v int32) *int32 { return &v }

func newTaskSpawner(name, namespace string, maxConcurrency *int32) *axonv1alpha1.TaskSpawner {
	return &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{
					WorkspaceRef: &axonv1alpha1.WorkspaceReference{Name: "test-ws"},
				},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: axonv1alpha1.Credentials{
					SecretRef: axonv1alpha1.SecretReference{Name: "test-secret"},
				},
			},
			MaxConcurrency: maxConcurrency,
		},
	}
}

func newTask(name, namespace, spawnerName string, phase axonv1alpha1.TaskPhase) *axonv1alpha1.Task {
	return &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"axon.io/taskspawner": spawnerName,
			},
		},
		Spec: axonv1alpha1.TaskSpec{
			Type: "claude-code",
			Credentials: axonv1alpha1.Credentials{
				SecretRef: axonv1alpha1.SecretReference{Name: "test-secret"},
			},
		},
		Status: axonv1alpha1.TaskStatus{
			Phase: phase,
		},
	}
}

func TestRunCycleWithSource(t *testing.T) {
	tests := []struct {
		name             string
		maxConcurrency   *int32
		existingTasks    []*axonv1alpha1.Task
		discoveredItems  []source.WorkItem
		wantTasksCreated int
		wantActiveTasks  int
	}{
		{
			name:           "No concurrency limit, creates all tasks",
			maxConcurrency: nil,
			discoveredItems: []source.WorkItem{
				{ID: "1", Title: "Issue 1"},
				{ID: "2", Title: "Issue 2"},
				{ID: "3", Title: "Issue 3"},
			},
			wantTasksCreated: 3,
			wantActiveTasks:  3,
		},
		{
			name:           "Zero concurrency means no limit",
			maxConcurrency: int32Ptr(0),
			discoveredItems: []source.WorkItem{
				{ID: "1", Title: "Issue 1"},
				{ID: "2", Title: "Issue 2"},
			},
			wantTasksCreated: 2,
			wantActiveTasks:  2,
		},
		{
			name:           "Max concurrency limits new tasks",
			maxConcurrency: int32Ptr(2),
			discoveredItems: []source.WorkItem{
				{ID: "1", Title: "Issue 1"},
				{ID: "2", Title: "Issue 2"},
				{ID: "3", Title: "Issue 3"},
			},
			wantTasksCreated: 2,
			wantActiveTasks:  2,
		},
		{
			name:           "Existing active tasks count toward limit",
			maxConcurrency: int32Ptr(2),
			existingTasks: []*axonv1alpha1.Task{
				newTask("spawner-existing1", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
			},
			discoveredItems: []source.WorkItem{
				{ID: "existing1", Title: "Existing 1"},
				{ID: "new1", Title: "New 1"},
				{ID: "new2", Title: "New 2"},
			},
			wantTasksCreated: 1,
			wantActiveTasks:  2,
		},
		{
			name:           "Terminal tasks do not count toward limit",
			maxConcurrency: int32Ptr(2),
			existingTasks: []*axonv1alpha1.Task{
				newTask("spawner-done1", "default", "spawner", axonv1alpha1.TaskPhaseSucceeded),
				newTask("spawner-done2", "default", "spawner", axonv1alpha1.TaskPhaseFailed),
			},
			discoveredItems: []source.WorkItem{
				{ID: "done1", Title: "Done 1"},
				{ID: "done2", Title: "Done 2"},
				{ID: "new1", Title: "New 1"},
				{ID: "new2", Title: "New 2"},
			},
			wantTasksCreated: 2,
			wantActiveTasks:  2,
		},
		{
			name:           "Already at limit creates no tasks",
			maxConcurrency: int32Ptr(1),
			existingTasks: []*axonv1alpha1.Task{
				newTask("spawner-active1", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
			},
			discoveredItems: []source.WorkItem{
				{ID: "active1", Title: "Active 1"},
				{ID: "new1", Title: "New 1"},
			},
			wantTasksCreated: 0,
			wantActiveTasks:  1,
		},
		{
			name:           "Pending tasks count as active",
			maxConcurrency: int32Ptr(2),
			existingTasks: []*axonv1alpha1.Task{
				newTask("spawner-pending1", "default", "spawner", axonv1alpha1.TaskPhasePending),
			},
			discoveredItems: []source.WorkItem{
				{ID: "pending1", Title: "Pending 1"},
				{ID: "new1", Title: "New 1"},
				{ID: "new2", Title: "New 2"},
			},
			wantTasksCreated: 1,
			wantActiveTasks:  2,
		},
		{
			name:           "Mix of active and terminal tasks",
			maxConcurrency: int32Ptr(3),
			existingTasks: []*axonv1alpha1.Task{
				newTask("spawner-running1", "default", "spawner", axonv1alpha1.TaskPhaseRunning),
				newTask("spawner-succeeded1", "default", "spawner", axonv1alpha1.TaskPhaseSucceeded),
				newTask("spawner-pending1", "default", "spawner", axonv1alpha1.TaskPhasePending),
			},
			discoveredItems: []source.WorkItem{
				{ID: "running1", Title: "Running 1"},
				{ID: "succeeded1", Title: "Succeeded 1"},
				{ID: "pending1", Title: "Pending 1"},
				{ID: "new1", Title: "New 1"},
				{ID: "new2", Title: "New 2"},
			},
			wantTasksCreated: 1,
			wantActiveTasks:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTaskSpawner("spawner", "default", tt.maxConcurrency)
			objs := []runtime.Object{ts}
			for _, task := range tt.existingTasks {
				objs = append(objs, task)
			}

			cl := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&axonv1alpha1.TaskSpawner{}).
				Build()

			src := &fakeSource{items: tt.discoveredItems}
			key := types.NamespacedName{Name: "spawner", Namespace: "default"}

			err := runCycleWithSource(context.Background(), cl, key, src)
			if err != nil {
				t.Fatalf("runCycleWithSource() error = %v", err)
			}

			// Count tasks that were created (new tasks not in the existing set)
			var taskList axonv1alpha1.TaskList
			if err := cl.List(context.Background(), &taskList); err != nil {
				t.Fatalf("listing tasks: %v", err)
			}

			existingNames := make(map[string]bool)
			for _, task := range tt.existingTasks {
				existingNames[task.Name] = true
			}

			newlyCreated := 0
			for _, task := range taskList.Items {
				if !existingNames[task.Name] {
					newlyCreated++
				}
			}

			if newlyCreated != tt.wantTasksCreated {
				t.Errorf("tasks created = %d, want %d", newlyCreated, tt.wantTasksCreated)
			}

			// Verify status was updated
			var updatedTS axonv1alpha1.TaskSpawner
			if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
				t.Fatalf("getting TaskSpawner: %v", err)
			}

			if updatedTS.Status.ActiveTasks != tt.wantActiveTasks {
				t.Errorf("activeTasks = %d, want %d", updatedTS.Status.ActiveTasks, tt.wantActiveTasks)
			}

			if updatedTS.Status.Phase != axonv1alpha1.TaskSpawnerPhaseRunning {
				t.Errorf("phase = %s, want %s", updatedTS.Status.Phase, axonv1alpha1.TaskSpawnerPhaseRunning)
			}
		})
	}
}

func TestRunCycleWithSource_NoNewItems(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", int32Ptr(5))
	existingTask := newTask("spawner-1", "default", "spawner", axonv1alpha1.TaskPhaseRunning)

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithRuntimeObjects(ts, existingTask).
		WithStatusSubresource(&axonv1alpha1.TaskSpawner{}).
		Build()

	src := &fakeSource{items: []source.WorkItem{
		{ID: "1", Title: "Existing"},
	}}
	key := types.NamespacedName{Name: "spawner", Namespace: "default"}

	err := runCycleWithSource(context.Background(), cl, key, src)
	if err != nil {
		t.Fatalf("runCycleWithSource() error = %v", err)
	}

	var updatedTS axonv1alpha1.TaskSpawner
	if err := cl.Get(context.Background(), key, &updatedTS); err != nil {
		t.Fatalf("getting TaskSpawner: %v", err)
	}

	if updatedTS.Status.ActiveTasks != 1 {
		t.Errorf("activeTasks = %d, want 1", updatedTS.Status.ActiveTasks)
	}

	if updatedTS.Status.TotalDiscovered != 1 {
		t.Errorf("totalDiscovered = %d, want 1", updatedTS.Status.TotalDiscovered)
	}
}

func TestParsePollInterval(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "5m0s"},
		{"5m", "5m0s"},
		{"30s", "30s"},
		{"1h", "1h0m0s"},
		{"60", "1m0s"},
		{"invalid", "5m0s"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			got := parsePollInterval(tt.input)
			if got.String() != tt.want {
				t.Errorf("parsePollInterval(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
