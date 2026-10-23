package sessionruntime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelos-dev/kelos/internal/sessionupdate"
)

func TestSessionLoopCommands(t *testing.T) {
	for _, agent := range []string{"codex", "claude-code", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			journal := NewJournal()
			defer journal.Close()
			provider := &fakeProvider{}
			server := NewServer(Config{AgentType: agent}, journal, provider)
			for _, command := range []string{"/loop", "/loop 5m Check CI", "/loop pause", "/loop resume", "/loop clear"} {
				if err := server.submitClientMessage(t.Context(), command, "request"); err != nil {
					t.Fatalf("%s: %v", command, err)
				}
			}
			events := journal.Snapshot()
			if len(events) != 10 {
				t.Fatalf("Loop command events = %#v", events)
			}
			for index, status := range []string{"empty", "active", "paused", "active", "cleared"} {
				event := events[index*2+1]
				if event.Type != EventLoopUpdated || event.Status != status {
					t.Fatalf("Loop command result = %#v, want %s", event, status)
				}
			}
			if journal.currentLoop() != nil || server.loopActive.Load() || len(server.turns) != 0 {
				t.Fatal("Cleared loop still has scheduled work")
			}
		})
	}
}

func TestSessionLoopRejectsInvalidControls(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	for _, command := range []string{"/loop pause", "/loop resume"} {
		if err := server.submitClientMessage(t.Context(), command, "request"); err == nil {
			t.Fatalf("%s succeeded without a loop", command)
		}
	}
	if err := server.submitClientMessage(t.Context(), "/loop 5m Check CI", "request", "attachment"); err == nil {
		t.Fatal("Loop command accepted attachments")
	}
	if err := server.submitClientMessage(t.Context(), "/loop 5m Check CI", "request"); err != nil {
		t.Fatal(err)
	}
	before := len(journal.Snapshot())
	if err := server.submitClientMessage(t.Context(), "/loop 1m Another prompt", "request"); err == nil {
		t.Fatal("Loop command replaced an existing loop")
	}
	if len(journal.Snapshot()) != before || journal.currentLoop().Prompt != "Check CI" {
		t.Fatal("Rejected loop command modified the journal")
	}
}

func TestSessionLoopRunsPromptAndReschedules(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	provider := &fakeProvider{}
	server := NewServer(Config{}, journal, provider)
	if err := server.submitClientMessage(t.Context(), "/loop 5m !This is agent input", "request"); err != nil {
		t.Fatal(err)
	}
	due := journal.currentLoop().NextRunAt
	if err := server.submitLoopPrompt(due.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(server.turns) != 0 {
		t.Fatal("Loop ran before its scheduled time")
	}
	if err := server.submitLoopPrompt(due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	turn := <-server.turns
	if turn.command.kind != sessionCommandMessage || turn.text != "!This is agent input" {
		t.Fatalf("Loop turn = %#v", turn)
	}
	server.runTurn(t.Context(), turn)
	provider.mu.Lock()
	prompts := append([]string(nil), provider.prompts...)
	provider.mu.Unlock()
	if len(prompts) != 1 || prompts[0] != "!This is agent input" {
		t.Fatalf("Provider prompts = %#v", prompts)
	}
	if got := journal.currentLoop().NextRunAt; !got.Equal(due.Add(65 * time.Minute)) {
		t.Fatalf("Next run = %s", got)
	}
	if err := server.submitLoopPrompt(due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(server.turns) != 0 {
		t.Fatal("Loop replayed missed intervals")
	}
}

func TestSessionLoopWaitsForIdle(t *testing.T) {
	for _, busy := range []string{"turn", "background", "update", "idle drain", "provider stop"} {
		t.Run(busy, func(t *testing.T) {
			journal := NewJournal()
			defer journal.Close()
			server := NewServer(Config{}, journal, &fakeProvider{})
			if err := server.submitClientMessage(t.Context(), "/loop 5m Check CI", "request"); err != nil {
				t.Fatal(err)
			}
			switch busy {
			case "turn":
				if err := server.submitMessage("User work", "user"); err != nil {
					t.Fatal(err)
				}
			case "background":
				server.backgroundActive.Store(true)
			case "update":
				server.updateRequest = &sessionupdate.Request{ID: "update"}
			case "idle drain":
				server.idleDrainRequest = &sessionupdate.Request{ID: "idle"}
			case "provider stop":
				server.providerStopping.Store(true)
			}
			before := len(journal.Snapshot())
			if err := server.submitLoopPrompt(journal.currentLoop().NextRunAt); err != nil {
				t.Fatal(err)
			}
			if len(journal.Snapshot()) != before {
				t.Fatal("Busy Session accepted a recurring prompt")
			}
			if busy == "turn" && server.pendingTurn.text != "User work" {
				t.Fatal("Loop modified pending user work")
			}
		})
	}
}

func TestSessionLoopWaitsForGoal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	journal := NewJournal()
	defer journal.Close()
	provider := &fakeGoalProvider{
		goalCommands: make(chan goalCommand, 2),
		goalStarted:  make(chan struct{}),
		goalStopped:  make(chan struct{}),
	}
	server := NewServer(Config{}, journal, provider)
	done := make(chan struct{})
	go func() { defer close(done); server.runTurns(ctx) }()
	defer func() { cancel(); <-done }()
	if err := server.submitClientMessage(ctx, "/goal Improve coverage", "goal"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.goalStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Goal did not start")
	}
	if err := server.submitClientMessage(ctx, "/loop 5m Check CI", "loop"); err != nil {
		t.Fatal(err)
	}
	due := journal.currentLoop().NextRunAt
	if err := server.submitLoopPrompt(due); err != nil {
		t.Fatal(err)
	}
	if len(server.turns) != 0 {
		t.Fatal("Recurring prompt was queued during active goal work")
	}
	server.activeMu.Lock()
	goalDone := server.activeTurnDone
	server.activeMu.Unlock()
	if err := server.submitClientMessage(ctx, "/goal pause", "pause"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-goalDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Goal work did not finish")
	}
	if err := server.submitLoopPrompt(due); err != nil {
		t.Fatal(err)
	}
	waitForSessionEvent(t, journal, func(event Event) bool { return event.Type == EventAssistantDelta && event.TurnID == "turn-2" })
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.prompts) != 1 || provider.prompts[0] != "Check CI" {
		t.Fatalf("Prompts after goal work = %#v", provider.prompts)
	}
}

func TestSessionLoopScheduler(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	if err := server.submitClientMessage(ctx, "/loop 1s Check CI", "request"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); server.runLoop(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case turn := <-server.turns:
		if turn.text != "Check CI" {
			t.Fatalf("Scheduled prompt = %q", turn.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Loop timer did not submit the prompt")
	}
	if err := server.submitClientMessage(ctx, "/loop pause", "pause"); err != nil {
		t.Fatal(err)
	}
	if err := server.submitLoopPrompt(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(server.turns) != 0 || server.pendingTurn.text != "Check CI" {
		t.Fatal("Pause changed the accepted prompt or scheduled another run")
	}
}

func TestSessionLoopSurvivesJournalCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := openJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{}, journal, &fakeProvider{})
	if err := server.submitClientMessage(t.Context(), "/loop 5m Check CI", "request"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if err := journal.Append(Event{Type: EventAssistantMessage, Text: "More work"}); err != nil {
			t.Fatal(err)
		}
	}
	journal.Close()
	journal, err = openJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	server = NewServer(Config{}, journal, &fakeProvider{})
	if err := server.initializeLoop(); err != nil {
		t.Fatal(err)
	}
	if loop := journal.currentLoop(); loop == nil || loop.Prompt != "Check CI" || !server.loopActive.Load() {
		t.Fatalf("Recovered loop = %#v", loop)
	}
	if err := server.submitClientMessage(t.Context(), "/loop clear", "clear"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if err := journal.Append(Event{Type: EventAssistantMessage, Text: "More work"}); err != nil {
			t.Fatal(err)
		}
	}
	journal.Close()
	journal, err = openJournal(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if journal.currentLoop() != nil {
		t.Fatal("Cleared loop returned after compaction and restart")
	}
}

func TestSessionLoopPreventsIdleReclamation(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	if err := server.submitClientMessage(t.Context(), "/loop 5m Check CI", "request"); err != nil {
		t.Fatal(err)
	}
	if queue := server.sessionStatusPublishQueue; len(queue) != 1 || !queue[0].active {
		t.Fatalf("Scheduled loop activity = %#v", queue)
	}
	server.publishSessionStatus = nil
	server.updateRequest = &sessionupdate.Request{ID: "update"}
	server.idleDrainRequest = &sessionupdate.Request{ID: "idle"}
	update, idle := server.sessionDrainReports()
	if update.Phase != sessionupdate.PhaseDrained || idle.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("Loop drain states = (%s, %s)", update.Phase, idle.Phase)
	}
	if err := server.submitClientMessage(t.Context(), "/loop pause", "pause"); err != nil {
		t.Fatal(err)
	}
	_, idle = server.sessionDrainReports()
	if idle.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("Paused loop drain = %s", idle.Phase)
	}
}

func TestSessionLoopRejectsInvalidRetainedState(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	if err := journal.Append(Event{Type: EventLoopUpdated, Loop: &Loop{Prompt: "Check CI", Interval: "invalid", Status: "active"}}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{}, journal, &fakeProvider{})
	if err := server.initializeLoop(); err == nil || !strings.Contains(err.Error(), "invalid loop") {
		t.Fatalf("initializeLoop() = %v", err)
	}
}
