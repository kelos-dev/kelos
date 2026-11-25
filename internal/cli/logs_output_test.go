package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestLogOutputFlagsResolve(t *testing.T) {
	notTerminal, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer notTerminal.Close()

	tests := []struct {
		name       string
		flags      logOutputFlags
		wantPretty bool
	}{
		{name: "default plain"},
		{name: "pretty on nonterminal", flags: logOutputFlags{pretty: true}, wantPretty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.flags.resolve(notTerminal)
			if out.pretty != tt.wantPretty {
				t.Errorf("pretty = %v, want %v", out.pretty, tt.wantPretty)
			}
			if out.color {
				t.Error("color = true for nonterminal output")
			}
		})
	}
}

func TestLogOutputValidatesPrettyAgent(t *testing.T) {
	for _, agentType := range []string{agentTypeCodex, agentTypeGemini, agentTypeOpenCode, "cursor", ""} {
		err := (logOutput{pretty: true}).validate("my-task", agentType)
		if err == nil || !strings.Contains(err.Error(), "my-task") || !strings.Contains(err.Error(), "Claude Code") {
			t.Errorf("agent %q: got %v, want named-task unsupported-agent error", agentType, err)
		}
	}
	for _, out := range []logOutput{{}, {pretty: true}} {
		if err := out.validate("my-task", agentTypeClaudeCode); err != nil {
			t.Errorf("Claude Code: %v", err)
		}
	}
}

func TestColorDisabledByEnv(t *testing.T) {
	tests := []struct {
		name    string
		noColor string
		term    string
		want    bool
	}{
		{name: "color allowed", term: "xterm-256color"},
		{name: "empty NO_COLOR keeps color", noColor: "", term: "xterm-256color"},
		{name: "non-empty NO_COLOR disables color", noColor: "1", term: "xterm-256color", want: true},
		{name: "dumb terminal disables color", term: "dumb", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.noColor)
			t.Setenv("TERM", tt.term)
			if got := colorDisabledByEnv(); got != tt.want {
				t.Errorf("colorDisabledByEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLogOutputFlagsRegisterPretty(t *testing.T) {
	var flags logOutputFlags
	cmd := &cobra.Command{}
	flags.register(cmd)
	if err := cmd.Flags().Parse([]string{"--pretty"}); err != nil {
		t.Fatal(err)
	}
	if !flags.pretty {
		t.Errorf("flags = %+v, want pretty enabled", flags)
	}
	if cmd.Flags().Lookup("verbose") != nil {
		t.Error("--verbose flag is still registered")
	}
	if cmd.Flags().ShorthandLookup("v") != nil {
		t.Error("-v shorthand is still registered")
	}
	if cmd.Flags().Lookup("color") != nil {
		t.Error("obsolete --color flag is still registered")
	}
	if cmd.Flags().Lookup("raw") != nil {
		t.Error("--raw flag is still registered")
	}
}
