package capture

import (
	"path/filepath"
	"testing"
)

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		content   string
		want      string
	}{
		{
			name:      "codex single agent_message",
			agentType: "codex",
			content: `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"2 + 2 = 4"}}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":8}}
`,
			want: "2 + 2 = 4",
		},
		{
			name:      "codex multiple agent_messages concatenated",
			agentType: "codex",
			content: `{"type":"item.completed","item":{"type":"agent_message","text":"First part."}}
{"type":"item.completed","item":{"type":"agent_message","text":"Second part."}}
`,
			want: "First part.\n\nSecond part.",
		},
		{
			name:      "codex ignores non-agent_message items",
			agentType: "codex",
			content: `{"type":"item.completed","item":{"type":"tool_use","text":"ls -la"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Here are the files."}}
`,
			want: "Here are the files.",
		},
		{
			name:      "codex no agent_message returns empty",
			agentType: "codex",
			content: `{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":50,"output_tokens":0}}
`,
			want: "",
		},
		{
			name:      "claude-code result field preferred",
			agentType: "claude-code",
			content: `{"type":"assistant","message":{"content":[{"type":"text","text":"Thinking..."}]}}
{"type":"result","result":"Final answer here.","usage":{"input_tokens":100,"output_tokens":50}}
`,
			want: "Final answer here.",
		},
		{
			name:      "claude-code falls back to assistant blocks when result has no text",
			agentType: "claude-code",
			content: `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello."}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"World."}]}}
{"type":"result","usage":{"input_tokens":100,"output_tokens":50}}
`,
			want: "Hello.\n\nWorld.",
		},
		{
			name:      "claude-code ignores non-text content blocks",
			agentType: "claude-code",
			content: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"text","text":"Done."}]}}
`,
			want: "Done.",
		},
		{
			name:      "gemini text events concatenated without separator",
			agentType: "gemini",
			content: `{"type":"text","text":"Here are "}
{"type":"text","text":"the files."}
{"type":"result","stats":{"inputTokens":100,"outputTokens":50}}
`,
			want: "Here are the files.",
		},
		{
			name:      "gemini assistant content blocks",
			agentType: "gemini",
			content: `{"type":"assistant","message":{"content":[{"type":"text","text":"Result."}]}}
`,
			want: "Result.",
		},
		{
			name:      "opencode text events",
			agentType: "opencode",
			content: `{"type":"text","text":"Step 1."}
{"type":"text","text":" Step 2."}
{"type":"step_finish","part":{"tokens":{"input":100,"output":50}}}
`,
			want: "Step 1. Step 2.",
		},
		{
			name:      "opencode step_finish part.text fallback",
			agentType: "opencode",
			content: `{"type":"step_finish","part":{"text":"Final answer.","tokens":{"input":100,"output":50}}}
`,
			want: "Final answer.",
		},
		{
			name:      "cursor uses claude-code shape",
			agentType: "cursor",
			content: `{"type":"result","result":"Cursor reply."}
`,
			want: "Cursor reply.",
		},
		{
			name:      "unknown agent returns empty",
			agentType: "unknown",
			content:   `{"type":"item.completed","item":{"type":"agent_message","text":"ignored"}}`,
			want:      "",
		},
		{
			name:      "empty agent type returns empty",
			agentType: "",
			content:   `{"type":"item.completed","item":{"type":"agent_message","text":"ignored"}}`,
			want:      "",
		},
		{
			name:      "malformed JSON lines skipped",
			agentType: "codex",
			content: `not json
{"type":"item.completed","item":{"type":"agent_message","text":"Recovered."}}
also not json
`,
			want: "Recovered.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, tc.content)
			got := ParseResponse(tc.agentType, path)
			if got != tc.want {
				t.Errorf("ParseResponse(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestParseResponseMissingFile(t *testing.T) {
	got := ParseResponse("codex", filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if got != "" {
		t.Errorf("ParseResponse on missing file = %q, want empty string", got)
	}
}

func TestParseResponseEmptyFile(t *testing.T) {
	got := ParseResponse("codex", writeTempFile(t, ""))
	if got != "" {
		t.Errorf("ParseResponse on empty file = %q, want empty string", got)
	}
}

