package slack

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

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

var triggerRegexpCache sync.Map

func getOrCompileTriggerRegexp(pattern string) (*regexp.Regexp, error) {
	if cached, ok := triggerRegexpCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	triggerRegexpCache.Store(pattern, re)
	return re, nil
}

// MatchesSpawner checks whether a Slack message matches the given TaskSpawner's
// Slack configuration (channels, bot mention, and trigger patterns).
func MatchesSpawner(slackCfg *v1alpha1.Slack, msg *SlackMessageData, botUserID string) bool {
	if slackCfg == nil {
		return false
	}
	if !matchesChannel(msg.ChannelID, slackCfg.Channels) {
		return false
	}
	// Slash commands bypass mention and trigger filters.
	if msg.IsSlashCommand {
		return true
	}
	// No triggers: fire on any bot mention.
	if len(slackCfg.Triggers) == 0 {
		return hasBotMention(msg.Text, botUserID)
	}
	// With triggers: OR across triggers.
	return matchesTriggers(msg.Text, slackCfg.Triggers, botUserID)
}

// ExtractSlackWorkItem builds the template variables map from a Slack message
// for use with taskbuilder.BuildTask. The keys match the standard template
// variables available in promptTemplate and branch.
func ExtractSlackWorkItem(msg *SlackMessageData) map[string]interface{} {
	id := msg.Timestamp
	if msg.IsSlashCommand {
		id = msg.SlashCommandID
	}

	title := msg.Text
	if idx := strings.Index(title, "\n"); idx != -1 {
		title = title[:idx]
	}

	return map[string]interface{}{
		"ID":    id,
		"Title": title,
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

// hasBotMention returns true if the message text contains an @-mention of
// the bot user ID. Slack encodes mentions as <@USER_ID> or <@USER_ID|name>.
func hasBotMention(text string, botUserID string) bool {
	if botUserID == "" {
		return false
	}
	return strings.Contains(text, fmt.Sprintf("<@%s>", botUserID)) ||
		strings.Contains(text, fmt.Sprintf("<@%s|", botUserID))
}

// matchesTriggers evaluates trigger patterns against message text with OR
// semantics. Each trigger requires pattern match AND bot mention, unless
// MentionOptional is true on that trigger.
func matchesTriggers(text string, triggers []v1alpha1.SlackTrigger, botUserID string) bool {
	mentioned := hasBotMention(text, botUserID)
	for _, t := range triggers {
		re, err := getOrCompileTriggerRegexp(t.Pattern)
		if err != nil {
			continue
		}
		if !re.MatchString(text) {
			continue
		}
		if t.MentionOptional != nil && *t.MentionOptional {
			return true
		}
		if mentioned {
			return true
		}
	}
	return false
}
