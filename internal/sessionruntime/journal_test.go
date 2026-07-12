package sessionruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalPersistsEventsAndRecoversInterruptedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), journalFileName)
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journal.Append(Event{Type: EventUserMessage, TurnID: "turn-7", Text: "work"})
	journal.Append(Event{Type: EventTurnStarted, TurnID: "turn-7", Status: "running"})
	journal.Append(Event{Type: EventInputRequested, TurnID: "turn-7", InputID: "input-3", Status: "pending"})
	journal.Close()

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovery, err := recoverJournal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.nextTurnID != 7 || recovery.nextInputID != 3 {
		t.Fatalf("recovery counters = %#v", recovery)
	}
	events := reopened.Snapshot()
	if len(events) != 6 {
		t.Fatalf("recovered events = %#v", events)
	}
	if events[3].Type != EventRuntimeRecovered || !strings.Contains(events[3].Text, "unfinished work") {
		t.Fatalf("recovery event = %#v", events[3])
	}
	if events[4].Type != EventInputResolved || events[4].Status != "cancelled" {
		t.Fatalf("input recovery event = %#v", events[4])
	}
	if events[5].Type != EventTurnCompleted || events[5].Status != "interrupted" {
		t.Fatalf("turn recovery event = %#v", events[5])
	}
	for i, event := range events {
		if event.ID != int64(i+1) {
			t.Fatalf("event %d ID = %d, want %d", i, event.ID, i+1)
		}
	}
}

func TestJournalDiscardsIncompleteTrailingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), journalFileName)
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journal.Append(Event{Type: EventAssistantMessage, Text: "complete"})
	journal.Close()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"id":2,"type":"assistant.message"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events := reopened.Snapshot()
	if len(events) != 1 || events[0].Text != "complete" {
		t.Fatalf("events after partial record recovery = %#v", events)
	}
}

func TestJournalReportsPersistentWriteFailure(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(Event{Type: EventAssistantMessage, Text: "lost"}); err == nil {
		t.Fatal("journal append succeeded after the backing file was closed")
	}
	select {
	case <-journal.Failed():
	default:
		t.Fatal("journal write failure was not reported")
	}
	if journal.Err() == nil {
		t.Fatal("journal write failure has no error")
	}
}

func TestRecoverJournalKeepsCompletedTurnCompleted(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	journal.Append(Event{Type: EventUserMessage, TurnID: "turn-2", Text: "done"})
	journal.Append(Event{Type: EventTurnStarted, TurnID: "turn-2", Status: "running"})
	journal.Append(Event{Type: EventTurnCompleted, TurnID: "turn-2", Status: "completed"})
	recovery, err := recoverJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.nextTurnID != 2 {
		t.Fatalf("next turn ID = %d, want 2", recovery.nextTurnID)
	}
	events := journal.Snapshot()
	if len(events) != 4 || events[3].Type != EventRuntimeRecovered || strings.Contains(events[3].Text, "unfinished") {
		t.Fatalf("events = %#v", events)
	}
}
