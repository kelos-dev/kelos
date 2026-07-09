package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestRunDeleteWorkerPoolDeletesNamedPool(t *testing.T) {
	ctx := context.Background()
	wp := &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-a",
			Namespace: "default",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(wp).Build()

	var out strings.Builder
	if err := runDeleteWorkerPool(ctx, cl, "default", []string{"pool-a"}, false, &out); err != nil {
		t.Fatalf("runDeleteWorkerPool: %v", err)
	}

	got := &kelos.WorkerPool{}
	err := cl.Get(ctx, client.ObjectKey{Name: "pool-a", Namespace: "default"}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected worker pool to be deleted, got error %v", err)
	}
	if out.String() != "workerpool/pool-a deleted\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunDeleteWorkerPoolAllDeletesPoolsInNamespace(t *testing.T) {
	ctx := context.Background()
	wpA := workerPool("pool-a", "default")
	wpB := workerPool("pool-b", "default")
	wpOtherNamespace := workerPool("pool-c", "other")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(wpA, wpB, wpOtherNamespace).Build()

	var out strings.Builder
	if err := runDeleteWorkerPool(ctx, cl, "default", nil, true, &out); err != nil {
		t.Fatalf("runDeleteWorkerPool: %v", err)
	}

	for _, name := range []string{"pool-a", "pool-b"} {
		got := &kelos.WorkerPool{}
		err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, got)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("expected worker pool %s to be deleted, got error %v", name, err)
		}
	}

	remaining := &kelos.WorkerPool{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "pool-c", Namespace: "other"}, remaining); err != nil {
		t.Fatalf("expected other namespace worker pool to remain: %v", err)
	}

	gotLines := strings.Split(strings.TrimSpace(out.String()), "\n")
	slices.Sort(gotLines)
	wantLines := []string{
		"workerpool/pool-a deleted",
		"workerpool/pool-b deleted",
	}
	if !slices.Equal(gotLines, wantLines) {
		t.Fatalf("unexpected output lines: got %v, want %v", gotLines, wantLines)
	}
}

func TestRunDeleteWorkerPoolAllNoPools(t *testing.T) {
	ctx := context.Background()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	var out strings.Builder
	if err := runDeleteWorkerPool(ctx, cl, "default", nil, true, &out); err != nil {
		t.Fatalf("runDeleteWorkerPool: %v", err)
	}

	if out.String() != "No worker pools found\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func workerPool(name, namespace string) *kelos.WorkerPool {
	return &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}
