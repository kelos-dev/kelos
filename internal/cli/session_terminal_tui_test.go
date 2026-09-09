package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

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
	if len(lines) != model.composerHeight() {
		t.Fatalf("composer has %d rows, want %d: %q", len(lines), model.composerHeight(), lines)
	}
	if !strings.HasPrefix(lines[1], "> draft") {
		t.Fatalf("composer text row = %q, want > prefix", lines[1])
	}
	viewLines := strings.Split(stripSessionTUIANSI(model.View()), "\n")
	if len(viewLines) != model.footerHeight() {
		t.Fatalf("terminal view has %d rows, want %d: %q", len(viewLines), model.footerHeight(), viewLines)
	}
	if gap := viewLines[0]; gap != "" {
		t.Fatalf("row before composer = %q, want an unstyled blank row", gap)
	}
	if status := strings.TrimSpace(viewLines[len(viewLines)-1]); status != "Ready" {
		t.Fatalf("status bar = %q, want Ready", status)
	}
}

func TestSessionTUIComposerGapFollowsProgress(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
	model.ready = true
	model.connectionStatus = ""
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})

	lines := strings.Split(stripSessionTUIANSI(model.View()), "\n")
	if progress := strings.TrimSpace(lines[0]); !strings.HasPrefix(progress, "• Working (") {
		t.Fatalf("first footer row = %q, want working progress", progress)
	}
	if gap := lines[1]; gap != "" {
		t.Fatalf("row above composer = %q, want an unstyled blank row", gap)
	}
	if prompt := strings.TrimSpace(lines[3]); prompt != ">" {
		t.Fatalf("composer prompt row = %q, want >", prompt)
	}
}

func TestSessionTUIStatusBarShowsRuntimeAndWorkspaceDetails(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 180, Height: 12})
	model.applyEvent(sessionruntime.Event{
		Type: sessionruntime.EventRuntimeStatus,
		Runtime: &sessionruntime.RuntimeStatus{
			SessionName:       "fix-session-tui",
			AgentType:         "codex",
			Model:             "gpt-5.6-sol",
			Effort:            "xhigh",
			WorkingDir:        "/home/agent/workspace/kelos",
			HomeDir:           "/home/agent",
			Branch:            "agent/fix-session-tui-multiline-prompt",
			PullRequestNumber: 1547,
			Usage: &sessionruntime.RuntimeUsage{
				ContextWindow: 200_000,
			},
			WeeklyLimit: &sessionruntime.RuntimeRateLimit{UsedPercent: 31},
		},
	})

	statusBar := strings.TrimSpace(stripSessionTUIANSI(model.statusBarView()))
	want := "fix-session-tui · codex · gpt-5.6-sol xhigh · ~/workspace/kelos · agent/fix-session-tui-multiline-prompt · PR #1547 · Context 0% used · weekly 69% left · 0 in · 0 out"
	if statusBar != want {
		t.Fatalf("status bar = %q, want %q", statusBar, want)
	}
}

func TestSessionTUIStatusBarPrioritizesModelAndPathAtNarrowWidths(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.runtimeStatus = sessionruntime.RuntimeStatus{
		SessionName:       "fix-session-tui",
		AgentType:         "codex",
		Model:             "gpt-5.6-sol",
		Effort:            "xhigh",
		WorkingDir:        "/home/agent/workspace/kelos",
		HomeDir:           "/home/agent",
		Branch:            "agent/fix-session-tui-multiline-prompt",
		PullRequestNumber: 1547,
		Usage:             &sessionruntime.RuntimeUsage{ContextWindow: 200_000},
		WeeklyLimit:       &sessionruntime.RuntimeRateLimit{UsedPercent: 31},
	}
	model.Update(tea.WindowSizeMsg{Width: 50, Height: 8})

	statusBar := stripSessionTUIANSI(model.statusBarView())
	assertSessionTUIBlockWidth(t, statusBar, 50)
	for _, want := range []string{"gpt-5.6-sol xhigh", "~/workspace/kelos"} {
		if !strings.Contains(statusBar, want) {
			t.Fatalf("narrow status bar = %q, want %q", statusBar, want)
		}
	}
	for _, omitted := range []string{"fix-session-tui", "Context", "weekly", " in", " out"} {
		if strings.Contains(statusBar, omitted) {
			t.Fatalf("narrow status bar retained %q: %q", omitted, statusBar)
		}
	}
}

func TestFormatSessionTUITokens(t *testing.T) {
	for _, test := range []struct {
		value int64
		want  string
	}{
		{value: 0, want: "0"},
		{value: 999, want: "999"},
		{value: 1_200, want: "1.2K"},
		{value: 12_345, want: "12.3K"},
		{value: 100_000, want: "100K"},
		{value: 1_250_000, want: "1.25M"},
	} {
		if got := formatSessionTUITokens(test.value); got != test.want {
			t.Errorf("formatSessionTUITokens(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestSessionTUIContextUsedPercent(t *testing.T) {
	for _, test := range []struct {
		usage sessionruntime.RuntimeUsage
		want  int64
	}{
		{usage: sessionruntime.RuntimeUsage{ContextWindow: 200_000}, want: 0},
		{usage: sessionruntime.RuntimeUsage{ContextTokens: 106_000, ContextWindow: 200_000}, want: 50},
		{usage: sessionruntime.RuntimeUsage{ContextTokens: 250_000, ContextWindow: 200_000}, want: 100},
	} {
		if got := sessionTUIContextUsedPercent(test.usage); got != test.want {
			t.Errorf("sessionTUIContextUsedPercent(%#v) = %d, want %d", test.usage, got, test.want)
		}
	}
}

func TestSessionTUIPendingMessageStaysAboveComposerUntilAccepted(t *testing.T) {
	model, _ := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.ready = true
	model.Update(tea.WindowSizeMsg{Width: 14, Height: 10})

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-1", Text: "waiting"})
	view := stripSessionTUIANSI(model.View())
	if !strings.Contains(view, "Pending") || !strings.Contains(view, "> waiting") {
		t.Fatalf("terminal view = %q, want pending user block", view)
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

func TestSessionTUIAssistantBlockUsesCodexStyleBullet(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 12, Height: 8})

	rendered := stripSessionTUIANSI(model.renderBlock(sessionTUIBlock{
		kind: sessionTUIBlockAssistant,
		text: "first line\nsecond",
	}))
	lines := strings.Split(rendered, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	if want := "• first line\n  second"; strings.Join(lines, "\n") != want {
		t.Fatalf("assistant block = %q, want %q", rendered, want)
	}
}

func TestSessionTUIStreamingAssistantBlockUsesCodexStyleBullet(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "working"})
	model.Update(sessionTUIRefreshMsg{})

	if view := stripSessionTUIANSI(model.View()); !strings.Contains(view, "• working") {
		t.Fatalf("streaming assistant view = %q, want bullet prefix", view)
	}
}

func TestSessionTUIToolBlockShowsCodexStyleOutputPreview(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 24, Height: 12})
	model.applyEvent(sessionruntime.Event{
		Type:     sessionruntime.EventToolStarted,
		ToolID:   "tool-1",
		ToolName: "make test",
	})
	model.applyEvent(sessionruntime.Event{
		Type:   sessionruntime.EventToolCompleted,
		ToolID: "tool-1",
		Output: "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8",
		Status: "completed",
	})

	lines := strings.Split(stripSessionTUIANSI(model.renderTranscript()), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	want := []string{
		"• Ran make test",
		"  └ line 1",
		"    line 2",
		"    … +4 lines",
		"    line 7",
		"    line 8",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("tool block = %#v, want %#v", lines, want)
	}
}

func TestSessionTUIToolOutputStripsTerminalControlSequences(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{
		Type:     sessionruntime.EventToolStarted,
		ToolID:   "tool-1",
		ToolName: "command",
	})
	model.applyEvent(sessionruntime.Event{
		Type:   sessionruntime.EventToolCompleted,
		ToolID: "tool-1",
		Output: "safe\x1b]52;c;Y2xpcGJvYXJk\x07\n\x1b[2Jspoof\rrewritten\x00",
		Status: "completed",
	})

	rendered := model.renderTranscript()
	for _, unsafe := range []string{"\x1b", "\x07", "\r", "\x00", "Y2xpcGJvYXJk"} {
		if strings.Contains(rendered, unsafe) {
			t.Fatalf("tool output contains unsafe terminal content %q: %q", unsafe, rendered)
		}
	}
	for _, want := range []string{"safe", "spoofrewritten"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("tool output = %q, want %q", rendered, want)
		}
	}
}

func TestSessionTUIStreamsToolOutputIntoCompletion(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventToolStarted, ToolID: "tool-1", ToolName: "make test"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventToolDelta, ToolID: "tool-1", Output: "first\n"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventToolDelta, ToolID: "tool-1", Output: "second\n"})

	if rendered := stripSessionTUIANSI(model.renderTranscript()); !strings.Contains(rendered, "first") || !strings.Contains(rendered, "second") {
		t.Fatalf("streamed tool output = %q", rendered)
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventToolCompleted, ToolID: "tool-1", Output: "first\nsecond\n", Status: "completed"})
	rendered := stripSessionTUIANSI(model.renderTranscript())
	if strings.Count(rendered, "first") != 1 || strings.Count(rendered, "second") != 1 {
		t.Fatalf("completed streamed tool output = %q", rendered)
	}
}

func TestSessionTUIRendersGoalStatus(t *testing.T) {
	model, _ := newSessionTUITestModel()
	budget := int64(1000)
	model.applyEvent(sessionruntime.Event{
		Type:   sessionruntime.EventGoalUpdated,
		Status: "active",
		Goal: &sessionruntime.Goal{
			Objective:   "Improve coverage",
			Status:      "active",
			TokenBudget: &budget,
			TokensUsed:  125,
		},
	})

	rendered := stripSessionTUIANSI(model.renderTranscript())
	if !strings.Contains(rendered, "Goal active (125/1000 tokens): Improve coverage") {
		t.Fatalf("goal status = %q", rendered)
	}
}

func TestSessionTUIAttributesParallelToolCompletion(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	model.applyEvent(sessionruntime.Event{
		Type:     sessionruntime.EventToolStarted,
		ToolID:   "tool-a",
		ToolName: "command A",
	})
	model.applyEvent(sessionruntime.Event{
		Type:     sessionruntime.EventToolStarted,
		ToolID:   "tool-b",
		ToolName: "command B",
	})
	model.applyEvent(sessionruntime.Event{
		Type:   sessionruntime.EventToolCompleted,
		ToolID: "tool-a",
		Output: "A output",
		Status: "completed",
	})

	rendered := stripSessionTUIANSI(model.renderTranscript())
	if !strings.Contains(rendered, "  └ command A: A output") {
		t.Fatalf("parallel tool transcript = %q, want completion attributed to command A", rendered)
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

func TestSessionTUIShowsTurnProgressUntilCompletion(t *testing.T) {
	model, _ := newSessionTUITestModel()
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	model.now = func() time.Time { return now }
	model.connectionStarted = now
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); got != "• Connecting (0s)" {
		t.Fatalf("connecting progress = %q", got)
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	if progress := model.progressView(); progress != "" {
		t.Fatalf("idle progress = %q, want empty", progress)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); got != "• Working (0s • esc to interrupt)" {
		t.Fatalf("working progress = %q", got)
	}

	now = now.Add(65 * time.Second)
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); got != "• Working (1m 05s • esc to interrupt)" {
		t.Fatalf("elapsed progress = %q", got)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventInputRequested, TurnID: "turn-1", InputID: "input-1"})
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); got != "• Waiting for input (1m 05s • esc to interrupt)" {
		t.Fatalf("input progress = %q", got)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventInputResolved, TurnID: "turn-1", InputID: "input-1"})
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); !strings.HasPrefix(got, "• Working") {
		t.Fatalf("resumed progress = %q", got)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed"})
	if progress := model.progressView(); progress != "" {
		t.Fatalf("completed progress = %q, want empty", progress)
	}
	if transcript := stripSessionTUIANSI(model.renderTranscript()); !strings.Contains(transcript, "─ Worked for 1m 05s ─") {
		t.Fatalf("completed transcript = %q, want live fallback duration", transcript)
	}
}

func TestSessionTUIRendersTurnDurationSeparator(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.Update(tea.WindowSizeMsg{Width: 48, Height: 12})
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(5*time.Minute + 19*time.Second)

	model.applyEvent(sessionruntime.Event{
		Type:      sessionruntime.EventTurnStarted,
		TurnID:    "turn-1",
		Timestamp: &startedAt,
	})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "First reply"})
	model.applyEvent(sessionruntime.Event{
		Type:      sessionruntime.EventTurnCompleted,
		TurnID:    "turn-1",
		Timestamp: &completedAt,
	})

	lines := strings.Split(stripSessionTUIANSI(model.renderTranscript()), "\n")
	separator := lines[len(lines)-1]
	if !strings.HasPrefix(separator, "─ Worked for 5m 19s ─") {
		t.Fatalf("turn separator = %q, want worked duration", separator)
	}
	assertSessionTUIBlockWidth(t, separator, 48)
}

func TestSessionTUIOmitsUnknownHistoryDuration(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "Historical reply"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	if transcript := stripSessionTUIANSI(model.renderTranscript()); strings.Contains(transcript, "Worked for") {
		t.Fatalf("historical transcript = %q, want unknown duration omitted", transcript)
	}
}

func TestSessionTUIOmitsRuntimeRecoveryDuration(t *testing.T) {
	model, _ := newSessionTUITestModel()
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Hour)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Timestamp: &startedAt})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventRuntimeRecovered, Text: "Session runtime restarted"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventInputResolved, TurnID: "turn-1", InputID: "input-1", Status: "cancelled"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "interrupted", Timestamp: &completedAt})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	if transcript := stripSessionTUIANSI(model.renderTranscript()); strings.Contains(transcript, "Worked for") {
		t.Fatalf("historical transcript = %q, want runtime recovery duration omitted", transcript)
	}
}

func TestSessionTUIRestoresTurnProgressFromHistoryTimestamp(t *testing.T) {
	model, _ := newSessionTUITestModel()
	now := time.Date(2026, time.July, 23, 12, 1, 5, 0, time.UTC)
	startedAt := now.Add(-65 * time.Second)
	model.now = func() time.Time { return now }

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.applyEvent(sessionruntime.Event{
		Type:      sessionruntime.EventTurnStarted,
		TurnID:    "turn-1",
		Timestamp: &startedAt,
	})

	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); got != "• Working (1m 05s • esc to interrupt)" {
		t.Fatalf("restored progress = %q", got)
	}
}

func TestSessionTUIEscInterruptsActiveTurn(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})

	model.Update(tea.KeyMsg{Type: tea.KeyEsc})

	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "interrupt" {
		t.Fatalf("escape request = %#v, want interrupt", request)
	}
	progress := strings.TrimSpace(stripSessionTUIANSI(model.progressView()))
	if !strings.HasPrefix(progress, "• Interrupting (") || strings.Contains(progress, "esc to interrupt") {
		t.Fatalf("interrupt progress = %q", progress)
	}

	model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if requests.Len() != 0 {
		t.Fatalf("second escape submitted another request: %q", requests.String())
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventError, Status: "rejected", Text: "no active turn"})
	if progress := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); !strings.HasPrefix(progress, "• Working (") {
		t.Fatalf("rejected interrupt progress = %q", progress)
	}
}

func TestSessionTUIReconnectProgressOverridesActiveTurn(t *testing.T) {
	model, _ := newSessionTUITestModel()
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	model.now = func() time.Time { return now }
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	model.applyEvent(sessionruntime.Event{
		Type:   sessionTerminalEventDiagnostic,
		Status: sessionTerminalStatusReconnecting,
		Text:   "Session connection lost",
	})

	now = now.Add(5 * time.Second)
	model.applyEvent(sessionruntime.Event{
		Type:   sessionTerminalEventDiagnostic,
		Status: sessionTerminalStatusReconnecting,
		Text:   "Still reconnecting",
	})
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); got != "• Reconnecting (5s)" {
		t.Fatalf("reconnect progress = %q", got)
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	if got := strings.TrimSpace(stripSessionTUIANSI(model.progressView())); !strings.HasPrefix(got, "• Working (") {
		t.Fatalf("reconnected progress = %q", got)
	}
}

func TestFormatSessionTUIElapsed(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 59 * time.Second, want: "59s"},
		{elapsed: 60 * time.Second, want: "1m 00s"},
		{elapsed: 61 * time.Second, want: "1m 01s"},
		{elapsed: 2*time.Hour + 3*time.Minute + 9*time.Second, want: "2h 03m 09s"},
	}
	for _, test := range tests {
		if got := formatSessionTUIElapsed(test.elapsed); got != test.want {
			t.Errorf("formatSessionTUIElapsed(%s) = %q, want %q", test.elapsed, got, test.want)
		}
	}
}

func TestSessionTUIProgressStaysOnOneRow(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	model.resize(12, 8)

	progress := model.progressView()
	if strings.Contains(progress, "\n") {
		t.Fatalf("narrow progress wrapped: %q", progress)
	}
	assertSessionTUIBlockWidth(t, progress, 12)
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

func TestSessionTUIKeepsPendingInitialHistoryOutOfManagedView(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.resize(40, 8)
	writes := captureAsynchronousSessionTUIHistory(model)
	question := strings.Repeat("long history line\n", 10)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: question})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "loaded answer"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	if len(*writes) != 1 || !strings.Contains((*writes)[0], "long history line") {
		t.Fatalf("initial history write = %q", *writes)
	}
	view := stripSessionTUIANSI(model.View())
	if strings.Contains(view, "long history line") || strings.Contains(view, "loaded answer") {
		t.Fatalf("pending initial history remains in managed view: %q", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > model.footerHeight()+1 {
		t.Fatalf("managed view has %d lines, want only footer height %d: %q", lines, model.footerHeight(), view)
	}
	if model.historyHiddenUntil != len(model.blocks) {
		t.Fatalf("hidden history boundary = %d, want %d", model.historyHiddenUntil, len(model.blocks))
	}

	model.finishHistoryWrite(model.historyWrites[0].id)
	if model.historyHiddenUntil != 0 || model.committed != len(model.blocks) {
		t.Fatalf("completed history state = hidden %d committed %d, want hidden 0 committed %d", model.historyHiddenUntil, model.committed, len(model.blocks))
	}
}

func TestSessionTUIReportsLimitedHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, HistoryLimited: true, HistoryCursor: "cursor-1"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	if len(*history) != 1 {
		t.Fatalf("history was committed in %d batches, want 1: %q", len(*history), *history)
	}
	if got := stripSessionTUIANSI((*history)[0]); !strings.Contains(got, "/history or Page Up") {
		t.Fatalf("limited history notice = %q", got)
	}
}

func TestSessionTUILoadsAndPrependsOlderHistoryPage(t *testing.T) {
	model, requests := newSessionTUITestModel()
	history := captureSessionTUIHistory(model)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, HistoryLimited: true, HistoryCursor: "cursor-1"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "recent question"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-2"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-2", Text: "recent answer"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-2", Status: "completed"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	model.input.SetValue("/history")
	model.submitInput()
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "history" || request.RequestID == "" || request.HistoryCursor != "cursor-1" {
		t.Fatalf("history request = %#v", request)
	}
	if !model.historyPageLoading || !strings.Contains(stripSessionTUIANSI(model.progressView()), "Loading history") {
		t.Fatalf("history request did not enter loading state: loading=%t progress=%q", model.historyPageLoading, stripSessionTUIANSI(model.progressView()))
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, HistoryPage: true, RequestID: request.RequestID})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-1", Text: "earlier question"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-1", Text: "earlier answer"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd, HistoryPage: true, RequestID: request.RequestID})

	if len(*history) != 2 {
		t.Fatalf("history writes = %d, want initial write and replay: %q", len(*history), *history)
	}
	replayed := (*history)[1]
	if !strings.HasPrefix(replayed, sessionTUIClearHistory) {
		t.Fatalf("older history replay = %q, want terminal history reset", replayed)
	}
	view := stripSessionTUIANSI(replayed)
	for _, want := range []string{"earlier question", "earlier answer", "recent question", "recent answer", "All retained Session history is loaded"} {
		if !strings.Contains(view, want) {
			t.Fatalf("older history replay = %q, want %q", view, want)
		}
	}
	if strings.Index(view, "earlier question") > strings.Index(view, "recent question") {
		t.Fatalf("older history was not prepended: %q", view)
	}
	if model.historyCursor != "" || model.historyPageLoading || model.historyPageReading {
		t.Fatalf("history page state was not completed: cursor=%q loading=%t reading=%t", model.historyCursor, model.historyPageLoading, model.historyPageReading)
	}
}

func TestSessionTUIAppliesStateFromProjectedHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, Text: "active request"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventInputRequested, InputID: "input-1", Questions: []sessionruntime.InputQuestion{{ID: "confirm", Question: "Continue?"}}})
	model.applyEvent(sessionruntime.Event{
		Type: sessionruntime.EventHistoryEnd,
		HistoryState: &sessionruntime.HistoryState{
			ActiveTurnID:      "turn-2",
			ActiveTurnStarted: &startedAt,
			WaitingForInput:   true,
			PendingTurn:       &sessionruntime.HistoryPendingTurn{TurnID: "turn-3", Text: "pending request"},
		},
	})

	if !model.turnActive || model.activeTurnID != "turn-2" || !model.activeTurnStarted.Equal(startedAt) || !model.waitingForInput {
		t.Fatalf("active state = active %t ID %q started %v waiting %t", model.turnActive, model.activeTurnID, model.activeTurnStarted, model.waitingForInput)
	}
	if model.pendingTurnID != "turn-3" || model.pendingTurnText != "pending request" {
		t.Fatalf("pending state = ID %q text %q", model.pendingTurnID, model.pendingTurnText)
	}
	if transcript := stripSessionTUIANSI(model.renderTranscript()); !strings.Contains(transcript, "active request") || !strings.Contains(transcript, "Continue?") {
		t.Fatalf("projected transcript = %q", transcript)
	}
}

func TestSessionTUICancelsPartialHistoryPageOnReconnect(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, HistoryLimited: true, HistoryCursor: "cursor-1"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.historyPageLoading = true
	model.historyRequestID = "history-1"
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, HistoryPage: true, RequestID: "history-1", HistoryCursor: "cursor-2"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-1", Text: "partial page"})

	model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Status: sessionTerminalStatusReconnecting})

	if model.historyPageLoading || model.historyPageReading || len(model.historyPageEvents) != 0 {
		t.Fatalf("partial history page was not discarded: loading=%t reading=%t events=%#v", model.historyPageLoading, model.historyPageReading, model.historyPageEvents)
	}
	if model.historyCursor != "cursor-1" {
		t.Fatalf("history cursor = %q, want retry cursor", model.historyCursor)
	}
	if strings.Contains(model.renderTranscript(), "partial page") {
		t.Fatalf("partial history page was rendered: %q", stripSessionTUIANSI(model.renderTranscript()))
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

func TestSessionTUIPendingUserMessagePreservesActiveResponse(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "active"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "follow up"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: " response"})

	if got := model.blocks[0].stream.String(); got != "active response" {
		t.Fatalf("active assistant block = %q, want active response", got)
	}
	transcript := stripSessionTUIANSI(model.renderTranscript())
	if strings.Contains(transcript, "follow up") || strings.Contains(transcript, "Pending") {
		t.Fatalf("pending message rendered in transcript: %q", transcript)
	}
	pending := stripSessionTUIANSI(model.pendingView())
	for _, want := range []string{"> follow up", "Pending"} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending = %q, want %q", pending, want)
		}
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventToolStarted, ToolName: "search"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-2"})
	accepted := stripSessionTUIANSI(model.renderTranscript())
	if strings.Contains(accepted, "Pending") {
		t.Fatalf("accepted transcript still marks message pending: %q", accepted)
	}
	if got := strings.Count(accepted, "follow up"); got != 1 {
		t.Fatalf("accepted message rendered %d times, want once: %q", got, accepted)
	}
	if strings.Index(accepted, "search") > strings.Index(accepted, "follow up") {
		t.Fatalf("accepted message appears before prior turn output: %q", accepted)
	}
	if pending := stripSessionTUIANSI(model.pendingView()); pending != "" {
		t.Fatalf("accepted message remains pending: %q", pending)
	}
}

func TestSessionTUICompletedPendingTurnLoadsAsAccepted(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-1", Text: "loaded message"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "completed"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	rendered := stripSessionTUIANSI(model.renderTranscript())
	if strings.Contains(rendered, "Pending") {
		t.Fatalf("completed history still marks message pending: %q", rendered)
	}
	if !strings.Contains(rendered, "> loaded message") {
		t.Fatalf("completed history = %q, want accepted user message", rendered)
	}
}

func TestSessionTUIUnstartedTurnRemainsPendingAfterHistory(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "still waiting"})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})

	if transcript := stripSessionTUIANSI(model.renderTranscript()); strings.Contains(transcript, "still waiting") {
		t.Fatalf("unstarted message rendered in transcript: %q", transcript)
	}
	pending := stripSessionTUIANSI(model.pendingView())
	for _, want := range []string{"> still waiting", "Pending"} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending = %q, want %q", pending, want)
		}
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

func TestSessionTUIStatusOnlyDiagnosticDoesNotRenderTranscriptBlock(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{
		Type:   sessionTerminalEventDiagnostic,
		Status: sessionTerminalStatusReconnecting,
	})

	if len(model.blocks) != 0 {
		t.Fatalf("status-only diagnostic rendered %d transcript blocks", len(model.blocks))
	}
	if got := model.connectionStatus; got != sessionTerminalStatusReconnecting {
		t.Fatalf("connection status = %q, want reconnecting", got)
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

func TestSessionTUIUpdatesPendingMessage(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "original", Revision: 1})
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessageUpdated, TurnID: "turn-2", Text: "revised", Revision: 2})

	if model.pendingTurnID != "turn-2" || model.pendingTurnText != "revised" || model.pendingTurnInput != "revised" || model.pendingRevision != 2 {
		t.Fatalf("pending message = ID %q text %q input %q revision %d", model.pendingTurnID, model.pendingTurnText, model.pendingTurnInput, model.pendingRevision)
	}
}

func TestSessionTUIEditsPendingMessageWithAltUp(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{
		Type:        sessionruntime.EventUserMessage,
		TurnID:      "turn-2",
		Text:        "original\nmessage",
		Revision:    3,
		Attachments: []sessionruntime.Attachment{{ID: "attachment-1", Name: "notes.txt"}},
	})

	model.Update(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if model.input.Value() != "original\nmessage" {
		t.Fatalf("recalled input = %q", model.input.Value())
	}
	if model.pendingEditTurnID != "turn-2" || model.pendingEditRev != 3 {
		t.Fatalf("pending edit = ID %q revision %d", model.pendingEditTurnID, model.pendingEditRev)
	}
	if !strings.Contains(model.pendingTurnText, "notes.txt") {
		t.Fatalf("rendered pending message = %q", model.pendingTurnText)
	}

	model.input.SetValue("revised\nmessage")
	if cmd := model.submitInput(); cmd != nil {
		t.Fatal("submitInput() returned a command")
	}
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "message.edit" || request.TurnID != "turn-2" || request.Text != "revised\nmessage" || request.ExpectedRevision != 3 {
		t.Fatalf("edit request = %#v", request)
	}
}

func TestSessionTUIRemovesPendingMessageWithEmptyEdit(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessage, TurnID: "turn-2", Text: "remove this", Revision: 3})
	model.Update(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	model.input.Reset()
	if cmd := model.submitInput(); cmd != nil {
		t.Fatal("submitInput() returned a command")
	}
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "message.remove" || request.TurnID != "turn-2" || request.ExpectedRevision != 3 {
		t.Fatalf("remove request = %#v", request)
	}
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventUserMessageRemoved, TurnID: "turn-2", Revision: 4})
	if model.pendingTurnID != "" || model.pendingTurnText != "" || model.pendingTurnInput != "" || model.pendingRevision != 0 {
		t.Fatalf("pending message remained after removal: ID %q text %q input %q revision %d", model.pendingTurnID, model.pendingTurnText, model.pendingTurnInput, model.pendingRevision)
	}
}

func TestSessionTUIDroppedFileIsAttachedToNextMessage(t *testing.T) {
	path := t.TempDir() + "/screen shot.png"
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})
	decoder := json.NewDecoder(requests)
	var attachmentRequest sessionruntime.ClientRequest
	if err := decoder.Decode(&attachmentRequest); err != nil {
		t.Fatal(err)
	}
	if attachmentRequest.Type != sessionTerminalRequestAttachment || attachmentRequest.Text != path {
		t.Fatalf("attachment request = %#v", attachmentRequest)
	}

	attachment := sessionruntime.Attachment{ID: "attachment-1", Name: "screen shot.png", MediaType: "image/png", SizeBytes: 5}
	model.applyEvent(sessionruntime.Event{Type: sessionTerminalEventAttachmentAdded, Attachments: []sessionruntime.Attachment{attachment}})
	if !strings.Contains(stripSessionTUIANSI(model.composerView()), "Attached: screen shot.png") {
		t.Fatalf("composer = %q", stripSessionTUIANSI(model.composerView()))
	}
	model.input.SetValue("review this")
	if cmd := model.submitInput(); cmd != nil {
		t.Fatal("submitInput() returned a command")
	}
	var message sessionruntime.ClientRequest
	if err := decoder.Decode(&message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "message" || message.Text != "review this" || !reflect.DeepEqual(message.AttachmentIDs, []string{"attachment-1"}) {
		t.Fatalf("message request = %#v", message)
	}
	if len(model.pendingAttachments) != 0 {
		t.Fatalf("pending attachments = %#v", model.pendingAttachments)
	}
}

func TestSessionTUICtrlJInsertsNewlineAndEnterSubmits(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.input.SetValue("first")

	model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	_ = model.composerView()
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})

	if got := model.input.Value(); got != "first\nsecond" {
		t.Fatalf("input after Ctrl+J = %q, want multiline prompt", got)
	}
	if got := model.input.Height(); got != 2 {
		t.Fatalf("composer input height = %d, want 2", got)
	}
	lines := strings.Split(stripSessionTUIANSI(model.composerView()), "\n")
	if len(lines) != 4 {
		t.Fatalf("multiline composer has %d rows, want 4: %q", len(lines), lines)
	}
	rendered := strings.Join(lines, "\n")
	if got := strings.Count(rendered, ">"); got != 1 {
		t.Fatalf("multiline composer has %d prompts, want 1: %q", got, lines)
	}
	for _, want := range []string{"> first", "  second"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("multiline composer = %q, want %q", lines, want)
		}
	}

	model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "message" || request.Text != "first\nsecond" {
		t.Fatalf("submitted request = %#v, want multiline message", request)
	}
	if got := model.input.Height(); got != 1 {
		t.Fatalf("composer input height after submit = %d, want 1", got)
	}
}

func TestSessionTUICtrlJAllowsInputBeyondComposerHeight(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.input.SetValue("line 1")

	for line := 2; line <= sessionTUIComposerMaxVisibleRows+1; line++ {
		model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
		model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(fmt.Sprintf("line %d", line))})
	}

	if got := model.input.LineCount(); got != sessionTUIComposerMaxVisibleRows+1 {
		t.Fatalf("input line count = %d, want %d", got, sessionTUIComposerMaxVisibleRows+1)
	}
	if got := model.input.Height(); got != sessionTUIComposerMaxVisibleRows {
		t.Fatalf("composer input height = %d, want viewport cap %d", got, sessionTUIComposerMaxVisibleRows)
	}
	if got := model.input.Value(); !strings.HasSuffix(got, fmt.Sprintf("line %d", sessionTUIComposerMaxVisibleRows+1)) {
		t.Fatalf("input beyond composer height = %q", got)
	}
	if got := strings.Count(stripSessionTUIANSI(model.composerView()), ">"); got != 1 {
		t.Fatalf("scrolled multiline composer has %d prompts, want 1", got)
	}
}

func TestSessionTUICtrlJKeepsPromptVisibleInShortComposer(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.Update(tea.WindowSizeMsg{Width: 14, Height: 4})
	model.input.SetValue("first")
	model.input.CursorEnd()

	model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	_ = model.composerView()
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})

	view := stripSessionTUIANSI(model.composerView())
	if got := strings.Count(view, ">"); got != 1 {
		t.Fatalf("short multiline composer has %d prompts, want 1: %q", got, view)
	}
	if !strings.Contains(view, "> second") {
		t.Fatalf("short multiline composer = %q, want visible prompt before current line", view)
	}
}

func TestSessionTUICtrlJKeepsWrappedFirstLineVisible(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
	model.Update(tea.WindowSizeMsg{Width: 14, Height: 8})

	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first line wraps")})
	if got := model.input.Height(); got != 2 {
		t.Fatalf("wrapped first line uses %d rows, want 2", got)
	}
	_ = model.composerView()
	model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})

	view := stripSessionTUIANSI(model.composerView())
	if !strings.Contains(view, "> first line") {
		t.Fatalf("multiline composer hid the first wrapped line after Ctrl+J: %q", view)
	}
}

func TestSessionTUICtrlCInterruptsActiveTurnAndQuitsWhenIdle(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})

	if _, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatal("Ctrl+C returned a quit command while a turn was active")
	}
	var request sessionruntime.ClientRequest
	if err := json.NewDecoder(requests).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "interrupt" {
		t.Fatalf("Ctrl+C request = %#v, want interrupt", request)
	}
	if model.quitRequested {
		t.Fatal("Ctrl+C requested terminal exit while a turn was active")
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: "interrupted"})
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C did not quit while idle")
	}
	if message := cmd(); message == nil {
		t.Fatal("Ctrl+C idle quit command returned no message")
	} else if _, ok := message.(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+C idle quit command returned %T, want tea.QuitMsg", message)
	}
}

func TestSessionTUIJournalResetClearsActiveTurn(t *testing.T) {
	model, _ := newSessionTUITestModel()
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1"})
	if !model.turnActive {
		t.Fatal("turn.started did not mark the turn active")
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, JournalID: "original"})
	if !model.turnActive {
		t.Fatal("unchanged journal cleared the active turn")
	}

	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, JournalID: "replacement", Reset: true})

	if model.turnActive {
		t.Fatal("replacement journal did not clear the active turn")
	}
}

func TestSessionTUIInputHistoryRestoresDraft(t *testing.T) {
	model, requests := newSessionTUITestModel()
	model.ready = true
	model.connectionStatus = ""
	model.input.SetValue("draft")

	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	request := readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID, Prompts: []sessionruntime.Prompt{
		{ID: 1, Text: "first"},
		{ID: 2, Text: "second"},
	}})
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
	model.connectionStatus = ""
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
	request = readSessionTUIPromptRequest(t, requests)
	model.applyEvent(sessionruntime.Event{Type: sessionruntime.EventPrompts, RequestID: request.RequestID})
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
