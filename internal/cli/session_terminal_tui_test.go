package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/muesli/termenv"
)

var sessionTUIANSISequence = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func TestRunSessionTUICommitsHistoryWithoutAlternateScreen(t *testing.T) {
	events := &bytes.Buffer{}
	encoder := json.NewEncoder(events)
	for _, event := range []sessionruntime.Event{
		{Type: sessionruntime.EventUserMessage, Text: "loaded question"},
		{Type: sessionruntime.EventAssistantMessage, Text: "loaded answer"},
		{Type: sessionruntime.EventHistoryEnd},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	requests := &bytes.Buffer{}
	output := &bytes.Buffer{}
	if err := runSessionTUI(
		context.Background(),
		nil,
		output,
		json.NewDecoder(events),
		json.NewEncoder(requests),
		false,
	); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(output.String(), "\x1b[?1049h") {
		t.Fatalf("terminal UI entered the alternate screen: %q", output.String())
	}
	rendered := stripSessionTUIANSI(output.String())
	for _, want := range []string{"loaded question", "loaded answer"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("terminal output = %q, want %q", rendered, want)
		}
	}
}

func TestSessionTUIUserBlockUsesFullWidthPadding(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 12, Height: 8})

	assertSessionTUIBlockWidth(t, model.renderUserBlock("hello"), 12)
	lines := strings.Split(stripSessionTUIANSI(model.renderUserBlock("hello")), "\n")
	if len(lines) != 3 {
		t.Fatalf("user block has %d rows, want 3: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[1], "> hello") {
		t.Fatalf("user text row = %q, want > prefix", lines[1])
	}

	model.Update(tea.WindowSizeMsg{Width: 8, Height: 8})
	assertSessionTUIBlockWidth(t, model.renderUserBlock("hello"), 8)
}

func TestSessionTUIComposerUsesFullWidthPadding(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 14, Height: 8})
	model.ready = true
	model.input.SetValue("draft")

	composer := model.composerView()
	assertSessionTUIBlockWidth(t, composer, 14)
	lines := strings.Split(stripSessionTUIANSI(composer), "\n")
	if len(lines) != sessionTUIComposerHeight {
		t.Fatalf("composer has %d rows, want %d: %q", len(lines), sessionTUIComposerHeight, lines)
	}
	if !strings.HasPrefix(lines[1], "> draft") {
		t.Fatalf("composer text row = %q, want > prefix", lines[1])
	}
	viewLines := strings.Split(stripSessionTUIANSI(model.View()), "\n")
	if len(viewLines) != sessionTUIFooterHeight {
		t.Fatalf("terminal view has %d rows, want %d: %q", len(viewLines), sessionTUIFooterHeight, viewLines)
	}
	if gap := viewLines[0]; gap != "" {
		t.Fatalf("row before composer = %q, want an unstyled blank row", gap)
	}
}

func TestSessionTUIQueueStaysAboveComposerUntilAccepted(t *testing.T) {
	model, _ := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.ready = true
	model.Update(tea.WindowSizeMsg{Width: 14, Height: 10})

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-1", Text: "waiting"})
	view := stripSessionTUIANSI(model.View())
	if !strings.Contains(view, "Queued") || !strings.Contains(view, "> waiting") {
		t.Fatalf("terminal view = %q, want queued user block", view)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	if view := stripSessionTUIANSI(model.View()); strings.Contains(view, "waiting") {
		t.Fatalf("accepted message remains in the inline view: %q", view)
	}
	if got := strings.Join(*history, "\n"); !strings.Contains(stripSessionTUIANSI(got), "> waiting") {
		t.Fatalf("native history = %q, want accepted user message", got)
	}
}

func TestSessionTUIAddsBlankRowAfterUserBlock(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 12, Height: 8})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "hello"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "reply"})

	lines := strings.Split(stripSessionTUIANSI(model.renderTranscript()), "\n")
	if len(lines) != 5 {
		t.Fatalf("transcript has %d rows, want 5: %q", len(lines), lines)
	}
	if lines[3] != "" {
		t.Fatalf("row after user block = %q, want an unstyled blank row", lines[3])
	}
}

func TestSessionTUIKeepsInputWhileAssistantStreams(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	model.input.SetValue("typed during response")

	if commands := model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "streaming response"}); commands.ui == nil {
		t.Fatal("assistant delta did not schedule a refresh")
	}
	model.Update(sessionTUIRefreshMsg{})

	if got := model.input.Value(); got != "typed during response" {
		t.Fatalf("input after assistant delta = %q, want preserved draft", got)
	}
	view := stripSessionTUIANSI(model.View())
	for _, want := range []string{"streaming response", "> typed during response"} {
		if !strings.Contains(view, want) {
			t.Fatalf("terminal view = %q, want %q", view, want)
		}
	}
}

func TestSessionTUIBatchesLoadedHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "loaded question"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "loaded answer"})
	if len(*history) != 0 {
		t.Fatalf("history committed before history end: %q", *history)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	if len(*history) != 1 {
		t.Fatalf("history was committed in %d batches, want 1: %q", len(*history), *history)
	}
	view := stripSessionTUIANSI((*history)[0])
	for _, want := range []string{"loaded question", "loaded answer"} {
		if !strings.Contains(view, want) {
			t.Fatalf("history view = %q, want %q", view, want)
		}
	}
	if model.committed != len(model.blocks) {
		t.Fatalf("committed blocks = %d, want %d", model.committed, len(model.blocks))
	}
	if view := stripSessionTUIANSI(model.View()); strings.Contains(view, "loaded question") || strings.Contains(view, "loaded answer") {
		t.Fatalf("committed history remains in inline view: %q", view)
	}
}

func TestSessionTUICoalescesAssistantDeltaRefreshes(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	if commands := model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "one"}); commands.ui == nil {
		t.Fatal("first delta did not schedule a refresh")
	}
	if commands := model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: " two"}); commands.ui != nil {
		t.Fatal("second delta scheduled a duplicate refresh")
	}
	model.Update(sessionTUIRefreshMsg{})
	if view := stripSessionTUIANSI(model.activeView); !strings.Contains(view, "one two") {
		t.Fatalf("refreshed view = %q, want combined deltas", view)
	}
}

func TestSessionTUICommitsOnlyFinalizedBlocks(t *testing.T) {
	model, _ := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.ready = true

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "active response"})
	model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Text: "Reconnecting"})
	if len(*history) != 0 {
		t.Fatalf("mutable response committed to history: %q", *history)
	}
	view := stripSessionTUIANSI(model.View())
	for _, want := range []string{"active response", "Reconnecting"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inline view = %q, want %q", view, want)
		}
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "active response"})
	if len(*history) != 1 {
		t.Fatalf("finalized response was committed in %d batches, want 1: %q", len(*history), *history)
	}
	committed := stripSessionTUIANSI((*history)[0])
	if strings.Index(committed, "active response") > strings.Index(committed, "Reconnecting") {
		t.Fatalf("committed blocks are out of order: %q", committed)
	}
	if view := stripSessionTUIANSI(model.View()); strings.Contains(view, "active response") || strings.Contains(view, "Reconnecting") {
		t.Fatalf("finalized blocks remain in inline view: %q", view)
	}
}

func TestSessionTUIHistoryWritesAreSerialized(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.resize(12, 8)
	writes := captureAsynchronousSessionTUIHistory(model)
	model.ready = true

	commands := model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "hello"})
	if commands.history == nil {
		t.Fatal("user message did not start a history write")
	}
	if model.committed != 0 || model.historyQueued != 1 {
		t.Fatalf("history positions = committed %d, queued %d; want 0, 1", model.committed, model.historyQueued)
	}
	if view := stripSessionTUIANSI(model.View()); !strings.Contains(view, "hello") {
		t.Fatalf("unacknowledged history missing from inline view: %q", view)
	}

	reflow := model.resize(10, 8)
	if reflow == nil {
		t.Fatal("resize did not schedule a history reflow")
	}
	model.Update(reflow())
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "reply"})
	if len(*writes) != 1 {
		t.Fatalf("concurrent history writes started before acknowledgment: %q", *writes)
	}

	model.finishHistoryWrite(model.historyWrites[0].id)
	if len(*writes) != 2 || !strings.HasPrefix((*writes)[1], sessionTUIClearHistory) {
		t.Fatalf("second history write = %q, want resize replay", *writes)
	}
	if model.committed != 1 {
		t.Fatalf("committed blocks after first acknowledgment = %d, want 1", model.committed)
	}

	model.finishHistoryWrite(model.historyWrites[0].id)
	if len(*writes) != 3 || !strings.Contains((*writes)[2], "reply") {
		t.Fatalf("third history write = %q, want assistant reply", *writes)
	}
	if strings.Contains((*writes)[1], "reply") {
		t.Fatalf("resize replay included a block queued afterward: %q", (*writes)[1])
	}
	model.finishHistoryWrite(model.historyWrites[0].id)
	if model.committed != 2 || len(model.historyWrites) != 0 {
		t.Fatalf("final history state = committed %d, queued writes %d; want 2, 0", model.committed, len(model.historyWrites))
	}
}

func TestSessionTUIQuitWaitsForHistoryAcknowledgment(t *testing.T) {
	model, _ := newSessionTUITestModel()
	captureAsynchronousSessionTUIHistory(model)
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "final response"})

	if cmd := model.quit(); cmd != nil {
		t.Fatal("quit completed before the pending history write")
	}
	if !model.quitRequested || !model.quitting {
		t.Fatal("quit did not enter the waiting state")
	}
	cmd := model.finishHistoryWrite(model.historyWrites[0].id)
	if cmd == nil {
		t.Fatal("history acknowledgment did not resume quit")
	}
	message := cmd()
	if _, ok := message.(tea.QuitMsg); !ok {
		t.Fatalf("history acknowledgment returned %T, want tea.QuitMsg", message)
	}
}

func TestSessionTUIAccumulatesAssistantDeltasInBuffer(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	const chunks = 1000
	for range chunks {
		model.appendAssistantDelta("x")
	}

	block := &model.blocks[0]
	if block.stream == nil {
		t.Fatal("streaming assistant block does not have a buffer")
	}
	if block.text != "" {
		t.Fatalf("streaming assistant text was copied into an immutable string: %q", block.text)
	}
	if got := block.stream.Len(); got != chunks {
		t.Fatalf("streaming buffer length = %d, want %d", got, chunks)
	}
	model.refreshActiveView()
	if view := stripSessionTUIANSI(model.activeView); strings.Count(view, "x") != chunks {
		t.Fatalf("streaming view rendered %d characters, want %d", strings.Count(view, "x"), chunks)
	}
	model.finishStreaming()
	if block.stream != nil || block.text != strings.Repeat("x", chunks) {
		t.Fatal("finalized assistant block did not retain the streamed response")
	}
}

func TestSessionTUIReconnectNoticesKeepStreamingResponseTogether(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "streamed"})
	model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Text: "Reconnecting"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventRuntimeRecovered, Text: "Runtime recovered"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: " response"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "streamed response"})

	rendered := stripSessionTUIANSI(model.renderTranscript())
	if got := strings.Count(rendered, "streamed response"); got != 1 {
		t.Fatalf("streamed response rendered %d times, want once: %q", got, rendered)
	}
	for _, want := range []string{"Reconnecting", "Runtime recovered"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("transcript = %q, want %q", rendered, want)
		}
	}
}

func TestSessionTUIQueuedUserMessagePreservesActiveResponse(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "active"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "follow up"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: " response"})

	if got := model.blocks[0].stream.String(); got != "active response" {
		t.Fatalf("active assistant block = %q, want active response", got)
	}
	transcript := stripSessionTUIANSI(model.renderTranscript())
	if strings.Contains(transcript, "follow up") || strings.Contains(transcript, "Queued") {
		t.Fatalf("queued message rendered in transcript: %q", transcript)
	}
	queue := stripSessionTUIANSI(model.queueView())
	for _, want := range []string{"> follow up", "Queued"} {
		if !strings.Contains(queue, want) {
			t.Fatalf("queue = %q, want %q", queue, want)
		}
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventToolStarted, ToolName: "search"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-2"})
	accepted := stripSessionTUIANSI(model.renderTranscript())
	if strings.Contains(accepted, "Queued") {
		t.Fatalf("accepted transcript still marks message queued: %q", accepted)
	}
	if got := strings.Count(accepted, "follow up"); got != 1 {
		t.Fatalf("accepted message rendered %d times, want once: %q", got, accepted)
	}
	if strings.Index(accepted, "search") > strings.Index(accepted, "follow up") {
		t.Fatalf("accepted message appears before prior turn output: %q", accepted)
	}
	if queue := stripSessionTUIANSI(model.queueView()); queue != "" {
		t.Fatalf("accepted message remains queued: %q", queue)
	}
}

func TestSessionTUICompletedQueuedTurnLoadsAsAccepted(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-1", Text: "loaded message"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	rendered := stripSessionTUIANSI(model.renderTranscript())
	if strings.Contains(rendered, "Queued") {
		t.Fatalf("completed history still marks message queued: %q", rendered)
	}
	if !strings.Contains(rendered, "> loaded message") {
		t.Fatalf("completed history = %q, want accepted user message", rendered)
	}
}

func TestSessionTUIUnstartedTurnRemainsQueuedAfterHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "still waiting"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	if transcript := stripSessionTUIANSI(model.renderTranscript()); strings.Contains(transcript, "still waiting") {
		t.Fatalf("unstarted message rendered in transcript: %q", transcript)
	}
	queue := stripSessionTUIANSI(model.queueView())
	for _, want := range []string{"> still waiting", "Queued"} {
		if !strings.Contains(queue, want) {
			t.Fatalf("queue = %q, want %q", queue, want)
		}
	}
}

func TestSessionTUIQueuedMessagesKeepSubmissionOrder(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "first queued"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-3", Text: "second queued"})

	queue := stripSessionTUIANSI(model.queueView())
	if strings.Index(queue, "first queued") > strings.Index(queue, "second queued") {
		t.Fatalf("queued messages are out of order: %q", queue)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-2"})
	if transcript := stripSessionTUIANSI(model.renderTranscript()); !strings.Contains(transcript, "> first queued") {
		t.Fatalf("accepted message missing from transcript: %q", transcript)
	}
	queue = stripSessionTUIANSI(model.queueView())
	if strings.Contains(queue, "first queued") || !strings.Contains(queue, "second queued") {
		t.Fatalf("remaining queue = %q, want only second message", queue)
	}
}

func TestSessionTUIReusesRenderedBlocks(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "first"})
	model.blocks[0].rendered = "cached first"
	model.blocks[0].dirty = false

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "second"})
	if got := model.blocks[0].rendered; got != "cached first" {
		t.Fatalf("completed block was rendered again: %q", got)
	}
}

func TestSessionTUIResizeRebuildsNativeHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.resize(12, 8)
	history := captureSessionTUIHistory(model)
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "hello"})
	if len(*history) != 1 {
		t.Fatalf("initial history writes = %d, want 1: %q", len(*history), *history)
	}

	staleCmd := model.resize(10, 8)
	cmd := model.resize(8, 8)
	if staleCmd == nil {
		t.Fatal("first resize did not schedule history reflow")
	}
	if cmd == nil {
		t.Fatal("second resize did not schedule history reflow")
	}
	staleMessage, ok := staleCmd().(sessionTUIReflowMsg)
	if !ok {
		t.Fatalf("first resize command returned %T, want sessionTUIReflowMsg", staleMessage)
	}
	model.Update(staleMessage)
	if len(*history) != 1 {
		t.Fatalf("stale resize replayed history: %q", *history)
	}
	message, ok := cmd().(sessionTUIReflowMsg)
	if !ok {
		t.Fatalf("resize command returned %T, want sessionTUIReflowMsg", message)
	}
	model.Update(message)
	if len(*history) != 2 {
		t.Fatalf("history writes after resize = %d, want 2: %q", len(*history), *history)
	}
	reflowed := (*history)[1]
	if !strings.HasPrefix(reflowed, sessionTUIClearHistory) {
		t.Fatalf("reflowed history does not clear prior scrollback: %q", reflowed)
	}
	rendered := strings.TrimSuffix(strings.TrimPrefix(reflowed, sessionTUIClearHistory), "\n")
	assertSessionTUIBlockWidth(t, rendered, 8)
}

func TestSessionTUIDiagnosticRendersBeforeHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Text: "Waiting for Session"})
	if view := stripSessionTUIANSI(model.activeView); !strings.Contains(view, "Waiting for Session") {
		t.Fatalf("diagnostic view = %q", view)
	}
}

func TestSessionTUIForcedColorIgnoresNoColorEnvironment(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	renderer := newSessionTUIRenderer(io.Discard, true)
	if got := renderer.ColorProfile(); got != termenv.ANSI256 {
		t.Fatalf("forced color profile = %v, want ANSI256", got)
	}
}

func TestSessionTUIScreen256ColorUsesDetectedBackground(t *testing.T) {
	t.Setenv("TERM", "screen-256color")
	t.Setenv("COLORTERM", "")
	renderer := newSessionTUIRenderer(io.Discard, true)
	if got := renderer.ColorProfile(); got != termenv.ANSI256 {
		t.Fatalf("screen color profile = %v, want ANSI256", got)
	}

	tests := []struct {
		name       string
		background sessionTUIRGB
		want       lipgloss.Color
	}{
		{name: "dark", background: sessionTUIRGB{}, want: lipgloss.Color("#1e1e1e")},
		{name: "light", background: sessionTUIRGB{red: 255, green: 255, blue: 255}, want: lipgloss.Color("#f4f4f4")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			styles := newSessionTUIStyles(renderer, true, &sessionTUIDefaultColors{background: test.background})
			if got := styles.base.GetBackground(); got != (lipgloss.NoColor{}) {
				t.Errorf("base background = %v, want terminal default", got)
			}
			if got := styles.user.GetBackground(); got != test.want {
				t.Errorf("user and composer background = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSessionTUIScreen256ColorLeavesUnknownBackgroundUnstyled(t *testing.T) {
	t.Setenv("TERM", "screen-256color")
	renderer := newSessionTUIRenderer(io.Discard, true)
	styles := newSessionTUIStyles(renderer, true, nil)

	if got := styles.user.GetBackground(); got != (lipgloss.NoColor{}) {
		t.Fatalf("unknown user and composer background = %v, want terminal default", got)
	}
}

func TestSessionTUIDoesNotStyleWhenColorDisabled(t *testing.T) {
	renderer := newSessionTUIRenderer(io.Discard, false)
	styles := newSessionTUIStyles(renderer, false, &sessionTUIDefaultColors{background: sessionTUIRGB{}})

	if got := styles.base.Render("assistant") + styles.user.Render("user"); got != "assistantuser" {
		t.Fatalf("disabled color output = %q, want unstyled output", got)
	}
}

func TestParseSessionTUIDefaultColors(t *testing.T) {
	colors := parseSessionTUIDefaultColors([]byte("typed\x1b]11;rgba:00/80/ff/ff\x1b\\noise\x1b]10;rgb:eeee/eeee/eeee\a"))
	if colors == nil {
		t.Fatal("default colors were not parsed")
	}
	if want := (sessionTUIRGB{red: 238, green: 238, blue: 238}); colors.foreground != want {
		t.Errorf("foreground = %#v, want %#v", colors.foreground, want)
	}
	if want := (sessionTUIRGB{red: 0, green: 128, blue: 255}); colors.background != want {
		t.Errorf("background = %#v, want %#v", colors.background, want)
	}
}

func TestParseSessionTUIDefaultColorsRejectsIncompleteResponse(t *testing.T) {
	for _, response := range []string{
		"\x1b]10;rgb:eeee/eeee/eeee\x1b\\",
		"\x1b]10;rgb:eeee/eeee/eeee\x1b\\\x1b]11;rgb:nope\a",
		"\x1b]10;rgb:eeee/eeee/eeee\x1b\\\x1b]11;rgb:1111/1111/1111",
	} {
		if got := parseSessionTUIDefaultColors([]byte(response)); got != nil {
			t.Errorf("parseSessionTUIDefaultColors(%q) = %#v, want nil", response, got)
		}
	}
}

func TestSessionTUISubmittedTextRendersFromUserEventOnce(t *testing.T) {
	model, requests := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.ready = true
	model.input.SetValue("hello")

	if cmd := model.submitInput(); cmd != nil {
		t.Fatal("submitInput() returned a command for a regular message")
	}
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "message" || request.Text != "hello" {
		t.Fatalf("submitted request = %#v, want message hello", request)
	}
	if strings.Contains(stripSessionTUIANSI(model.View()), "hello") {
		t.Fatalf("submitted text rendered before user event: %q", stripSessionTUIANSI(model.View()))
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "hello"})
	if got := strings.Count(stripSessionTUIANSI(strings.Join(*history, "\n")), "hello"); got != 1 {
		t.Fatalf("submitted text committed %d times, want once: %q", got, *history)
	}
	if view := stripSessionTUIANSI(model.View()); strings.Contains(view, "hello") {
		t.Fatalf("submitted text remains in inline view after commit: %q", view)
	}
}

func TestSessionTUIInputHistoryRestoresDraft(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	for _, value := range []string{"first", "second"} {
		model.input.SetValue(value)
		if cmd := model.submitInput(); cmd != nil {
			t.Fatalf("submitInput(%q) returned a command", value)
		}
	}
	model.input.SetValue("draft")

	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := model.input.Value(); got != "second" {
		t.Fatalf("first history value = %q, want second", got)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := model.input.Value(); got != "first" {
		t.Fatalf("second history value = %q, want first", got)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := model.input.Value(); got != "draft" {
		t.Fatalf("restored input = %q, want draft", got)
	}
}

func TestSessionTUIInputHistoryDoesNotRetainAnswers(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.input.SetValue("/answer input-1 question-1 secret")
	if cmd := model.submitInput(); cmd != nil {
		t.Fatal("submitInput() returned a command")
	}
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "input" {
		t.Fatalf("submitted request type = %q, want input", request.Type)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := model.input.Value(); got != "" {
		t.Fatalf("input history restored secret answer %q", got)
	}
}

func TestSessionTUILoadedAndLiveUserMessagesUseSameBlock(t *testing.T) {
	model, _ := newSessionTUITestModel()
	message := sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "same shape"}

	model.applyEvent(message)
	loaded := model.renderBlock(model.blocks[0])
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.applyEvent(message)
	live := model.renderBlock(model.blocks[len(model.blocks)-1])

	if loaded != live {
		t.Fatalf("loaded user block = %q, live user block = %q", loaded, live)
	}
}

func TestSessionTUIDiffIsIndented(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	rendered := stripSessionTUIANSI(model.renderDiff("diff --git a/file b/file\n-old\n+new"))
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("diff line is not indented: %q in %q", line, rendered)
		}
	}
}

func newSessionTUITestModel() (*sessionTUIModel, *bytes.Buffer) {
	requests := &bytes.Buffer{}
	return newSessionTUIModel(
		json.NewDecoder(strings.NewReader("")),
		json.NewEncoder(requests),
		io.Discard,
		false,
		nil,
		nil,
	), requests
}

func captureSessionTUIHistory(model *sessionTUIModel) *[]string {
	history := []string{}
	model.printHistory = func(rendered string) tea.Cmd {
		history = append(history, rendered)
		return nil
	}
	return &history
}

func captureAsynchronousSessionTUIHistory(model *sessionTUIModel) *[]string {
	history := []string{}
	model.printHistory = func(rendered string) tea.Cmd {
		history = append(history, rendered)
		return func() tea.Msg { return nil }
	}
	return &history
}

func stripSessionTUIANSI(text string) string {
	return sessionTUIANSISequence.ReplaceAllString(text, "")
}

func assertSessionTUIBlockWidth(t *testing.T, block string, width int) {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("line width = %d, want %d: %q", got, width, line)
		}
	}
}
