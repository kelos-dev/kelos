package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func TestBuildCodexJob(t *testing.T) {
	builder := NewJobBuilder()

	t.Run("Basic codex job with API key", func(t *testing.T) {
		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-codex-task",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   AgentTypeCodex,
				Prompt: "Fix the bug",
				Credentials: axonv1alpha1.Credentials{
					Type: axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{
						Name: "openai-api-key",
					},
				},
			},
		}

		job, err := builder.Build(task, nil)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}

		if job.Name != "test-codex-task" {
			t.Errorf("Job name = %v, want test-codex-task", job.Name)
		}

		containers := job.Spec.Template.Spec.Containers
		if len(containers) != 1 {
			t.Fatalf("Expected 1 container, got %d", len(containers))
		}

		container := containers[0]
		if container.Name != "codex" {
			t.Errorf("Container name = %v, want codex", container.Name)
		}

		if container.Image != CodexImage {
			t.Errorf("Container image = %v, want %v", container.Image, CodexImage)
		}

		expectedArgs := []string{"exec", "--full-auto", "--json", "Fix the bug"}
		if len(container.Args) != len(expectedArgs) {
			t.Fatalf("Args length = %d, want %d", len(container.Args), len(expectedArgs))
		}
		for i, arg := range container.Args {
			if arg != expectedArgs[i] {
				t.Errorf("Args[%d] = %v, want %v", i, arg, expectedArgs[i])
			}
		}

		if len(container.Env) != 1 {
			t.Fatalf("Expected 1 env var, got %d", len(container.Env))
		}
		if container.Env[0].Name != "OPENAI_API_KEY" {
			t.Errorf("Env[0].Name = %v, want OPENAI_API_KEY", container.Env[0].Name)
		}
		if container.Env[0].ValueFrom.SecretKeyRef.Name != "openai-api-key" {
			t.Errorf("SecretKeyRef.Name = %v, want openai-api-key", container.Env[0].ValueFrom.SecretKeyRef.Name)
		}
		if container.Env[0].ValueFrom.SecretKeyRef.Key != "OPENAI_API_KEY" {
			t.Errorf("SecretKeyRef.Key = %v, want OPENAI_API_KEY", container.Env[0].ValueFrom.SecretKeyRef.Key)
		}
	})

	t.Run("Codex job with model override", func(t *testing.T) {
		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-codex-model",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   AgentTypeCodex,
				Prompt: "Create a test",
				Credentials: axonv1alpha1.Credentials{
					Type: axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{
						Name: "openai-api-key",
					},
				},
				Model: "o3",
			},
		}

		job, err := builder.Build(task, nil)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}

		container := job.Spec.Template.Spec.Containers[0]
		expectedArgs := []string{"exec", "--full-auto", "--json", "Create a test", "-m", "o3"}
		if len(container.Args) != len(expectedArgs) {
			t.Fatalf("Args length = %d, want %d", len(container.Args), len(expectedArgs))
		}
		for i, arg := range container.Args {
			if arg != expectedArgs[i] {
				t.Errorf("Args[%d] = %v, want %v", i, arg, expectedArgs[i])
			}
		}
	})

	t.Run("Codex job with workspace", func(t *testing.T) {
		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-codex-workspace",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   AgentTypeCodex,
				Prompt: "Fix the bug",
				Credentials: axonv1alpha1.Credentials{
					Type: axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{
						Name: "openai-api-key",
					},
				},
			},
		}

		workspace := &axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/example/repo.git",
			Ref:  "main",
		}

		job, err := builder.Build(task, workspace)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}

		// Verify init container
		initContainers := job.Spec.Template.Spec.InitContainers
		if len(initContainers) != 1 {
			t.Fatalf("Expected 1 init container, got %d", len(initContainers))
		}
		if initContainers[0].Name != "git-clone" {
			t.Errorf("Init container name = %v, want git-clone", initContainers[0].Name)
		}

		// Verify init container runs as codex user
		if initContainers[0].SecurityContext == nil || initContainers[0].SecurityContext.RunAsUser == nil {
			t.Fatal("Init container SecurityContext or RunAsUser is nil")
		}
		if *initContainers[0].SecurityContext.RunAsUser != CodexUID {
			t.Errorf("Init container RunAsUser = %v, want %v", *initContainers[0].SecurityContext.RunAsUser, CodexUID)
		}

		// Verify pod security context FSGroup
		if job.Spec.Template.Spec.SecurityContext == nil || job.Spec.Template.Spec.SecurityContext.FSGroup == nil {
			t.Fatal("Pod SecurityContext or FSGroup is nil")
		}
		if *job.Spec.Template.Spec.SecurityContext.FSGroup != CodexUID {
			t.Errorf("Pod FSGroup = %v, want %v", *job.Spec.Template.Spec.SecurityContext.FSGroup, CodexUID)
		}

		// Verify volume mounts on main container
		container := job.Spec.Template.Spec.Containers[0]
		if len(container.VolumeMounts) != 1 {
			t.Fatalf("Expected 1 volume mount, got %d", len(container.VolumeMounts))
		}
		if container.WorkingDir != "/workspace/repo" {
			t.Errorf("WorkingDir = %v, want /workspace/repo", container.WorkingDir)
		}
	})

	t.Run("Codex job with workspace and secretRef", func(t *testing.T) {
		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-codex-ws-secret",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   AgentTypeCodex,
				Prompt: "Create a PR",
				Credentials: axonv1alpha1.Credentials{
					Type: axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{
						Name: "openai-api-key",
					},
				},
			},
		}

		workspace := &axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/example/repo.git",
			Ref:  "main",
			SecretRef: &axonv1alpha1.SecretReference{
				Name: "github-token",
			},
		}

		job, err := builder.Build(task, workspace)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}

		// Verify main container has OPENAI_API_KEY, GITHUB_TOKEN, GH_TOKEN
		container := job.Spec.Template.Spec.Containers[0]
		if len(container.Env) != 3 {
			t.Fatalf("Expected 3 env vars, got %d", len(container.Env))
		}
		if container.Env[0].Name != "OPENAI_API_KEY" {
			t.Errorf("Env[0].Name = %v, want OPENAI_API_KEY", container.Env[0].Name)
		}
		if container.Env[1].Name != "GITHUB_TOKEN" {
			t.Errorf("Env[1].Name = %v, want GITHUB_TOKEN", container.Env[1].Name)
		}
		if container.Env[2].Name != "GH_TOKEN" {
			t.Errorf("Env[2].Name = %v, want GH_TOKEN", container.Env[2].Name)
		}

		// Verify init container has credential helper
		initContainer := job.Spec.Template.Spec.InitContainers[0]
		if len(initContainer.Command) != 3 {
			t.Fatalf("Expected init container command length 3, got %d", len(initContainer.Command))
		}
		if initContainer.Command[0] != "sh" {
			t.Errorf("Init container Command[0] = %v, want sh", initContainer.Command[0])
		}

		// Verify init container has GITHUB_TOKEN and GH_TOKEN
		if len(initContainer.Env) != 2 {
			t.Fatalf("Expected 2 init container env vars, got %d", len(initContainer.Env))
		}
		if initContainer.Env[0].Name != "GITHUB_TOKEN" {
			t.Errorf("Init container Env[0].Name = %v, want GITHUB_TOKEN", initContainer.Env[0].Name)
		}
		if initContainer.Env[1].Name != "GH_TOKEN" {
			t.Errorf("Init container Env[1].Name = %v, want GH_TOKEN", initContainer.Env[1].Name)
		}
	})

	t.Run("Unsupported agent type", func(t *testing.T) {
		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-unknown",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "unknown-agent",
				Prompt: "Do something",
				Credentials: axonv1alpha1.Credentials{
					Type: axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{
						Name: "some-secret",
					},
				},
			},
		}

		_, err := builder.Build(task, nil)
		if err == nil {
			t.Fatal("Build() expected error for unsupported agent type")
		}
	})
}

func TestBuildClaudeCodeJob(t *testing.T) {
	builder := NewJobBuilder()

	t.Run("Basic claude-code job", func(t *testing.T) {
		task := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-claude-task",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   AgentTypeClaudeCode,
				Prompt: "Fix the bug",
				Credentials: axonv1alpha1.Credentials{
					Type: axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{
						Name: "anthropic-api-key",
					},
				},
			},
		}

		job, err := builder.Build(task, nil)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}

		container := job.Spec.Template.Spec.Containers[0]
		if container.Name != "claude-code" {
			t.Errorf("Container name = %v, want claude-code", container.Name)
		}
		if container.Image != ClaudeCodeImage {
			t.Errorf("Container image = %v, want %v", container.Image, ClaudeCodeImage)
		}
	})
}
