package slack

import (
	"fmt"
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
	// HasThreadContext indicates that Body contains full thread context
	// rather than the raw message text.
	HasThreadContext bool
	// IsSlashCommand indicates this came from a slash command rather than a message event.
	IsSlashCommand bool
	// SlashCommandID is the composite ID for slash commands (channelID:command:triggerID).
	SlashCommandID string
}

// MatchesSpawner checks whether a Slack message matches the given TaskSpawner's
// Slack configuration (channels and mention requirements).
func MatchesSpawner(slackCfg *v1alpha1.Slack, msg *SlackMessageData) bool {
	if slackCfg == nil {
		return false
	}
	if !matchesChannel(msg.ChannelID, slackCfg.Channels) {
		return false
	}
	// Mention filter: bypassed for slash commands (the command name acts as
	// the trigger), but still required for thread replies.
	if !msg.IsSlashCommand {
		if !matchesMention(msg.Text, slackCfg.MentionUserIDs) {
			return false
		}
	}
	return true
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

// matchesMention returns true if the message text contains an @-mention of
// at least one of the specified user IDs. Slack encodes mentions as <@USER_ID>.
// If mentionUserIDs is empty, no mention is required and the function returns true.
func matchesMention(text string, mentionUserIDs []string) bool {
	if len(mentionUserIDs) == 0 {
		return true
	}
	for _, uid := range mentionUserIDs {
		// Slack encodes mentions as <@USERID> or <@USERID|display-name>
		if strings.Contains(text, fmt.Sprintf("<@%s>", uid)) ||
			strings.Contains(text, fmt.Sprintf("<@%s|", uid)) {
			return true
		}
	}
	return false
}
