package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

func TestSessionTerminalRendersANSIEvents(t *testing.T) {
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd, HistoryState: &sessionruntime.HistoryState{PendingTurn: &sessionruntime.HistoryPendingTurn{TurnID: "turn-2", Text: "pending"}}},
		{Type: sessionruntime.EventRuntimeRecovered, Text: "Session runtime restarted"},
		{Type: sessionruntime.EventUserMessage, Text: "hello"},
		{Type: sessionruntime.EventUserMessageUpdated, TurnID: "turn-2", Text: "pending\n\nwith more context"},
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
		"\x1b[7m  pending  \x1b[0m",
		"\x1b[7m  with more context  \x1b[0m",
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

func TestSessionTerminalSeparatesStreamedTextBlocks(t *testing.T) {
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd},
		{Type: sessionruntime.EventAssistantDelta, Text: "First block"},
		{Type: sessionruntime.EventAssistantMessage, Text: "First block"},
		{Type: sessionruntime.EventAssistantDelta, Text: "Second block"},
		{Type: sessionruntime.EventAssistantMessage, Text: "Second block"},
		{Type: sessionruntime.EventTurnCompleted},
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
	if err := runSessionTerminal(ctx, input, &output, &events, &requests, false); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	// Each streamed block closes on its assistant.message and renders on its own
	// prefixed line, so the two blocks are not concatenated into one bubble.
	if want := "agent › First block\nagent › Second block\n"; !strings.Contains(got, want) {
		t.Fatalf("terminal output = %q, want it to contain %q", got, want)
	}
	// The closing assistant.message must not re-print the streamed block text.
	if n := strings.Count(got, "First block"); n != 1 {
		t.Fatalf("terminal output contains %q %d times, want 1", "First block", n)
	}
	if n := strings.Count(got, "Second block"); n != 1 {
		t.Fatalf("terminal output contains %q %d times, want 1", "Second block", n)
	}
}

func TestSessionPlainTerminalDoesNotRepeatStreamedToolOutput(t *testing.T) {
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd},
		{Type: sessionruntime.EventToolStarted, ToolID: "tool-1", ToolName: "make test"},
		{Type: sessionruntime.EventToolDelta, ToolID: "tool-1", Output: "first\n"},
		{Type: sessionruntime.EventToolDelta, ToolID: "tool-1", Output: "second\n"},
		{Type: sessionruntime.EventToolCompleted, ToolID: "tool-1", Output: "first\nsecond\n", Status: "completed"},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionTerminal(ctx, input, &output, &events, io.Discard, false); err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{"first", "second"} {
		if count := strings.Count(output.String(), line); count != 1 {
			t.Fatalf("terminal output contains %q %d times, want 1: %q", line, count, output.String())
		}
	}
}

func TestSessionPlainTerminalIsolatesHistoryPagesFromLiveStream(t *testing.T) {
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	earlierCompletedAt := startedAt.Add(-2 * time.Minute)
	completedAt := startedAt.Add(5*time.Minute + 19*time.Second)
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryStart, HistoryLimited: true, HistoryCursor: "cursor-1"},
		{Type: sessionruntime.EventAssistantDelta, Text: "projected response"},
		{Type: sessionruntime.EventHistoryEnd, HistoryState: &sessionruntime.HistoryState{ActiveTurnID: "turn-1", ActiveTurnStarted: &startedAt}},
		{Type: sessionruntime.EventAssistantMessage, Text: "projected response"},
		{Type: sessionruntime.EventAssistantDelta, Text: "live response"},
		{Type: sessionruntime.EventHistoryStart, RequestID: "history-1", HistoryPage: true},
		{Type: sessionruntime.EventAssistantMessage, Text: "earlier response"},
		{Type: sessionruntime.EventTurnCompleted, Status: "completed", Timestamp: &earlierCompletedAt},
		{Type: sessionruntime.EventHistoryEnd, RequestID: "history-1", HistoryPage: true},
		{Type: sessionruntime.EventAssistantMessage, Text: "live response"},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed", Timestamp: &completedAt},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionTerminal(ctx, input, &output, &events, io.Discard, false); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	for _, response := range []string{"projected response", "live response", "earlier response"} {
		if count := strings.Count(got, response); count != 1 {
			t.Fatalf("terminal output contains %q %d times, want 1: %q", response, count, got)
		}
	}
	if !strings.Contains(got, "Earlier Session history is available. Use /history to load the previous page.") {
		t.Fatalf("terminal output = %q, want limited history notice", got)
	}
	separator := formatSessionTurnSeparator(completedAt.Sub(startedAt), sessionTUIDefaultWidth)
	if strings.Count(got, "Worked for") != 1 || !strings.Contains(got, separator) {
		t.Fatalf("terminal output = %q, want only the live turn separator %q", got, separator)
	}
}

func TestSessionPlainTerminalIgnoresDuplicateHistoryRequests(t *testing.T) {
	input, inputWriter := io.Pipe()
	events, eventsWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	defer events.Close()
	defer eventsWriter.Close()

	var output synchronizedBuffer
	var requests synchronizedBuffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runSessionPlainTerminalWithWidth(ctx, input, &output, json.NewDecoder(events), json.NewEncoder(&requests), false, func() int {
			return sessionTUIDefaultWidth
		})
	}()

	eventEncoder := json.NewEncoder(eventsWriter)
	if err := eventEncoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, HistoryLimited: true, HistoryCursor: "cursor-1"}); err != nil {
		t.Fatal(err)
	}
	if err := eventEncoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd}); err != nil {
		t.Fatal(err)
	}
	waitForSessionTerminalOutput(t, &output, "Connected.")
	if _, err := io.WriteString(inputWriter, "/history\n/history\n"); err != nil {
		t.Fatal(err)
	}
	waitForSessionTerminalOutput(t, &output, "Session history is already loading.")
	var firstRequest sessionruntime.ClientRequest
	if err := json.NewDecoder(strings.NewReader(requests.String())).Decode(&firstRequest); err != nil {
		t.Fatal(err)
	}
	if firstRequest.Type != "history" || firstRequest.RequestID == "" || firstRequest.HistoryCursor != "cursor-1" {
		t.Fatalf("first history request = %#v", firstRequest)
	}
	if err := eventEncoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, RequestID: firstRequest.RequestID, HistoryPage: true, HistoryCursor: "cursor-2"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionTerminalOutput(t, &output, "Earlier Session history:")
	outputLength := len(output.String())
	if err := eventEncoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd, RequestID: firstRequest.RequestID, HistoryPage: true}); err != nil {
		t.Fatal(err)
	}
	waitForSessionTerminalOutputLength(t, &output, outputLength+1)
	if _, err := io.WriteString(inputWriter, "/history\n/quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(strings.NewReader(requests.String()))
	var requestsSent []sessionruntime.ClientRequest
	for {
		var request sessionruntime.ClientRequest
		if err := decoder.Decode(&request); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		requestsSent = append(requestsSent, request)
	}
	if len(requestsSent) != 2 {
		t.Fatalf("history requests = %#v, want two requests", requestsSent)
	}
	if !reflect.DeepEqual(requestsSent[0], firstRequest) {
		t.Fatalf("first history request = %#v, want %#v", requestsSent[0], firstRequest)
	}
	request := requestsSent[1]
	if request.Type != "history" || request.RequestID == "" || request.RequestID == firstRequest.RequestID || request.HistoryCursor != "cursor-2" {
		t.Fatalf("second history request = %#v", request)
	}
}

func TestSessionTerminalRendersTurnDurationSeparator(t *testing.T) {
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(5*time.Minute + 19*time.Second)
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd},
		{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Timestamp: &startedAt},
		{Type: sessionruntime.EventAssistantMessage, Text: "First reply"},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Timestamp: &completedAt},
		{Type: sessionruntime.EventUserMessage, Text: "Second question"},
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
	if err := runSessionTerminal(ctx, input, &output, &events, &requests, false); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	separator := formatSessionTurnSeparator(5*time.Minute+19*time.Second, sessionTUIDefaultWidth)
	if !strings.Contains(got, separator+"\n") {
		t.Fatalf("terminal output = %q, want separator %q", got, separator)
	}
	if replyAt, separatorAt, questionAt := strings.Index(got, "First reply"), strings.Index(got, separator), strings.Index(got, "Second question"); replyAt < 0 || separatorAt < replyAt || questionAt < separatorAt {
		t.Fatalf("terminal output order = %q, want separator between turns", got)
	}
}

func TestSessionPlainTerminalContinuesProjectedTurnDuration(t *testing.T) {
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(2*time.Hour + 3*time.Minute)
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd, HistoryState: &sessionruntime.HistoryState{ActiveTurnID: "turn-1", ActiveTurnStarted: &startedAt}},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Timestamp: &completedAt},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionTerminal(ctx, input, &output, &events, io.Discard, false); err != nil {
		t.Fatal(err)
	}

	separator := formatSessionTurnSeparator(completedAt.Sub(startedAt), sessionTUIDefaultWidth)
	if got := output.String(); !strings.Contains(got, separator) {
		t.Fatalf("terminal output = %q, want separator %q", got, separator)
	}
}

func TestSessionPlainTerminalUsesCurrentWidthForTurnSeparator(t *testing.T) {
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryEnd},
		{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Timestamp: &startedAt},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Timestamp: timePointer(startedAt.Add(time.Second))},
		{Type: sessionruntime.EventTurnStarted, TurnID: "turn-2", Timestamp: timePointer(startedAt.Add(2 * time.Second))},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-2", Timestamp: timePointer(startedAt.Add(3 * time.Second))},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	widths := []int{36, 52}
	widthIndex := 0
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionPlainTerminalWithWidth(ctx, input, &output, json.NewDecoder(&events), json.NewEncoder(io.Discard), false, func() int {
		width := widths[widthIndex]
		widthIndex++
		return width
	}); err != nil {
		t.Fatal(err)
	}

	var gotWidths []int
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "─ Worked for") {
			gotWidths = append(gotWidths, len([]rune(line)))
		}
	}
	if !reflect.DeepEqual(gotWidths, widths) {
		t.Fatalf("turn separator widths = %v, want %v", gotWidths, widths)
	}
}

func TestSessionPlainTerminalOmitsUnknownHistoryDuration(t *testing.T) {
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryStart},
		{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"},
		{Type: sessionruntime.EventAssistantMessage, Text: "Historical reply"},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1"},
		{Type: sessionruntime.EventHistoryEnd},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionTerminal(ctx, input, &output, &events, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Contains(got, "Worked for") {
		t.Fatalf("terminal output = %q, want unknown history duration omitted", got)
	}
	if got := output.String(); !strings.Contains(got, "Historical reply\n\nConnected.") {
		t.Fatalf("terminal output = %q, want one blank line before the connection notice", got)
	}
}

func TestSessionPlainTerminalOmitsRuntimeRecoveryDuration(t *testing.T) {
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	var events bytes.Buffer
	encoder := json.NewEncoder(&events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventHistoryStart},
		{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Timestamp: &startedAt},
		{Type: sessionruntime.EventRuntimeRecovered, Text: "Session runtime restarted"},
		{Type: sessionruntime.EventInputResolved, TurnID: "turn-1", InputID: "input-1", Status: "cancelled"},
		{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "interrupted", Timestamp: timePointer(startedAt.Add(time.Hour))},
		{Type: sessionruntime.EventHistoryEnd},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}

	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := runSessionTerminal(ctx, input, &output, &events, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Contains(got, "Worked for") {
		t.Fatalf("terminal output = %q, want runtime recovery duration omitted", got)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
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
		{input: "/send", want: sessionruntime.ClientRequest{Type: "message"}},
		{input: "/attach /tmp/screen shot.png", want: sessionruntime.ClientRequest{Type: sessionTerminalRequestAttachment, Text: "/tmp/screen shot.png"}},
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

func TestSessionTerminalDoesNotUseTUIForDumbTerminal(t *testing.T) {
	for _, termType := range []string{"dumb", "DUMB"} {
		if sessionTerminalSupportsTUI(termType) {
			t.Fatalf("sessionTerminalSupportsTUI(%q) = true, want false", termType)
		}
	}
	if !sessionTerminalSupportsTUI("xterm-256color") {
		t.Fatal("sessionTerminalSupportsTUI(xterm-256color) = false, want true")
	}
}

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func waitForSessionTerminalOutput(t *testing.T, output *synchronizedBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("terminal output = %q, want %q", output.String(), text)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForSessionTerminalOutputLength(t *testing.T, output *synchronizedBuffer, length int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(output.String()) < length {
		if time.Now().After(deadline) {
			t.Fatalf("terminal output has %d bytes, want at least %d", len(output.String()), length)
		}
		time.Sleep(time.Millisecond)
	}
}
