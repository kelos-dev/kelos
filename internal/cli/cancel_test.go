package cli

import (
	"testing"
)

func TestCancelCommandRegistered(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"cancel", "task"})
	if cmd == nil {
		t.Fatal("expected cancel task subcommand to be registered")
	}
	if cmd.Use != "task <name>" {
		t.Errorf("expected Use to be 'task <name>', got %q", cmd.Use)
	}
}

func TestCancelTaskCommand_RequiresName(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"cancel", "task"})
	err := cmd.Args(cmd, []string{})
	if err == nil {
		t.Error("Expected error when no task name provided")
	}
}

func TestCancelTaskCommand_RejectsTooManyArgs(t *testing.T) {
	root := NewRootCommand()
	cmd := findSubcommand(t, root, []string{"cancel", "task"})
	err := cmd.Args(cmd, []string{"task1", "task2"})
	if err == nil {
		t.Error("Expected error when too many args provided")
	}
}
