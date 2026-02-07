package cli

import (
	"bytes"
	"encoding/json"
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
			name:       "assistant tool_use without input",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] Read\n",
		},
		{
			name:       "assistant tool_use with input",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] Read: /src/main.go\n",
		},
		{
			name:       "assistant text and tool_use with input",
			input:      `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check."},{"type":"tool_use","name":"Edit","input":{"file_path":"/src/main.go","old_string":"foo","new_string":"bar"}}]}}`,
			wantStdout: "Let me check.\n",
			wantStderr: "\n--- Turn 1 ---\n[tool] Edit: /src/main.go\n",
		},
		{
			name:       "bash tool shows command",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] Bash: go test ./...\n",
		},
		{
			name:       "glob tool shows pattern",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go"}}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] Glob: **/*.go\n",
		},
		{
			name:       "grep tool shows pattern",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"func main","path":"/src"}}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] Grep: func main\n",
		},
		{
			name:       "unknown tool shows only name",
			input:      `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"CustomTool","input":{"foo":"bar"}}]}}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n[tool] CustomTool\n",
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
			name: "mixed events sequence with tool inputs",
			input: strings.Join([]string{
				`{"type":"system","subtype":"init","model":"claude-sonnet-4-20250514"}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix the bug."},{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result"}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"Done."},{"type":"tool_use","name":"Edit","input":{"file_path":"/src/main.go","old_string":"old","new_string":"new"}}]}}`,
				`{"type":"result","result":"done","is_error":false,"num_turns":2,"total_cost_usd":0.05}`,
			}, "\n"),
			wantStdout: "I'll fix the bug.\nDone.\n",
			wantStderr: "[init] model=claude-sonnet-4-20250514\n\n--- Turn 1 ---\n[tool] Read: /src/main.go\n\n--- Turn 2 ---\n[tool] Edit: /src/main.go\n\n[result] completed (2 turns, $0.0500)\n",
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

func TestToolInputSummary(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    interface{}
		rawJSON  string // if set, used directly instead of marshaling input
		want     string
	}{
		{
			name:     "Read with file_path",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/workspace/main.go"},
			want:     "/workspace/main.go",
		},
		{
			name:     "Write with file_path",
			toolName: "Write",
			input:    map[string]interface{}{"file_path": "/workspace/out.txt", "content": "hello"},
			want:     "/workspace/out.txt",
		},
		{
			name:     "Edit with file_path",
			toolName: "Edit",
			input:    map[string]interface{}{"file_path": "/workspace/main.go", "old_string": "a", "new_string": "b"},
			want:     "/workspace/main.go",
		},
		{
			name:     "Bash with command",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "make test"},
			want:     "make test",
		},
		{
			name:     "Glob with pattern",
			toolName: "Glob",
			input:    map[string]interface{}{"pattern": "**/*.ts"},
			want:     "**/*.ts",
		},
		{
			name:     "Grep with pattern",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": "TODO", "path": "/src"},
			want:     "TODO",
		},
		{
			name:     "WebFetch with url",
			toolName: "WebFetch",
			input:    map[string]interface{}{"url": "https://example.com"},
			want:     "https://example.com",
		},
		{
			name:     "Task with description",
			toolName: "Task",
			input:    map[string]interface{}{"description": "Run unit tests"},
			want:     "Run unit tests",
		},
		{
			name:     "WebSearch with query",
			toolName: "WebSearch",
			input:    map[string]interface{}{"query": "golang context"},
			want:     "golang context",
		},
		{
			name:     "nil input returns empty",
			toolName: "Read",
			input:    nil,
			want:     "",
		},
		{
			name:     "empty input object returns empty",
			toolName: "Read",
			input:    map[string]interface{}{},
			want:     "",
		},
		{
			name:     "unknown tool returns empty",
			toolName: "CustomTool",
			input:    map[string]interface{}{"query": "hello"},
			want:     "",
		},
		{
			name:     "invalid JSON input returns empty",
			toolName: "Read",
			rawJSON:  `{invalid json}`,
			want:     "",
		},
		{
			name:     "long summary is truncated",
			toolName: "Bash",
			input:    map[string]interface{}{"command": strings.Repeat("a", 200)},
			want:     strings.Repeat("a", 120) + "...",
		},
		{
			name:     "multibyte runes are not split on truncation",
			toolName: "Bash",
			input:    map[string]interface{}{"command": strings.Repeat("\u4e16", 200)},
			want:     strings.Repeat("\u4e16", 120) + "...",
		},
		{
			name:     "newlines in summary are escaped",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "echo hello\necho world"},
			want:     "echo hello\\necho world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.rawJSON != "" {
				raw = json.RawMessage(tt.rawJSON)
			} else if tt.input != nil {
				b, err := json.Marshal(tt.input)
				if err != nil {
					t.Fatalf("Failed to marshal input: %v", err)
				}
				raw = b
			}
			got := toolInputSummary(tt.toolName, raw)
			if got != tt.want {
				t.Errorf("toolInputSummary(%q, ...):\n got: %q\nwant: %q", tt.toolName, got, tt.want)
			}
		})
	}
}
