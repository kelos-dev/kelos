package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServerClosesCancelledPromptConnection(t *testing.T) {
	server := &Server{journal: NewJournal()}
	serverConnection, connection := net.Pipe()
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		server.handleConnection(ctx, serverConnection)
		close(done)
	}()
	if err := connection.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(connection)
	// Keep the writer blocked and fill its queue so a prompt response waits for cancellation.
	for range 514 {
		if err := encoder.Encode(ClientRequest{Type: "prompts"}); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled prompt handler did not close the connection")
	}
}

func TestPromptHistoryPreservesLatestTextAndAttachments(t *testing.T) {
	timestamp := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	longPrompt := strings.Repeat("多行\n", maxHistoryMessageBytes)
	attachment := Attachment{ID: "file-1", Name: "notes.txt"}
	events := []Event{
		{ID: 1, Type: EventUserMessage, Text: "initial prompt", Timestamp: &timestamp},
		{ID: 2, Type: EventUserMessage, TurnID: "completed", Text: longPrompt},
		{ID: 3, Type: EventAssistantMessage, TurnID: "completed", Text: "answer"},
		{ID: 4, Type: EventTurnCompleted, TurnID: "completed", Status: "completed"},
		{ID: 5, Type: EventUserMessage, TurnID: "pending", Text: "draft", Timestamp: &timestamp},
		{ID: 6, Type: EventUserMessageUpdated, TurnID: "pending", Text: "edited\nprompt", Attachments: []Attachment{attachment}},
		{ID: 7, Type: EventUserMessage, TurnID: "removed", Text: "removed prompt"},
		{ID: 8, Type: EventUserMessageRemoved, TurnID: "removed"},
		{ID: 9, Type: EventUserMessage, TurnID: "merged", Text: "merged prompt"},
		{ID: 10, Type: EventTurnCompleted, TurnID: "merged", Status: "merged"},
		{ID: 11, Type: EventUserMessageUpdated, TurnID: "retained-update", Text: "retained prompt"},
	}
	items := promptHistoryItems(events)
	if len(items) != 4 {
		t.Fatalf("prompt items = %d, want 4", len(items))
	}
	for index, want := range []string{"initial prompt", longPrompt, "edited\nprompt", "retained prompt"} {
		if got := items[index].events[0].Text; got != want {
			t.Fatalf("prompt %d differs from its full accepted text", index)
		}
	}
	pending := items[2].events[0]
	if pending.ID != 5 || pending.Timestamp == nil || !pending.Timestamp.Equal(timestamp) || !reflect.DeepEqual(pending.Attachments, []Attachment{attachment}) {
		t.Fatalf("edited prompt metadata = %#v", pending)
	}
	if events[4].Text != "draft" {
		t.Fatal("projection modified source events")
	}
}

func TestServerReturnsPromptPagesWithoutSubscribing(t *testing.T) {
	journal := NewJournal()
	for index := 0; index < DefaultHistoryItemLimit+3; index++ {
		if err := journal.Append(Event{Type: EventUserMessage, TurnID: fmt.Sprintf("turn-%d", index), Text: fmt.Sprintf("prompt %d", index)}); err != nil {
			t.Fatal(err)
		}
		if err := journal.Append(Event{Type: EventToolCompleted, Output: "tool output"}); err != nil {
			t.Fatal(err)
		}
	}
	before := journal.Snapshot()
	server := &Server{journal: journal}
	serverConnection, connection := net.Pipe()
	t.Cleanup(func() { _ = connection.Close() })
	go server.handleConnection(t.Context(), serverConnection)
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	encoder, decoder := json.NewEncoder(connection), json.NewDecoder(connection)
	cursor := ""
	for page, wantCount := range []int{DefaultHistoryItemLimit, 3} {
		requestID := fmt.Sprintf("page-%d", page)
		if err := encoder.Encode(ClientRequest{Type: "prompts", RequestID: requestID, HistoryCursor: cursor}); err != nil {
			t.Fatal(err)
		}
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type != EventPrompts || event.RequestID != requestID || len(event.Prompts) != wantCount {
			t.Fatalf("prompt page = %#v", event)
		}
		first := 3
		if page == 1 {
			first = 0
		}
		for index, prompt := range event.Prompts {
			if prompt.ID != int64((first+index)*2+1) || prompt.TurnID != fmt.Sprintf("turn-%d", first+index) || prompt.Text != fmt.Sprintf("prompt %d", first+index) {
				t.Fatalf("prompt %d = %#v", index, prompt)
			}
		}
		cursor = event.HistoryCursor
		if (cursor != "") != (page == 0) {
			t.Fatalf("page %d cursor = %q", page, cursor)
		}
	}
	if !reflect.DeepEqual(before, journal.Snapshot()) {
		t.Fatal("reading prompts changed the journal")
	}
}

func TestPromptHistorySurvivesJournalReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(Event{Type: EventUserMessage, Text: "from an earlier connection"}); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal, err = OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(journal.Close)
	event, err := (&Server{journal: journal}).loadPrompts("request", "")
	if err != nil || len(event.Prompts) != 1 || event.Prompts[0].Text != "from an earlier connection" {
		t.Fatalf("reopened prompt history = %#v, error = %v", event, err)
	}
}

func TestPromptHistoryRejectsInvalidOrExpiredCursor(t *testing.T) {
	server := &Server{journal: NewJournal()}
	for _, cursor := range []string{"invalid", encodeHistoryCursor(sessionHistoryCursor{JournalID: "another-journal", BeforeEventID: 10, ItemLimit: 20, ByteLimit: 1024})} {
		if _, err := server.loadPrompts("request", cursor); err == nil {
			t.Fatalf("cursor %q was accepted", cursor)
		}
	}
}

func TestPromptHistoryPagesFullTextBeyondByteLimit(t *testing.T) {
	journal := NewJournal()
	longPrompt := strings.Repeat("prompt\n", DefaultHistoryByteLimit)
	for _, prompt := range []string{longPrompt, "recent prompt"} {
		if err := journal.Append(Event{Type: EventUserMessage, Text: prompt}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{journal: journal}
	page, err := server.loadPrompts("first", "")
	if err != nil || len(page.Prompts) != 1 || page.Prompts[0].Text != "recent prompt" || page.HistoryCursor == "" {
		t.Fatalf("recent prompt page = %#v, error = %v", page, err)
	}
	page, err = server.loadPrompts("earlier", page.HistoryCursor)
	if err != nil || len(page.Prompts) != 1 || page.Prompts[0].Text != longPrompt || page.HistoryCursor != "" {
		t.Fatalf("long prompt page: count = %d, cursor = %q, error = %v", len(page.Prompts), page.HistoryCursor, err)
	}
}
