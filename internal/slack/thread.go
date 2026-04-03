package slack

import (
	"context"
	"fmt"
	"strings"
	"time"

	goslack "github.com/slack-go/slack"
)

const threadFetchTimeout = 10 * time.Second

// BotParticipated returns true if any message in the thread was sent by the
// given bot user ID. This prevents processing thread replies in conversations
// the bot never participated in.
func BotParticipated(msgs []goslack.Message, botUserID string) bool {
	for _, m := range msgs {
		if m.User == botUserID {
			return true
		}
	}
	return false
}

// FormatThreadContext formats a Slack thread's messages into a readable
// conversation string for use as a follow-up task prompt. Messages from the
// bot itself are labeled as "Agent" while all others use "User".
func FormatThreadContext(msgs []goslack.Message, botUserID string) string {
	var b strings.Builder
	b.WriteString("Slack thread conversation:\n")
	for _, m := range msgs {
		if m.Text == "" {
			continue
		}
		role := "User"
		if m.User == botUserID || m.BotID != "" {
			role = "Agent"
		}
		fmt.Fprintf(&b, "\n%s: %s\n", role, m.Text)
	}
	return b.String()
}

// FetchThreadContext fetches the full thread history and returns formatted
// context if the bot has participated. Returns ("", false, nil) when the bot
// has not participated, and a non-nil error for Slack API failures.
func FetchThreadContext(ctx context.Context, api *goslack.Client, channelID, threadTS, botUserID string) (string, bool, error) {
	threadCtx, cancel := context.WithTimeout(ctx, threadFetchTimeout)
	defer cancel()

	msgs, _, _, err := api.GetConversationRepliesContext(threadCtx,
		&goslack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
		})
	if err != nil {
		return "", false, fmt.Errorf("fetching thread replies: %w", err)
	}

	if !BotParticipated(msgs, botUserID) {
		return "", false, nil
	}

	return FormatThreadContext(msgs, botUserID), true, nil
}
