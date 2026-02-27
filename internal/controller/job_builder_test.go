package controller

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildClaudeCodeJob_DefaultImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Hello world",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "claude-sonnet-4-20250514",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Default image should be used.
	if container.Image != ClaudeCodeImage {
		t.Errorf("Expected image %q, got %q", ClaudeCodeImage, container.Image)
	}

	// Command should be /kelos_entrypoint.sh (uniform interface).
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Hello world" {
		t.Errorf("Expected args [Hello world], got %v", container.Args)
	}

	// KELOS_MODEL should be set with the correct value.
	foundKelosModel := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MODEL" {
			foundKelosModel = true
			if env.Value != "claude-sonnet-4-20250514" {
				t.Errorf("KELOS_MODEL value: expected %q, got %q", "claude-sonnet-4-20250514", env.Value)
			}
		}
	}
	if !foundKelosModel {
		t.Error("Expected KELOS_MODEL env var to be set")
	}
}

func TestBuildClaudeCodeJob_CustomImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-custom",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "my-model",
			Image: "my-custom-agent:latest",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image should be used.
	if container.Image != "my-custom-agent:latest" {
		t.Errorf("Expected image %q, got %q", "my-custom-agent:latest", container.Image)
	}

	// Command should be /kelos_entrypoint.sh (same interface as default).
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Fix the bug" {
		t.Errorf("Expected args [Fix the bug], got %v", container.Args)
	}

	// KELOS_MODEL should be set with the correct value.
	foundKelosModel := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MODEL" {
			foundKelosModel = true
			if env.Value != "my-model" {
				t.Errorf("KELOS_MODEL value: expected %q, got %q", "my-model", env.Value)
			}
		}
	}
	if !foundKelosModel {
		t.Error("Expected KELOS_MODEL env var to be set")
	}
}

func TestBuildClaudeCodeJob_NoModel(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-model",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Hello",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// KELOS_MODEL should NOT be set when model is empty.
	for _, env := range container.Env {
		if env.Name == "KELOS_MODEL" {
			t.Error("KELOS_MODEL should not be set when model is empty")
		}
	}
}

func TestBuildClaudeCodeJob_WorkspaceWithRef(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workspace",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
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

func TestBuildClaudeCodeJob_WorkspaceWithInjectedFiles(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workspace-files",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Inject plugin files",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	skillContent := "---\nname: reviewer\n---\nUse this skill for reviews\n"
	agentsContent := "Follow these team guidelines\n"
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		Files: []kelosv1alpha1.WorkspaceFile{
			{
				Path:    ".claude/skills/reviewer/SKILL.md",
				Content: skillContent,
			},
			{
				Path:    "AGENTS.md",
				Content: agentsContent,
			},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	if len(job.Spec.Template.Spec.InitContainers) != 2 {
		t.Fatalf("Expected 2 init containers (clone + injection), got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	injection := job.Spec.Template.Spec.InitContainers[1]
	if injection.Name != "workspace-files" {
		t.Fatalf("Expected second init container name %q, got %q", "workspace-files", injection.Name)
	}
	if injection.Image != GitCloneImage {
		t.Errorf("Expected injection image %q, got %q", GitCloneImage, injection.Image)
	}
	if len(injection.Command) != 3 || injection.Command[0] != "sh" || injection.Command[1] != "-c" {
		t.Fatalf("Expected injection command [sh -c ...], got %v", injection.Command)
	}

	script := injection.Command[2]
	if !strings.Contains(script, WorkspaceMountPath+"/repo/.claude/skills/reviewer/SKILL.md") {
		t.Errorf("Expected script to target injected skill path, got script: %s", script)
	}
	if !strings.Contains(script, WorkspaceMountPath+"/repo/AGENTS.md") {
		t.Errorf("Expected script to target AGENTS.md path, got script: %s", script)
	}

	skillBase64 := base64.StdEncoding.EncodeToString([]byte(skillContent))
	if !strings.Contains(script, skillBase64) {
		t.Error("Expected script to include base64-encoded skill content")
	}
	agentsBase64 := base64.StdEncoding.EncodeToString([]byte(agentsContent))
	if !strings.Contains(script, agentsBase64) {
		t.Error("Expected script to include base64-encoded AGENTS.md content")
	}
}

func TestBuildClaudeCodeJob_WorkspaceWithInjectedFilesInvalidPath(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workspace-files-invalid",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Inject plugin files",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Files: []kelosv1alpha1.WorkspaceFile{
			{
				Path:    "../AGENTS.md",
				Content: "invalid",
			},
		},
	}

	_, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err == nil {
		t.Fatal("Expected Build() to fail for invalid workspace file path")
	}
	if !strings.Contains(err.Error(), "invalid workspace file path") {
		t.Errorf("Expected invalid path error, got: %v", err)
	}
}

func TestBuildClaudeCodeJob_CustomImageWithWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-custom-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			Image: "my-agent:v1",
			Model: "gpt-4",
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image with workspace should still use /kelos_entrypoint.sh.
	if container.Image != "my-agent:v1" {
		t.Errorf("Expected image %q, got %q", "my-agent:v1", container.Image)
	}
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
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

	// Should have KELOS_MODEL with correct value, ANTHROPIC_API_KEY, GITHUB_TOKEN, GH_TOKEN.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}
	for _, name := range []string{"KELOS_MODEL", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("Expected env var %q to be set", name)
		}
	}
	if envMap["KELOS_MODEL"] != "gpt-4" {
		t.Errorf("KELOS_MODEL value: expected %q, got %q", "gpt-4", envMap["KELOS_MODEL"])
	}
}

func TestBuildClaudeCodeJob_WorkspaceWithSecretRefPersistsCredentialHelper(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-persist-cred",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	initContainer := job.Spec.Template.Spec.InitContainers[0]

	// Verify the init container command uses sh -c.
	if len(initContainer.Command) != 3 || initContainer.Command[0] != "sh" || initContainer.Command[1] != "-c" {
		t.Fatalf("Expected command [sh -c ...], got %v", initContainer.Command)
	}

	script := initContainer.Command[2]

	// The script must clone with an inline credential helper AND persist it
	// to the repo config so the agent container can authenticate with git.
	if !strings.Contains(script, "git -c credential.helper=") {
		t.Error("Expected init container script to include inline credential helper for clone")
	}
	if !strings.Contains(script, "git -C "+WorkspaceMountPath+"/repo config credential.helper") {
		t.Error("Expected init container script to persist credential helper in repo config")
	}
}

func TestBuildClaudeCodeJob_EnterpriseWorkspaceSetsGHHostAndEnterpriseToken(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ghe",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.example.com/my-org/my-repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}

	// GH_HOST should be set for enterprise.
	if envMap["GH_HOST"] != "github.example.com" {
		t.Errorf("Expected GH_HOST = %q, got %q", "github.example.com", envMap["GH_HOST"])
	}
	// GH_ENTERPRISE_TOKEN should be set instead of GH_TOKEN for enterprise hosts.
	if _, ok := envMap["GH_ENTERPRISE_TOKEN"]; !ok {
		t.Error("Expected GH_ENTERPRISE_TOKEN to be set for enterprise workspace")
	}
	if _, ok := envMap["GH_TOKEN"]; ok {
		t.Error("GH_TOKEN should not be set for enterprise workspace")
	}
	// GITHUB_TOKEN should still be set (used for git credential helper).
	if _, ok := envMap["GITHUB_TOKEN"]; !ok {
		t.Error("Expected GITHUB_TOKEN to be set for enterprise workspace")
	}

	initContainer := job.Spec.Template.Spec.InitContainers[0]
	initEnvMap := map[string]string{}
	for _, env := range initContainer.Env {
		if env.Value != "" {
			initEnvMap[env.Name] = env.Value
		} else {
			initEnvMap[env.Name] = "(from-secret)"
		}
	}
	if initEnvMap["GH_HOST"] != "github.example.com" {
		t.Errorf("Expected init container GH_HOST = %q, got %q", "github.example.com", initEnvMap["GH_HOST"])
	}
	if _, ok := initEnvMap["GH_ENTERPRISE_TOKEN"]; !ok {
		t.Error("Expected GH_ENTERPRISE_TOKEN in init container for enterprise workspace")
	}
	if _, ok := initEnvMap["GH_TOKEN"]; ok {
		t.Error("GH_TOKEN should not be set in init container for enterprise workspace")
	}
}

func TestBuildClaudeCodeJob_GithubComWorkspaceUsesGHToken(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-ghe",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-org/my-repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}

	// GH_HOST should NOT be set for github.com.
	if _, ok := envMap["GH_HOST"]; ok {
		t.Error("GH_HOST should not be set for github.com workspace")
	}
	// GH_TOKEN should be set for github.com.
	if _, ok := envMap["GH_TOKEN"]; !ok {
		t.Error("Expected GH_TOKEN to be set for github.com workspace")
	}
	// GH_ENTERPRISE_TOKEN should NOT be set for github.com.
	if _, ok := envMap["GH_ENTERPRISE_TOKEN"]; ok {
		t.Error("GH_ENTERPRISE_TOKEN should not be set for github.com workspace")
	}
}

func TestBuildCodexJob_DefaultImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-codex",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "openai-secret"},
			},
			Model: "gpt-4.1",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Default codex image should be used.
	if container.Image != CodexImage {
		t.Errorf("Expected image %q, got %q", CodexImage, container.Image)
	}

	// Container name should match the agent type.
	if container.Name != AgentTypeCodex {
		t.Errorf("Expected container name %q, got %q", AgentTypeCodex, container.Name)
	}

	// Command should be /kelos_entrypoint.sh (uniform interface).
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Fix the bug" {
		t.Errorf("Expected args [Fix the bug], got %v", container.Args)
	}

	// KELOS_MODEL should be set.
	foundKelosModel := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MODEL" {
			foundKelosModel = true
			if env.Value != "gpt-4.1" {
				t.Errorf("KELOS_MODEL value: expected %q, got %q", "gpt-4.1", env.Value)
			}
		}
	}
	if !foundKelosModel {
		t.Error("Expected KELOS_MODEL env var to be set")
	}

	// CODEX_API_KEY should be set (not ANTHROPIC_API_KEY).
	foundCodexKey := false
	for _, env := range container.Env {
		if env.Name == "CODEX_API_KEY" {
			foundCodexKey = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Error("Expected CODEX_API_KEY to reference a secret")
			} else {
				if env.ValueFrom.SecretKeyRef.Name != "openai-secret" {
					t.Errorf("Expected secret name %q, got %q", "openai-secret", env.ValueFrom.SecretKeyRef.Name)
				}
				if env.ValueFrom.SecretKeyRef.Key != "CODEX_API_KEY" {
					t.Errorf("Expected secret key %q, got %q", "CODEX_API_KEY", env.ValueFrom.SecretKeyRef.Key)
				}
			}
		}
		if env.Name == "ANTHROPIC_API_KEY" {
			t.Error("ANTHROPIC_API_KEY should not be set for codex agent type")
		}
	}
	if !foundCodexKey {
		t.Error("Expected CODEX_API_KEY env var to be set")
	}
}

func TestBuildCodexJob_CustomImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-codex-custom",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Refactor the module",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "openai-secret"},
			},
			Image: "my-codex:v2",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image should be used.
	if container.Image != "my-codex:v2" {
		t.Errorf("Expected image %q, got %q", "my-codex:v2", container.Image)
	}

	// Command should be /kelos_entrypoint.sh.
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}
}

func TestBuildCodexJob_WithWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-codex-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "openai-secret"},
			},
			Model: "gpt-4.1",
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Should have workspace volume mount and working dir.
	if container.WorkingDir != WorkspaceMountPath+"/repo" {
		t.Errorf("Expected workingDir %q, got %q", WorkspaceMountPath+"/repo", container.WorkingDir)
	}
	if len(container.VolumeMounts) != 1 {
		t.Fatalf("Expected 1 volume mount, got %d", len(container.VolumeMounts))
	}

	// Should have CODEX_API_KEY (not ANTHROPIC_API_KEY), KELOS_MODEL, GITHUB_TOKEN, GH_TOKEN.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}
	for _, name := range []string{"KELOS_MODEL", "CODEX_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("Expected env var %q to be set", name)
		}
	}
	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should not be set for codex agent type")
	}

	// Verify init container and FSGroup.
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	if initContainer.SecurityContext == nil || initContainer.SecurityContext.RunAsUser == nil {
		t.Fatal("Expected init container SecurityContext.RunAsUser to be set")
	}
	if *initContainer.SecurityContext.RunAsUser != AgentUID {
		t.Errorf("Expected RunAsUser %d, got %d", AgentUID, *initContainer.SecurityContext.RunAsUser)
	}
}

func TestBuildCodexJob_OAuthCredentials(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-codex-oauth",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Review the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: kelosv1alpha1.SecretReference{Name: "codex-oauth"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// CODEX_AUTH_JSON should be set for codex oauth.
	foundCodexAuthJSON := false
	for _, env := range container.Env {
		if env.Name == "CODEX_AUTH_JSON" {
			foundCodexAuthJSON = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Error("Expected CODEX_AUTH_JSON to reference a secret")
			} else {
				if env.ValueFrom.SecretKeyRef.Name != "codex-oauth" {
					t.Errorf("Expected secret name %q, got %q", "codex-oauth", env.ValueFrom.SecretKeyRef.Name)
				}
				if env.ValueFrom.SecretKeyRef.Key != "CODEX_AUTH_JSON" {
					t.Errorf("Expected secret key %q, got %q", "CODEX_AUTH_JSON", env.ValueFrom.SecretKeyRef.Key)
				}
			}
		}
		if env.Name == "CODEX_API_KEY" {
			t.Error("CODEX_API_KEY should not be set for codex oauth credential type")
		}
		if env.Name == "CLAUDE_CODE_OAUTH_TOKEN" {
			t.Error("CLAUDE_CODE_OAUTH_TOKEN should not be set for codex agent type")
		}
	}
	if !foundCodexAuthJSON {
		t.Error("Expected CODEX_AUTH_JSON env var to be set")
	}
}

func TestBuildGeminiJob_DefaultImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gemini",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeGemini,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "gemini-secret"},
			},
			Model: "gemini-2.5-pro",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Default gemini image should be used.
	if container.Image != GeminiImage {
		t.Errorf("Expected image %q, got %q", GeminiImage, container.Image)
	}

	// Container name should match the agent type.
	if container.Name != AgentTypeGemini {
		t.Errorf("Expected container name %q, got %q", AgentTypeGemini, container.Name)
	}

	// Command should be /kelos_entrypoint.sh (uniform interface).
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Fix the bug" {
		t.Errorf("Expected args [Fix the bug], got %v", container.Args)
	}

	// KELOS_MODEL should be set.
	foundKelosModel := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MODEL" {
			foundKelosModel = true
			if env.Value != "gemini-2.5-pro" {
				t.Errorf("KELOS_MODEL value: expected %q, got %q", "gemini-2.5-pro", env.Value)
			}
		}
	}
	if !foundKelosModel {
		t.Error("Expected KELOS_MODEL env var to be set")
	}

	// GEMINI_API_KEY should be set (not ANTHROPIC_API_KEY or CODEX_API_KEY).
	foundGeminiKey := false
	for _, env := range container.Env {
		if env.Name == "GEMINI_API_KEY" {
			foundGeminiKey = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Error("Expected GEMINI_API_KEY to reference a secret")
			} else {
				if env.ValueFrom.SecretKeyRef.Name != "gemini-secret" {
					t.Errorf("Expected secret name %q, got %q", "gemini-secret", env.ValueFrom.SecretKeyRef.Name)
				}
				if env.ValueFrom.SecretKeyRef.Key != "GEMINI_API_KEY" {
					t.Errorf("Expected secret key %q, got %q", "GEMINI_API_KEY", env.ValueFrom.SecretKeyRef.Key)
				}
			}
		}
		if env.Name == "ANTHROPIC_API_KEY" {
			t.Error("ANTHROPIC_API_KEY should not be set for gemini agent type")
		}
		if env.Name == "CODEX_API_KEY" {
			t.Error("CODEX_API_KEY should not be set for gemini agent type")
		}
	}
	if !foundGeminiKey {
		t.Error("Expected GEMINI_API_KEY env var to be set")
	}
}

func TestBuildGeminiJob_CustomImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gemini-custom",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeGemini,
			Prompt: "Refactor the module",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "gemini-secret"},
			},
			Image: "my-gemini:v2",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image should be used.
	if container.Image != "my-gemini:v2" {
		t.Errorf("Expected image %q, got %q", "my-gemini:v2", container.Image)
	}

	// Command should be /kelos_entrypoint.sh.
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}
}

func TestBuildGeminiJob_WithWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gemini-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeGemini,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "gemini-secret"},
			},
			Model: "gemini-2.5-pro",
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Should have workspace volume mount and working dir.
	if container.WorkingDir != WorkspaceMountPath+"/repo" {
		t.Errorf("Expected workingDir %q, got %q", WorkspaceMountPath+"/repo", container.WorkingDir)
	}
	if len(container.VolumeMounts) != 1 {
		t.Fatalf("Expected 1 volume mount, got %d", len(container.VolumeMounts))
	}

	// Should have GEMINI_API_KEY (not ANTHROPIC_API_KEY), KELOS_MODEL, GITHUB_TOKEN, GH_TOKEN.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}
	for _, name := range []string{"KELOS_MODEL", "GEMINI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("Expected env var %q to be set", name)
		}
	}
	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should not be set for gemini agent type")
	}
	if _, ok := envMap["CODEX_API_KEY"]; ok {
		t.Error("CODEX_API_KEY should not be set for gemini agent type")
	}

	// Verify init container and FSGroup.
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	if initContainer.SecurityContext == nil || initContainer.SecurityContext.RunAsUser == nil {
		t.Fatal("Expected init container SecurityContext.RunAsUser to be set")
	}
	if *initContainer.SecurityContext.RunAsUser != AgentUID {
		t.Errorf("Expected RunAsUser %d, got %d", AgentUID, *initContainer.SecurityContext.RunAsUser)
	}
}

func TestBuildGeminiJob_OAuthCredentials(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gemini-oauth",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeGemini,
			Prompt: "Review the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: kelosv1alpha1.SecretReference{Name: "gemini-oauth"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// GEMINI_API_KEY should be set for gemini oauth.
	foundGeminiKey := false
	for _, env := range container.Env {
		if env.Name == "GEMINI_API_KEY" {
			foundGeminiKey = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Error("Expected GEMINI_API_KEY to reference a secret")
			} else {
				if env.ValueFrom.SecretKeyRef.Name != "gemini-oauth" {
					t.Errorf("Expected secret name %q, got %q", "gemini-oauth", env.ValueFrom.SecretKeyRef.Name)
				}
				if env.ValueFrom.SecretKeyRef.Key != "GEMINI_API_KEY" {
					t.Errorf("Expected secret key %q, got %q", "GEMINI_API_KEY", env.ValueFrom.SecretKeyRef.Key)
				}
			}
		}
		if env.Name == "CLAUDE_CODE_OAUTH_TOKEN" {
			t.Error("CLAUDE_CODE_OAUTH_TOKEN should not be set for gemini agent type")
		}
		if env.Name == "CODEX_API_KEY" {
			t.Error("CODEX_API_KEY should not be set for gemini agent type")
		}
	}
	if !foundGeminiKey {
		t.Error("Expected GEMINI_API_KEY env var to be set")
	}
}

func TestBuildOpenCodeJob_DefaultImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-opencode",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeOpenCode,
			Prompt: "Fix the bug",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "opencode-secret"},
			},
			Model: "anthropic/claude-sonnet-4-20250514",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Default opencode image should be used.
	if container.Image != OpenCodeImage {
		t.Errorf("Expected image %q, got %q", OpenCodeImage, container.Image)
	}

	// Container name should match the agent type.
	if container.Name != AgentTypeOpenCode {
		t.Errorf("Expected container name %q, got %q", AgentTypeOpenCode, container.Name)
	}

	// Command should be /kelos_entrypoint.sh (uniform interface).
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}

	// Args should be just the prompt.
	if len(container.Args) != 1 || container.Args[0] != "Fix the bug" {
		t.Errorf("Expected args [Fix the bug], got %v", container.Args)
	}

	// KELOS_MODEL should be set.
	foundKelosModel := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MODEL" {
			foundKelosModel = true
			if env.Value != "anthropic/claude-sonnet-4-20250514" {
				t.Errorf("KELOS_MODEL value: expected %q, got %q", "anthropic/claude-sonnet-4-20250514", env.Value)
			}
		}
	}
	if !foundKelosModel {
		t.Error("Expected KELOS_MODEL env var to be set")
	}

	// OPENCODE_API_KEY should be set (not ANTHROPIC_API_KEY, CODEX_API_KEY, or GEMINI_API_KEY).
	foundOpenCodeKey := false
	for _, env := range container.Env {
		if env.Name == "OPENCODE_API_KEY" {
			foundOpenCodeKey = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Error("Expected OPENCODE_API_KEY to reference a secret")
			} else {
				if env.ValueFrom.SecretKeyRef.Name != "opencode-secret" {
					t.Errorf("Expected secret name %q, got %q", "opencode-secret", env.ValueFrom.SecretKeyRef.Name)
				}
				if env.ValueFrom.SecretKeyRef.Key != "OPENCODE_API_KEY" {
					t.Errorf("Expected secret key %q, got %q", "OPENCODE_API_KEY", env.ValueFrom.SecretKeyRef.Key)
				}
			}
		}
		if env.Name == "ANTHROPIC_API_KEY" {
			t.Error("ANTHROPIC_API_KEY should not be set for opencode agent type")
		}
		if env.Name == "CODEX_API_KEY" {
			t.Error("CODEX_API_KEY should not be set for opencode agent type")
		}
		if env.Name == "GEMINI_API_KEY" {
			t.Error("GEMINI_API_KEY should not be set for opencode agent type")
		}
	}
	if !foundOpenCodeKey {
		t.Error("Expected OPENCODE_API_KEY env var to be set")
	}
}

func TestBuildOpenCodeJob_CustomImage(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-opencode-custom",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeOpenCode,
			Prompt: "Refactor the module",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "opencode-secret"},
			},
			Image: "my-opencode:v2",
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Custom image should be used.
	if container.Image != "my-opencode:v2" {
		t.Errorf("Expected image %q, got %q", "my-opencode:v2", container.Image)
	}

	// Command should be /kelos_entrypoint.sh.
	if len(container.Command) != 1 || container.Command[0] != "/kelos_entrypoint.sh" {
		t.Errorf("Expected command [/kelos_entrypoint.sh], got %v", container.Command)
	}
}

func TestBuildOpenCodeJob_WithWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-opencode-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeOpenCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "opencode-secret"},
			},
			Model: "anthropic/claude-sonnet-4-20250514",
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Should have workspace volume mount and working dir.
	if container.WorkingDir != WorkspaceMountPath+"/repo" {
		t.Errorf("Expected workingDir %q, got %q", WorkspaceMountPath+"/repo", container.WorkingDir)
	}
	if len(container.VolumeMounts) != 1 {
		t.Fatalf("Expected 1 volume mount, got %d", len(container.VolumeMounts))
	}

	// Should have OPENCODE_API_KEY (not ANTHROPIC_API_KEY), KELOS_MODEL, GITHUB_TOKEN, GH_TOKEN.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		} else {
			envMap[env.Name] = "(from-secret)"
		}
	}
	for _, name := range []string{"KELOS_MODEL", "OPENCODE_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		if _, ok := envMap[name]; !ok {
			t.Errorf("Expected env var %q to be set", name)
		}
	}
	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should not be set for opencode agent type")
	}
	if _, ok := envMap["CODEX_API_KEY"]; ok {
		t.Error("CODEX_API_KEY should not be set for opencode agent type")
	}
	if _, ok := envMap["GEMINI_API_KEY"]; ok {
		t.Error("GEMINI_API_KEY should not be set for opencode agent type")
	}

	// Verify init container and FSGroup.
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	if initContainer.SecurityContext == nil || initContainer.SecurityContext.RunAsUser == nil {
		t.Fatal("Expected init container SecurityContext.RunAsUser to be set")
	}
	if *initContainer.SecurityContext.RunAsUser != AgentUID {
		t.Errorf("Expected RunAsUser %d, got %d", AgentUID, *initContainer.SecurityContext.RunAsUser)
	}
}

func TestBuildOpenCodeJob_OAuthCredentials(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-opencode-oauth",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeOpenCode,
			Prompt: "Review the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeOAuth,
				SecretRef: kelosv1alpha1.SecretReference{Name: "opencode-oauth"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// OPENCODE_API_KEY should be set for opencode oauth.
	foundOpenCodeKey := false
	for _, env := range container.Env {
		if env.Name == "OPENCODE_API_KEY" {
			foundOpenCodeKey = true
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Error("Expected OPENCODE_API_KEY to reference a secret")
			} else {
				if env.ValueFrom.SecretKeyRef.Name != "opencode-oauth" {
					t.Errorf("Expected secret name %q, got %q", "opencode-oauth", env.ValueFrom.SecretKeyRef.Name)
				}
				if env.ValueFrom.SecretKeyRef.Key != "OPENCODE_API_KEY" {
					t.Errorf("Expected secret key %q, got %q", "OPENCODE_API_KEY", env.ValueFrom.SecretKeyRef.Key)
				}
			}
		}
		if env.Name == "CLAUDE_CODE_OAUTH_TOKEN" {
			t.Error("CLAUDE_CODE_OAUTH_TOKEN should not be set for opencode agent type")
		}
		if env.Name == "CODEX_API_KEY" {
			t.Error("CODEX_API_KEY should not be set for opencode agent type")
		}
		if env.Name == "GEMINI_API_KEY" {
			t.Error("GEMINI_API_KEY should not be set for opencode agent type")
		}
	}
	if !foundOpenCodeKey {
		t.Error("Expected OPENCODE_API_KEY env var to be set")
	}
}

func TestBuildClaudeCodeJob_UnsupportedType(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unsupported",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "unsupported-agent",
			Prompt: "Hello",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	_, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err == nil {
		t.Fatal("Expected error for unsupported agent type, got nil")
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestBuildJob_PodOverridesResources(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-resources",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			PodOverrides: &kelosv1alpha1.PodOverrides{
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("500m"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("2Gi"),
						corev1.ResourceCPU:    resource.MustParse("2"),
					},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	memReq := container.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "512Mi" {
		t.Errorf("Expected memory request 512Mi, got %s", memReq.String())
	}
	cpuReq := container.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "500m" {
		t.Errorf("Expected CPU request 500m, got %s", cpuReq.String())
	}
	memLimit := container.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "2Gi" {
		t.Errorf("Expected memory limit 2Gi, got %s", memLimit.String())
	}
	cpuLimit := container.Resources.Limits[corev1.ResourceCPU]
	if cpuLimit.String() != "2" {
		t.Errorf("Expected CPU limit 2, got %s", cpuLimit.String())
	}
}

func TestBuildJob_PodOverridesActiveDeadlineSeconds(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deadline",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			PodOverrides: &kelosv1alpha1.PodOverrides{
				ActiveDeadlineSeconds: int64Ptr(1800),
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("Expected ActiveDeadlineSeconds to be set")
	}
	if *job.Spec.ActiveDeadlineSeconds != 1800 {
		t.Errorf("Expected ActiveDeadlineSeconds 1800, got %d", *job.Spec.ActiveDeadlineSeconds)
	}
}

func TestBuildJob_PodOverridesEnv(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "claude-sonnet-4-20250514",
			PodOverrides: &kelosv1alpha1.PodOverrides{
				Env: []corev1.EnvVar{
					{Name: "HTTP_PROXY", Value: "http://proxy:8080"},
					{Name: "NO_PROXY", Value: "localhost"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	// User env vars should be present.
	if envMap["HTTP_PROXY"] != "http://proxy:8080" {
		t.Errorf("Expected HTTP_PROXY=http://proxy:8080, got %q", envMap["HTTP_PROXY"])
	}
	if envMap["NO_PROXY"] != "localhost" {
		t.Errorf("Expected NO_PROXY=localhost, got %q", envMap["NO_PROXY"])
	}

	// Built-in env vars should still be present.
	if envMap["KELOS_MODEL"] != "claude-sonnet-4-20250514" {
		t.Errorf("Expected KELOS_MODEL to still be set, got %q", envMap["KELOS_MODEL"])
	}
}

func TestBuildJob_PodOverridesEnvBuiltinPrecedence(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env-precedence",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			Model: "claude-sonnet-4-20250514",
			PodOverrides: &kelosv1alpha1.PodOverrides{
				Env: []corev1.EnvVar{
					// Attempt to override a built-in env var.
					{Name: "KELOS_MODEL", Value: "should-not-take-effect"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// User env vars that collide with built-in names should be filtered out
	// so that built-in vars always take precedence.
	var kelosModelCount int
	for _, e := range container.Env {
		if e.Name == "KELOS_MODEL" {
			kelosModelCount++
			if e.Value != "claude-sonnet-4-20250514" {
				t.Errorf("Expected KELOS_MODEL value %q, got %q", "claude-sonnet-4-20250514", e.Value)
			}
		}
	}
	if kelosModelCount != 1 {
		t.Errorf("Expected exactly 1 KELOS_MODEL env var, got %d", kelosModelCount)
	}
}

func TestBuildJob_PodOverridesNodeSelector(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node-selector",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
			PodOverrides: &kelosv1alpha1.PodOverrides{
				NodeSelector: map[string]string{
					"workload-type": "ai-agent",
					"gpu":           "true",
				},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	ns := job.Spec.Template.Spec.NodeSelector
	if ns == nil {
		t.Fatal("Expected NodeSelector to be set")
	}
	if ns["workload-type"] != "ai-agent" {
		t.Errorf("Expected nodeSelector workload-type=ai-agent, got %q", ns["workload-type"])
	}
	if ns["gpu"] != "true" {
		t.Errorf("Expected nodeSelector gpu=true, got %q", ns["gpu"])
	}
}

func TestBuildJob_PodOverridesAllFields(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-all-overrides",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "openai-secret"},
			},
			PodOverrides: &kelosv1alpha1.PodOverrides{
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
				ActiveDeadlineSeconds: int64Ptr(3600),
				Env: []corev1.EnvVar{
					{Name: "HTTPS_PROXY", Value: "http://proxy:8080"},
				},
				NodeSelector: map[string]string{
					"pool": "agents",
				},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Resources
	memLimit := container.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "4Gi" {
		t.Errorf("Expected memory limit 4Gi, got %s", memLimit.String())
	}

	// ActiveDeadlineSeconds
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 3600 {
		t.Errorf("Expected ActiveDeadlineSeconds 3600, got %v", job.Spec.ActiveDeadlineSeconds)
	}

	// Env
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}
	if envMap["HTTPS_PROXY"] != "http://proxy:8080" {
		t.Errorf("Expected HTTPS_PROXY=http://proxy:8080, got %q", envMap["HTTPS_PROXY"])
	}

	// NodeSelector
	if job.Spec.Template.Spec.NodeSelector["pool"] != "agents" {
		t.Errorf("Expected nodeSelector pool=agents, got %q", job.Spec.Template.Spec.NodeSelector["pool"])
	}
}

func TestBuildJob_NoPodOverrides(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-overrides",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// No resources should be set.
	if len(container.Resources.Requests) != 0 || len(container.Resources.Limits) != 0 {
		t.Error("Expected no resources to be set when PodOverrides is nil")
	}

	// No ActiveDeadlineSeconds.
	if job.Spec.ActiveDeadlineSeconds != nil {
		t.Error("Expected no ActiveDeadlineSeconds when PodOverrides is nil")
	}

	// No NodeSelector.
	if job.Spec.Template.Spec.NodeSelector != nil {
		t.Error("Expected no NodeSelector when PodOverrides is nil")
	}
}

func TestBuildJob_AgentConfigAgentsMD(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-agentsmd",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Follow TDD. Always write tests first.",
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// KELOS_AGENTS_MD should be set.
	foundAgentsMD := false
	for _, env := range container.Env {
		if env.Name == "KELOS_AGENTS_MD" {
			foundAgentsMD = true
			if env.Value != "Follow TDD. Always write tests first." {
				t.Errorf("KELOS_AGENTS_MD value: expected %q, got %q", "Follow TDD. Always write tests first.", env.Value)
			}
		}
	}
	if !foundAgentsMD {
		t.Error("Expected KELOS_AGENTS_MD env var to be set")
	}

	// No plugin volume or init containers should be created.
	if len(job.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("Expected no volumes, got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("Expected no init containers, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
}

func TestBuildJob_AgentConfigPlugins(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-plugins",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "team-tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "deploy", Content: "Deploy instructions here"},
				},
				Agents: []kelosv1alpha1.AgentDefinition{
					{Name: "reviewer", Content: "You are a code reviewer"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Should have plugin volume.
	foundPluginVolume := false
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == PluginVolumeName {
			foundPluginVolume = true
			if v.VolumeSource.EmptyDir == nil {
				t.Error("Expected EmptyDir volume source for plugin volume")
			}
		}
	}
	if !foundPluginVolume {
		t.Error("Expected plugin volume to be created")
	}

	// Should have plugin-setup init container.
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	if initContainer.Name != "plugin-setup" {
		t.Errorf("Expected init container name %q, got %q", "plugin-setup", initContainer.Name)
	}
	if initContainer.Image != GitCloneImage {
		t.Errorf("Expected init container image %q, got %q", GitCloneImage, initContainer.Image)
	}

	// Verify script contains expected paths.
	script := initContainer.Command[2]
	if !strings.Contains(script, PluginMountPath+"/team-tools/skills/deploy/SKILL.md") {
		t.Errorf("Expected script to target skill path, got: %s", script)
	}
	if !strings.Contains(script, PluginMountPath+"/team-tools/agents/reviewer.md") {
		t.Errorf("Expected script to target agent path, got: %s", script)
	}

	// Verify base64-encoded content in script.
	skillBase64 := base64.StdEncoding.EncodeToString([]byte("Deploy instructions here"))
	if !strings.Contains(script, skillBase64) {
		t.Error("Expected script to include base64-encoded skill content")
	}
	agentBase64 := base64.StdEncoding.EncodeToString([]byte("You are a code reviewer"))
	if !strings.Contains(script, agentBase64) {
		t.Error("Expected script to include base64-encoded agent content")
	}

	// Main container should have plugin volume mount.
	container := job.Spec.Template.Spec.Containers[0]
	foundPluginMount := false
	for _, vm := range container.VolumeMounts {
		if vm.Name == PluginVolumeName && vm.MountPath == PluginMountPath {
			foundPluginMount = true
		}
	}
	if !foundPluginMount {
		t.Error("Expected plugin volume mount on main container")
	}

	// KELOS_PLUGIN_DIR should be set.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("Expected KELOS_PLUGIN_DIR=%q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}
}

func TestBuildJob_AgentConfigFull(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-full-config",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Follow TDD",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "deploy", Content: "Deploy content"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	// Both env vars should be set.
	if envMap["KELOS_AGENTS_MD"] != "Follow TDD" {
		t.Errorf("Expected KELOS_AGENTS_MD=%q, got %q", "Follow TDD", envMap["KELOS_AGENTS_MD"])
	}
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("Expected KELOS_PLUGIN_DIR=%q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}

	// Should have plugin volume and init container.
	if len(job.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Errorf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
}

func TestBuildJob_AgentConfigWithWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Follow TDD",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "deploy", Content: "Deploy content"},
				},
			},
		},
	}

	job, err := builder.Build(task, workspace, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Should have both workspace and plugin volumes.
	if len(job.Spec.Template.Spec.Volumes) != 2 {
		t.Errorf("Expected 2 volumes (workspace + plugin), got %d", len(job.Spec.Template.Spec.Volumes))
	}

	// Should have git-clone + plugin-setup init containers.
	if len(job.Spec.Template.Spec.InitContainers) != 2 {
		t.Errorf("Expected 2 init containers (git-clone + plugin-setup), got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// Main container should have both volume mounts.
	container := job.Spec.Template.Spec.Containers[0]
	if len(container.VolumeMounts) != 2 {
		t.Errorf("Expected 2 volume mounts, got %d", len(container.VolumeMounts))
	}

	// Working dir should be set from workspace.
	if container.WorkingDir != WorkspaceMountPath+"/repo" {
		t.Errorf("Expected workingDir %q, got %q", WorkspaceMountPath+"/repo", container.WorkingDir)
	}
}

func TestBuildJob_AgentConfigWithoutWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config-no-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Follow TDD",
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// Should work without workspace.
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}
	if envMap["KELOS_AGENTS_MD"] != "Follow TDD" {
		t.Errorf("Expected KELOS_AGENTS_MD=%q, got %q", "Follow TDD", envMap["KELOS_AGENTS_MD"])
	}

	// No workspace volume.
	if len(job.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("Expected no volumes, got %d", len(job.Spec.Template.Spec.Volumes))
	}

	// No working dir.
	if container.WorkingDir != "" {
		t.Errorf("Expected empty workingDir, got %q", container.WorkingDir)
	}
}

func TestBuildJob_AgentConfigCodex(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-codex-agentconfig",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Follow TDD. Always write tests first.",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "team-tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "deploy", Content: "Deploy instructions here"},
				},
				Agents: []kelosv1alpha1.AgentDefinition{
					{Name: "reviewer", Content: "You are a code reviewer"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	// KELOS_AGENTS_MD should be set for codex tasks.
	if envMap["KELOS_AGENTS_MD"] != "Follow TDD. Always write tests first." {
		t.Errorf("Expected KELOS_AGENTS_MD=%q, got %q", "Follow TDD. Always write tests first.", envMap["KELOS_AGENTS_MD"])
	}

	// KELOS_PLUGIN_DIR should be set for codex tasks.
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("Expected KELOS_PLUGIN_DIR=%q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}

	// Should have plugin volume and init container.
	if len(job.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Errorf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// Container name should be the agent type.
	if container.Name != AgentTypeCodex {
		t.Errorf("Expected container name %q, got %q", AgentTypeCodex, container.Name)
	}
}

func TestBuildJob_AgentConfigGemini(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gemini-agentconfig",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeGemini,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Use conventional commits.",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "ci-tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "lint", Content: "Run linter before committing"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	// KELOS_AGENTS_MD should be set for gemini tasks.
	if envMap["KELOS_AGENTS_MD"] != "Use conventional commits." {
		t.Errorf("Expected KELOS_AGENTS_MD=%q, got %q", "Use conventional commits.", envMap["KELOS_AGENTS_MD"])
	}

	// KELOS_PLUGIN_DIR should be set for gemini tasks.
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("Expected KELOS_PLUGIN_DIR=%q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}

	// Should have plugin volume and init container.
	if len(job.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Errorf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// Container name should be the agent type.
	if container.Name != AgentTypeGemini {
		t.Errorf("Expected container name %q, got %q", AgentTypeGemini, container.Name)
	}
}

func TestBuildJob_AgentConfigOpenCode(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-opencode-agentconfig",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeOpenCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Always run tests before committing.",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "dev-tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "test", Content: "Run unit tests first"},
				},
				Agents: []kelosv1alpha1.AgentDefinition{
					{Name: "linter", Content: "You are a code linter"},
				},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	// KELOS_AGENTS_MD should be set for opencode tasks.
	if envMap["KELOS_AGENTS_MD"] != "Always run tests before committing." {
		t.Errorf("Expected KELOS_AGENTS_MD=%q, got %q", "Always run tests before committing.", envMap["KELOS_AGENTS_MD"])
	}

	// KELOS_PLUGIN_DIR should be set for opencode tasks.
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("Expected KELOS_PLUGIN_DIR=%q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}

	// Should have plugin volume and init container.
	if len(job.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Errorf("Expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// Container name should be the agent type.
	if container.Name != AgentTypeOpenCode {
		t.Errorf("Expected container name %q, got %q", AgentTypeOpenCode, container.Name)
	}
}

func TestBuildJob_AgentConfigPluginNamePathTraversal(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-traversal",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	tests := []struct {
		name       string
		config     *kelosv1alpha1.AgentConfigSpec
		wantErrStr string
	}{
		{
			name: "plugin name with slash",
			config: &kelosv1alpha1.AgentConfigSpec{
				Plugins: []kelosv1alpha1.PluginSpec{
					{Name: "../../etc", Skills: []kelosv1alpha1.SkillDefinition{{Name: "s", Content: "c"}}},
				},
			},
			wantErrStr: "path separators",
		},
		{
			name: "skill name with slash",
			config: &kelosv1alpha1.AgentConfigSpec{
				Plugins: []kelosv1alpha1.PluginSpec{
					{Name: "ok", Skills: []kelosv1alpha1.SkillDefinition{{Name: "../evil", Content: "c"}}},
				},
			},
			wantErrStr: "path separators",
		},
		{
			name: "agent name dot-dot",
			config: &kelosv1alpha1.AgentConfigSpec{
				Plugins: []kelosv1alpha1.PluginSpec{
					{Name: "ok", Agents: []kelosv1alpha1.AgentDefinition{{Name: "..", Content: "c"}}},
				},
			},
			wantErrStr: "path traversal",
		},
		{
			name: "plugin name is dot",
			config: &kelosv1alpha1.AgentConfigSpec{
				Plugins: []kelosv1alpha1.PluginSpec{
					{Name: ".", Skills: []kelosv1alpha1.SkillDefinition{{Name: "s", Content: "c"}}},
				},
			},
			wantErrStr: "path traversal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := builder.Build(task, nil, tt.config, task.Spec.Prompt)
			if err == nil {
				t.Fatal("Expected Build() to fail for path traversal, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrStr) {
				t.Errorf("Expected error containing %q, got: %v", tt.wantErrStr, err)
			}
		})
	}
}

func TestBuildJob_BranchSetupInitContainer(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-branch",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Branch: "feature-x",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Find the branch-setup init container.
	var branchSetup *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "branch-setup" {
			branchSetup = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if branchSetup == nil {
		t.Fatal("Expected branch-setup init container")
	}

	// Verify command structure.
	if len(branchSetup.Command) != 3 || branchSetup.Command[0] != "sh" || branchSetup.Command[1] != "-c" {
		t.Fatalf("Expected command [sh -c ...], got %v", branchSetup.Command)
	}
	script := branchSetup.Command[2]
	if !strings.Contains(script, "$KELOS_BRANCH") {
		t.Error("Expected branch-setup script to reference $KELOS_BRANCH")
	}
	if !strings.Contains(script, "git checkout") {
		t.Error("Expected branch-setup script to include git checkout")
	}
	if !strings.Contains(script, "git fetch") {
		t.Error("Expected branch-setup script to include git fetch")
	}
	// Without secretRef, no credential helper should be used.
	if strings.Contains(script, "credential.helper") {
		t.Error("Expected no credential helper without secretRef")
	}

	// Verify KELOS_BRANCH env var on init container.
	var foundBranch bool
	for _, env := range branchSetup.Env {
		if env.Name == "KELOS_BRANCH" && env.Value == "feature-x" {
			foundBranch = true
		}
	}
	if !foundBranch {
		t.Error("Expected KELOS_BRANCH=feature-x env var on branch-setup")
	}

	// Verify KELOS_BRANCH env var on main container.
	mainContainer := job.Spec.Template.Spec.Containers[0]
	var foundMainBranch bool
	for _, env := range mainContainer.Env {
		if env.Name == "KELOS_BRANCH" && env.Value == "feature-x" {
			foundMainBranch = true
		}
	}
	if !foundMainBranch {
		t.Error("Expected KELOS_BRANCH=feature-x env var on main container")
	}
}

func TestBuildJob_BranchSetupWithSecretRefUsesCredentialHelper(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-branch-cred",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Branch: "feature-y",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	var branchSetup *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "branch-setup" {
			branchSetup = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if branchSetup == nil {
		t.Fatal("Expected branch-setup init container")
	}

	script := branchSetup.Command[2]
	if !strings.Contains(script, "credential.helper") {
		t.Error("Expected branch-setup script to include credential helper when secretRef is set")
	}

	// Verify GITHUB_TOKEN env var is present on branch-setup.
	var foundToken bool
	for _, env := range branchSetup.Env {
		if env.Name == "GITHUB_TOKEN" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Error("Expected GITHUB_TOKEN env var on branch-setup init container")
	}
}

func TestBuildJob_BranchWithoutWorkspaceNoInitContainer(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-branch-no-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Branch: "feature-z",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Without workspace, no init containers should exist.
	if len(job.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("Expected 0 init containers without workspace, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// KELOS_BRANCH should still be set on the main container.
	mainContainer := job.Spec.Template.Spec.Containers[0]
	var foundBranch bool
	for _, env := range mainContainer.Env {
		if env.Name == "KELOS_BRANCH" && env.Value == "feature-z" {
			foundBranch = true
		}
	}
	if !foundBranch {
		t.Error("Expected KELOS_BRANCH=feature-z env var even without workspace")
	}
}

func TestBuildJob_BranchEnvDoesNotMutateWorkspaceEnvVars(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-branch-env-safety",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Branch: "feature-w",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
		SecretRef: &kelosv1alpha1.SecretReference{
			Name: "github-token",
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// The git-clone init container should NOT have KELOS_BRANCH in its env.
	gitClone := job.Spec.Template.Spec.InitContainers[0]
	if gitClone.Name != "git-clone" {
		t.Fatalf("Expected first init container to be git-clone, got %s", gitClone.Name)
	}
	for _, env := range gitClone.Env {
		if env.Name == "KELOS_BRANCH" {
			t.Error("git-clone init container should not have KELOS_BRANCH env var (slice mutation bug)")
		}
	}
}

func TestBuildJob_KelosAgentTypeAlwaysSet(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
	}{
		{"claude-code", AgentTypeClaudeCode},
		{"codex", AgentTypeCodex},
		{"gemini", AgentTypeGemini},
		{"opencode", AgentTypeOpenCode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewJobBuilder()
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-agent-type",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   tt.agentType,
					Prompt: "Hello",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
					},
				},
			}

			job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
			if err != nil {
				t.Fatalf("Build() returned error: %v", err)
			}

			container := job.Spec.Template.Spec.Containers[0]
			found := false
			for _, env := range container.Env {
				if env.Name == "KELOS_AGENT_TYPE" {
					found = true
					if env.Value != tt.agentType {
						t.Errorf("KELOS_AGENT_TYPE: expected %q, got %q", tt.agentType, env.Value)
					}
				}
			}
			if !found {
				t.Error("Expected KELOS_AGENT_TYPE env var to be set")
			}
		})
	}
}

func TestBuildJob_AgentConfigMCPServers(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{
				Name: "github",
				Type: "http",
				URL:  "https://api.githubcopilot.com/mcp/",
			},
			{
				Name:    "local-db",
				Type:    "stdio",
				Command: "npx",
				Args:    []string{"-y", "@bytebase/dbhub"},
				Env:     map[string]string{"DSN": "postgres://localhost/db"},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	// KELOS_MCP_SERVERS should be set.
	var mcpJSON string
	for _, env := range container.Env {
		if env.Name == "KELOS_MCP_SERVERS" {
			mcpJSON = env.Value
		}
	}
	if mcpJSON == "" {
		t.Fatal("Expected KELOS_MCP_SERVERS env var to be set")
	}

	// Verify the JSON structure matches .mcp.json format.
	var parsed struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcpJSON), &parsed); err != nil {
		t.Fatalf("Failed to parse KELOS_MCP_SERVERS JSON: %v", err)
	}

	if len(parsed.MCPServers) != 2 {
		t.Fatalf("Expected 2 MCP servers, got %d", len(parsed.MCPServers))
	}

	github, ok := parsed.MCPServers["github"]
	if !ok {
		t.Fatal("Expected 'github' MCP server entry")
	}
	if github.Type != "http" {
		t.Errorf("Expected github type 'http', got %q", github.Type)
	}
	if github.URL != "https://api.githubcopilot.com/mcp/" {
		t.Errorf("Expected github URL, got %q", github.URL)
	}

	localDB, ok := parsed.MCPServers["local-db"]
	if !ok {
		t.Fatal("Expected 'local-db' MCP server entry")
	}
	if localDB.Type != "stdio" {
		t.Errorf("Expected local-db type 'stdio', got %q", localDB.Type)
	}
	if localDB.Command != "npx" {
		t.Errorf("Expected local-db command 'npx', got %q", localDB.Command)
	}
	if len(localDB.Args) != 2 || localDB.Args[0] != "-y" || localDB.Args[1] != "@bytebase/dbhub" {
		t.Errorf("Expected local-db args [-y @bytebase/dbhub], got %v", localDB.Args)
	}
	if localDB.Env["DSN"] != "postgres://localhost/db" {
		t.Errorf("Expected local-db env DSN, got %v", localDB.Env)
	}

	// No extra volumes or init containers should be created for MCP.
	if len(job.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("Expected no volumes for MCP-only config, got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("Expected no init containers for MCP-only config, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
}

func TestBuildJob_AgentConfigMCPServersWithHTTPHeaders(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-headers",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{
				Name:    "secure-api",
				Type:    "http",
				URL:     "https://mcp.example.com/mcp",
				Headers: map[string]string{"Authorization": "Bearer token123"},
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]

	var mcpJSON string
	for _, env := range container.Env {
		if env.Name == "KELOS_MCP_SERVERS" {
			mcpJSON = env.Value
		}
	}
	if mcpJSON == "" {
		t.Fatal("Expected KELOS_MCP_SERVERS env var to be set")
	}

	var parsed struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcpJSON), &parsed); err != nil {
		t.Fatalf("Failed to parse KELOS_MCP_SERVERS JSON: %v", err)
	}

	secureAPI, ok := parsed.MCPServers["secure-api"]
	if !ok {
		t.Fatal("Expected 'secure-api' MCP server entry")
	}
	if secureAPI.Headers["Authorization"] != "Bearer token123" {
		t.Errorf("Expected Authorization header, got %v", secureAPI.Headers)
	}
}

func TestBuildJob_AgentConfigMCPServersWithPluginsAndAgentsMD(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-full",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "Follow TDD",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "tools",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "deploy", Content: "Deploy content"},
				},
			},
		},
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{
				Name: "github",
				Type: "http",
				URL:  "https://api.githubcopilot.com/mcp/",
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	envMap := map[string]string{}
	for _, env := range container.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	// All three should be set: KELOS_AGENTS_MD, KELOS_PLUGIN_DIR, KELOS_MCP_SERVERS.
	if envMap["KELOS_AGENTS_MD"] != "Follow TDD" {
		t.Errorf("Expected KELOS_AGENTS_MD=%q, got %q", "Follow TDD", envMap["KELOS_AGENTS_MD"])
	}
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("Expected KELOS_PLUGIN_DIR=%q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}
	if envMap["KELOS_MCP_SERVERS"] == "" {
		t.Error("Expected KELOS_MCP_SERVERS to be set")
	}

	// Should have plugin volume and init container.
	if len(job.Spec.Template.Spec.Volumes) != 1 {
		t.Errorf("Expected 1 volume (plugin only), got %d", len(job.Spec.Template.Spec.Volumes))
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Errorf("Expected 1 init container (plugin-setup), got %d", len(job.Spec.Template.Spec.InitContainers))
	}
}

func TestBuildJob_AgentConfigMCPServersCodex(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-codex",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeCodex,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "openai-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{
				Name: "github",
				Type: "http",
				URL:  "https://api.githubcopilot.com/mcp/",
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	foundMCP := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MCP_SERVERS" {
			foundMCP = true
		}
	}
	if !foundMCP {
		t.Error("Expected KELOS_MCP_SERVERS env var to be set for codex")
	}
}

func TestBuildJob_AgentConfigMCPServersGemini(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-gemini",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeGemini,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "gemini-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{
				Name: "github",
				Type: "http",
				URL:  "https://api.githubcopilot.com/mcp/",
			},
		},
	}

	job, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	foundMCP := false
	for _, env := range container.Env {
		if env.Name == "KELOS_MCP_SERVERS" {
			foundMCP = true
		}
	}
	if !foundMCP {
		t.Error("Expected KELOS_MCP_SERVERS env var to be set for gemini")
	}
}

func TestBuildJob_AgentConfigMCPServersEmptyName(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-empty-name",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{
				Name: "",
				Type: "http",
				URL:  "https://example.com/mcp",
			},
		},
	}

	_, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err == nil {
		t.Fatal("Expected Build() to fail for empty MCP server name, got nil")
	}
	if !strings.Contains(err.Error(), "MCP server name is empty") {
		t.Errorf("Expected empty name error, got: %v", err)
	}
}

func TestBuildJob_AgentConfigMCPServersDuplicateName(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-dup",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix issue",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{Name: "github", Type: "http", URL: "https://api.githubcopilot.com/mcp/"},
			{Name: "github", Type: "sse", URL: "https://other.example.com/sse"},
		},
	}

	_, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
	if err == nil {
		t.Fatal("Expected Build() to fail for duplicate MCP server name, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate MCP server name") {
		t.Errorf("Expected duplicate name error, got: %v", err)
	}
}

func TestBuildJob_AgentConfigMCPServerNamePathTraversal(t *testing.T) {
	tests := []struct {
		name       string
		mcpName    string
		wantErrStr string
	}{
		{"slash in name", "foo/bar", "contains path separator"},
		{"backslash in name", `foo\bar`, "contains path separator"},
		{"dot-dot", "..", "path traversal"},
		{"dot", ".", "path traversal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewJobBuilder()
			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-mcp-traversal",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   AgentTypeClaudeCode,
					Prompt: "Fix issue",
					Credentials: kelosv1alpha1.Credentials{
						Type:      kelosv1alpha1.CredentialTypeAPIKey,
						SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
					},
				},
			}
			agentConfig := &kelosv1alpha1.AgentConfigSpec{
				MCPServers: []kelosv1alpha1.MCPServerSpec{
					{Name: tt.mcpName, Type: "http", URL: "https://example.com/mcp"},
				},
			}
			_, err := builder.Build(task, nil, agentConfig, task.Spec.Prompt)
			if err == nil {
				t.Fatal("Expected Build() to fail, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrStr) {
				t.Errorf("Expected error containing %q, got: %v", tt.wantErrStr, err)
			}
		})
	}
}

func TestBuildJob_KelosBaseBranchSetWhenWorkspaceRefPresent(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-base-branch",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "develop",
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	found := false
	for _, env := range container.Env {
		if env.Name == "KELOS_BASE_BRANCH" {
			found = true
			if env.Value != "develop" {
				t.Errorf("KELOS_BASE_BRANCH: expected %q, got %q", "develop", env.Value)
			}
		}
	}
	if !found {
		t.Error("Expected KELOS_BASE_BRANCH env var to be set when workspace.Ref is non-empty")
	}
}

func TestBuildJob_KelosBaseBranchAbsentWhenRefEmpty(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-base-branch-empty",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	for _, env := range container.Env {
		if env.Name == "KELOS_BASE_BRANCH" {
			t.Error("KELOS_BASE_BRANCH should not be set when workspace.Ref is empty")
		}
	}
}

func TestBuildJob_KelosBaseBranchAbsentWithoutWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-base-branch-no-ws",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	for _, env := range container.Env {
		if env.Name == "KELOS_BASE_BRANCH" {
			t.Error("KELOS_BASE_BRANCH should not be set when workspace is nil")
		}
	}
}

func TestBuildJob_WorkspaceWithOneRemote(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-one-remote",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/org/repo.git",
		Ref:  "main",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "private", URL: "https://github.com/user/repo.git"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	var remoteSetup *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "remote-setup" {
			remoteSetup = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if remoteSetup == nil {
		t.Fatal("Expected remote-setup init container")
	}

	script := remoteSetup.Command[2]
	if !strings.Contains(script, "git remote add 'private' 'https://github.com/user/repo.git'") {
		t.Errorf("Expected script to add quoted private remote, got %q", script)
	}

	if *remoteSetup.SecurityContext.RunAsUser != ClaudeCodeUID {
		t.Errorf("Expected RunAsUser %d, got %d", ClaudeCodeUID, *remoteSetup.SecurityContext.RunAsUser)
	}
}

func TestBuildJob_WorkspaceWithMultipleRemotes(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-multi-remote",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/org/repo.git",
		Ref:  "main",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "private", URL: "https://github.com/user/repo.git"},
			{Name: "downstream", URL: "https://github.com/vendor/repo.git"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	var remoteSetup *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "remote-setup" {
			remoteSetup = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if remoteSetup == nil {
		t.Fatal("Expected remote-setup init container")
	}

	script := remoteSetup.Command[2]
	if !strings.Contains(script, "git remote add 'private' 'https://github.com/user/repo.git'") {
		t.Errorf("Expected script to add quoted private remote, got %q", script)
	}
	if !strings.Contains(script, "git remote add 'downstream' 'https://github.com/vendor/repo.git'") {
		t.Errorf("Expected script to add quoted downstream remote, got %q", script)
	}
}

func TestBuildJob_WorkspaceWithNoRemotesNoRemoteSetupContainer(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-remotes",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/org/repo.git",
		Ref:  "main",
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	for _, c := range job.Spec.Template.Spec.InitContainers {
		if c.Name == "remote-setup" {
			t.Error("Expected no remote-setup init container when remotes is empty")
		}
	}
}

func TestBuildJob_RemoteSetupOrderingWithBranchSetup(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-remote-order",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Work on feature",
			Branch: "feature-x",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/org/repo.git",
		Ref:  "main",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "private", URL: "https://github.com/user/repo.git"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	initContainers := job.Spec.Template.Spec.InitContainers
	nameOrder := make([]string, len(initContainers))
	for i, c := range initContainers {
		nameOrder[i] = c.Name
	}

	cloneIdx, remoteIdx, branchIdx := -1, -1, -1
	for i, name := range nameOrder {
		switch name {
		case "git-clone":
			cloneIdx = i
		case "remote-setup":
			remoteIdx = i
		case "branch-setup":
			branchIdx = i
		}
	}

	if cloneIdx < 0 || remoteIdx < 0 || branchIdx < 0 {
		t.Fatalf("Expected git-clone, remote-setup, branch-setup; got %v", nameOrder)
	}
	if !(cloneIdx < remoteIdx && remoteIdx < branchIdx) {
		t.Errorf("Expected ordering git-clone < remote-setup < branch-setup, got %v", nameOrder)
	}
}

func TestBuildJob_RemoteSetupQuotesShellMetacharacters(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-remote-injection",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do work",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/org/repo.git",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "bad;rm -rf /", URL: "https://evil.com$(whoami)"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	var remoteSetup *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		if job.Spec.Template.Spec.InitContainers[i].Name == "remote-setup" {
			remoteSetup = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if remoteSetup == nil {
		t.Fatal("Expected remote-setup init container")
	}

	script := remoteSetup.Command[2]
	expected := "git remote add 'bad;rm -rf /' 'https://evil.com$(whoami)'"
	if !strings.Contains(script, expected) {
		t.Errorf("Expected shell metacharacters to be single-quoted:\nwant substring: %s\ngot script: %s", expected, script)
	}
}

func TestBuildJob_WorkspaceWithUpstreamRemoteInjectsEnv(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-upstream-env",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-fork/repo.git",
		Ref:  "main",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "upstream", URL: "https://github.com/upstream-org/repo.git"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	mainContainer := job.Spec.Template.Spec.Containers[0]
	found := false
	for _, env := range mainContainer.Env {
		if env.Name == "KELOS_UPSTREAM_REPO" {
			found = true
			if env.Value != "upstream-org/repo" {
				t.Errorf("KELOS_UPSTREAM_REPO = %q, want %q", env.Value, "upstream-org/repo")
			}
			break
		}
	}
	if !found {
		t.Error("Expected KELOS_UPSTREAM_REPO env var on main container")
	}
}

func TestBuildJob_WorkspaceWithNonUpstreamRemoteNoEnv(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-upstream-env",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-fork/repo.git",
		Ref:  "main",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "other", URL: "https://github.com/other-org/repo.git"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	mainContainer := job.Spec.Template.Spec.Containers[0]
	for _, env := range mainContainer.Env {
		if env.Name == "KELOS_UPSTREAM_REPO" {
			t.Errorf("Expected no KELOS_UPSTREAM_REPO env var, but found value %q", env.Value)
		}
	}
}

func TestBuildJob_WorkspaceWithInvalidUpstreamRemoteNoEnv(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-invalid-upstream",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Fix the code",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/my-fork/repo.git",
		Ref:  "main",
		Remotes: []kelosv1alpha1.GitRemote{
			{Name: "upstream", URL: "not-a-valid-url"},
		},
	}

	job, err := builder.Build(task, workspace, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	mainContainer := job.Spec.Template.Spec.Containers[0]
	for _, env := range mainContainer.Env {
		if env.Name == "KELOS_UPSTREAM_REPO" {
			t.Errorf("Expected no KELOS_UPSTREAM_REPO for invalid URL, but found value %q", env.Value)
		}
	}
}

func TestBuildJob_TaskSpawnerLabelInjectsEnv(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner-env",
			Namespace: "default",
			Labels: map[string]string{
				"kelos.dev/taskspawner": "kelos-workers",
			},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Hello",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	found := false
	for _, env := range container.Env {
		if env.Name == "KELOS_TASKSPAWNER" {
			found = true
			if env.Value != "kelos-workers" {
				t.Errorf("KELOS_TASKSPAWNER: expected %q, got %q", "kelos-workers", env.Value)
			}
		}
	}
	if !found {
		t.Error("Expected KELOS_TASKSPAWNER env var to be set when label is present")
	}
}

func TestBuildJob_NoTaskSpawnerLabelNoEnv(t *testing.T) {
	builder := NewJobBuilder()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-spawner-env",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Hello",
			Credentials: kelosv1alpha1.Credentials{
				Type:      kelosv1alpha1.CredentialTypeAPIKey,
				SecretRef: kelosv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil, nil, task.Spec.Prompt)
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	for _, env := range container.Env {
		if env.Name == "KELOS_TASKSPAWNER" {
			t.Errorf("Expected no KELOS_TASKSPAWNER env var when label is absent, but found value %q", env.Value)
		}
	}
}
