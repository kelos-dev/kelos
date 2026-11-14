package agentlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// GeminiEvent represents a single NDJSON event from gemini --output-format stream-json.
type GeminiEvent struct {
	Type       string          `json:"type"`
	Model      string          `json:"model,omitempty"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	Delta      bool            `json:"delta,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolID     string          `json:"tool_id,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Status     string          `json:"status,omitempty"`
	Output     string          `json:"output,omitempty"`
	Stats      *GeminiStats    `json:"stats,omitempty"`
}

// GeminiStats holds session statistics from the result event.
type GeminiStats struct {
	TotalInputTokens  int `json:"totalInputTokens,omitempty"`
	TotalOutputTokens int `json:"totalOutputTokens,omitempty"`
	TotalCalls        int `json:"totalCalls,omitempty"`
}

// ParseAndFormatGeminiLogs reads NDJSON lines from gemini --output-format stream-json
// and writes formatted output: assistant messages go to stdout, status/tool info
// goes to stderr. Non-JSON lines are passed through to stdout as-is.
func ParseAndFormatGeminiLogs(r io.Reader, stdout, stderr io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	turnCount := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event GeminiEvent
		if err := json.Unmarshal(line, &event); err != nil {
			fmt.Fprintf(stdout, "%s\n", line)
			continue
		}

		switch event.Type {
		case "init":
			if event.Model != "" {
				fmt.Fprintf(stderr, "[init] model=%s\n", event.Model)
			}
		case "message":
			if event.Role == "assistant" && event.Content != "" {
				if !event.Delta {
					turnCount++
					fmt.Fprintf(stderr, "\n--- Turn %d ---\n", turnCount)
				}
				fmt.Fprintf(stdout, "%s\n", event.Content)
			}
		case "tool_use":
			if summary := geminiToolSummary(event.ToolName, event.Parameters); summary != "" {
				fmt.Fprintf(stderr, "[tool] %s: %s\n", event.ToolName, summary)
			} else if event.ToolName != "" {
				fmt.Fprintf(stderr, "[tool] %s\n", event.ToolName)
			}
		case "tool_result":
			// tool_result events are not displayed by default.
		case "result":
			fmt.Fprintf(stderr, "\n[result] ")
			if event.Status == "error" {
				fmt.Fprintf(stderr, "error")
			} else {
				fmt.Fprintf(stderr, "completed")
			}
			if event.Stats != nil {
				fmt.Fprintf(stderr, " (input=%d, output=%d)", event.Stats.TotalInputTokens, event.Stats.TotalOutputTokens)
			}
			fmt.Fprintf(stderr, "\n")
		}
	}

	return scanner.Err()
}

// geminiToolSummary extracts a concise summary from gemini tool parameters.
func geminiToolSummary(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var params map[string]interface{}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}

	var summary string
	switch toolName {
	case "Bash", "Shell":
		summary = stringField(params, "command")
	case "ReadFile", "WriteFile", "EditFile":
		summary = stringField(params, "file_path")
		if summary == "" {
			summary = stringField(params, "path")
		}
	case "Glob", "Grep":
		summary = stringField(params, "pattern")
	case "WebFetch":
		summary = stringField(params, "url")
	case "WebSearch":
		summary = stringField(params, "query")
	}

	if summary == "" {
		return ""
	}

	summary = strings.ReplaceAll(summary, "\n", "\\n")

	if len([]rune(summary)) > maxSummaryLen {
		runes := []rune(summary)
		summary = string(runes[:maxSummaryLen]) + "..."
	}

	return summary
}
