package sessionruntime

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundedToolOutputKeepsHeadAndTail(t *testing.T) {
	output := strings.Repeat("head-", maxToolOutputBytes/5) +
		strings.Repeat("middle-", maxToolOutputBytes/7) +
		"tail"

	got := truncateToolOutput(output)

	if len(got) > maxToolOutputBytes {
		t.Fatalf("bounded tool output has %d bytes, want at most %d", len(got), maxToolOutputBytes)
	}
	if !strings.HasPrefix(got, "head-head-") {
		t.Fatalf("bounded tool output does not retain the head: %q", got[:32])
	}
	if !strings.Contains(got, toolOutputTruncationMarker) {
		t.Fatal("bounded tool output does not contain the truncation marker")
	}
	if !strings.HasSuffix(got, "tail") {
		t.Fatalf("bounded tool output does not retain the tail: %q", got[len(got)-32:])
	}
}

func TestBoundedToolOutputBoundsStreamedUnicode(t *testing.T) {
	output := newBoundedToolOutput(maxToolOutputBytes)
	for range 1024 {
		output.WriteString(strings.Repeat("界", 256))
	}

	got := output.String()
	if len(got) > maxToolOutputBytes {
		t.Fatalf("streamed tool output has %d bytes, want at most %d", len(got), maxToolOutputBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("streamed tool output is not valid UTF-8")
	}
	if !strings.Contains(got, toolOutputTruncationMarker) {
		t.Fatal("streamed tool output does not contain the truncation marker")
	}
}

func TestBoundedToolOutputLeavesSmallOutputUnchanged(t *testing.T) {
	const output = "line one\nline two\n"
	if got := truncateToolOutput(output); got != output {
		t.Fatalf("bounded tool output = %q, want %q", got, output)
	}
}

func TestTurnSinkBoundsToolOutputBeforeDurableJournal(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	sink := &turnSink{server: server, turnID: "turn-1"}

	sink.Emit(Event{
		Type:   EventToolCompleted,
		ToolID: "tool-1",
		Output: strings.Repeat("\x01", maxToolOutputBytes*2),
		Status: "completed",
	})

	if err := journal.Err(); err != nil {
		t.Fatalf("durable journal failed for bounded tool output: %v", err)
	}
	events := journal.Snapshot()
	if len(events) != 1 {
		t.Fatalf("journal events = %d, want 1", len(events))
	}
	if len(events[0].Output) > maxToolOutputBytes {
		t.Fatalf("journaled tool output has %d bytes, want at most %d", len(events[0].Output), maxToolOutputBytes)
	}
	if !strings.Contains(events[0].Output, toolOutputTruncationMarker) {
		t.Fatal("journaled tool output does not contain the truncation marker")
	}
}
