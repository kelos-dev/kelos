package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndFormatLogs(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "system init event",
			input:      `{"type":"system","subtype":"init","model":"claude-sonnet-4-20250514"}`,
			wantStdout: "",
			wantStderr: "[init] model=claude-sonnet-4-20250514\n",
		},
		{
			name:       "assistant text",
			input:      `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`,
			wantStdout: "Hello world\n",
			wantStderr: "\n--- Turn 1 ---\n",
		},
		{
			name:       "assistant tool_use",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] Read\n",
		},
		{
			name:       "assistant text and tool_use",
			input:      `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check."},{"type":"tool_use","name":"Edit"}]}}`,
			wantStdout: "Let me check.\n",
			wantStderr: "\n--- Turn 1 ---\n[tool] Edit\n",
		},
		{
			name:       "result success",
			input:      `{"type":"result","result":"done","is_error":false,"num_turns":3,"total_cost_usd":0.0142}`,
			wantStdout: "",
			wantStderr: "\n[result] completed (3 turns, $0.0142)\n",
		},
		{
			name:       "result error",
			input:      `{"type":"result","result":"something went wrong","is_error":true,"num_turns":1,"total_cost_usd":0.001}`,
			wantStdout: "something went wrong\n",
			wantStderr: "\n[result] error (1 turns, $0.0010)\n",
		},
		{
			name:       "non-JSON line passes through",
			input:      "this is plain text",
			wantStdout: "this is plain text\n",
			wantStderr: "",
		},
		{
			name:       "unknown type ignored",
			input:      `{"type":"user","message":{"content":[{"type":"tool_result"}]}}`,
			wantStdout: "",
			wantStderr: "",
		},
		{
			name: "mixed events sequence",
			input: strings.Join([]string{
				`{"type":"system","subtype":"init","model":"claude-sonnet-4-20250514"}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix the bug."},{"type":"tool_use","name":"Read"}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result"}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"Done."},{"type":"tool_use","name":"Edit"}]}}`,
				`{"type":"result","result":"done","is_error":false,"num_turns":2,"total_cost_usd":0.05}`,
			}, "\n"),
			wantStdout: "I'll fix the bug.\nDone.\n",
			wantStderr: "[init] model=claude-sonnet-4-20250514\n\n--- Turn 1 ---\n[tool] Read\n\n--- Turn 2 ---\n[tool] Edit\n\n[result] completed (2 turns, $0.0500)\n",
		},
		{
			name:       "empty lines skipped",
			input:      "\n\n" + `{"type":"system","subtype":"init","model":"test"}` + "\n\n",
			wantStdout: "",
			wantStderr: "[init] model=test\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ParseAndFormatLogs(strings.NewReader(tt.input), &stdout, &stderr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout:\n got: %q\nwant: %q", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("stderr:\n got: %q\nwant: %q", got, tt.wantStderr)
			}
		})
	}
}
