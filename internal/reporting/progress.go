package reporting

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

// maxProgressLen is the maximum number of runes for a progress message.
const maxProgressLen = 500

// ProgressReader reads the latest assistant text from a running task's pod logs.
type ProgressReader interface {
	ReadProgress(ctx context.Context, namespace, podName, container, agentType string) string
}

// DefaultProgressReader reads progress from Kubernetes pod logs.
type DefaultProgressReader struct {
	Clientset kubernetes.Interface
}

// ReadProgress reads the tail of a running pod's logs and extracts the latest
// assistant text. Returns empty string on any error (best-effort).
func (r *DefaultProgressReader) ReadProgress(ctx context.Context, namespace, podName, container, agentType string) string {
	log := ctrl.Log.WithName("progress-reader")

	var tailLines int64 = 300
	stream, err := r.Clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
	}).Stream(ctx)
	if err != nil {
		log.V(1).Info("Unable to read pod logs for progress", "pod", podName, "error", err)
		return ""
	}
	defer stream.Close()

	return ExtractLatestAssistantText(stream, agentType)
}

// ExtractLatestAssistantText parses NDJSON log lines and returns the text from
// the most recent assistant turn. Returns empty string if no text is found.
func ExtractLatestAssistantText(r io.Reader, agentType string) string {
	switch agentType {
	case "codex":
		return extractCodexText(r)
	case "gemini":
		return extractGeminiText(r)
	case "opencode":
		return extractOpenCodeText(r)
	case "claude-code", "cursor", "":
		return extractClaudeText(r)
	default:
		return ""
	}
}

// extractClaudeText parses Claude Code NDJSON and returns text from the last
// assistant turn.
func extractClaudeText(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastText string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event claudeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		if event.Type != "assistant" || event.Message == nil {
			continue
		}

		// Collect all text blocks from this turn
		var parts []string
		for _, block := range event.Message.Content {
			if block.Type == "text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			lastText = strings.Join(parts, "\n")
		}
	}

	return truncate(lastText)
}

// extractCodexText parses Codex NDJSON and returns text from the last agent message.
func extractCodexText(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastText string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event codexProgressEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		if event.Type == "item.completed" && event.Item != nil &&
			event.Item.Type == "agent_message" && event.Item.Text != "" {
			lastText = event.Item.Text
		}
	}

	return truncate(lastText)
}

// extractGeminiText parses Gemini NDJSON and returns text from the last
// assistant message.
func extractGeminiText(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastText string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event geminiProgressEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		if event.Type == "message" && event.Role == "assistant" &&
			!event.Delta && event.Content != "" {
			lastText = event.Content
		}
	}

	return truncate(lastText)
}

// extractOpenCodeText parses OpenCode NDJSON and returns text from the last
// text event.
func extractOpenCodeText(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastText string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event openCodeProgressEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		if event.Type == "text" && event.Text != "" {
			lastText = event.Text
		}
	}

	return truncate(lastText)
}

// truncate trims text to maxProgressLen runes, appending "..." if truncated.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxProgressLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxProgressLen]) + "..."
}

// Minimal NDJSON structs — local copies to avoid cross-package dependency on
// internal/cli.

type claudeEvent struct {
	Type    string              `json:"type"`
	Message *claudeMessageBlock `json:"message,omitempty"`
}

type claudeMessageBlock struct {
	Content []claudeContentBlock `json:"content,omitempty"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type codexProgressEvent struct {
	Type string          `json:"type"`
	Item *codexItemBlock `json:"item,omitempty"`
}

type codexItemBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type geminiProgressEvent struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	Delta   bool   `json:"delta,omitempty"`
}

type openCodeProgressEvent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
