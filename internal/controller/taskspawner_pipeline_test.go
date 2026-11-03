package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestCountActiveTasksCountsActiveTaskPipelines(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	spawned := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"kelos.dev/taskspawner": "front-door"},
		}
	}
	objects := []client.Object{
		&kelos.TaskPipeline{ObjectMeta: spawned("pending")},
		&kelos.TaskPipeline{ObjectMeta: spawned("running"), Status: kelos.TaskPipelineStatus{Phase: kelos.TaskPipelinePhaseRunning}},
		&kelos.TaskPipeline{ObjectMeta: spawned("succeeded"), Status: kelos.TaskPipelineStatus{Phase: kelos.TaskPipelinePhaseSucceeded}},
		&kelos.TaskPipeline{ObjectMeta: spawned("failed"), Status: kelos.TaskPipelineStatus{Phase: kelos.TaskPipelinePhaseFailed}},
		&kelos.TaskPipeline{ObjectMeta: metav1.ObjectMeta{
			Name: "other-spawner", Namespace: "default",
			Labels: map[string]string{"kelos.dev/taskspawner": "other"},
		}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &TaskSpawnerReconciler{Client: cl, Scheme: scheme}

	active, err := r.countActiveTasks(context.Background(), types.NamespacedName{Namespace: "default", Name: "front-door"})
	if err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("countActiveTasks() = %d, want 2 active TaskPipelines", active)
	}
}

func TestFindTaskSpawnersForTaskMapsTaskPipeline(t *testing.T) {
	r := &TaskSpawnerReconciler{}
	pipeline := &kelos.TaskPipeline{ObjectMeta: metav1.ObjectMeta{
		Name:      "front-door-slack-abc",
		Namespace: "default",
		Labels:    map[string]string{"kelos.dev/taskspawner": "front-door"},
	}}

	requests := r.findTaskSpawnersForTask(context.Background(), pipeline)
	if len(requests) != 1 || requests[0].NamespacedName != (types.NamespacedName{Namespace: "default", Name: "front-door"}) {
		t.Fatalf("requests = %#v, want the owning TaskSpawner", requests)
	}
}
