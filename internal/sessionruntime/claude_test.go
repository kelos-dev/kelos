package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeGoalCommandsUseNativeInput(t *testing.T) {
	for _, test := range []struct {
		command goalCommand
		text    string
	}{
		{command: goalCommand{action: goalCommandShow}, text: "/goal"},
		{command: goalCommand{action: goalCommandCreate, objective: "All tests pass"}, text: "/goal All tests pass"},
		{command: goalCommand{action: goalCommandClear}, text: "/goal clear"},
	} {
		t.Run(test.text, func(t *testing.T) {
			stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
			provider := &ClaudeProvider{ctx: t.Context(), stdin: stdin, sessionID: "session-1"}
			if err := provider.StartGoal(t.Context(), test.command); err != nil {
				t.Fatal(err)
			}
			var message struct {
				SessionID string `json:"session_id"`
				Message   struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(<-stdin.writes, &message); err != nil {
				t.Fatal(err)
			}
			if message.SessionID != "session-1" || len(message.Message.Content) != 1 || message.Message.Content[0].Text != test.text {
				t.Fatalf("Native goal input = %#v", message)
			}
		})
	}
}

func TestClaudeGoalCommandsPreservePendingShellContext(t *testing.T) {
	stdin := &channelWriteCloser{writes: make(chan []byte, 2)}
	provider := &ClaudeProvider{ctx: t.Context(), stdin: stdin, sessionID: "session-1"}
	record := shellCommandRecord{command: "make test", output: "All tests pass"}
	if err := provider.recordShellCommand(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := provider.StartGoal(t.Context(), goalCommand{action: goalCommandCreate, objective: "All tests pass"}); err != nil {
		t.Fatal(err)
	}
	var goal struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(<-stdin.writes, &goal); err != nil {
		t.Fatal(err)
	}
	if len(goal.Message.Content) != 1 || goal.Message.Content[0].Text != "/goal All tests pass" {
		t.Fatalf("Native goal input = %#v", goal)
	}
	if err := provider.StartTurn(t.Context(), TurnInput{Text: "Continue"}); err != nil {
		t.Fatal(err)
	}
	var ordinary struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(<-stdin.writes, &ordinary); err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Message.Content) != 2 || ordinary.Message.Content[0].Text != formatShellCommandRecord(record) || ordinary.Message.Content[1].Text != "Continue" {
		t.Fatalf("Ordinary prompt after native command = %#v", ordinary)
	}
}

func TestClaudeGoalRejectsUnknownAction(t *testing.T) {
	stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
	provider := &ClaudeProvider{ctx: t.Context(), stdin: stdin}
	if err := provider.StartGoal(t.Context(), goalCommand{action: "unknown"}); err == nil || !strings.Contains(err.Error(), claudeGoalUsage) {
		t.Fatalf("StartGoal(unknown) = %v", err)
	}
	if len(stdin.writes) != 0 || provider.active {
		t.Fatal("Unknown goal action started work")
	}
}

func TestClaudeNativeGoalStreamsAndQueuesControls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
	provider := &ClaudeProvider{ctx: ctx, config: ProviderConfig{StateDir: t.TempDir()}, stdin: stdin, sessionID: "session-1", done: make(chan struct{})}
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{AgentType: "claude-code"}, journal, provider)
	server.readWorkspaceDiff = func(context.Context, string) string { return "" }
	go provider.readLoop(reader)
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.runTurns(ctx)
	}()
	defer func() { cancel(); <-done; _ = writer.Close(); <-provider.done }()
	if err := server.submitClientMessage(ctx, "/goal All tests pass", "create"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stdin.writes:
	case <-time.After(5 * time.Second):
		t.Fatal("Native goal was not submitted")
	}
	for _, line := range []string{
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Goal set: All tests pass"}]},"local_command_run":{"command":"goal","args":"All tests pass"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"First attempt"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Second attempt"}]}}`,
	} {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			t.Fatal(err)
		}
	}
	waitForSessionEvent(t, journal, func(event Event) bool { return event.Type == EventAssistantMessage && event.Text == "Second attempt" })
	if err := server.submitClientMessage(ctx, "/goal clear", "clear"); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-stdin.writes:
		t.Fatalf("Goal control ran during native goal work: %s", data)
	default:
	}
	for _, event := range journal.Snapshot() {
		if event.Type == EventTurnCompleted {
			t.Fatal("Native goal completed before Claude's result")
		}
	}
	if _, err := fmt.Fprintln(writer, `{"type":"result","subtype":"success"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-stdin.writes:
		if !strings.Contains(string(data), `"text":"/goal clear"`) {
			t.Fatalf("Queued native goal input = %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Queued goal control did not run after Claude's result")
	}
	for _, line := range []string{
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"No goal set"}]},"local_command_run":{"command":"goal","args":"clear"}}`,
		`{"type":"result","subtype":"success","local_command":"goal"}`,
	} {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			t.Fatal(err)
		}
	}
	waitForSessionEvent(t, journal, func(event Event) bool { return event.Type == EventTurnCompleted && event.TurnID == "turn-2" })
	completed := 0
	for _, event := range journal.Snapshot() {
		if event.Type == EventTurnCompleted {
			completed++
			if event.Status != "completed" {
				t.Fatalf("Goal completion = %#v", event)
			}
		}
	}
	if completed != 2 {
		t.Fatalf("Completed turns = %d, want 2", completed)
	}
}

func TestClaudeNativeCommandPreservesRuntimeModelAndUsage(t *testing.T) {
	provider := &ClaudeProvider{model: "claude-sonnet-4-6", usage: &RuntimeUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, ContextTokens: 120}}
	sink := newProviderTestSink(nil)
	_, err := provider.handleClaudeLine([]byte(`{"type":"assistant","message":{"id":"local-command","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"No goal set"}]}}`), sink)
	if err != nil {
		t.Fatal(err)
	}
	status := provider.runtimeStatusSnapshot()
	if status.Model != "claude-sonnet-4-6" || status.Usage == nil || status.Usage.TotalTokens != 120 || status.Usage.ContextTokens != 120 {
		t.Fatalf("Runtime status after native goal command = %#v", status)
	}
	assertEventTypes(t, sink.snapshot(), EventAssistantMessage)
}

type channelWriteCloser struct {
	writes chan []byte
}

func (w *channelWriteCloser) Write(data []byte) (int, error) {
	w.writes <- append([]byte(nil), data...)
	return len(data), nil
}

func (w *channelWriteCloser) Close() error {
	return nil
}

func TestClaudeProviderIncludesShellCommandWithNextPrompt(t *testing.T) {
	stdin := &channelWriteCloser{writes: make(chan []byte, 1)}
	provider := &ClaudeProvider{
		ctx:       context.Background(),
		stdin:     stdin,
		sessionID: "session-1",
	}
	record := shellCommandRecord{
		command:  "printf shell-output",
		exitCode: 0,
		duration: 1234 * time.Millisecond,
		output:   "shell-output",
	}
	if err := provider.recordShellCommand(t.Context(), record); err != nil {
		t.Fatalf("recordShellCommand() error = %v", err)
	}
	select {
	case message := <-stdin.writes:
		t.Fatalf("recordShellCommand() wrote a Claude message: %s", message)
	default:
	}

	provider.SetEventSink(newProviderTestSink(nil))
	if err := provider.StartTurn(t.Context(), TurnInput{Text: "continue"}); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	var message struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	select {
	case data := <-stdin.writes:
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Claude prompt was not submitted")
	}
	wantContext := "<user_shell_command>\n<command>\nprintf shell-output\n</command>\n<result>\nExit code: 0\nDuration: 1.2340 seconds\nOutput:\nshell-output\n</result>\n</user_shell_command>"
	if len(message.Message.Content) != 2 || message.Message.Content[0].Type != "text" || message.Message.Content[0].Text != wantContext || message.Message.Content[1].Text != "continue" {
		t.Fatalf("Claude prompt content = %#v", message.Message.Content)
	}
	provider.shellContextMu.Lock()
	defer provider.shellContextMu.Unlock()
	if len(provider.pendingShellCommands) != 0 {
		t.Fatalf("pending shell commands = %#v", provider.pendingShellCommands)
	}
}

// TestClaudeProviderClosesEachTextBlock verifies that a streamed turn with two
// text blocks emits an assistant.message after each block's deltas, so clients
// render them as separate bubbles instead of concatenating the whole turn.
func TestClaudeProviderClosesEachTextBlock(t *testing.T) {
	provider := &ClaudeProvider{blockText: map[int]*strings.Builder{}}
	sink := newProviderTestSink(nil)

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Second block"}}`,
		`{"type":"content_block_stop","index":1}`,
	}
	for _, raw := range events {
		provider.emitClaudeStreamEvent(json.RawMessage(raw), sink)
	}

	want := []Event{
		{Type: EventAssistantDelta, Text: "Hello"},
		{Type: EventAssistantDelta, Text: " there"},
		{Type: EventAssistantMessage, Text: "Hello there"},
		{Type: EventAssistantDelta, Text: "Second block"},
		{Type: EventAssistantMessage, Text: "Second block"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}

// TestClaudeProviderStreamedToolBlockEmitsNoMessage verifies that closing a
// non-text content block (a tool_use) does not emit an empty assistant.message.
func TestClaudeProviderStreamedToolBlockEmitsNoMessage(t *testing.T) {
	provider := &ClaudeProvider{blockText: map[int]*strings.Builder{}}
	sink := newProviderTestSink(nil)

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"Bash"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, raw := range events {
		provider.emitClaudeStreamEvent(json.RawMessage(raw), sink)
	}

	want := []Event{
		{Type: EventToolStarted, ToolID: "tool-1", ToolName: "Bash", Status: "running"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}

// TestClaudeProviderEmitsMessageWithoutStreaming verifies that when no text
// deltas were streamed, the assembled assistant message still emits one
// assistant.message per text block.
func TestClaudeProviderEmitsMessageWithoutStreaming(t *testing.T) {
	provider := &ClaudeProvider{}
	sink := newProviderTestSink(nil)

	message := `{"content":[{"type":"text","text":"First"},{"type":"text","text":"Second"}]}`
	provider.emitClaudeMessage("assistant", json.RawMessage(message), sink)

	want := []Event{
		{Type: EventAssistantMessage, Text: "First"},
		{Type: EventAssistantMessage, Text: "Second"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}

// TestClaudeProviderRuntimeStatusMapping verifies that the model, token usage,
// and context window reported by Claude Code become runtime status updates,
// that repeated per-block usage for one assistant message is counted once, and
// that a result event's session-cumulative usage replaces the per-message sums
// (it also covers API calls that never appear as assistant events).
func TestClaudeProviderRuntimeStatusMapping(t *testing.T) {
	provider := &ClaudeProvider{config: ProviderConfig{Effort: "high"}}
	sink := newProviderTestSink(nil)

	// An init event records status even when no consumer is attached.
	if _, err := provider.handleClaudeLine([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-6","session_id":"ses-1"}`), nil); err != nil {
		t.Fatalf("handleClaudeLine() error = %v", err)
	}

	lines := []string{
		`{"type":"assistant","message":{"id":"msg-1","model":"claude-opus-4-6","usage":{"input_tokens":10,"cache_creation_input_tokens":40,"cache_read_input_tokens":50,"output_tokens":5},"content":[]}}`,
		`{"type":"assistant","message":{"id":"msg-1","model":"claude-opus-4-6","usage":{"input_tokens":10,"cache_creation_input_tokens":40,"cache_read_input_tokens":50,"output_tokens":5},"content":[]}}`,
		`{"type":"assistant","message":{"id":"msg-2","model":"claude-opus-4-6","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":100,"output_tokens":10},"content":[]}}`,
		`{"type":"result","subtype":"success","usage":{"input_tokens":40,"cache_creation_input_tokens":60,"cache_read_input_tokens":200,"output_tokens":25},"modelUsage":{"claude-opus-4-6":{"contextWindow":200000}}}`,
	}
	for _, line := range lines {
		if _, err := provider.handleClaudeLine([]byte(line), sink); err != nil {
			t.Fatalf("handleClaudeLine(%s) error = %v", line, err)
		}
	}

	events := sink.snapshot()
	if len(events) != 3 {
		t.Fatalf("runtime status events = %#v", events)
	}
	for _, event := range events {
		if event.Type != EventRuntimeStatus || event.Runtime == nil {
			t.Fatalf("runtime status event = %#v", event)
		}
	}
	// The per-message sums (220 in, 15 out) provide live updates mid-turn.
	live := events[1].Runtime
	if live.Usage == nil || live.Usage.InputTokens != 220 || live.Usage.OutputTokens != 15 {
		t.Fatalf("Claude mid-turn runtime status = %#v", live)
	}
	// The result event's cumulative usage exceeds the visible message sums and
	// replaces them.
	status := events[2].Runtime
	if status.Model != "claude-opus-4-6" ||
		status.Effort != "high" ||
		status.Usage == nil ||
		status.Usage.InputTokens != 300 ||
		status.Usage.OutputTokens != 25 ||
		status.Usage.TotalTokens != 325 ||
		status.Usage.ContextTokens != 130 ||
		status.Usage.ContextWindow != 200000 {
		t.Fatalf("Claude runtime status = %#v", status)
	}

	server := NewServer(Config{AgentType: "claude-code"}, NewJournal(), provider)
	t.Cleanup(func() { server.journal.Close() })
	initial := server.runtimeStatusSnapshot()
	if initial.Model != "claude-opus-4-6" || initial.Usage == nil || initial.Usage.TotalTokens != 325 {
		t.Fatalf("initial server runtime status = %#v", initial)
	}
}

// TestClaudeProviderStreamingSuppressesAssembledMessage verifies that once text
// deltas have streamed (and been closed per block), the assembled assistant
// message does not re-emit the same text as duplicate assistant.message events.
func TestClaudeProviderStreamingSuppressesAssembledMessage(t *testing.T) {
	provider := &ClaudeProvider{blockText: map[int]*strings.Builder{}}
	sink := newProviderTestSink(nil)

	stream := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Streamed"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, raw := range stream {
		provider.emitClaudeStreamEvent(json.RawMessage(raw), sink)
	}
	provider.emitClaudeMessage("assistant", json.RawMessage(`{"content":[{"type":"text","text":"Streamed"}]}`), sink)

	want := []Event{
		{Type: EventAssistantDelta, Text: "Streamed"},
		{Type: EventAssistantMessage, Text: "Streamed"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}
