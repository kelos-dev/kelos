package cli

import (
	"testing"
)

func TestDetailFlagRegistered(t *testing.T) {
	root := NewRootCommand()

	tests := []struct {
		name string
		path []string
	}{
		{"get task", []string{"get", "task"}},
		{"get taskspawner", []string{"get", "taskspawner"}},
		{"get workspace", []string{"get", "workspace"}},
		{"get agentconfig", []string{"get", "agentconfig"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := findSubcommand(t, root, tt.path)
			f := cmd.Flags().Lookup("detail")
			if f == nil {
				t.Fatalf("expected --detail flag on %q", tt.name)
			}
			if f.Shorthand != "d" {
				t.Errorf("expected shorthand -d, got %q", f.Shorthand)
			}
			if f.DefValue != "false" {
				t.Errorf("expected default value false, got %q", f.DefValue)
			}
		})
	}
}
