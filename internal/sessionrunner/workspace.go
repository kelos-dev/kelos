/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sessionrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const workspaceRepoPath = "/workspace/repo"

// WorkspaceManager handles git workspace reset between tasks.
type WorkspaceManager struct {
	resetGit     bool
	preserveDirs []string
	baseBranch   string

	// runGitCmd executes a git command. Defaults to gitCmdExec; override in tests.
	runGitCmd func(ctx context.Context, args ...string) error
}

// NewWorkspaceManager creates a new WorkspaceManager configured from environment variables.
func NewWorkspaceManager() *WorkspaceManager {
	wm := &WorkspaceManager{
		resetGit:   true,
		baseBranch: os.Getenv("KELOS_BASE_BRANCH"),
	}

	if v := os.Getenv("KELOS_WORKSPACE_RESET_GIT"); v == "false" {
		wm.resetGit = false
	}

	if v := os.Getenv("KELOS_WORKSPACE_RESET_PRESERVE_DIRS"); v != "" {
		var dirs []string
		if err := json.Unmarshal([]byte(v), &dirs); err == nil {
			wm.preserveDirs = dirs
		}
	}

	wm.runGitCmd = wm.gitCmdExec

	return wm
}

// Reset resets the workspace to a clean state, optionally checking out a branch.
func (wm *WorkspaceManager) Reset(ctx context.Context, branch string) error {
	if !wm.resetGit {
		// If git reset is disabled, just checkout the branch if specified.
		if branch != "" {
			return wm.checkoutBranch(ctx, branch)
		}
		return nil
	}

	// Fetch latest from origin.
	if err := wm.gitCmd(ctx, "fetch", "origin"); err != nil {
		fmt.Printf("Warning: git fetch failed: %v\n", err)
	}

	// Determine the base branch to reset to.
	baseBranch := wm.baseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Discard tracked-file modifications so checkout won't abort on a dirty tree.
	if err := wm.gitCmd(ctx, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("failed to reset working tree: %w", err)
	}

	// Clean untracked files, preserving specified directories.
	cleanArgs := []string{"clean", "-fdx"}
	for _, dir := range wm.preserveDirs {
		cleanArgs = append(cleanArgs, "-e", dir)
	}
	if err := wm.gitCmd(ctx, cleanArgs...); err != nil {
		return fmt.Errorf("failed to clean workspace: %w", err)
	}

	// Checkout base branch and sync to remote.
	if err := wm.gitCmd(ctx, "checkout", baseBranch); err != nil {
		return fmt.Errorf("failed to checkout base branch %s: %w", baseBranch, err)
	}

	if err := wm.gitCmd(ctx, "reset", "--hard", "origin/"+baseBranch); err != nil {
		return fmt.Errorf("failed to reset to origin/%s: %w", baseBranch, err)
	}

	// Checkout task branch if specified.
	if branch != "" {
		return wm.checkoutBranch(ctx, branch)
	}

	return nil
}

// checkoutBranch checks out or creates the specified branch, always starting
// fresh from the current HEAD (which Reset has already set to origin/<base>).
func (wm *WorkspaceManager) checkoutBranch(ctx context.Context, branch string) error {
	// Delete any leftover local branch so we always start fresh from the
	// current base HEAD. Without this, a branch reused across tasks would
	// carry commits from the previous task.
	_ = wm.gitCmd(ctx, "branch", "-D", branch)

	// Create the branch from the current HEAD (origin/<base> after Reset).
	return wm.gitCmd(ctx, "checkout", "-b", branch)
}

// gitCmd dispatches to the configured git command runner.
func (wm *WorkspaceManager) gitCmd(ctx context.Context, args ...string) error {
	if wm.runGitCmd == nil {
		return wm.gitCmdExec(ctx, args...)
	}
	return wm.runGitCmd(ctx, args...)
}

// gitCmdExec runs a git command in the workspace directory.
func (wm *WorkspaceManager) gitCmdExec(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspaceRepoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("git %s\n", strings.Join(args, " "))
	return cmd.Run()
}
