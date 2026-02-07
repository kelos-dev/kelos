package cli

import (
	"strings"
	"testing"
)

func TestCreateCommand_RequiresSubcommand(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no subcommand specified")
	}
}

func TestCreateCommand_HasWorkspaceSubcommand(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"create", "workspace"})
	if cmd == nil {
		t.Fatal("expected workspace subcommand under create")
	}
}

func TestCreateCommand_HasTaskSpawnerSubcommand(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"create", "taskspawner"})
	if cmd == nil {
		t.Fatal("expected taskspawner subcommand under create")
	}
}

func TestCreateWorkspace_RequiresName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "workspace", "--repo", "https://github.com/org/repo"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when name is missing")
	}
}

func TestCreateWorkspace_RequiresRepo(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "workspace", "my-workspace"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --repo is missing")
	}
}

func TestCreateWorkspace_RejectsTokenAndSecret(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "workspace", "my-workspace", "--repo", "https://github.com/org/repo", "--token", "tok", "--secret", "sec"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --token and --secret specified")
	}
	if !strings.Contains(err.Error(), "cannot specify both") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCreateTaskSpawner_RequiresName(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "taskspawner", "--workspace", "ws"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when name is missing")
	}
}

func TestCreateTaskSpawner_RequiresWorkspace(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "taskspawner", "my-ts", "--secret", "sec"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --workspace is missing")
	}
	if !strings.Contains(err.Error(), "workspace is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCreateTaskSpawner_RequiresCredentials(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "taskspawner", "my-ts", "--workspace", "ws"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no credentials configured")
	}
	if !strings.Contains(err.Error(), "no credentials configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCreateTaskSpawner_NoValidArgsFunction(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"create", "taskspawner"})
	if cmd.ValidArgsFunction != nil {
		t.Error("create taskspawner should not have ValidArgsFunction since it creates new resources")
	}
}

func TestFlagCompletionCreateTaskSpawnerCredentialType(t *testing.T) {
	root := NewRootCommand()

	root.SetArgs([]string{"__complete", "create", "taskspawner", "--credential-type", ""})
	out := &strings.Builder{}
	root.SetOut(out)
	root.Execute()

	output := out.String()
	if !strings.Contains(output, "api-key") {
		t.Errorf("expected api-key in credential-type completions, got %q", output)
	}
	if !strings.Contains(output, "oauth") {
		t.Errorf("expected oauth in credential-type completions, got %q", output)
	}
}

func TestFlagCompletionCreateTaskSpawnerState(t *testing.T) {
	root := NewRootCommand()

	root.SetArgs([]string{"__complete", "create", "taskspawner", "--state", ""})
	out := &strings.Builder{}
	root.SetOut(out)
	root.Execute()

	output := out.String()
	if !strings.Contains(output, "open") {
		t.Errorf("expected open in state completions, got %q", output)
	}
	if !strings.Contains(output, "closed") {
		t.Errorf("expected closed in state completions, got %q", output)
	}
	if !strings.Contains(output, "all") {
		t.Errorf("expected all in state completions, got %q", output)
	}
}
