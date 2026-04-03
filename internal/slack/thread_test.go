package slack

import (
	"strings"
	"testing"

	goslack "github.com/slack-go/slack"
)

func TestBotParticipated(t *testing.T) {
	tests := []struct {
		name      string
		msgs      []goslack.Message
		botUserID string
		want      bool
	}{
		{
			name:      "empty thread",
			msgs:      nil,
			botUserID: "UBOT",
			want:      false,
		},
		{
			name: "bot not in thread",
			msgs: []goslack.Message{
				{Msg: goslack.Msg{User: "U1", Text: "hello"}},
				{Msg: goslack.Msg{User: "U2", Text: "world"}},
			},
			botUserID: "UBOT",
			want:      false,
		},
		{
			name: "bot in thread",
			msgs: []goslack.Message{
				{Msg: goslack.Msg{User: "U1", Text: "hello"}},
				{Msg: goslack.Msg{User: "UBOT", Text: "I can help"}},
			},
			botUserID: "UBOT",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BotParticipated(tt.msgs, tt.botUserID); got != tt.want {
				t.Errorf("BotParticipated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatThreadContext(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{User: "U1", Text: "fix the bug please"}},
		{Msg: goslack.Msg{User: "UBOT", Text: "Working on it"}},
		{Msg: goslack.Msg{User: "U1", Text: "any updates?"}},
		{Msg: goslack.Msg{Text: ""}}, // empty text, should be skipped
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.HasPrefix(result, "Slack thread conversation:\n") {
		t.Error("Expected thread context to start with header")
	}
	if !strings.Contains(result, "User: fix the bug please") {
		t.Error("Expected user message to be labeled as User")
	}
	if !strings.Contains(result, "Agent: Working on it") {
		t.Error("Expected bot message to be labeled as Agent")
	}
	if !strings.Contains(result, "User: any updates?") {
		t.Error("Expected follow-up user message")
	}
	// Empty text message should be skipped
	if strings.Count(result, "User:") != 2 || strings.Count(result, "Agent:") != 1 {
		t.Errorf("Unexpected message count in output:\n%s", result)
	}
}

func TestFormatThreadContext_BotIDMessage(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{User: "U1", Text: "hello"}},
		{Msg: goslack.Msg{BotID: "B123", Text: "Bot response"}},
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.Contains(result, "Agent: Bot response") {
		t.Error("Expected message with BotID to be labeled as Agent")
	}
}
