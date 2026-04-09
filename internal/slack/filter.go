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
	// HasThreadContext indicates that Body contains full thread context
	// rather than the raw message text.
	HasThreadContext bool
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
	// Mention filter: bypassed for slash commands (the command name acts as
	// the trigger), but still required for thread replies.
	if !msg.IsSlashCommand {
		if !matchesMention(msg.Text, slackCfg.MentionUserIDs) {
			return false
		}
	}
	// ExcludeCommands filter: reject messages matching any excluded prefix.
	// Applied consistently for all message types including thread replies.
	if matchesExcludeCommands(msg.Text, slackCfg.ExcludeCommands) {
		return false
	}
	return true
}

// ProcessTriggerCommand checks whether the message text matches the TaskSpawner's
// trigger command prefix. Leading @-mentions are stripped before matching so that
// "@bot /cmd args" works the same as "/cmd args". Returns the processed body
// (with prefix removed) and true if the message should be processed.
func ProcessTriggerCommand(text, triggerCmd string) (string, bool) {
	if triggerCmd != "" {
		// Strip leading Slack mentions so "@bot /cmd args" works like "/cmd args"
		cleaned := stripLeadingMentions(text)
		if !strings.HasPrefix(cleaned, triggerCmd) {
			return "", false
		}
		body := strings.TrimSpace(strings.TrimPrefix(cleaned, triggerCmd))
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

// stripLeadingMentions removes Slack mention tokens (<@USERID> or
// <@USERID|display-name>) from the beginning of text so that trigger
// command matching works regardless of mention placement.
func stripLeadingMentions(text string) string {
	s := text
	for {
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, "<@") {
			return s
		}
		end := strings.Index(s, ">")
		if end == -1 {
			return s
		}
		s = s[end+1:]
	}
}

// matchesExcludeCommands returns true if the message text (after stripping
// leading @-mentions) starts with any of the exclude command prefixes.
// When it returns true, the spawner should NOT process this message.
func matchesExcludeCommands(text string, excludeCommands []string) bool {
	if len(excludeCommands) == 0 {
		return false
	}
	cleaned := stripLeadingMentions(text)
	for _, prefix := range excludeCommands {
		if strings.HasPrefix(cleaned, prefix) {
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
