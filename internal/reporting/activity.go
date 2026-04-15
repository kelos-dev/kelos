package reporting

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

// ActivityReader reads a short activity string from a running task's pod logs.
type ActivityReader interface {
	ReadActivity(ctx context.Context, namespace, podName, container, agentType string) string
}

// DefaultActivityReader reads activity from Kubernetes pod logs.
type DefaultActivityReader struct {
	Clientset kubernetes.Interface
}

// ReadActivity reads the tail of a running pod's logs and extracts a short
// activity string describing the agent's current action. Returns empty string
// on any error (best-effort).
func (r *DefaultActivityReader) ReadActivity(ctx context.Context, namespace, podName, container, agentType string) string {
	log := ctrl.Log.WithName("activity-reader")

	var tailLines int64 = 50
	stream, err := r.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
	}).Stream(ctx)
	if err != nil {
		log.V(1).Info("Unable to read pod logs for activity", "pod", podName, "error", err)
		return ""
	}
	defer stream.Close()

	return ExtractActivity(stream, agentType)
}

// ExtractActivity parses NDJSON log lines and returns a short human-readable
// string describing the agent's most recent action. Returns empty string if
// no actionable event is found.
func ExtractActivity(r io.Reader, agentType string) string {
	switch agentType {
	case "codex":
		return extractCodexActivity(r)
	case "gemini":
		return extractGeminiActivity(r)
	case "opencode":
		return extractOpenCodeActivity(r)
	case "claude-code", "cursor", "":
		return extractClaudeActivity(r)
	default:
		return ""
	}
}

// extractClaudeActivity parses Claude Code NDJSON and returns an activity
// string from the most recent tool_use or assistant text.
func extractClaudeActivity(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastActivity string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event claudeActivityEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		if event.Type != "assistant" || event.Message == nil {
			continue
		}

		for _, block := range event.Message.Content {
			switch block.Type {
			case "tool_use":
				if s := claudeToolActivity(block.Name, block.Input); s != "" {
					lastActivity = s
				}
			case "text":
				// If the last block is text (thinking/responding), mark as thinking.
				lastActivity = "Thinking..."
			}
		}
	}

	return lastActivity
}

// claudeToolActivity returns a short activity string for a Claude tool_use block.
func claudeToolActivity(toolName string, raw json.RawMessage) string {
	var input map[string]interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			input = nil
		}
	}

	switch toolName {
	case "Read":
		if p := activityStringField(input, "file_path"); p != "" {
			return fmt.Sprintf("Reading `%s`...", shortenPath(p))
		}
		return "Reading file..."
	case "Write":
		if p := activityStringField(input, "file_path"); p != "" {
			return fmt.Sprintf("Writing `%s`...", shortenPath(p))
		}
		return "Writing file..."
	case "Edit":
		if p := activityStringField(input, "file_path"); p != "" {
			return fmt.Sprintf("Editing `%s`...", shortenPath(p))
		}
		return "Editing file..."
	case "Bash":
		if cmd := activityStringField(input, "command"); cmd != "" {
			return fmt.Sprintf("Running `%s`...", truncateActivity(cmd, 50))
		}
		return "Running command..."
	case "Glob":
		if p := activityStringField(input, "pattern"); p != "" {
			return fmt.Sprintf("Searching for `%s`...", truncateActivity(p, 50))
		}
		return "Searching files..."
	case "Grep":
		if p := activityStringField(input, "pattern"); p != "" {
			return fmt.Sprintf("Searching for `%s`...", truncateActivity(p, 50))
		}
		return "Searching code..."
	case "WebFetch":
		return "Fetching web page..."
	case "WebSearch":
		if q := activityStringField(input, "query"); q != "" {
			return fmt.Sprintf("Searching `%s`...", truncateActivity(q, 50))
		}
		return "Searching the web..."
	case "Task":
		return "Managing tasks..."
	default:
		if toolName != "" {
			return fmt.Sprintf("Using %s...", toolName)
		}
		return ""
	}
}

// extractCodexActivity parses Codex NDJSON and returns an activity string.
func extractCodexActivity(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastActivity string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event codexActivityEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "item.started":
			if event.Item != nil {
				switch event.Item.Type {
				case "command_execution":
					if event.Item.Command != "" {
						lastActivity = fmt.Sprintf("Running `%s`...", truncateActivity(event.Item.Command, 50))
					} else {
						lastActivity = "Running command..."
					}
				case "agent_message":
					lastActivity = "Thinking..."
				}
			}
		}
	}

	return lastActivity
}

// extractGeminiActivity parses Gemini NDJSON and returns an activity string.
func extractGeminiActivity(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastActivity string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event geminiActivityEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "tool_use":
			if s := geminiToolActivity(event.ToolName, event.Parameters); s != "" {
				lastActivity = s
			}
		case "message":
			if event.Role == "assistant" {
				lastActivity = "Thinking..."
			}
		}
	}

	return lastActivity
}

// geminiToolActivity returns a short activity string for a Gemini tool_use event.
func geminiToolActivity(toolName string, raw json.RawMessage) string {
	var params map[string]interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			params = nil
		}
	}

	switch toolName {
	case "Bash", "Shell":
		if cmd := activityStringField(params, "command"); cmd != "" {
			return fmt.Sprintf("Running `%s`...", truncateActivity(cmd, 50))
		}
		return "Running command..."
	case "ReadFile":
		p := activityStringField(params, "file_path")
		if p == "" {
			p = activityStringField(params, "path")
		}
		if p != "" {
			return fmt.Sprintf("Reading `%s`...", shortenPath(p))
		}
		return "Reading file..."
	case "WriteFile":
		p := activityStringField(params, "file_path")
		if p == "" {
			p = activityStringField(params, "path")
		}
		if p != "" {
			return fmt.Sprintf("Writing `%s`...", shortenPath(p))
		}
		return "Writing file..."
	case "EditFile":
		p := activityStringField(params, "file_path")
		if p == "" {
			p = activityStringField(params, "path")
		}
		if p != "" {
			return fmt.Sprintf("Editing `%s`...", shortenPath(p))
		}
		return "Editing file..."
	case "Glob", "Grep":
		if p := activityStringField(params, "pattern"); p != "" {
			return fmt.Sprintf("Searching for `%s`...", truncateActivity(p, 50))
		}
		return "Searching..."
	case "WebFetch":
		return "Fetching web page..."
	case "WebSearch":
		if q := activityStringField(params, "query"); q != "" {
			return fmt.Sprintf("Searching `%s`...", truncateActivity(q, 50))
		}
		return "Searching the web..."
	default:
		if toolName != "" {
			return fmt.Sprintf("Using %s...", toolName)
		}
		return ""
	}
}

// extractOpenCodeActivity parses OpenCode NDJSON and returns an activity string.
func extractOpenCodeActivity(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastActivity string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event openCodeActivityEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "step_start":
			lastActivity = "Working..."
		case "text":
			lastActivity = "Thinking..."
		}
	}

	return lastActivity
}

// shortenPath returns the last two path components (dir/file) for brevity.
func shortenPath(p string) string {
	p = strings.ReplaceAll(p, "\n", "")
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	parent := filepath.Base(dir)
	if parent == "." || parent == "/" {
		return base
	}
	return parent + "/" + base
}

// truncateActivity truncates a string to maxLen runes, appending "..." if needed.
func truncateActivity(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + "..."
}

// activityStringField returns a string field from a map, or empty string.
func activityStringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
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

// Minimal NDJSON structs for activity parsing.

type claudeActivityEvent struct {
	Type    string                  `json:"type"`
	Message *claudeActivityMsgBlock `json:"message,omitempty"`
}

type claudeActivityMsgBlock struct {
	Content []claudeActivityContentBlock `json:"content,omitempty"`
}

type claudeActivityContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type codexActivityEvent struct {
	Type string             `json:"type"`
	Item *codexActivityItem `json:"item,omitempty"`
}

type codexActivityItem struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
}

type geminiActivityEvent struct {
	Type       string          `json:"type"`
	Role       string          `json:"role,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type openCodeActivityEvent struct {
	Type string `json:"type"`
}
