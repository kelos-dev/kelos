package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

func TestSessionTerminalRendersANSIEvents(t *testing.T) {
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd},
		{Type: sessionruntime.EventRuntimeRecovered, Text: "Session runtime restarted"},
		{Type: sessionruntime.EventUserMessage, Text: "hello"},
		{Type: sessionruntime.EventAssistantDelta, Text: "working"},
		{Type: sessionruntime.EventToolStarted, ToolName: "shell"},
		{Type: sessionruntime.EventToolCompleted, Status: "completed"},
		{Type: sessionruntime.EventFileDiff, Diff: "-old\n+new"},
		{Type: sessionruntime.EventError, Text: "failed"},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	var requests bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionTerminal(ctx, input, &output, &events, &requests, true); err != nil {
		t.Fatal(err)
	}

	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(&requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "subscribe" {
		t.Fatalf("initial request type = %q, want subscribe", request.Type)
	}

	got := output.String()
	for _, want := range []string{
		"\x1b[7m  hello  \x1b[0m",
		"\x1b[1m\x1b[33mSession runtime restarted\x1b[0m",
		"\x1b[1m\x1b[36m↳ shell\x1b[0m",
		"\x1b[32mcompleted\x1b[0m",
		"\x1b[31m-old\x1b[0m",
		"\x1b[32m+new\x1b[0m",
		"\x1b[1m\x1b[31merror: failed\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("terminal output = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "you ›") || strings.Contains(got, "agent ›") {
		t.Fatalf("terminal output = %q, want no role prefixes in color mode", got)
	}
}

func TestSessionTerminalFormatterUsesANSIStyles(t *testing.T) {
	formatter := sessionTerminalFormatter{color: true}
	if got, want := formatter.userMessage("hello"), "\x1b[7m  hello  \x1b[0m"; got != want {
		t.Fatalf("userMessage() = %q, want %q", got, want)
	}
	if got, want := formatter.tool("shell"), "  \x1b[1m\x1b[36m↳ shell\x1b[0m"; got != want {
		t.Fatalf("tool() = %q, want %q", got, want)
	}
	if got, want := formatter.toolStatus("completed"), "    \x1b[32mcompleted\x1b[0m"; got != want {
		t.Fatalf("toolStatus() = %q, want %q", got, want)
	}
	if got, want := formatter.error("error: failed"), "\x1b[1m\x1b[31merror: failed\x1b[0m"; got != want {
		t.Fatalf("error() = %q, want %q", got, want)
	}
}

func TestSessionTerminalFormatterColorizesDiff(t *testing.T) {
	formatter := sessionTerminalFormatter{color: true}
	diff := "diff --git a/file b/file\n--- a/file\n+++ b/file\n@@ -1 +1 @@\n context\n-old\n+new"
	want := "\x1b[2mdiff --git a/file b/file\x1b[0m\n" +
		"\x1b[1m--- a/file\x1b[0m\n" +
		"\x1b[1m+++ b/file\x1b[0m\n" +
		"\x1b[36m@@ -1 +1 @@\x1b[0m\n" +
		" context\n" +
		"\x1b[31m-old\x1b[0m\n" +
		"\x1b[32m+new\x1b[0m"
	if got := formatter.diff(diff); got != want {
		t.Fatalf("diff() = %q, want %q", got, want)
	}
}

func TestSessionTerminalFormatterKeepsPlainTextFallback(t *testing.T) {
	formatter := sessionTerminalFormatter{}
	if got, want := formatter.userMessage("hello"), "you › hello"; got != want {
		t.Fatalf("userMessage() = %q, want %q", got, want)
	}
	if got, want := formatter.tool("shell"), "  ↳ shell"; got != want {
		t.Fatalf("tool() = %q, want %q", got, want)
	}
	diff := "-old\n+new"
	if got := formatter.diff(diff); got != diff {
		t.Fatalf("diff() = %q, want %q", got, diff)
	}
}

func TestSessionTerminalRequest(t *testing.T) {
	tests := []struct {
		input string
		want  sessionruntime.ClientRequest
	}{
		{input: "hello", want: sessionruntime.ClientRequest{Type: "message", Text: "hello"}},
		{input: "/interrupt", want: sessionruntime.ClientRequest{Type: "interrupt"}},
		{input: "/answer input-1 question-1 first, second", want: sessionruntime.ClientRequest{Type: "input", InputID: "input-1", Answers: map[string][]string{"question-1": {"first", "second"}}}},
		{input: "/cancel-input input-2", want: sessionruntime.ClientRequest{Type: "input", InputID: "input-2", Cancel: true}},
	}
	for _, test := range tests {
		if got := sessionTerminalRequest(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("sessionTerminalRequest(%q) = %#v, want %#v", test.input, got, test.want)
		}
	}
}
