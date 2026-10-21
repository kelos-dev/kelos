package conversion

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestTaskConvert_IdentityRoundTrip(t *testing.T) {
	orig := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"},
		Spec: v1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do the thing",
			Credentials: v1alpha1.Credentials{
				Type:      v1alpha1.CredentialTypeAPIKey,
				SecretRef: &v1alpha1.SecretReference{Name: "creds"},
			},
			Branch: "kelos-task-1",
			PodOverrides: &v1alpha1.PodOverrides{
				Env: []corev1.EnvVar{{Name: "FOO", Value: "bar"}},
			},
		},
		Status: v1alpha1.TaskStatus{Phase: v1alpha1.TaskPhaseRunning, JobName: "job-1"},
	}

	hub := &v1alpha2.Task{}
	if err := taskToHub(context.Background(), orig, hub); err != nil {
		t.Fatalf("taskToHub() error = %v", err)
	}
	back := &v1alpha1.Task{}
	if err := taskFromHub(context.Background(), hub, back); err != nil {
		t.Fatalf("taskFromHub() error = %v", err)
	}
	if !reflect.DeepEqual(orig.Spec, back.Spec) {
		t.Errorf("spec round-trip mismatch:\n orig=%#v\n back=%#v", orig.Spec, back.Spec)
	}
	if !reflect.DeepEqual(orig.Status, back.Status) {
		t.Errorf("status round-trip mismatch:\n orig=%#v\n back=%#v", orig.Status, back.Status)
	}
}

func TestTaskConvert_BranchLockRoundTrips(t *testing.T) {
	hub := &v1alpha2.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "reviewer",
			Namespace:   "ns",
			Annotations: map[string]string{"team": "review"},
		},
		Spec: v1alpha2.TaskSpec{
			Type:   "claude-code",
			Prompt: "review",
			Credentials: &v1alpha2.Credentials{
				Type:      v1alpha2.CredentialTypeAPIKey,
				SecretRef: &v1alpha2.SecretReference{Name: "creds"},
			},
			Branch:     "feature-1",
			BranchLock: v1alpha2.BranchLockNone,
		},
	}

	// hub -> spoke: v1alpha1 has no branchLock field, so it is preserved in an
	// internal annotation rather than dropped.
	spoke := &v1alpha1.Task{}
	if err := taskFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskFromHub() error = %v", err)
	}
	if got := spoke.Annotations[preservedBranchLockAnnotation]; got != "None" {
		t.Fatalf("preserved annotation = %q, want %q", got, "None")
	}
	if _, ok := hub.Annotations[preservedBranchLockAnnotation]; ok {
		t.Fatal("taskFromHub() mutated the hub object's annotations")
	}

	// spoke -> hub: the field is restored, so a v1alpha1 write-back leaves the
	// immutable spec unchanged, and the internal annotation is removed.
	back := &v1alpha2.Task{}
	if err := taskToHub(context.Background(), spoke, back); err != nil {
		t.Fatalf("taskToHub() error = %v", err)
	}
	if !reflect.DeepEqual(hub.Spec, back.Spec) {
		t.Errorf("spec round-trip mismatch:\n orig=%#v\n back=%#v", hub.Spec, back.Spec)
	}
	if _, ok := back.Annotations[preservedBranchLockAnnotation]; ok {
		t.Error("internal preservation annotation leaked onto hub object")
	}
	if got := back.Annotations["team"]; got != "review" {
		t.Errorf("user annotation team = %q, want %q", got, "review")
	}
}

func TestTaskConvert_NoBranchLockOmitsAnnotation(t *testing.T) {
	hub := &v1alpha2.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: "ns"},
		Spec:       v1alpha2.TaskSpec{Type: "claude-code", Prompt: "write", Branch: "feature-1"},
	}
	spoke := &v1alpha1.Task{}
	if err := taskFromHub(context.Background(), hub, spoke); err != nil {
		t.Fatalf("taskFromHub() error = %v", err)
	}
	if _, ok := spoke.Annotations[preservedBranchLockAnnotation]; ok {
		t.Error("annotation should not be set when branchLock is empty")
	}
}

func TestTaskToHub_IgnoresUnknownBranchLockAnnotation(t *testing.T) {
	spoke := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "reviewer",
			Namespace:   "ns",
			Annotations: map[string]string{preservedBranchLockAnnotation: "Shared"},
		},
		Spec: v1alpha1.TaskSpec{Type: "claude-code", Prompt: "review", Branch: "feature-1"},
	}
	hub := &v1alpha2.Task{}
	if err := taskToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("taskToHub() error = %v", err)
	}
	if got := hub.Spec.BranchLock; got != "" {
		t.Errorf("BranchLock = %q, want empty for an unknown annotation value", got)
	}
	if _, ok := hub.Annotations[preservedBranchLockAnnotation]; ok {
		t.Error("internal preservation annotation leaked onto hub object")
	}
}

func TestTaskToHub_FoldsAgentConfigRefIntoRefs(t *testing.T) {
	src := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"},
		Spec: v1alpha1.TaskSpec{
			AgentConfigRef: &v1alpha1.AgentConfigReference{Name: "legacy-config"},
		},
	}

	hub := &v1alpha2.Task{}
	if err := taskToHub(context.Background(), src, hub); err != nil {
		t.Fatalf("taskToHub() error = %v", err)
	}
	if len(hub.Spec.AgentConfigRefs) != 1 {
		t.Fatalf("agentConfigRefs length = %d, want 1", len(hub.Spec.AgentConfigRefs))
	}
	if hub.Spec.AgentConfigRefs[0].Name != "legacy-config" {
		t.Errorf("agentConfigRefs[0].name = %q, want legacy-config", hub.Spec.AgentConfigRefs[0].Name)
	}
}

func TestTaskFromHub_BackfillsLegacyFieldsFromWorker(t *testing.T) {
	src := &v1alpha2.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"},
		Spec: v1alpha2.TaskSpec{
			Prompt: "do the thing",
			Worker: &v1alpha2.WorkerSpec{
				Type: "codex",
				Credentials: &v1alpha2.Credentials{
					Type:      v1alpha2.CredentialTypeAPIKey,
					SecretRef: &v1alpha2.SecretReference{Name: "creds"},
				},
				Model:        "gpt-5",
				Effort:       "high",
				Image:        "agent:latest",
				WorkspaceRef: &v1alpha2.WorkspaceReference{Name: "workspace"},
				AgentConfigRefs: []v1alpha2.AgentConfigReference{
					{Name: "config"},
				},
				PodOverrides: &v1alpha2.PodOverrides{
					Env: []corev1.EnvVar{{Name: "FOO", Value: "bar"}},
				},
			},
		},
	}

	dst := &v1alpha1.Task{}
	if err := taskFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("taskFromHub() error = %v", err)
	}

	if dst.Spec.Type != "codex" {
		t.Errorf("type = %q, want codex", dst.Spec.Type)
	}
	if dst.Spec.Credentials.Type != v1alpha1.CredentialTypeAPIKey || dst.Spec.Credentials.SecretRef == nil || dst.Spec.Credentials.SecretRef.Name != "creds" {
		t.Errorf("credentials not backfilled: %#v", dst.Spec.Credentials)
	}
	if dst.Spec.Model != "gpt-5" || dst.Spec.Effort != "high" || dst.Spec.Image != "agent:latest" {
		t.Errorf("model/effort/image not backfilled: %#v", dst.Spec)
	}
	if dst.Spec.WorkspaceRef == nil || dst.Spec.WorkspaceRef.Name != "workspace" {
		t.Errorf("workspaceRef not backfilled: %#v", dst.Spec.WorkspaceRef)
	}
	if len(dst.Spec.AgentConfigRefs) != 1 || dst.Spec.AgentConfigRefs[0].Name != "config" {
		t.Errorf("agentConfigRefs not backfilled: %#v", dst.Spec.AgentConfigRefs)
	}
	if dst.Spec.PodOverrides == nil || len(dst.Spec.PodOverrides.Env) != 1 || dst.Spec.PodOverrides.Env[0].Name != "FOO" {
		t.Errorf("podOverrides not backfilled: %#v", dst.Spec.PodOverrides)
	}
}

func TestWorkspaceConvert_IdentityRoundTrip(t *testing.T) {
	orig := &v1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "ns"},
		Spec: v1alpha1.WorkspaceSpec{
			Repo:      "https://github.com/o/r",
			Ref:       "main",
			SecretRef: &v1alpha1.SecretReference{Name: "gh"},
		},
	}
	hub := &v1alpha2.Workspace{}
	if err := workspaceToHub(context.Background(), orig, hub); err != nil {
		t.Fatalf("workspaceToHub() error = %v", err)
	}
	back := &v1alpha1.Workspace{}
	if err := workspaceFromHub(context.Background(), hub, back); err != nil {
		t.Fatalf("workspaceFromHub() error = %v", err)
	}
	if !reflect.DeepEqual(orig.Spec, back.Spec) {
		t.Errorf("spec round-trip mismatch:\n orig=%#v\n back=%#v", orig.Spec, back.Spec)
	}
}
