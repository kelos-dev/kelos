package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// OpenCodeEvent represents a single NDJSON event from opencode run --format json.
type OpenCodeEvent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	SessionID string          `json:"sessionID,omitempty"`
	Part      json.RawMessage `json:"part,omitempty"`
}

// openCodePart represents the nested part field in an OpenCode event.
type openCodePart struct {
	Type   string          `json:"type,omitempty"`
	Text   string          `json:"text,omitempty"`
	Tokens *openCodeTokens `json:"tokens,omitempty"`
}

// openCodeTokens holds token usage from step_finish events.
type openCodeTokens struct {
	Input  int `json:"input,omitempty"`
	Output int `json:"output,omitempty"`
}

// ParseAndFormatOpenCodeLogs reads NDJSON lines from opencode run --format json
// and writes formatted output: text content goes to stdout, status info goes
// to stderr. Non-JSON lines are passed through to stdout as-is.
func ParseAndFormatOpenCodeLogs(r io.Reader, stdout, stderr io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event OpenCodeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not valid JSON; pass through as-is.
			fmt.Fprintf(stdout, "%s\n", line)
			continue
		}

		switch event.Type {
		case "text":
			if event.Text != "" {
				fmt.Fprint(stdout, event.Text)
			} else if len(event.Part) > 0 {
				var part openCodePart
				if err := json.Unmarshal(event.Part, &part); err == nil && part.Text != "" {
					fmt.Fprint(stdout, part.Text)
				}
			}
		case "step_start":
			// Step boundaries are not displayed by default.
		case "step_finish":
			if len(event.Part) > 0 {
				var part openCodePart
				if err := json.Unmarshal(event.Part, &part); err == nil && part.Tokens != nil {
					fmt.Fprintf(stderr, "\n[result] completed (input=%d, output=%d)\n",
						part.Tokens.Input, part.Tokens.Output)
				} else {
					fmt.Fprintf(stderr, "\n[result] completed\n")
				}
			} else {
				fmt.Fprintf(stderr, "\n[result] completed\n")
			}
		}
	}

	return scanner.Err()
}
