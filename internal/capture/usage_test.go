package capture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseUsage(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		content   string
		want      map[string]string
	}{
		{
			name:      "claude-code result",
			agentType: "claude-code",
			content: `{"type":"assistant","message":"thinking..."}
{"type":"result","total_cost_usd":0.0532,"usage":{"input_tokens":15230,"output_tokens":4821}}
`,
			want: map[string]string{
				"cost-usd":      "0.0532",
				"input-tokens":  "15230",
				"output-tokens": "4821",
			},
		},
		{
			name:      "claude-code uses last result",
			agentType: "claude-code",
			content: `{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":100,"output_tokens":50}}
{"type":"result","total_cost_usd":0.05,"usage":{"input_tokens":200,"output_tokens":100}}
`,
			want: map[string]string{
				"cost-usd":      "0.05",
				"input-tokens":  "200",
				"output-tokens": "100",
			},
		},
		{
			name:      "codex sums turns",
			agentType: "codex",
			content: `{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":200,"output_tokens":150}}
`,
			want: map[string]string{
				"input-tokens":  "300",
				"output-tokens": "200",
			},
		},
		{
			name:      "codex no turns returns nil",
			agentType: "codex",
			content:   `{"type":"something_else"}` + "\n",
			want:      nil,
		},
		{
			name:      "gemini result with stats",
			agentType: "gemini",
			content: `{"type":"progress","message":"working..."}
{"type":"result","stats":{"inputTokens":8000,"outputTokens":3200}}
`,
			want: map[string]string{
				"input-tokens":  "8000",
				"output-tokens": "3200",
			},
		},
		{
			name:      "opencode sums steps",
			agentType: "opencode",
			content: `{"type":"step_start"}
{"type":"step_finish","part":{"tokens":{"input":500,"output":200}}}
{"type":"step_start"}
{"type":"step_finish","part":{"tokens":{"input":300,"output":100}}}
`,
			want: map[string]string{
				"input-tokens":  "800",
				"output-tokens": "300",
			},
		},
		{
			name:      "unknown agent type returns nil",
			agentType: "unknown-agent",
			content:   `{"type":"result"}` + "\n",
			want:      nil,
		},
		{
			name:      "empty agent type returns nil",
			agentType: "",
			content:   `{"type":"result"}` + "\n",
			want:      nil,
		},
		{
			name:      "empty file returns nil",
			agentType: "claude-code",
			content:   "",
			want:      nil,
		},
		{
			name:      "malformed JSON lines are skipped",
			agentType: "claude-code",
			content: `not json
{"type":"result","total_cost_usd":0.1,"usage":{"input_tokens":500,"output_tokens":200}}
`,
			want: map[string]string{
				"cost-usd":      "0.1",
				"input-tokens":  "500",
				"output-tokens": "200",
			},
		},
		{
			name:      "claude-code no result lines returns nil",
			agentType: "claude-code",
			content:   `{"type":"assistant","message":"done"}` + "\n",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, tt.content)
			got := ParseUsage(tt.agentType, path)
			assertMapEqual(t, tt.want, got)
		})
	}
}

func TestParseUsageMissingFile(t *testing.T) {
	got := ParseUsage("claude-code", "/nonexistent/path/file.jsonl")
	if got != nil {
		t.Errorf("expected nil for missing file, got %v", got)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-output.jsonl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertMapEqual(t *testing.T, want, got map[string]string) {
	t.Helper()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if len(want) != len(got) {
		t.Errorf("map length mismatch: want %d, got %d\n  want: %v\n  got:  %v", len(want), len(got), want, got)
		return
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("missing key %q: want %q", k, wv)
			continue
		}
		if gv != wv {
			t.Errorf("key %q: want %q, got %q", k, wv, gv)
		}
	}
}
