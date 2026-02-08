package controller

import (
	"testing"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildClaudeCodeJob_DefaultImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Hello world",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "claude-sonnet-4-20250514",
		},
	}

	job, err := builder.Build(task, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Default image should be used.
	if container.Image != ClaudeCodeImage {
		t.Errorf("Expected image %q, got %q", ClaudeCodeImage, container.Image)
	}

	// Command should be /axon_entrypoint.sh (uniform interface).
	if len(container.Command) != 1 || container.Command[0] != "/axon_entrypoint.sh" {
		t.Errorf("Expected command [/axon_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Hello world" {
		t.Errorf("Expected args [Hello world], got %v", container.Args)
	}

	// AXON_MODEL should be set with the correct value.
	foundAxonModel := false
	for _, env := range container.Env {
		if env.Name == "AXON_MODEL" {
			foundAxonModel = true
			if env.Value != "claude-sonnet-4-20250514" {
				t.Errorf("AXON_MODEL value: expected %q, got %q", "claude-sonnet-4-20250514", env.Value)
			}
		}
	}
	if !foundAxonModel {
		t.Error("Expected AXON_MODEL env var to be set")
	}
}

func TestBuildClaudeCodeJob_CustomImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-custom",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the bug",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "my-model",
			Image: "my-custom-agent:latest",
		},
	}

	job, err := builder.Build(task, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image should be used.
	if container.Image != "my-custom-agent:latest" {
		t.Errorf("Expected image %q, got %q", "my-custom-agent:latest", container.Image)
	}

	// Command should be /axon_entrypoint.sh (same interface as default).
	if len(container.Command) != 1 || container.Command[0] != "/axon_entrypoint.sh" {
		t.Errorf("Expected command [/axon_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Fix the bug" {
		t.Errorf("Expected args [Fix the bug], got %v", container.Args)
	}

	// AXON_MODEL should be set with the correct value.
	foundAxonModel := false
	for _, env := range container.Env {
		if env.Name == "AXON_MODEL" {
			foundAxonModel = true
			if env.Value != "my-model" {
				t.Errorf("AXON_MODEL value: expected %q, got %q", "my-model", env.Value)
			}
		}
	}
	if !foundAxonModel {
		t.Error("Expected AXON_MODEL env var to be set")
	}
}

func TestBuildClaudeCodeJob_NoModel(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-model",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Hello",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// AXON_MODEL should NOT be set when model is empty.
	for _, env := range container.Env {
		if env.Name == "AXON_MODEL" {
			t.Error("AXON_MODEL should not be set when model is empty")
		}
	}
}

func TestBuildClaudeCodeJob_WorkspaceWithRef(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workspace",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &axonv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	}

	job, err := builder.Build(task, workspace)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Verify git clone args.
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	expectedArgs := []string{
		"clone",
		"--branch", "main", "--no-single-branch", "--depth", "1",
		"--", "https://github.com/example/repo.git", WorkspaceMountPath + "/repo",
	}

	if len(initContainer.Args) != len(expectedArgs) {
		t.Fatalf("Expected %d clone args, got %d: %v", len(expectedArgs), len(initContainer.Args), initContainer.Args)
	}
	for i, arg := range expectedArgs {
		if initContainer.Args[i] != arg {
			t.Errorf("Clone args[%d]: expected %q, got %q", i, arg, initContainer.Args[i])
		}
	}

	// Verify init container runs as ClaudeCodeUID.
	if initContainer.SecurityContext == nil || initContainer.SecurityContext.RunAsUser == nil {
		t.Fatal("Expected init container SecurityContext.RunAsUser to be set")
	}
	if *initContainer.SecurityContext.RunAsUser != ClaudeCodeUID {
		t.Errorf("Expected RunAsUser %d, got %d", ClaudeCodeUID, *initContainer.SecurityContext.RunAsUser)
	}

	// Verify FSGroup.
	if job.Spec.Template.Spec.SecurityContext == nil || job.Spec.Template.Spec.SecurityContext.FSGroup == nil {
		t.Fatal("Expected pod SecurityContext.FSGroup to be set")
	}
	if *job.Spec.Template.Spec.SecurityContext.FSGroup != ClaudeCodeUID {
		t.Errorf("Expected FSGroup %d, got %d", ClaudeCodeUID, *job.Spec.Template.Spec.SecurityContext.FSGroup)
	}

	// Verify main container working dir.
	container := job.Spec.Template.Spec.Containers[0]
	if container.WorkingDir != WorkspaceMountPath+"/repo" {
		t.Errorf("Expected workingDir %q, got %q", WorkspaceMountPath+"/repo", container.WorkingDir)
	}
}

func TestBuildClaudeCodeJob_CustomImageWithWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-custom-ws",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the bug",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			Image: "my-agent:v1",
			Model: "gpt-4",
		},
	}

	workspace := &axonv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		SecretRef: &axonv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image with workspace should still use /axon_entrypoint.sh.
	if container.Image != "my-agent:v1" {
		t.Errorf("Expected image %q, got %q", "my-agent:v1", container.Image)
	}
	if len(container.Command) != 1 || container.Command[0] != "/axon_entrypoint.sh" {
		t.Errorf("Expected command [/axon_entrypoint.sh], got %v", container.Command)
	}
	if len(container.Args) != 1 || container.Args[0] != "Fix the bug" {
		t.Errorf("Expected args [Fix the bug], got %v", container.Args)
	}

	// Should have workspace volume mount and working dir.
	if container.WorkingDir != WorkspaceMountPath+"/repo" {
		t.Errorf("Expected workingDir %q, got %q", WorkspaceMountPath+"/repo", container.WorkingDir)
	}
	if len(container.VolumeMounts) != 1 {
		t.Fatalf("Expected 1 volume mount, got %d", len(container.VolumeMounts))
	}

	// Verify FSGroup.
	if job.Spec.Template.Spec.SecurityContext == nil || job.Spec.Template.Spec.SecurityContext.FSGroup == nil {
		t.Fatal("Expected pod SecurityContext.FSGroup to be set")
	}
	if *job.Spec.Template.Spec.SecurityContext.FSGroup != ClaudeCodeUID {
		t.Errorf("Expected FSGroup %d, got %d", ClaudeCodeUID, *job.Spec.Template.Spec.SecurityContext.FSGroup)
	}

	// Should have AXON_MODEL with correct value, ANTHROPIC_API_KEY, GITHUB_TOKEN, GH_TOKEN.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}
	for _, name := range []string{"AXON_MODEL", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("Expected env var %q to be set", name)
		}
	}
	if envMap["AXON_MODEL"] != "gpt-4" {
		t.Errorf("AXON_MODEL value: expected %q, got %q", "gpt-4", envMap["AXON_MODEL"])
	}
}

func TestBuildClaudeCodeJob_UnsupportedType(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unsupported",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "unsupported-agent",
			Prompt: "Hello",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	_, err := builder.Build(task, nil)
	if err == nil {
		t.Fatal("Expected error for unsupported agent type, got nil")
	}
}
