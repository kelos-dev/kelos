package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
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
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
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
						if summary := toolInputSummary(block.Name, block.Input); summary != "" {
							fmt.Fprintf(stderr, "[tool] %s: %s\n", block.Name, summary)
						} else {
							fmt.Fprintf(stderr, "[tool] %s\n", block.Name)
						}
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

// maxSummaryLen is the maximum number of runes for a tool input summary line.
const maxSummaryLen = 120

// toolInputSummary extracts a concise summary string from tool input parameters.
// Returns an empty string if the input is empty or cannot be parsed.
func toolInputSummary(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}

	var summary string
	switch toolName {
	case "Read", "Write", "Edit":
		summary = stringField(input, "file_path")
	case "Bash":
		summary = stringField(input, "command")
	case "Glob", "Grep":
		summary = stringField(input, "pattern")
	case "WebFetch":
		summary = stringField(input, "url")
	case "Task":
		summary = stringField(input, "description")
	case "WebSearch":
		summary = stringField(input, "query")
	}

	if summary == "" {
		return ""
	}

	// Collapse newlines for display
	summary = strings.ReplaceAll(summary, "\n", "\\n")

	if utf8.RuneCountInString(summary) > maxSummaryLen {
		runes := []rune(summary)
		summary = string(runes[:maxSummaryLen]) + "..."
	}

	return summary
}

// stringField returns the value of a string field from a map, or empty string if not found.
func stringField(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
