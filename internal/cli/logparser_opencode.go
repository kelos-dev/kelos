package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// OpenCodeEvent represents a single NDJSON event from opencode run --format json.
type OpenCodeEvent struct {
	Type      string        `json:"type"`
	Text      string        `json:"text,omitempty"`
	SessionID string        `json:"sessionID,omitempty"`
	Part      *openCodePart `json:"part,omitempty"`
}

// openCodePart represents the nested part field in an OpenCode event.
type openCodePart struct {
	Type   string             `json:"type,omitempty"`
	Text   string             `json:"text,omitempty"`
	Tool   string             `json:"tool,omitempty"`
	State  *openCodeToolState `json:"state,omitempty"`
	Tokens *openCodeTokens    `json:"tokens,omitempty"`
}

type openCodeToolState struct {
	Status   string                `json:"status,omitempty"`
	Input    json.RawMessage       `json:"input,omitempty"`
	Metadata *openCodeToolMetadata `json:"metadata,omitempty"`
}

type openCodeToolMetadata struct {
	Exit      *int  `json:"exit,omitempty"`
	Truncated *bool `json:"truncated,omitempty"`
}

// openCodeTokens holds token usage from step_finish events.
type openCodeTokens struct {
	Total     int                `json:"total,omitempty"`
	Input     int                `json:"input,omitempty"`
	Output    int                `json:"output,omitempty"`
	Reasoning int                `json:"reasoning,omitempty"`
	Cache     openCodeTokenCache `json:"cache,omitempty"`
}

type openCodeTokenCache struct {
	Read  int `json:"read,omitempty"`
	Write int `json:"write,omitempty"`
}

// ParseAndFormatOpenCodeLogs reads NDJSON lines from opencode run --format json
// and writes formatted output: text content goes to stdout, status info goes
// to stderr. Non-JSON lines are passed through to stdout as-is.
func ParseAndFormatOpenCodeLogs(r io.Reader, stdout, stderr io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	stepCount := 0

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
			} else if event.Part != nil && event.Part.Text != "" {
				fmt.Fprint(stdout, event.Part.Text)
			}
		case "step_start":
			stepCount++
			fmt.Fprintf(stderr, "\n--- Step %d ---\n", stepCount)
		case "tool_use":
			if event.Part != nil && event.Part.Type == "tool" {
				openCodeToolSummary(event.Part, stderr)
			}
		case "step_finish":
			if event.Part != nil && event.Part.Tokens != nil {
				fmt.Fprintln(stderr, openCodeUsageSummary(event.Part.Tokens))
			} else {
				fmt.Fprintf(stderr, "[result] completed\n")
			}
		}
	}

	return scanner.Err()
}

func openCodeToolSummary(part *openCodePart, stderr io.Writer) {
	tool := part.Tool
	if tool == "" {
		tool = "tool"
	}
	summary := ""
	if part.State != nil {
		summary = openCodeToolInputSummary(tool, part.State.Input)
	}
	if summary != "" {
		fmt.Fprintf(stderr, "[tool] %s: %s", tool, summary)
	} else {
		fmt.Fprintf(stderr, "[tool] %s", tool)
	}
	if part.State != nil {
		if status := openCodeToolStatus(part.State); status != "" {
			fmt.Fprintf(stderr, " (%s)", status)
		}
	}
	fmt.Fprintln(stderr)
}

func openCodeToolStatus(state *openCodeToolState) string {
	var fields []string
	if state.Status != "" && state.Status != "completed" {
		fields = append(fields, "status="+state.Status)
	}
	if state.Metadata != nil {
		if state.Metadata.Exit != nil && *state.Metadata.Exit != 0 {
			fields = append(fields, fmt.Sprintf("exit=%d", *state.Metadata.Exit))
		}
		if state.Metadata.Truncated != nil && *state.Metadata.Truncated {
			fields = append(fields, "truncated")
		}
	}
	return strings.Join(fields, ", ")
}

func openCodeToolInputSummary(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}

	var summary string
	switch strings.ToLower(toolName) {
	case "bash", "shell":
		summary = stringField(input, "command")
	case "read", "write", "edit":
		summary = firstStringField(input, "filePath", "file_path", "path")
	case "grep", "glob":
		summary = stringField(input, "pattern")
	case "webfetch":
		summary = stringField(input, "url")
	case "websearch":
		summary = stringField(input, "query")
	case "task":
		summary = stringField(input, "description")
	}

	if summary == "" {
		return ""
	}
	summary = strings.ReplaceAll(summary, "\n", "\\n")
	if utf8.RuneCountInString(summary) > maxSummaryLen {
		runes := []rune(summary)
		summary = string(runes[:maxSummaryLen]) + "..."
	}
	return summary
}

func firstStringField(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringField(m, key); value != "" {
			return value
		}
	}
	return ""
}

func openCodeUsageSummary(tokens *openCodeTokens) string {
	fields := []string{
		fmt.Sprintf("input=%d", tokens.Input),
		fmt.Sprintf("cached=%d", tokens.Cache.Read),
		fmt.Sprintf("output=%d", tokens.Output),
	}
	if tokens.Reasoning != 0 {
		fields = append(fields, fmt.Sprintf("reasoning=%d", tokens.Reasoning))
	}
	if tokens.Cache.Write != 0 {
		fields = append(fields, fmt.Sprintf("cache_write=%d", tokens.Cache.Write))
	}
	if tokens.Total != 0 {
		fields = append(fields, fmt.Sprintf("total=%d", tokens.Total))
	}
	return "[usage] " + strings.Join(fields, " ")
}
