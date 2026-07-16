package sessionruntime

import (
	"fmt"
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

func TestJournalIdentityPersistsUntilJournalIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), journalFileName)
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	initialBounds, _, _, _, cancel := journal.SubscribeWithBounds(0, "")
	cancel()
	if initialBounds.JournalID == "" {
		t.Fatal("new journal has an empty identity")
	}
	journal.Close()

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedBounds, _, _, _, cancel := reopened.SubscribeWithBounds(0, "")
	cancel()
	if reopenedBounds.JournalID != initialBounds.JournalID {
		t.Fatalf("reopened journal identity = %q, want %q", reopenedBounds.JournalID, initialBounds.JournalID)
	}
	reopened.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replaced, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer replaced.Close()
	replacedBounds, _, _, _, cancel := replaced.SubscribeWithBounds(0, "")
	cancel()
	if replacedBounds.JournalID == initialBounds.JournalID {
		t.Fatalf("replaced journal retained identity %q", replacedBounds.JournalID)
	}
}

func TestJournalIdentityFollowsReplacedContents(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, journalFileName)
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(Event{Type: EventAssistantMessage, Text: "original"}); err != nil {
		t.Fatal(err)
	}
	originalBounds, _, _, _, cancel := journal.SubscribeWithBounds(0, "")
	cancel()
	journal.Close()

	replacementPath := filepath.Join(directory, "replacement.jsonl")
	replacement, err := OpenJournal(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Append(Event{Type: EventAssistantMessage, Text: "replacement"}); err != nil {
		t.Fatal(err)
	}
	replacementBounds, _, _, _, cancel := replacement.SubscribeWithBounds(0, "")
	cancel()
	replacement.Close()
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedBounds, _, _, _, cancel := reopened.SubscribeWithBounds(0, "")
	cancel()
	if reopenedBounds.JournalID != replacementBounds.JournalID {
		t.Fatalf("replaced journal identity = %q, want %q", reopenedBounds.JournalID, replacementBounds.JournalID)
	}
	if reopenedBounds.JournalID == originalBounds.JournalID {
		t.Fatalf("replaced journal retained original identity %q", reopenedBounds.JournalID)
	}
}

func TestJournalMigratesLegacyRecordsToEmbeddedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), journalFileName)
	if err := os.WriteFile(path, []byte("{\"id\":1,\"type\":\"assistant.message\",\"text\":\"legacy\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	bounds, _, _, _, cancel := journal.SubscribeWithBounds(0, "")
	cancel()
	journal.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"journalId":"`+bounds.JournalID+`"`) {
		t.Fatalf("migrated journal does not contain identity %q: %s", bounds.JournalID, data)
	}

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedBounds, retained, _, _, cancel := reopened.SubscribeWithBounds(0, "")
	cancel()
	if reopenedBounds.JournalID != bounds.JournalID {
		t.Fatalf("reopened journal identity = %q, want %q", reopenedBounds.JournalID, bounds.JournalID)
	}
	if len(retained) != 1 || retained[0].Text != "legacy" || retained[0].JournalID != "" {
		t.Fatalf("retained legacy events = %#v", retained)
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

func TestJournalSubscriptionReportsBoundsAndResetsStaleCursor(t *testing.T) {
	journal := newJournal(3)
	defer journal.Close()
	for i := 0; i < 5; i++ {
		if err := journal.Append(Event{Type: EventAssistantDelta, Text: fmt.Sprintf("event-%d", i+1)}); err != nil {
			t.Fatal(err)
		}
	}

	bounds, retained, _, _, cancel := journal.SubscribeWithBounds(1, "")
	defer cancel()
	if bounds.FirstEventID != 3 || bounds.LastEventID != 5 || !bounds.Reset {
		t.Fatalf("history bounds = %#v, want first=3 last=5 reset=true", bounds)
	}
	if len(retained) != 3 || retained[0].ID != 3 || retained[2].ID != 5 {
		t.Fatalf("retained events = %#v", retained)
	}
}

func TestJournalSubscriptionResetsMissingIdentityWithNonzeroCursor(t *testing.T) {
	journal := newJournal(2)
	defer journal.Close()
	for i := 0; i < 3; i++ {
		if err := journal.Append(Event{Type: EventAssistantDelta, Text: fmt.Sprintf("event-%d", i+1)}); err != nil {
			t.Fatal(err)
		}
	}

	bounds, retained, _, _, cancel := journal.SubscribeWithBounds(2, "")
	defer cancel()
	if !bounds.Reset {
		t.Fatalf("history bounds = %#v, want reset=true", bounds)
	}
	if len(retained) != 2 || retained[0].ID != 2 || retained[1].ID != 3 {
		t.Fatalf("retained events = %#v, want IDs 2 and 3", retained)
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
