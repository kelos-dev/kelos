package cli

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestSuspendCommand_MissingName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"suspend", "taskspawner"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when name is missing")
	}
	if !strings.Contains(err.Error(), "task spawner name is required") {
		t.Errorf("Expected 'task spawner name is required' error, got: %v", err)
	}
}

func TestSuspendCommand_TooManyArgs(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"suspend", "taskspawner", "a", "b"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error with too many arguments")
	}
	if !strings.Contains(err.Error(), "too many arguments") {
		t.Errorf("Expected 'too many arguments' error, got: %v", err)
	}
}

func TestSuspendCommand_NoResourceType(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"suspend"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when no resource type specified")
	}
	if !strings.Contains(err.Error(), "must specify a resource type") {
		t.Errorf("Expected 'must specify a resource type' error, got: %v", err)
	}
}

func TestResumeCommand_MissingName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"resume", "taskspawner"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when name is missing")
	}
	if !strings.Contains(err.Error(), "task spawner name is required") {
		t.Errorf("Expected 'task spawner name is required' error, got: %v", err)
	}
}

func TestResumeCommand_TooManyArgs(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"resume", "taskspawner", "a", "b"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error with too many arguments")
	}
	if !strings.Contains(err.Error(), "too many arguments") {
		t.Errorf("Expected 'too many arguments' error, got: %v", err)
	}
}

func TestResumeCommand_NoResourceType(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"resume"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Expected error when no resource type specified")
	}
	if !strings.Contains(err.Error(), "must specify a resource type") {
		t.Errorf("Expected 'must specify a resource type' error, got: %v", err)
	}
}

func TestSuspendTaskSpawner_PatchPreservesFields(t *testing.T) {
	maxConc := int32(5)
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			PollInterval:   "10m",
			MaxConcurrency: &maxConc,
			Suspend:        ptr.To(false),
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ts).Build()
	ctx := context.Background()
	key := client.ObjectKey{Name: "my-spawner", Namespace: "default"}

	// Get, patch suspend=true (same logic as the CLI command)
	fetched := &kelosv1alpha1.TaskSpawner{}
	if err := cl.Get(ctx, key, fetched); err != nil {
		t.Fatalf("Get: %v", err)
	}
	base := fetched.DeepCopy()
	fetched.Spec.Suspend = ptr.To(true)
	if err := cl.Patch(ctx, fetched, client.MergeFrom(base)); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	// Verify suspend changed and other fields preserved
	updated := &kelosv1alpha1.TaskSpawner{}
	if err := cl.Get(ctx, key, updated); err != nil {
		t.Fatalf("Get after patch: %v", err)
	}
	if updated.Spec.Suspend == nil || !*updated.Spec.Suspend {
		t.Error("Expected TaskSpawner to be suspended")
	}
	if updated.Spec.PollInterval != "10m" {
		t.Errorf("PollInterval = %q, want %q", updated.Spec.PollInterval, "10m")
	}
	if updated.Spec.MaxConcurrency == nil || *updated.Spec.MaxConcurrency != 5 {
		t.Error("MaxConcurrency was not preserved")
	}
}

func TestResumeTaskSpawner_PatchPreservesFields(t *testing.T) {
	maxConc := int32(3)
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			PollInterval:   "2m",
			MaxConcurrency: &maxConc,
			Suspend:        ptr.To(true),
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ts).Build()
	ctx := context.Background()
	key := client.ObjectKey{Name: "my-spawner", Namespace: "default"}

	// Get, patch suspend=false (same logic as the CLI command)
	fetched := &kelosv1alpha1.TaskSpawner{}
	if err := cl.Get(ctx, key, fetched); err != nil {
		t.Fatalf("Get: %v", err)
	}
	base := fetched.DeepCopy()
	fetched.Spec.Suspend = ptr.To(false)
	if err := cl.Patch(ctx, fetched, client.MergeFrom(base)); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	// Verify suspend changed and other fields preserved
	updated := &kelosv1alpha1.TaskSpawner{}
	if err := cl.Get(ctx, key, updated); err != nil {
		t.Fatalf("Get after patch: %v", err)
	}
	if updated.Spec.Suspend == nil || *updated.Spec.Suspend {
		t.Error("Expected TaskSpawner to not be suspended")
	}
	if updated.Spec.PollInterval != "2m" {
		t.Errorf("PollInterval = %q, want %q", updated.Spec.PollInterval, "2m")
	}
	if updated.Spec.MaxConcurrency == nil || *updated.Spec.MaxConcurrency != 3 {
		t.Error("MaxConcurrency was not preserved")
	}
}
