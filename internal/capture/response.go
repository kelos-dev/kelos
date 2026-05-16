package capture

import (
	"bufio"
	"os"
	"strings"
)

// ParseResponse extracts the agent's final response text from the agent
// output file. The returned string is the user-visible answer (or
// concatenation of answers across turns) and is intended for reporters
// that surface task results back to the originating channel (Slack thread,
// GitHub PR comment, etc.).
//
// Returns an empty string if the file is unreadable, the agent type is
// unknown, or the agent produced no extractable response text.
func ParseResponse(agentType, filePath string) string {
	if agentType == "" {
		return ""
	}
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if len(lines) == 0 {
		return ""
	}

	switch agentType {
	case "claude-code":
		return parseClaudeCodeResponse(lines)
	case "codex":
		return parseCodexResponse(lines)
	case "gemini":
		return parseGeminiResponse(lines)
	case "opencode":
		return parseOpencodeResponse(lines)
	case "cursor":
		return parseCursorResponse(lines)
	default:
		return ""
	}
}

// parseCodexResponse concatenates the text of every
// {"type":"item.completed","item":{"type":"agent_message","text":"..."}}
// event, in order. Codex may emit several agent_message items across turns
// or alongside tool use, so joining them preserves the full visible answer.
func parseCodexResponse(lines [][]byte) string {
	var parts []string
	for _, line := range lines {
		m := parseLine(line)
		if m == nil || m["type"] != "item.completed" {
			continue
		}
		item, ok := m["item"].(map[string]any)
		if !ok || item["type"] != "agent_message" {
			continue
		}
		if text, ok := item["text"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// parseClaudeCodeResponse extracts the assistant message text. Claude
// Code's stream-json format emits text in either:
//   - {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}}
//     (one per turn during the run), or
//   - {"type":"result","result":"..."} (a final summary line).
//
// The final summary is preferred when present; otherwise we fall back to
// the concatenated text blocks across all assistant turns.
func parseClaudeCodeResponse(lines [][]byte) string {
	if last := findLastByType(lines, "result"); last != nil {
		if result, ok := last["result"].(string); ok && result != "" {
			return result
		}
	}
	var parts []string
	for _, line := range lines {
		m := parseLine(line)
		if m == nil || m["type"] != "assistant" {
			continue
		}
		msg, ok := m["message"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "text" {
				continue
			}
			if text, ok := b["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// parseGeminiResponse concatenates the text of every
// {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}}
// event. Gemini follows the same assistant-content shape as claude-code
// for visible output (the final {"type":"result"} line carries only stats).
func parseGeminiResponse(lines [][]byte) string {
	var parts []string
	for _, line := range lines {
		m := parseLine(line)
		if m == nil {
			continue
		}
		// Streamed text events: {"type":"text","text":"..."}
		if m["type"] == "text" {
			if text, ok := m["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
			continue
		}
		// Structured assistant events with content blocks.
		if m["type"] != "assistant" {
			continue
		}
		msg, ok := m["message"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "text" {
				continue
			}
			if text, ok := b["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "")
}

// parseOpencodeResponse extracts text from {"type":"text","text":"..."}
// events, falling back to {"type":"step_finish","part":{"text":"..."}}
// when the streamed text events are absent. Opencode emits raw text
// fragments without an outer message envelope.
func parseOpencodeResponse(lines [][]byte) string {
	var parts []string
	for _, line := range lines {
		m := parseLine(line)
		if m == nil {
			continue
		}
		switch m["type"] {
		case "text":
			if text, ok := m["text"].(string); ok && text != "" {
				parts = append(parts, text)
				continue
			}
			if part, ok := m["part"].(map[string]any); ok {
				if text, ok := part["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		case "step_finish":
			if part, ok := m["part"].(map[string]any); ok {
				if text, ok := part["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

// parseCursorResponse extracts text from cursor's stream-json output. The
// shape mirrors claude-code: assistant events carry the visible text and
// the trailing result event carries usage stats.
func parseCursorResponse(lines [][]byte) string {
	return parseClaudeCodeResponse(lines)
}

