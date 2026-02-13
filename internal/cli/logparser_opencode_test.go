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
			wantStderr: "\n[result] completed (input=100, output=50)\n",
		},
		{
			name:       "step_finish event without tokens",
			input:      `{"type":"step_finish","part":{"type":"step_finish"}}`,
			wantStdout: "",
			wantStderr: "\n[result] completed\n",
		},
		{
			name:       "step_finish event without part",
			input:      `{"type":"step_finish"}`,
			wantStdout: "",
			wantStderr: "\n[result] completed\n",
		},
		{
			name:       "step_start event is silent",
			input:      `{"type":"step_start","sessionID":"abc123"}`,
			wantStdout: "",
			wantStderr: "",
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
			wantStderr: "\n[result] completed (input=200, output=75)\n",
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
