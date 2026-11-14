package agentlog

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndFormatGeminiLogs(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "init event with model",
			input:      `{"type":"init","model":"gemini-2.5-pro"}`,
			wantStdout: "",
			wantStderr: "[init] model=gemini-2.5-pro\n",
		},
		{
			name:       "assistant message non-delta",
			input:      `{"type":"message","role":"assistant","content":"Hello world"}`,
			wantStdout: "Hello world\n",
			wantStderr: "\n--- Turn 1 ---\n",
		},
		{
			name:       "assistant message delta",
			input:      `{"type":"message","role":"assistant","content":"continued text","delta":true}`,
			wantStdout: "continued text\n",
			wantStderr: "",
		},
		{
			name:       "user message ignored",
			input:      `{"type":"message","role":"user","content":"hello"}`,
			wantStdout: "",
			wantStderr: "",
		},
		{
			name:       "tool use with Bash command",
			input:      `{"type":"tool_use","tool_name":"Bash","tool_id":"bash-1","parameters":{"command":"ls -la"}}`,
			wantStdout: "",
			wantStderr: "[tool] Bash: ls -la\n",
		},
		{
			name:       "tool use with no parameters",
			input:      `{"type":"tool_use","tool_name":"ReadFile","tool_id":"rf-1"}`,
			wantStdout: "",
			wantStderr: "[tool] ReadFile\n",
		},
		{
			name:       "tool result event not displayed",
			input:      `{"type":"tool_result","tool_id":"bash-1","status":"success","output":"file1.txt"}`,
			wantStdout: "",
			wantStderr: "",
		},
		{
			name:       "result success with stats",
			input:      `{"type":"result","status":"success","stats":{"totalInputTokens":1000,"totalOutputTokens":200}}`,
			wantStdout: "",
			wantStderr: "\n[result] completed (input=1000, output=200)\n",
		},
		{
			name:       "result error",
			input:      `{"type":"result","status":"error"}`,
			wantStdout: "",
			wantStderr: "\n[result] error\n",
		},
		{
			name:       "non-JSON line passes through",
			input:      "this is plain text",
			wantStdout: "this is plain text\n",
			wantStderr: "",
		},
		{
			name:       "empty lines skipped",
			input:      "\n\n" + `{"type":"init","model":"gemini-2.5-pro"}` + "\n\n",
			wantStdout: "",
			wantStderr: "[init] model=gemini-2.5-pro\n",
		},
		{
			name: "full sequence",
			input: strings.Join([]string{
				`{"type":"init","model":"gemini-2.5-pro","session_id":"abc123"}`,
				`{"type":"message","role":"user","content":"List files"}`,
				`{"type":"tool_use","tool_name":"Bash","tool_id":"bash-1","parameters":{"command":"ls -la"}}`,
				`{"type":"tool_result","tool_id":"bash-1","status":"success","output":"file1.txt\nfile2.txt"}`,
				`{"type":"message","role":"assistant","content":"Here are the files in your directory."}`,
				`{"type":"result","status":"success","stats":{"totalInputTokens":500,"totalOutputTokens":50}}`,
			}, "\n"),
			wantStdout: "Here are the files in your directory.\n",
			wantStderr: "[init] model=gemini-2.5-pro\n[tool] Bash: ls -la\n\n--- Turn 1 ---\n\n[result] completed (input=500, output=50)\n",
		},
		{
			name: "multiple turns",
			input: strings.Join([]string{
				`{"type":"message","role":"assistant","content":"Analyzing code."}`,
				`{"type":"tool_use","tool_name":"Shell","tool_id":"sh-1","parameters":{"command":"go test ./..."}}`,
				`{"type":"tool_result","tool_id":"sh-1","status":"success","output":"ok"}`,
				`{"type":"message","role":"assistant","content":"All tests pass."}`,
				`{"type":"result","status":"success","stats":{"totalInputTokens":200,"totalOutputTokens":30}}`,
			}, "\n"),
			wantStdout: "Analyzing code.\nAll tests pass.\n",
			wantStderr: "\n--- Turn 1 ---\n[tool] Shell: go test ./...\n\n--- Turn 2 ---\n\n[result] completed (input=200, output=30)\n",
		},
		{
			name:       "tool use with ReadFile and path parameter",
			input:      `{"type":"tool_use","tool_name":"ReadFile","tool_id":"rf-1","parameters":{"path":"/workspace/repo/main.go"}}`,
			wantStdout: "",
			wantStderr: "[tool] ReadFile: /workspace/repo/main.go\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ParseAndFormatGeminiLogs(strings.NewReader(tt.input), &stdout, &stderr)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
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
