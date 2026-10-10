package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestColoredLogWriterPreservesFormattedLogs(t *testing.T) {
	input := strings.NewReader("" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Bash","input":{"command":"pwd"}}]}}` + "\n" +
		`{"type":"result","subtype":"success","num_turns":1}` + "\n")
	var stdout, stderr bytes.Buffer
	if err := ParseAndFormatLogs(input, &stdout, newColoredLogWriter(&stderr)); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "hello\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	want := "\n" + logColorBlue + "--- Turn 1 ---" + logColorReset + "\n" +
		logColorCyan + "[tool] Bash: pwd" + logColorReset + "\n\n" +
		logColorGreen + "[result] completed (1 turns, $0.0000)" + logColorReset + "\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestColoredLogWriterHandlesSplitLines(t *testing.T) {
	var output bytes.Buffer
	writer := newColoredLogWriter(&output)
	for _, part := range []string{"[result] ", "error (failed)", "\n[usage] input=1\n"} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	want := logColorRed + "[result] error (failed)" + logColorReset + "\n" +
		logColorDim + "[usage] input=1" + logColorReset + "\n"
	if got := output.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
