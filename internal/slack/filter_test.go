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
			name: "mention filter with channel and user filters all match",
			slackCfg: &v1alpha1.Slack{
				Channels:       []string{"C1"},
				AllowedUsers:   []string{"U1"},
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
		{
			name: "excludeCommands rejects matching message",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs:  []string{"UBOT1"},
				ExcludeCommands: []string{"/solve"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> /solve fix this"},
			want: false,
		},
		{
			name: "excludeCommands allows non-matching message",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs:  []string{"UBOT1"},
				ExcludeCommands: []string{"/solve"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> this is broken"},
			want: true,
		},
		{
			name: "excludeCommands NOT bypassed for thread replies",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs:  []string{"UBOT1"},
				ExcludeCommands: []string{"/solve"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> /solve go", ThreadTS: "1234567890.123456"},
			want: false,
		},
		{
			name: "excludeCommands allows thread reply without excluded prefix",
			slackCfg: &v1alpha1.Slack{
				MentionUserIDs:  []string{"UBOT1"},
				ExcludeCommands: []string{"/solve"},
			},
			msg:  &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> more context", ThreadTS: "1234567890.123456"},
			want: true,
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
		triggerCmd string
		wantBody   string
		wantOK     bool
	}{
		{
			name:       "no trigger command",
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
			name:       "mention before trigger command",
			text:       "<@UBOT1> /kelos fix the bug",
			triggerCmd: "/kelos",
			wantBody:   "fix the bug",
			wantOK:     true,
		},
		{
			name:       "mention with display name before trigger command",
			text:       "<@UBOT1|gravity> /kelos fix the bug",
			triggerCmd: "/kelos",
			wantBody:   "fix the bug",
			wantOK:     true,
		},
		{
			name:       "multiple mentions before trigger command",
			text:       "<@UBOT1> <@UBOT2> /kelos fix the bug",
			triggerCmd: "/kelos",
			wantBody:   "fix the bug",
			wantOK:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBody, gotOK := ProcessTriggerCommand(tt.text, tt.triggerCmd)
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

func TestMatchesExcludeCommands(t *testing.T) {
	tests := []struct {
		name            string
		text            string
		excludeCommands []string
		want            bool
	}{
		{"empty list never matches", "/solve fix", nil, false},
		{"exact prefix matches", "/solve fix", []string{"/solve"}, true},
		{"non-matching prefix", "/triage check", []string{"/solve"}, false},
		{"mention before excluded prefix", "<@UBOT1> /solve fix", []string{"/solve"}, true},
		{"mention with display name", "<@UBOT1|gravity> /solve fix", []string{"/solve"}, true},
		{"multiple excludes, first matches", "/solve fix", []string{"/solve", "/deploy"}, true},
		{"multiple excludes, second matches", "/deploy now", []string{"/solve", "/deploy"}, true},
		{"multiple excludes, none match", "hello world", []string{"/solve", "/deploy"}, false},
		{"text is just the command", "/solve", []string{"/solve"}, true},
		{"empty text", "", []string{"/solve"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesExcludeCommands(tt.text, tt.excludeCommands); got != tt.want {
				t.Errorf("matchesExcludeCommands() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripLeadingMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"no mentions", "/kelos fix", "/kelos fix"},
		{"single mention", "<@UBOT1> /kelos fix", "/kelos fix"},
		{"mention with display name", "<@UBOT1|gravity> /kelos fix", "/kelos fix"},
		{"multiple mentions", "<@UBOT1> <@UBOT2> /kelos fix", "/kelos fix"},
		{"mention only", "<@UBOT1>", ""},
		{"empty string", "", ""},
		{"no closing bracket", "<@UBOT1 broken", "<@UBOT1 broken"},
		{"non-mention angle bracket", "<#C123> hello", "<#C123> hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLeadingMentions(tt.text); got != tt.want {
				t.Errorf("stripLeadingMentions() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTriageSolverRouting validates the full end-to-end routing for the
// triage/solver spawner configuration used in dquality.
func TestTriageSolverRouting(t *testing.T) {
	triageCfg := &v1alpha1.Slack{
		MentionUserIDs:  []string{"UBOT1"},
		ExcludeCommands: []string{"/solve"},
		Channels:        []string{"C1"},
	}
	solverCfg := &v1alpha1.Slack{
		MentionUserIDs: []string{"UBOT1"},
		TriggerCommand: "/solve",
		Channels:       []string{"C1"},
	}

	scenarios := []struct {
		name       string
		msg        *SlackMessageData
		wantTriage bool
		wantSolver bool
	}{
		{
			name:       "top-level mention triggers triage only",
			msg:        &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> this endpoint is broken"},
			wantTriage: true,
			wantSolver: false,
		},
		{
			name:       "top-level /solve triggers solver only",
			msg:        &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> /solve fix it"},
			wantTriage: false,
			wantSolver: true,
		},
		{
			name:       "thread reply with mention triggers triage only",
			msg:        &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> more context", ThreadTS: "123.456"},
			wantTriage: true,
			wantSolver: false,
		},
		{
			name:       "thread reply with /solve triggers solver only",
			msg:        &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "<@UBOT1> /solve go ahead", ThreadTS: "123.456"},
			wantTriage: false,
			wantSolver: true,
		},
		{
			name:       "no mention triggers neither",
			msg:        &SlackMessageData{UserID: "U1", ChannelID: "C1", Text: "this is broken"},
			wantTriage: false,
			wantSolver: false,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			triageMatch := MatchesSpawner(triageCfg, sc.msg)
			if triageMatch {
				_, triageMatch = ProcessTriggerCommand(sc.msg.Text, triageCfg.TriggerCommand)
			}

			solverMatch := MatchesSpawner(solverCfg, sc.msg)
			if solverMatch {
				_, solverMatch = ProcessTriggerCommand(sc.msg.Text, solverCfg.TriggerCommand)
			}

			if triageMatch != sc.wantTriage {
				t.Errorf("triage: got %v, want %v", triageMatch, sc.wantTriage)
			}
			if solverMatch != sc.wantSolver {
				t.Errorf("solver: got %v, want %v", solverMatch, sc.wantSolver)
			}
		})
	}
}
