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
			name: "mention filter matches",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hey <@UBOT1> fix this"},
			want: true,
		},
		{
			name: "mention filter rejects when no mention present",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hey fix this"},
			want: false,
		},
		{
			name: "mention filter matches any of multiple IDs",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs: []string{"UBOT1", "UBOT2"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hey <@UBOT2> help"},
			want: true,
		},
		{
			name: "mention filter required for thread replies",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "follow up no mention", ThreadTS: "1234567890.123456"},
			want: false,
		},
		{
			name: "mention filter passes for thread reply with mention",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> follow up", ThreadTS: "1234567890.123456"},
			want: true,
		},
		{
			name: "mention filter bypassed for slash commands",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "fix this", IsSlashCommand: true},
			want: true,
		},
		{
			name: "mention filter with channel filter both match",
			slackCfg: &v1alpha1.Slack{
				Channels:       []string{"C1"},
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> do the thing"},
			want: true,
		},
		{
			name: "mention filter passes but channel rejects",
			slackCfg: &v1alpha1.Slack{
				Channels:       []string{"C2"},
				MentionUserIDs: []string{"UBOT1"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> help"},
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
		hasContent bool
		selfUserID string
		want       bool
	}{
		{
			name:       "normal message",
			userID:     "U1",
			hasContent: true,
			selfUserID: "UBOT",
			want:       true,
		},
		{
			name:       "self message filtered",
			userID:     "UBOT",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "bot_message subtype filtered",
			userID:     "U1",
			subtype:    "bot_message",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_changed subtype filtered",
			userID:     "U1",
			subtype:    "message_changed",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_deleted subtype filtered",
			userID:     "U1",
			subtype:    "message_deleted",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "message_replied subtype filtered",
			userID:     "U1",
			subtype:    "message_replied",
			hasContent: true,
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name:       "no content filtered",
			userID:     "U1",
			hasContent: false,
			selfUserID: "UBOT",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldProcess(tt.userID, tt.subtype, tt.hasContent, tt.selfUserID)
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

func TestMatchesMention(t *testing.T) {
	tests := []struct {
		name           string
		text           string
		mentionUserIDs []string
		want           bool
	}{
		{"empty mention list matches all", "hello", nil, true},
		{"mention present matches", "hey <@UBOT1> fix", []string{"UBOT1"}, true},
		{"mention absent rejects", "hey fix this", []string{"UBOT1"}, false},
		{"partial user ID does not match", "hey <@UBOT10> fix", []string{"UBOT1"}, false},
		{"any of multiple mentions matches", "hey <@UBOT2>", []string{"UBOT1", "UBOT2"}, true},
		{"none of multiple mentions rejects", "hey there", []string{"UBOT1", "UBOT2"}, false},
		{"mention with display name matches", "hey <@UBOT1|kelos-bot> fix", []string{"UBOT1"}, true},
		{"mention without angle brackets does not match", "hey @UBOT1 fix", []string{"UBOT1"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesMention(tt.text, tt.mentionUserIDs); got != tt.want {
				t.Errorf("matchesMention() = %v, want %v", got, tt.want)
			}
		})
	}
}
