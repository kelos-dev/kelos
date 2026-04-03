package slack

import (
	"strings"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// SlackMessageData holds the parsed fields from a Slack message or slash
// command needed for matching and task creation.
type SlackMessageData struct {
	// UserID is the Slack user ID of the message author.
	UserID string
	// ChannelID is the Slack channel ID where the message was posted.
	ChannelID string
	// ChannelName is the human-readable channel name.
	ChannelName string
	// UserName is the display name of the message author.
	UserName string
	// Text is the raw message text.
	Text string
	// ThreadTS is the parent message timestamp when this is a thread reply.
	ThreadTS string
	// Timestamp is the message's own timestamp (used as ID and thread_ts for replies).
	Timestamp string
	// Permalink is the Slack permalink URL for the message.
	Permalink string
	// Body is the processed message body (trigger prefix stripped, or full thread context).
	Body string
	// IsSlashCommand indicates this came from a slash command rather than a message event.
	IsSlashCommand bool
	// SlashCommandID is the composite ID for slash commands (channelID:command:triggerID).
	SlashCommandID string
}

// MatchesSpawner checks whether a Slack message matches the given TaskSpawner's
// Slack configuration (channels and allowed users). Trigger command matching
// is handled separately during message preprocessing.
func MatchesSpawner(slackCfg *v1alpha1.Slack, msg *SlackMessageData) bool {
	if slackCfg == nil {
		return false
	}
	if !matchesChannel(msg.ChannelID, slackCfg.Channels) {
		return false
	}
	if !matchesUser(msg.UserID, slackCfg.AllowedUsers) {
		return false
	}
	return true
}

// ProcessTriggerCommand checks whether the message text matches the TaskSpawner's
// trigger command prefix. For thread replies, the trigger is not required.
// Returns the processed body and true if the message should be processed.
func ProcessTriggerCommand(text, threadTS, triggerCmd string) (string, bool) {
	// Thread replies are follow-ups — no trigger required
	if threadTS != "" {
		return text, true
	}

	if triggerCmd != "" {
		if !strings.HasPrefix(text, triggerCmd) {
			return "", false
		}
		body := strings.TrimSpace(strings.TrimPrefix(text, triggerCmd))
		if body == "" {
			return "", false
		}
		return body, true
	}

	return text, true
}

// ExtractSlackWorkItem builds the template variables map from a Slack message
// for use with taskbuilder.BuildTask. The keys match the standard template
// variables available in promptTemplate and branch.
func ExtractSlackWorkItem(msg *SlackMessageData) map[string]interface{} {
	id := msg.Timestamp
	if msg.IsSlashCommand {
		id = msg.SlashCommandID
	}

	return map[string]interface{}{
		"ID":    id,
		"Title": msg.UserName,
		"Body":  msg.Body,
		"URL":   msg.Permalink,
		"Kind":  "SlackMessage",
	}
}

// matchesChannel returns true if channelID is in the allowed list,
// or if the allowed list is empty (all channels permitted).
func matchesChannel(channelID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == channelID {
			return true
		}
	}
	return false
}

// matchesUser returns true if userID is in the allowed list,
// or if the allowed list is empty (all users permitted).
func matchesUser(userID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == userID {
			return true
		}
	}
	return false
}
