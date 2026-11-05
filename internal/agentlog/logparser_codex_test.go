package agentlog

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndFormatCodexLogs(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "thread started",
			input:      `{"type":"thread.started","thread_id":"abc-123"}`,
			wantStdout: "",
			wantStderr: "",
		},
		{
			name:       "turn started",
			input:      `{"type":"turn.started"}`,
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n",
		},
		{
			name:       "turn completed with usage",
			input:      `{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":500,"output_tokens":200}}`,
			wantStdout: "",
			wantStderr: "[usage] input=1000 cached=500 output=200\n",
		},
		{
			name:       "item started command execution",
			input:      `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}`,
			wantStdout: "",
			wantStderr: "[command] bash -lc ls\n",
		},
		{
			name:       "item completed agent message",
			input:      `{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Repo contains docs and src directories."}}`,
			wantStdout: "Repo contains docs and src directories.\n",
			wantStderr: "",
		},
		{
			name:       "item completed non-message type",
			input:      `{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"ls","status":"completed"}}`,
			wantStdout: "",
			wantStderr: "",
		},
		{
			name:       "error event",
			input:      `{"type":"error","message":"Something went wrong"}`,
			wantStdout: "",
			wantStderr: "[error] {\"type\":\"error\",\"message\":\"Something went wrong\"}\n",
		},
		{
			name:       "non-JSON line passes through",
			input:      "this is plain text",
			wantStdout: "this is plain text\n",
			wantStderr: "",
		},
		{
			name:       "empty lines skipped",
			input:      "\n\n" + `{"type":"turn.started"}` + "\n\n",
			wantStdout: "",
			wantStderr: "\n--- Turn 1 ---\n",
		},
		{
			name: "full sequence",
			input: strings.Join([]string{
				`{"type":"thread.started","thread_id":"abc-123"}`,
				`{"type":"turn.started"}`,
				`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}`,
				`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Found 3 files."}}`,
				`{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50}}`,
			}, "\n"),
			wantStdout: "Found 3 files.\n",
			wantStderr: "\n--- Turn 1 ---\n[command] bash -lc ls\n[usage] input=500 cached=100 output=50\n",
		},
		{
			name: "multiple turns",
			input: strings.Join([]string{
				`{"type":"turn.started"}`,
				`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Analyzing code."}}`,
				`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":20}}`,
				`{"type":"turn.started"}`,
				`{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"go test ./...","status":"in_progress"}}`,
				`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"All tests pass."}}`,
				`{"type":"turn.completed","usage":{"input_tokens":200,"cached_input_tokens":100,"output_tokens":30}}`,
			}, "\n"),
			wantStdout: "Analyzing code.\nAll tests pass.\n",
			wantStderr: "\n--- Turn 1 ---\n[usage] input=100 cached=0 output=20\n\n--- Turn 2 ---\n[command] go test ./...\n[usage] input=200 cached=100 output=30\n",
		},
		{
			name:       "item started with unknown type",
			input:      `{"type":"item.started","item":{"id":"item_1","type":"web_search"}}`,
			wantStdout: "",
			wantStderr: "[web_search]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ParseAndFormatCodexLogs(strings.NewReader(tt.input), &stdout, &stderr)
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
