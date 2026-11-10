package agentlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// CodexEvent represents a single NDJSON event from codex exec --json.
type CodexEvent struct {
	Type  string      `json:"type"`
	Item  *CodexItem  `json:"item,omitempty"`
	Usage *CodexUsage `json:"usage,omitempty"`
}

// CodexItem represents an item within a codex event.
type CodexItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
	Status  string `json:"status,omitempty"`
}

// CodexUsage holds token usage information from a codex turn.
type CodexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

// ParseAndFormatCodexLogs reads NDJSON lines from codex exec --json output
// and writes formatted output: agent messages go to stdout, status/tool info
// goes to stderr. Non-JSON lines are passed through to stdout as-is.
func ParseAndFormatCodexLogs(r io.Reader, stdout, stderr io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	turnCount := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event CodexEvent
		if err := json.Unmarshal(line, &event); err != nil {
			fmt.Fprintf(stdout, "%s\n", line)
			continue
		}

		switch event.Type {
		case "thread.started":
			// No-op; session has started.
		case "turn.started":
			turnCount++
			fmt.Fprintf(stderr, "\n--- Turn %d ---\n", turnCount)
		case "turn.completed":
			if event.Usage != nil {
				fmt.Fprintf(stderr, "[usage] input=%d cached=%d output=%d\n",
					event.Usage.InputTokens, event.Usage.CachedInputTokens, event.Usage.OutputTokens)
			}
		case "item.started":
			if event.Item != nil {
				codexItemSummary(event.Item, stderr)
			}
		case "item.completed":
			if event.Item != nil {
				switch event.Item.Type {
				case "agent_message":
					if event.Item.Text != "" {
						fmt.Fprintf(stdout, "%s\n", event.Item.Text)
					}
				}
			}
		case "error":
			fmt.Fprintf(stderr, "[error] %s\n", line)
		}
	}

	return scanner.Err()
}

// codexItemSummary writes a summary of a codex item to stderr.
func codexItemSummary(item *CodexItem, stderr io.Writer) {
	switch item.Type {
	case "command_execution":
		cmd := item.Command
		cmd = strings.ReplaceAll(cmd, "\n", "\\n")
		if len([]rune(cmd)) > maxSummaryLen {
			runes := []rune(cmd)
			cmd = string(runes[:maxSummaryLen]) + "..."
		}
		if cmd != "" {
			fmt.Fprintf(stderr, "[command] %s\n", cmd)
		} else {
			fmt.Fprintf(stderr, "[command]\n")
		}
	default:
		if item.Type != "" {
			fmt.Fprintf(stderr, "[%s]\n", item.Type)
		}
	}
}
