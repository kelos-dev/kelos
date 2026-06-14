package slack

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
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
	// IsBotMessage indicates the message originated from a bot.
	IsBotMessage bool
	// IsSelfMessage indicates the message was sent by the bot itself.
	IsSelfMessage bool
}

var regexpCache sync.Map

type regexpCacheEntry struct {
	re  *regexp.Regexp
	err error
}

func getOrCompileRegexp(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexpCache.Load(pattern); ok {
		entry := cached.(*regexpCacheEntry)
		return entry.re, entry.err
	}
	re, err := regexp.Compile(pattern)
	entry := &regexpCacheEntry{re: re, err: err}
	if actual, loaded := regexpCache.LoadOrStore(pattern, entry); loaded {
		e := actual.(*regexpCacheEntry)
		return e.re, e.err
	}
	if err != nil {
		log.Printf("Invalid regex pattern %q: %v", pattern, err)
	}
	return re, err
}

// MatchesSpawner checks whether a Slack message matches the given TaskSpawner's
// Slack configuration (channels, bot mention, trigger patterns, exclude
// patterns, and bot message policy).
func MatchesSpawner(slackCfg *kelos.Slack, msg *SlackMessageData, botUserID string) bool {
	if slackCfg == nil {
		return false
	}
	if !matchesChannel(msg.ChannelID, slackCfg.Channels) {
		return false
	}
	// Slash commands bypass mention, trigger, and exclude filters.
	if msg.IsSlashCommand {
		return true
	}
	// Apply bot message policy.
	if msg.IsBotMessage {
		switch slackCfg.BotMessagePolicy {
		case kelos.BotMessagePolicyAll:
			// Allow all bot messages including self.
		case kelos.BotMessagePolicyOthersOnly:
			if msg.IsSelfMessage {
				return false
			}
		default:
			// None or empty — reject all bot messages.
			return false
		}
	}
	var positiveMatch bool
	if len(slackCfg.Triggers) == 0 {
		positiveMatch = hasBotMention(msg.Text, botUserID)
	} else {
		positiveMatch = matchesTriggers(msg.Text, slackCfg.Triggers, botUserID)
	}
	if !positiveMatch {
		return false
	}
	if matchesExcludePatterns(msg.Text, slackCfg.ExcludePatterns) {
		return false
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

// stripLeadingMentions removes Slack mention tokens (<@USERID> or
// <@USERID|display-name>) from the beginning of text so that trigger
// and exclude pattern matching targets semantic content.
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

// matchesExcludePatterns returns true if the message text matches any of
// the given regular expressions. Leading @-mentions are stripped before
// matching so patterns target semantic content.
func matchesExcludePatterns(text string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	text = stripLeadingMentions(text)
	for _, p := range patterns {
		re, err := getOrCompileRegexp(p)
		if err != nil {
			continue
		}
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// matchesTriggers evaluates trigger patterns against message text with OR
// semantics. Leading @-mentions are stripped before pattern matching so
// patterns target semantic content. Each trigger requires a bot mention
// (checked against the original text) unless MentionOptional is true.
func matchesTriggers(text string, triggers []kelos.SlackTrigger, botUserID string) bool {
	mentioned := hasBotMention(text, botUserID)
	stripped := stripLeadingMentions(text)
	for _, t := range triggers {
		re, err := getOrCompileRegexp(t.Pattern)
		if err != nil {
			continue
		}
		if !re.MatchString(stripped) {
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
