package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kelos-dev/kelos/internal/logview"
)

// logOutputFlags are the logs command flags that choose how agent output
// is printed.
type logOutputFlags struct {
	pretty bool
}

const (
	agentTypeClaudeCode = "claude-code"
	agentTypeCodex      = "codex"
	agentTypeGemini     = "gemini"
	agentTypeOpenCode   = "opencode"
)

func (f *logOutputFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.pretty, "pretty", false, "render Claude Code logs with tool and diff previews")
}

// logOutput is how agent output is printed once the flags are resolved.
type logOutput struct {
	pretty bool
	color  bool
}

// resolve chooses the format and whether the pretty layout uses ANSI color.
func (f *logOutputFlags) resolve(stdout *os.File) logOutput {
	return logOutput{
		pretty: f.pretty,
		color:  f.pretty && isColorTerminal(stdout),
	}
}

// isColorTerminal reports whether f is a terminal that accepts color.
func isColorTerminal(f *os.File) bool {
	return !colorDisabledByEnv() && term.IsTerminal(int(f.Fd()))
}

// colorDisabledByEnv reports whether the environment turns color off: a
// non-empty NO_COLOR (https://no-color.org) or a dumb terminal.
func colorDisabledByEnv() bool {
	return os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
}

// terminalWidth returns f's width in columns, or 0 when f is not a
// terminal.
func terminalWidth(f *os.File) int {
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}

// write prints agentType's log stream to stdout, and to stderr for the
// status lines of the plain format.
func (o logOutput) write(agentType string, stream io.Reader) error {
	if o.pretty {
		renderer := logview.NewRenderer(os.Stdout, logview.Options{Color: o.color, Width: terminalWidth(os.Stdout)})
		// Close a streamed line even when parsing stops on an error.
		defer renderer.Finish()
		return logview.Parse(stream, renderer.Render)
	}
	return parsePlainAgentLogs(agentType, stream)
}

func (o logOutput) validate(taskName, agentType string) error {
	if o.pretty && agentType != agentTypeClaudeCode {
		return fmt.Errorf("task %q: --pretty supports only Claude Code logs (agent %q)", taskName, agentType)
	}
	return nil
}

// parsePlainAgentLogs prints agent output in the plain format: assistant
// text on stdout, status and tool lines on stderr.
func parsePlainAgentLogs(agentType string, stream io.Reader) error {
	switch agentType {
	case agentTypeCodex:
		return ParseAndFormatCodexLogs(stream, os.Stdout, os.Stderr)
	case agentTypeGemini:
		return ParseAndFormatGeminiLogs(stream, os.Stdout, os.Stderr)
	case agentTypeOpenCode:
		return ParseAndFormatOpenCodeLogs(stream, os.Stdout, os.Stderr)
	default:
		return ParseAndFormatLogs(stream, os.Stdout, os.Stderr)
	}
}
