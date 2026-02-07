package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestValidArgsFunctionWired(t *testing.T) {
	root := NewRootCommand()

	tests := []struct {
		name string
		path []string
	}{
		{"get task", []string{"get", "task"}},
		{"get taskspawner", []string{"get", "taskspawner"}},
		{"delete task", []string{"delete", "task"}},
		{"logs", []string{"logs"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := findSubcommand(t, root, tt.path)
			if cmd.ValidArgsFunction == nil {
				t.Errorf("expected ValidArgsFunction to be set on %q", tt.name)
			}
		})
	}
}

func TestCompletionWithInvalidKubeconfig(t *testing.T) {
	cfg := &ClientConfig{Kubeconfig: "/nonexistent/kubeconfig"}

	fns := []struct {
		name string
		fn   cobra.CompletionFunc
	}{
		{"completeTaskNames", completeTaskNames(cfg)},
		{"completeTaskSpawnerNames", completeTaskSpawnerNames(cfg)},
	}

	for _, tt := range fns {
		t.Run(tt.name, func(t *testing.T) {
			results, directive := tt.fn(nil, nil, "")
			if len(results) != 0 {
				t.Errorf("expected no completions, got %v", results)
			}
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("expected ShellCompDirectiveNoFileComp, got %d", directive)
			}
		})
	}
}

func TestCompletionSkipsAfterFirstArg(t *testing.T) {
	cfg := &ClientConfig{Kubeconfig: "/nonexistent/kubeconfig"}

	fns := []struct {
		name string
		fn   cobra.CompletionFunc
	}{
		{"completeTaskNames", completeTaskNames(cfg)},
		{"completeTaskSpawnerNames", completeTaskSpawnerNames(cfg)},
	}

	for _, tt := range fns {
		t.Run(tt.name, func(t *testing.T) {
			results, directive := tt.fn(nil, []string{"already-provided"}, "")
			if len(results) != 0 {
				t.Errorf("expected no completions when arg already provided, got %v", results)
			}
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("expected ShellCompDirectiveNoFileComp, got %d", directive)
			}
		})
	}
}

func TestFlagCompletionOutput(t *testing.T) {
	root := NewRootCommand()

	root.SetArgs([]string{"__complete", "get", "task", "--output", ""})
	out := &strings.Builder{}
	root.SetOut(out)
	root.Execute()

	output := out.String()
	if !strings.Contains(output, "yaml") {
		t.Errorf("expected yaml in output flag completions, got %q", output)
	}
	if !strings.Contains(output, "json") {
		t.Errorf("expected json in output flag completions, got %q", output)
	}
	if !strings.Contains(output, ":4") {
		t.Errorf("expected ShellCompDirectiveNoFileComp (:4) in output, got %q", output)
	}
}

func TestFlagCompletionCredentialType(t *testing.T) {
	root := NewRootCommand()

	root.SetArgs([]string{"__complete", "run", "--credential-type", ""})
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
	if !strings.Contains(output, ":4") {
		t.Errorf("expected ShellCompDirectiveNoFileComp (:4) in output, got %q", output)
	}
}

func findSubcommand(t *testing.T, root *cobra.Command, path []string) *cobra.Command {
	t.Helper()
	cmd := root
	for _, name := range path {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				cmd = sub
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("subcommand %q not found under %q", name, cmd.Name())
		}
	}
	return cmd
}
