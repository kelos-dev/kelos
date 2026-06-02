package sessionrunner

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestNewWorkspaceManager_Defaults(t *testing.T) {
	os.Unsetenv("KELOS_BASE_BRANCH")
	os.Unsetenv("KELOS_WORKSPACE_RESET_GIT")
	os.Unsetenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS")

	wm := NewWorkspaceManager()

	if !wm.resetGit {
		t.Error("resetGit: expected true by default")
	}
	if wm.baseBranch != "" {
		t.Errorf("baseBranch: expected empty, got %q", wm.baseBranch)
	}
	if len(wm.preserveDirs) != 0 {
		t.Errorf("preserveDirs: expected empty, got %v", wm.preserveDirs)
	}
}

func TestNewWorkspaceManager_GitResetDisabled(t *testing.T) {
	t.Setenv("KELOS_WORKSPACE_RESET_GIT", "false")

	wm := NewWorkspaceManager()

	if wm.resetGit {
		t.Error("resetGit: expected false when KELOS_WORKSPACE_RESET_GIT=false")
	}
}

func TestNewWorkspaceManager_BaseBranch(t *testing.T) {
	t.Setenv("KELOS_BASE_BRANCH", "develop")

	wm := NewWorkspaceManager()

	if wm.baseBranch != "develop" {
		t.Errorf("baseBranch: expected 'develop', got %q", wm.baseBranch)
	}
}

func TestNewWorkspaceManager_PreserveDirs(t *testing.T) {
	t.Setenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS", `["node_modules",".venv"]`)

	wm := NewWorkspaceManager()

	if len(wm.preserveDirs) != 2 {
		t.Fatalf("preserveDirs: expected 2 entries, got %d", len(wm.preserveDirs))
	}
	if wm.preserveDirs[0] != "node_modules" {
		t.Errorf("preserveDirs[0]: expected 'node_modules', got %q", wm.preserveDirs[0])
	}
	if wm.preserveDirs[1] != ".venv" {
		t.Errorf("preserveDirs[1]: expected '.venv', got %q", wm.preserveDirs[1])
	}
}

func TestNewWorkspaceManager_InvalidPreserveDirsJSON(t *testing.T) {
	t.Setenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS", "not-json")

	wm := NewWorkspaceManager()

	if len(wm.preserveDirs) != 0 {
		t.Errorf("preserveDirs: expected empty on invalid JSON, got %v", wm.preserveDirs)
	}
}

func TestReset_CleanBeforeCheckout(t *testing.T) {
	t.Setenv("KELOS_BASE_BRANCH", "main")
	t.Setenv("KELOS_WORKSPACE_RESET_GIT", "true")
	t.Setenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS", `[".cache"]`)

	wm := NewWorkspaceManager()

	var commands []string
	wm.runGitCmd = func(_ context.Context, args ...string) error {
		commands = append(commands, strings.Join(args, " "))
		return nil
	}

	if err := wm.Reset(context.Background(), ""); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	// Expected order:
	// 1. fetch origin
	// 2. reset --hard HEAD
	// 3. clean -fdx -e .cache
	// 4. checkout main
	// 5. reset --hard origin/main
	expected := []string{
		"fetch origin",
		"reset --hard HEAD",
		"clean -fdx -e .cache",
		"checkout main",
		"reset --hard origin/main",
	}

	if len(commands) != len(expected) {
		t.Fatalf("expected %d commands, got %d: %v", len(expected), len(commands), commands)
	}

	for i, exp := range expected {
		if commands[i] != exp {
			t.Errorf("command[%d]: expected %q, got %q", i, exp, commands[i])
		}
	}

	// Verify reset and clean come before checkout.
	resetIdx, cleanIdx, checkoutIdx := -1, -1, -1
	for i, cmd := range commands {
		switch {
		case cmd == "reset --hard HEAD":
			resetIdx = i
		case strings.HasPrefix(cmd, "clean"):
			cleanIdx = i
		case cmd == "checkout main":
			checkoutIdx = i
		}
	}
	if resetIdx >= checkoutIdx {
		t.Error("reset --hard HEAD must come before checkout")
	}
	if cleanIdx >= checkoutIdx {
		t.Error("git clean must come before checkout")
	}
}

func TestReset_CleanBeforeCheckout_WithTaskBranch(t *testing.T) {
	t.Setenv("KELOS_BASE_BRANCH", "main")
	t.Setenv("KELOS_WORKSPACE_RESET_GIT", "true")
	t.Setenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS", "")

	wm := NewWorkspaceManager()

	var commands []string
	wm.runGitCmd = func(_ context.Context, args ...string) error {
		commands = append(commands, strings.Join(args, " "))
		return nil
	}

	if err := wm.Reset(context.Background(), "feature/my-task"); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	// Verify clean and reset --hard HEAD appear before checkout main.
	checkoutBaseIdx := -1
	for i, cmd := range commands {
		if cmd == "checkout main" {
			checkoutBaseIdx = i
			break
		}
	}
	if checkoutBaseIdx == -1 {
		t.Fatal("checkout main not found in commands")
	}

	foundReset, foundClean := false, false
	for i, cmd := range commands {
		if i >= checkoutBaseIdx {
			break
		}
		if cmd == "reset --hard HEAD" {
			foundReset = true
		}
		if strings.HasPrefix(cmd, "clean -fdx") {
			foundClean = true
		}
	}
	if !foundReset {
		t.Error("reset --hard HEAD must appear before checkout main")
	}
	if !foundClean {
		t.Error("git clean must appear before checkout main")
	}
}
