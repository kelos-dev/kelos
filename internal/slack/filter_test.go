package slack

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func boolPtr(b bool) *bool { return &b }

func TestMatchesSpawner(t *testing.T) {
	tests := []struct {
		name      string
		slackCfg  *v1alpha1.Slack
		msg       *SlackMessageData
		botUserID string
		want      bool
	}{
		{
			name:      "nil slack config",
			slackCfg:  nil,
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name:      "empty config with bot mention matches",
			slackCfg:  &v1alpha1.Slack{},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hey <@UBOT1> help"},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "empty config without bot mention rejects",
			slackCfg:  &v1alpha1.Slack{},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hey help"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "channel filter matches",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C1", "C2"},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> hi"},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name: "channel filter rejects",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C2", "C3"},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> hi"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "trigger with pattern and mention matches",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "fix.*bug"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> fix the bug"},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name: "trigger with pattern match but no mention rejects",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "fix.*bug"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "fix the bug"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "trigger with mention but pattern does not match rejects",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "deploy"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> fix the bug"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "trigger with mentionOptional fires on pattern alone",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "fix.*bug", MentionOptional: boolPtr(true)},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "fix the bug"},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name: "trigger with mentionOptional=false requires mention",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "fix.*bug", MentionOptional: boolPtr(false)},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "fix the bug"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "multiple triggers OR semantics first misses second hits",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "deploy"},
					{Pattern: "fix"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> fix the bug"},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name: "multiple triggers none match",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "deploy"},
					{Pattern: "rollback"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> fix the bug"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "slash command bypasses mention and triggers",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "deploy"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "fix this", IsSlashCommand: true},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "thread reply with bot mention matches",
			slackCfg:  &v1alpha1.Slack{},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> follow up", ThreadTS: "1234567890.123456"},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "thread reply without bot mention rejects",
			slackCfg:  &v1alpha1.Slack{},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "follow up", ThreadTS: "1234567890.123456"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "channel filter passes but no mention rejects",
			slackCfg: &v1alpha1.Slack{
				Channels: []string{"C1"},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "hello"},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "invalid trigger regex is skipped",
			slackCfg: &v1alpha1.Slack{
				Triggers: []v1alpha1.SlackTrigger{
					{Pattern: "[invalid"},
					{Pattern: "fix"},
				},
			},
			msg:       &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> fix it"},
			botUserID: "UBOT1",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesSpawner(tt.slackCfg, tt.msg, tt.botUserID)
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
			Text:      "fix the login page",
			Body:      "fix the login page",
			Timestamp: "1234567890.123456",
			Permalink: "https://slack.com/archives/C1/p1234567890123456",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["ID"] != "1234567890.123456" {
			t.Errorf("ID = %v, want %v", vars["ID"], "1234567890.123456")
		}
		if vars["Title"] != "fix the login page" {
			t.Errorf("Title = %v, want %v", vars["Title"], "fix the login page")
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
			Text:           "do something",
			Body:           "do something",
			IsSlashCommand: true,
			SlashCommandID: "C1:/kelos:trigger123",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["ID"] != "C1:/kelos:trigger123" {
			t.Errorf("ID = %v, want %v", vars["ID"], "C1:/kelos:trigger123")
		}
	})

	t.Run("multi-line message uses first line as title", func(t *testing.T) {
		msg := &SlackMessageData{
			UserID:    "U123",
			UserName:  "Alice",
			Text:      "fix the login page\nmore details here\nand more",
			Body:      "fix the login page\nmore details here\nand more",
			Timestamp: "1234567890.123456",
		}

		vars := ExtractSlackWorkItem(msg)

		if vars["Title"] != "fix the login page" {
			t.Errorf("Title = %v, want %v", vars["Title"], "fix the login page")
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

func TestHasBotMention(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      bool
	}{
		{"mention present", "hey <@UBOT1> fix", "UBOT1", true},
		{"mention with display name", "hey <@UBOT1|kelos-bot> fix", "UBOT1", true},
		{"mention absent", "hey fix this", "UBOT1", false},
		{"empty bot user ID", "hey <@UBOT1> fix", "", false},
		{"partial ID does not match", "hey <@UBOT10> fix", "UBOT1", false},
		{"mention without angle brackets", "hey @UBOT1 fix", "UBOT1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasBotMention(tt.text, tt.botUserID); got != tt.want {
				t.Errorf("hasBotMention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesTriggers(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		triggers  []v1alpha1.SlackTrigger
		botUserID string
		want      bool
	}{
		{
			name:      "pattern matches with mention",
			text:      "<@UBOT1> deploy prod",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy"}},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "pattern matches without mention requires mention",
			text:      "deploy prod",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy"}},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name:      "mentionOptional allows pattern only",
			text:      "deploy prod",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy", MentionOptional: boolPtr(true)}},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "pattern does not match",
			text:      "<@UBOT1> rollback",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "deploy"}},
			botUserID: "UBOT1",
			want:      false,
		},
		{
			name: "OR semantics across triggers",
			text: "<@UBOT1> rollback",
			triggers: []v1alpha1.SlackTrigger{
				{Pattern: "deploy"},
				{Pattern: "rollback"},
			},
			botUserID: "UBOT1",
			want:      true,
		},
		{
			name:      "invalid regex skipped",
			text:      "<@UBOT1> fix it",
			triggers:  []v1alpha1.SlackTrigger{{Pattern: "[invalid"}, {Pattern: "fix"}},
			botUserID: "UBOT1",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesTriggers(tt.text, tt.triggers, tt.botUserID); got != tt.want {
				t.Errorf("matchesTriggers() = %v, want %v", got, tt.want)
			}
		})
	}
}
