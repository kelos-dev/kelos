package source

import (
	"context"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestShouldProcess(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		subtype    string
		threadTS   string
		text       string
		selfUserID string
		triggerCmd string
		wantBody   string
		wantOK     bool
	}{
		{
			name:       "top-level message, no trigger command",
			userID:     "U001",
			text:       "fix the login page",
			selfUserID: "UBOT",
			wantBody:   "fix the login page",
			wantOK:     true,
		},
		{
			name:       "top-level message with trigger prefix",
			userID:     "U001",
			text:       "/kelos fix the login page",
			selfUserID: "UBOT",
			triggerCmd: "/kelos",
			wantBody:   "fix the login page",
			wantOK:     true,
		},
		{
			name:       "top-level message with trigger prefix and extra spaces",
			userID:     "U001",
			text:       "/kelos   fix the login page",
			selfUserID: "UBOT",
			triggerCmd: "/kelos",
			wantBody:   "fix the login page",
			wantOK:     true,
		},
		{
			name:       "message prefix trigger",
			userID:     "U001",
			text:       "!fix broken button",
			selfUserID: "UBOT",
			triggerCmd: "!fix",
			wantBody:   "broken button",
			wantOK:     true,
		},
		{
			name:       "trigger prefix only, no body after stripping",
			userID:     "U001",
			text:       "/kelos",
			selfUserID: "UBOT",
			triggerCmd: "/kelos",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "does not match trigger prefix",
			userID:     "U001",
			text:       "unrelated message",
			selfUserID: "UBOT",
			triggerCmd: "/kelos",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "threaded message accepted as follow-up",
			userID:     "U001",
			text:       "this is a reply",
			selfUserID: "UBOT",
			threadTS:   "1234567890.123456",
			wantBody:   "this is a reply",
			wantOK:     true,
		},
		{
			name:       "threaded message skips trigger command",
			userID:     "U001",
			text:       "my github handle is foo",
			selfUserID: "UBOT",
			threadTS:   "1234567890.123456",
			triggerCmd: "/kelos",
			wantBody:   "my github handle is foo",
			wantOK:     true,
		},
		{
			name:       "message from self ignored",
			userID:     "UBOT",
			text:       "my own message",
			selfUserID: "UBOT",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "bot_message subtype ignored",
			userID:     "U002",
			subtype:    "bot_message",
			text:       "bot says hello",
			selfUserID: "UBOT",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "message_changed subtype ignored",
			userID:     "U001",
			subtype:    "message_changed",
			text:       "edited message",
			selfUserID: "UBOT",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "message_deleted subtype ignored",
			userID:     "U001",
			subtype:    "message_deleted",
			text:       "deleted message",
			selfUserID: "UBOT",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "message_replied subtype ignored",
			userID:     "U001",
			subtype:    "message_replied",
			text:       "reply notification",
			selfUserID: "UBOT",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "empty text ignored",
			userID:     "U001",
			text:       "",
			selfUserID: "UBOT",
			wantBody:   "",
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, ok := shouldProcess(tt.userID, tt.subtype, tt.threadTS, tt.text, tt.selfUserID, tt.triggerCmd)
			if ok != tt.wantOK {
				t.Errorf("shouldProcess() ok = %v, want %v", ok, tt.wantOK)
			}
			if body != tt.wantBody {
				t.Errorf("shouldProcess() body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestMatchesChannel(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		allowed   []string
		want      bool
	}{
		{
			name:      "empty allow list permits all",
			channelID: "C123",
			allowed:   nil,
			want:      true,
		},
		{
			name:      "matching channel",
			channelID: "C123",
			allowed:   []string{"C123", "C456"},
			want:      true,
		},
		{
			name:      "non-matching channel",
			channelID: "C789",
			allowed:   []string{"C123", "C456"},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesChannel(tt.channelID, tt.allowed)
			if got != tt.want {
				t.Errorf("matchesChannel(%q, %v) = %v, want %v", tt.channelID, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestMatchesUser(t *testing.T) {
	tests := []struct {
		name    string
		userID  string
		allowed []string
		want    bool
	}{
		{
			name:    "empty allow list permits all",
			userID:  "U001",
			allowed: nil,
			want:    true,
		},
		{
			name:    "matching user",
			userID:  "U001",
			allowed: []string{"U001", "U002"},
			want:    true,
		},
		{
			name:    "non-matching user",
			userID:  "U003",
			allowed: []string{"U001", "U002"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesUser(tt.userID, tt.allowed)
			if got != tt.want {
				t.Errorf("matchesUser(%q, %v) = %v, want %v", tt.userID, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestBuildWorkItem(t *testing.T) {
	item := buildWorkItem("1234567890.123456", 42, "Jane Doe", "fix the bug", "https://slack.com/link", "test-channel", "C123ABC")

	if item.ID != "1234567890.123456" {
		t.Errorf("expected ID %q, got %q", "1234567890.123456", item.ID)
	}
	if item.Number != 42 {
		t.Errorf("expected Number 42, got %d", item.Number)
	}
	if item.Title != "Jane Doe" {
		t.Errorf("expected Title %q, got %q", "Jane Doe", item.Title)
	}
	if item.Body != "fix the bug" {
		t.Errorf("expected Body %q, got %q", "fix the bug", item.Body)
	}
	if item.URL != "https://slack.com/link" {
		t.Errorf("expected URL %q, got %q", "https://slack.com/link", item.URL)
	}
	if len(item.Labels) != 2 || item.Labels[0] != "test-channel" || item.Labels[1] != "C123ABC" {
		t.Errorf("expected Labels [test-channel C123ABC], got %v", item.Labels)
	}
	if item.Kind != "SlackMessage" {
		t.Errorf("expected Kind %q, got %q", "SlackMessage", item.Kind)
	}
}

func TestBotParticipated(t *testing.T) {
	tests := []struct {
		name       string
		msgs       []slack.Message
		selfUserID string
		want       bool
	}{
		{
			name: "bot present",
			msgs: []slack.Message{
				{Msg: slack.Msg{User: "U001", Text: "hello"}},
				{Msg: slack.Msg{User: "UBOT", Text: "hi back"}},
			},
			selfUserID: "UBOT",
			want:       true,
		},
		{
			name: "bot absent",
			msgs: []slack.Message{
				{Msg: slack.Msg{User: "U001", Text: "hello"}},
				{Msg: slack.Msg{User: "U002", Text: "hi"}},
			},
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "empty messages",
			msgs:       []slack.Message{},
			selfUserID: "UBOT",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := botParticipated(tt.msgs, tt.selfUserID)
			if got != tt.want {
				t.Errorf("botParticipated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatThreadContext(t *testing.T) {
	msgs := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "add me to codeowners"}},
		{Msg: slack.Msg{User: "UBOT", Text: "What is your GitHub handle?"}},
		{Msg: slack.Msg{User: "U001", Text: "my handle is jdoe"}},
	}

	got := formatThreadContext(msgs, "UBOT")

	if !strings.Contains(got, "User: add me to codeowners") {
		t.Errorf("expected user message, got %q", got)
	}
	if !strings.Contains(got, "Agent: What is your GitHub handle?") {
		t.Errorf("expected agent message, got %q", got)
	}
	if !strings.Contains(got, "User: my handle is jdoe") {
		t.Errorf("expected user reply, got %q", got)
	}
}

func TestFormatThreadContext_SkipsEmptyMessages(t *testing.T) {
	msgs := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "hello"}},
		{Msg: slack.Msg{User: "U001", Text: ""}},
		{Msg: slack.Msg{User: "U001", Text: "world"}},
	}

	got := formatThreadContext(msgs, "UBOT")

	if strings.Count(got, "User:") != 2 {
		t.Errorf("expected 2 user messages, got %q", got)
	}
}

func TestFormatThreadContext_BotIDAsAgent(t *testing.T) {
	msgs := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "hello"}},
		{Msg: slack.Msg{BotID: "B123", Text: "I am a bot"}},
	}

	got := formatThreadContext(msgs, "UBOT")

	if !strings.Contains(got, "Agent: I am a bot") {
		t.Errorf("expected bot message labeled as Agent, got %q", got)
	}
}

// newStartedSlackSource returns a SlackSource where the startOnce has
// already fired (so Discover won't call Start).
func newStartedSlackSource() *SlackSource {
	s := &SlackSource{}
	s.startOnce.Do(func() {}) // Mark as started without actually connecting
	return s
}

func TestDiscoverDrainsPending(t *testing.T) {
	s := newStartedSlackSource()

	// Pre-populate pending items
	s.pending = []WorkItem{
		{ID: "1", Title: "User A", Body: "task one"},
		{ID: "2", Title: "User B", Body: "task two"},
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != "1" || items[1].ID != "2" {
		t.Errorf("unexpected items: %+v", items)
	}

	// Pending should be empty now
	if len(s.pending) != 0 {
		t.Errorf("expected pending to be empty, got %d items", len(s.pending))
	}
}

func TestDiscoverEmpty(t *testing.T) {
	s := newStartedSlackSource()

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestDiscoverMultipleCalls(t *testing.T) {
	s := newStartedSlackSource()

	// First batch
	s.pending = []WorkItem{{ID: "1", Body: "first"}}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "1" {
		t.Errorf("first drain: expected [{ID:1}], got %+v", items)
	}

	// Second batch
	s.pending = []WorkItem{{ID: "2", Body: "second"}, {ID: "3", Body: "third"}}

	items, err = s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 || items[0].ID != "2" || items[1].ID != "3" {
		t.Errorf("second drain: expected [{ID:2},{ID:3}], got %+v", items)
	}

	// Empty after drain
	items, err = s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty after drain, got %d items", len(items))
	}
}
