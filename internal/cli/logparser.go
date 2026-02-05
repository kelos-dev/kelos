package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// StreamEvent represents a single NDJSON event from claude-code --output-format stream-json.
type StreamEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype,omitempty"`
	Model        string          `json:"model,omitempty"`
	Message      *MessagePayload `json:"message,omitempty"`
	Result       string          `json:"result,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	NumTurns     int             `json:"num_turns,omitempty"`
	TotalCostUSD float64         `json:"total_cost_usd,omitempty"`
}

// MessagePayload is the message field within assistant events.
type MessagePayload struct {
	Content []ContentBlock `json:"content,omitempty"`
}

// ContentBlock represents a single content block (text or tool_use).
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
}

// ParseAndFormatLogs reads NDJSON lines from r and writes formatted output:
// assistant text goes to stdout, status/tool info goes to stderr.
// Non-JSON lines are passed through to stdout as-is.
func ParseAndFormatLogs(r io.Reader, stdout, stderr io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	turnCount := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event StreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			fmt.Fprintf(stdout, "%s\n", line)
			continue
		}

		switch event.Type {
		case "system":
			if event.Subtype == "init" && event.Model != "" {
				fmt.Fprintf(stderr, "[init] model=%s\n", event.Model)
			}
		case "assistant":
			turnCount++
			fmt.Fprintf(stderr, "\n--- Turn %d ---\n", turnCount)
			if event.Message != nil {
				for _, block := range event.Message.Content {
					switch block.Type {
					case "text":
						if block.Text != "" {
							fmt.Fprintf(stdout, "%s\n", block.Text)
						}
					case "tool_use":
						fmt.Fprintf(stderr, "[tool] %s\n", block.Name)
					}
				}
			}
		case "result":
			fmt.Fprintf(stderr, "\n[result] ")
			if event.IsError {
				fmt.Fprintf(stderr, "error")
			} else {
				fmt.Fprintf(stderr, "completed")
			}
			fmt.Fprintf(stderr, " (%d turns, $%.4f)\n", event.NumTurns, event.TotalCostUSD)
			if event.IsError && event.Result != "" {
				fmt.Fprintf(stdout, "%s\n", event.Result)
			}
		}
	}

	return scanner.Err()
}
