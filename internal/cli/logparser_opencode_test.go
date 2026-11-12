package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndFormatOpenCodeLogs(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "text event with top-level text field",
			input:      `{"type":"text","text":"Here is the explanation of context in Go."}`,
			wantStdout: "Here is the explanation of context in Go.",
			wantStderr: "",
		},
		{
			name:       "text event with nested part text",
			input:      `{"type":"text","part":{"type":"text","text":"Hello world"}}`,
			wantStdout: "Hello world",
			wantStderr: "",
		},
		{
			name:       "step_finish event with tokens",
			input:      `{"type":"step_finish","part":{"tokens":{"input":100,"output":50}}}`,
			wantStdout: "",
			wantStderr: "[usage] input=100 cached=0 output=50\n",
		},
		{
			name:       "step_finish event with opencode cache fields",
			input:      `{"type":"step_finish","part":{"type":"step-finish","tokens":{"total":13564,"input":6646,"output":120,"reasoning":14,"cache":{"write":0,"read":6784}}}}`,
			wantStdout: "",
			wantStderr: "[usage] input=6646 cached=6784 output=120 reasoning=14 total=13564\n",
		},
		{
			name:       "step_finish event without tokens",
			input:      `{"type":"step_finish","part":{"type":"step_finish"}}`,
			wantStdout: "",
			wantStderr: "[result] completed\n",
		},
		{
			name:       "step_finish event without part",
			input:      `{"type":"step_finish"}`,
			wantStdout: "",
			wantStderr: "[result] completed\n",
		},
		{
			name:       "step_start event prints step boundary",
			input:      `{"type":"step_start","sessionID":"abc123"}`,
			wantStdout: "",
			wantStderr: "\n--- Step 1 ---\n",
		},
		{
			name:       "tool_use bash event prints command summary",
			input:      `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"go test ./...","workdir":"/workspace/repo"},"metadata":{"exit":0,"truncated":false}}}}`,
			wantStdout: "",
			wantStderr: "[tool] bash: go test ./...\n",
		},
		{
			name:       "tool_use read event prints file path summary",
			input:      `{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"/workspace/repo/internal/cli/logs.go"},"metadata":{"truncated":true}}}}`,
			wantStdout: "",
			wantStderr: "[tool] read: /workspace/repo/internal/cli/logs.go (truncated)\n",
		},
		{
			name:       "tool_use grep event prints pattern summary",
			input:      `{"type":"tool_use","part":{"type":"tool","tool":"grep","state":{"status":"completed","input":{"path":"/workspace/repo","pattern":"ValidatingWebhook"}}}}`,
			wantStdout: "",
			wantStderr: "[tool] grep: ValidatingWebhook\n",
		},
		{
			name:       "tool_use failure prints status and exit without output",
			input:      `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"make test"},"output":"very noisy output","metadata":{"output":"very noisy output","exit":2,"truncated":false}}}}`,
			wantStdout: "",
			wantStderr: "[tool] bash: make test (exit=2)\n",
		},
		{
			name:       "non-JSON passthrough",
			input:      "this is plain text output",
			wantStdout: "this is plain text output\n",
			wantStderr: "",
		},
		{
			name:       "empty input",
			input:      "",
			wantStdout: "",
			wantStderr: "",
		},
		{
			name: "multiple NDJSON events",
			input: `{"type":"step_start","sessionID":"s1"}
{"type":"text","text":"Line one."}
{"type":"text","text":"\nLine two."}
{"type":"step_finish","part":{"tokens":{"input":200,"output":75}}}`,
			wantStdout: "Line one.\nLine two.",
			wantStderr: "\n--- Step 1 ---\n[usage] input=200 cached=0 output=75\n",
		},
		{
			name:       "unknown event type is ignored",
			input:      `{"type":"unknown_event","data":"something"}`,
			wantStdout: "",
			wantStderr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ParseAndFormatOpenCodeLogs(strings.NewReader(tt.input), &stdout, &stderr)
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
