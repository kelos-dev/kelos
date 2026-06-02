package controller

import (
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildGitCloneInitContainer_NoAuth(t *testing.T) {
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
		Ref:  "main",
	}

	c := buildGitCloneInitContainer(workspace, nil)

	if c.Name != "git-clone" {
		t.Errorf("Expected name 'git-clone', got %q", c.Name)
	}
	if c.Image != GitCloneImage {
		t.Errorf("Expected image %q, got %q", GitCloneImage, c.Image)
	}
	// Should use Args (not Command) when no auth.
	if len(c.Command) != 0 {
		t.Errorf("Expected no Command for unauthenticated clone, got %v", c.Command)
	}
	// Should include --branch main.
	foundBranch := false
	for i, arg := range c.Args {
		if arg == "--branch" && i+1 < len(c.Args) && c.Args[i+1] == "main" {
			foundBranch = true
		}
	}
	if !foundBranch {
		t.Errorf("Expected --branch main in args, got %v", c.Args)
	}
	// SecurityContext must run as AgentUID.
	if c.SecurityContext == nil || c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != AgentUID {
		t.Errorf("Expected SecurityContext.RunAsUser = %d, got %v", AgentUID, c.SecurityContext)
	}
	// Workspace volume must be mounted.
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].Name != WorkspaceVolumeName || c.VolumeMounts[0].MountPath != WorkspaceMountPath {
		t.Errorf("Expected workspace VolumeMount {Name:%q, MountPath:%q}, got %v", WorkspaceVolumeName, WorkspaceMountPath, c.VolumeMounts)
	}
}

func TestBuildGitCloneInitContainer_WithAuth(t *testing.T) {
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo:      "https://github.com/test/repo.git",
		SecretRef: &kelosv1alpha1.SecretReference{Name: "my-secret"},
	}

	envVars := []corev1.EnvVar{
		{Name: "GITHUB_TOKEN", Value: "test-token"},
	}

	c := buildGitCloneInitContainer(workspace, envVars)

	// Should use Command with credential helper.
	if len(c.Command) == 0 {
		t.Error("Expected Command to be set for authenticated clone")
	}
	if c.Command[0] != "sh" {
		t.Errorf("Expected command 'sh', got %q", c.Command[0])
	}
	// Args must start with "--" to separate shell args from git args.
	if len(c.Args) == 0 || c.Args[0] != "--" {
		t.Errorf("Expected Args to start with '--' for authenticated clone, got %v", c.Args)
	}
	// SecurityContext must run as AgentUID.
	if c.SecurityContext == nil || c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != AgentUID {
		t.Errorf("Expected SecurityContext.RunAsUser = %d, got %v", AgentUID, c.SecurityContext)
	}
	// Workspace volume must be mounted.
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].Name != WorkspaceVolumeName || c.VolumeMounts[0].MountPath != WorkspaceMountPath {
		t.Errorf("Expected workspace VolumeMount {Name:%q, MountPath:%q}, got %v", WorkspaceVolumeName, WorkspaceMountPath, c.VolumeMounts)
	}
}

func TestBuildRemoteSetupInitContainer_NoRemotes(t *testing.T) {
	c := buildRemoteSetupInitContainer(nil)
	if c != nil {
		t.Error("Expected nil container for no remotes")
	}
}

func TestBuildRemoteSetupInitContainer_WithRemotes(t *testing.T) {
	remotes := []kelosv1alpha1.GitRemote{
		{Name: "upstream", URL: "https://github.com/upstream/repo.git"},
	}

	c := buildRemoteSetupInitContainer(remotes)
	if c == nil {
		t.Fatal("Expected non-nil container for remotes")
	}
	if c.Name != "remote-setup" {
		t.Errorf("Expected name 'remote-setup', got %q", c.Name)
	}
}

func TestBuildBranchSetupInitContainer_NoBranch(t *testing.T) {
	c := buildBranchSetupInitContainer("", &kelosv1alpha1.WorkspaceSpec{}, nil)
	if c != nil {
		t.Error("Expected nil container for no branch")
	}
}

func TestBuildBranchSetupInitContainer_WithBranch(t *testing.T) {
	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
	}

	c := buildBranchSetupInitContainer("feature-branch", workspace, nil)
	if c == nil {
		t.Fatal("Expected non-nil container for branch")
	}
	if c.Name != "branch-setup" {
		t.Errorf("Expected name 'branch-setup', got %q", c.Name)
	}
	// Should have KELOS_BRANCH env var.
	foundBranchEnv := false
	for _, env := range c.Env {
		if env.Name == "KELOS_BRANCH" && env.Value == "feature-branch" {
			foundBranchEnv = true
		}
	}
	if !foundBranchEnv {
		t.Error("Expected KELOS_BRANCH env var set to 'feature-branch'")
	}
}

func TestBuildWorkspaceFilesInitContainer_NoFiles(t *testing.T) {
	c, err := buildWorkspaceFilesInitContainer(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c != nil {
		t.Error("Expected nil container for no files")
	}
}

func TestBuildWorkspaceFilesInitContainer_WithFiles(t *testing.T) {
	files := []kelosv1alpha1.WorkspaceFile{
		{Path: "CLAUDE.md", Content: "# Instructions"},
	}

	c, err := buildWorkspaceFilesInitContainer(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("Expected non-nil container for files")
	}
	if c.Name != "workspace-files" {
		t.Errorf("Expected name 'workspace-files', got %q", c.Name)
	}
}

func TestBuildPluginSetupInitContainer_NoPlugins(t *testing.T) {
	c, err := buildPluginSetupInitContainer(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c != nil {
		t.Error("Expected nil container for no plugins")
	}
}

func TestBuildSkillsInstallInitContainer_NoSkills(t *testing.T) {
	c, err := buildSkillsInstallInitContainer(nil, "claude-code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c != nil {
		t.Error("Expected nil container for no skills")
	}
}
