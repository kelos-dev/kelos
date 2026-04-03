package slack

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestMatchesSpawner(t *testing.T) {
	tests := []struct {
		name     string
		slackCfg *v1alpha1.Slack
		msg      *SlackMessageData
		want     bool
	}{
		{
			name:     "nil slack config",
			slackCfg: nil,
			msg:      &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want:     false,
		},
		{
			name:     "no filters matches everything",
			slackCfg: &v1alpha1.Slack{},
			msg:      &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want:     true,
		},
		{
			name: "channel filter matches",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C1", "C2"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: true,
		},
		{
			name: "channel filter rejects",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C2", "C3"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: false,
		},
		{
			name: "user filter matches",
			slackCfg: &v1alpha1.Slack{
				AllowedUsers: []string{"U1", "U2"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: true,
		},
		{
			name: "user filter rejects",
			slackCfg: &v1alpha1.Slack{
				AllowedUsers: []string{"U2", "U3"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: false,
		},
		{
			name: "both filters match",
			slackCfg: &v1alpha1.Slack{
				Channels:     []string{"C1"},
				AllowedUsers: []string{"U1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: true,
		},
		{
			name: "channel matches but user rejected",
			slackCfg: &v1alpha1.Slack{
				Channels:     []string{"C1"},
				AllowedUsers: []string{"U2"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesSpawner(tt.slackCfg, tt.msg)
			if got != tt.want {
				t.Errorf("MatchesSpawner() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessTriggerCommand(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		threadTS   string
		triggerCmd string
		wantBody   string
		wantOK     bool
	}{
		{
			name:       "no trigger command, top-level message",
			text:       "hello world",
			triggerCmd: "",
			wantBody:   "hello world",
			wantOK:     true,
		},
		{
			name:       "trigger matches",
			text:       "/kelos fix the bug",
			triggerCmd: "/kelos",
			wantBody:   "fix the bug",
			wantOK:     true,
		},
		{
			name:       "trigger does not match",
			text:       "hello world",
			triggerCmd: "/kelos",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "trigger matches but empty body after strip",
			text:       "/kelos",
			triggerCmd: "/kelos",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "trigger with only whitespace after strip",
			text:       "/kelos   ",
			triggerCmd: "/kelos",
			wantBody:   "",
			wantOK:     false,
		},
		{
			name:       "thread reply bypasses trigger",
			text:       "follow up message",
			threadTS:   "1234567890.123456",
			triggerCmd: "/kelos",
			wantBody:   "follow up message",
			wantOK:     true,
		},
		{
			name:       "thread reply with no trigger configured",
			text:       "follow up",
			threadTS:   "1234567890.123456",
			triggerCmd: "",
			wantBody:   "follow up",
			wantOK:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBody, gotOK := ProcessTriggerCommand(tt.text, tt.threadTS, tt.triggerCmd)
			if gotBody != tt.wantBody || gotOK != tt.wantOK {
				t.Errorf("ProcessTriggerCommand() = (%q, %v), want (%q, %v)",
					gotBody, gotOK, tt.wantBody, tt.wantOK)
			}
		})
	}
}

func TestExtractSlackWorkItem(t *testing.T) {
	t.Run("regular message", func(t *testing.T) {
		msg := &SlackMessageData{
			UserID:    "U123",
			UserName:  "Alice",
			Body:      "fix the login page",
			Timestamp: "1234567890.123456",
			Permalink: "https://slack.com/archives/C1/p1234567890123456",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["ID"] != "1234567890.123456" {
			t.Errorf("ID = %v, want %v", vars["ID"], "1234567890.123456")
		}
		if vars["Title"] != "Alice" {
			t.Errorf("Title = %v, want %v", vars["Title"], "Alice")
		}
		if vars["Body"] != "fix the login page" {
			t.Errorf("Body = %v, want %v", vars["Body"], "fix the login page")
		}
		if vars["URL"] != "https://slack.com/archives/C1/p1234567890123456" {
			t.Errorf("URL = %v, want %v", vars["URL"], msg.Permalink)
		}
		if vars["Kind"] != "SlackMessage" {
			t.Errorf("Kind = %v, want %v", vars["Kind"], "SlackMessage")
		}
	})

	t.Run("slash command uses composite ID", func(t *testing.T) {
		msg := &SlackMessageData{
			UserID:         "U123",
			UserName:       "Alice",
			Body:           "do something",
			IsSlashCommand: true,
			SlashCommandID: "C1:/kelos:trigger123",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["ID"] != "C1:/kelos:trigger123" {
			t.Errorf("ID = %v, want %v", vars["ID"], "C1:/kelos:trigger123")
		}
	})
}

func TestShouldProcess(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		subtype    string
		text       string
		selfUserID string
		want       bool
	}{
		{
			name:       "normal message",
			userID:     "U1",
			text:       "hello",
			selfUserID: "UBOT",
			want:       true,
		},
		{
			name:       "self message filtered",
			userID:     "UBOT",
			text:       "hello",
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "bot_message subtype filtered",
			userID:     "U1",
			subtype:    "bot_message",
			text:       "hello",
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_changed subtype filtered",
			userID:     "U1",
			subtype:    "message_changed",
			text:       "hello",
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_deleted subtype filtered",
			userID:     "U1",
			subtype:    "message_deleted",
			text:       "hello",
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_replied subtype filtered",
			userID:     "U1",
			subtype:    "message_replied",
			text:       "hello",
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "empty text filtered",
			userID:     "U1",
			text:       "",
			selfUserID: "UBOT",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldProcess(tt.userID, tt.subtype, tt.text, tt.selfUserID)
			if got != tt.want {
				t.Errorf("shouldProcess() = %v, want %v", got, tt.want)
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
		{"empty allowed list matches all", "C1", nil, true},
		{"in allowed list", "C1", []string{"C1", "C2"}, true},
		{"not in allowed list", "C3", []string{"C1", "C2"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesChannel(tt.channelID, tt.allowed); got != tt.want {
				t.Errorf("matchesChannel() = %v, want %v", got, tt.want)
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
		{"empty allowed list matches all", "U1", nil, true},
		{"in allowed list", "U1", []string{"U1", "U2"}, true},
		{"not in allowed list", "U3", []string{"U1", "U2"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesUser(tt.userID, tt.allowed); got != tt.want {
				t.Errorf("matchesUser() = %v, want %v", got, tt.want)
			}
		})
	}
}
