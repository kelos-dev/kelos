package reporting

import (
	"strings"
	"testing"
)

func TestExtractLatestAssistantText_ClaudeCode(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-4-20250514"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the configuration files..."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Found the issue in the auth middleware."}]}}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	want := "Found the issue in the auth middleware."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_ClaudeCodeMultipleTextBlocks(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"First part."},{"type":"tool_use","name":"Read"},{"type":"text","text":"Second part."}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	want := "First part.\nSecond part."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_ClaudeCodeIgnoresToolUseOnly(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for tool-use-only turn, got %q", got)
	}
}

func TestExtractLatestAssistantText_EmptyLogs(t *testing.T) {
	got := ExtractLatestAssistantText(strings.NewReader(""), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for empty logs, got %q", got)
	}
}

func TestExtractLatestAssistantText_NonJSON(t *testing.T) {
	logs := "not json at all\nanother line\n"

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for non-JSON logs, got %q", got)
	}
}

func TestExtractLatestAssistantText_Truncation(t *testing.T) {
	longText := strings.Repeat("a", 600)
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + longText + `"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	wantLen := maxProgressLen + len("...")
	if len([]rune(got)) != wantLen {
		t.Errorf("expected truncated length %d, got %d", wantLen, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected truncated text to end with ...")
	}
}

func TestExtractLatestAssistantText_NoTruncationAtLimit(t *testing.T) {
	text := strings.Repeat("b", maxProgressLen)
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != text {
		t.Errorf("text at exactly maxProgressLen should not be truncated")
	}
}

func TestExtractLatestAssistantText_UnknownAgent(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "unknown-agent")
	if got != "" {
		t.Errorf("expected empty string for unknown agent, got %q", got)
	}
}

func TestExtractLatestAssistantText_EmptyAgentType(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "")
	if got != "hello" {
		t.Errorf("empty agent type should default to claude-code, got %q", got)
	}
}

func TestExtractLatestAssistantText_Codex(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Checking the release tags..."}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Found the merge commit."}}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "codex")
	want := "Found the merge commit."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_CodexIgnoresNonAgentMessage(t *testing.T) {
	logs := `{"type":"item.completed","item":{"type":"command_execution","text":"ls -la"}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "codex")
	if got != "" {
		t.Errorf("expected empty string for non-agent-message item, got %q", got)
	}
}

func TestExtractLatestAssistantText_Gemini(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"init","model":"gemini-2.5-pro"}`,
		`{"type":"message","role":"assistant","content":"Searching for config files...","delta":false}`,
		`{"type":"message","role":"assistant","content":"Found the issue.","delta":false}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "gemini")
	want := "Found the issue."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_GeminiIgnoresDelta(t *testing.T) {
	logs := `{"type":"message","role":"assistant","content":"partial...","delta":true}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "gemini")
	if got != "" {
		t.Errorf("expected empty string for delta message, got %q", got)
	}
}

func TestExtractLatestAssistantText_OpenCode(t *testing.T) {
	logs := strings.Join([]string{
		`{"type":"text","text":"Analyzing the codebase..."}`,
		`{"type":"step_finish"}`,
		`{"type":"text","text":"Identified the root cause."}`,
	}, "\n")

	got := ExtractLatestAssistantText(strings.NewReader(logs), "opencode")
	want := "Identified the root cause."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractLatestAssistantText_WhitespaceOnly(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"   \n  "}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "claude-code")
	if got != "" {
		t.Errorf("expected empty string for whitespace-only text, got %q", got)
	}
}

func TestExtractLatestAssistantText_CursorFallsBackToClaude(t *testing.T) {
	logs := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello from cursor"}]}}`

	got := ExtractLatestAssistantText(strings.NewReader(logs), "cursor")
	if got != "hello from cursor" {
		t.Errorf("cursor should use claude parser, got %q", got)
	}
}
