package slack

import (
	"context"
	"fmt"
	"strings"
	"time"

	goslack "github.com/slack-go/slack"
)

const threadFetchTimeout = 10 * time.Second

// FormatThreadContext formats a Slack thread's messages into a readable
// conversation string for use as a follow-up task prompt. Messages from the
// bot itself are labeled as "Agent" while all others use "User". The last
// non-empty message is prefixed with [TRIGGERING MESSAGE] to indicate which
// message spawned the task.
func FormatThreadContext(msgs []goslack.Message, botUserID string) string {
	lastIdx := -1
	for i, m := range msgs {
		attachText := formatAttachments(m.Attachments)
		if m.Text != "" || attachText != "" {
			lastIdx = i
		}
	}

	var b strings.Builder
	b.WriteString("Slack thread conversation:\n")
	for i, m := range msgs {
		attachText := formatAttachments(m.Attachments)
		if m.Text == "" && attachText == "" {
			continue
		}
		role := "User"
		if m.User == botUserID || m.BotID != "" {
			role = "Agent"
		}
		prefix := ""
		if i == lastIdx {
			prefix = "[TRIGGERING MESSAGE] "
		}
		switch {
		case m.Text != "" && attachText != "":
			fmt.Fprintf(&b, "\n%s%s: %s\n%s\n", prefix, role, m.Text, attachText)
		case m.Text != "":
			fmt.Fprintf(&b, "\n%s%s: %s\n", prefix, role, m.Text)
		default:
			fmt.Fprintf(&b, "\n%s%s: [attachment]\n%s\n", prefix, role, attachText)
		}
	}
	return b.String()
}

// formatAttachments extracts text content from Slack message attachments
// (forwarded messages, unfurls, etc.) and returns a formatted string.
// Returns empty string if there are no text-bearing attachments.
func formatAttachments(attachments []goslack.Attachment) string {
	var parts []string
	for _, a := range attachments {
		var lines []string
		if a.Pretext != "" {
			lines = append(lines, a.Pretext)
		}
		if a.Text != "" {
			lines = append(lines, "> "+strings.ReplaceAll(a.Text, "\n", "\n> "))
		}
		if a.Fallback != "" && a.Text == "" {
			lines = append(lines, "> "+strings.ReplaceAll(a.Fallback, "\n", "\n> "))
		}
		if len(lines) > 0 {
			parts = append(parts, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(parts, "\n")
}

// FetchThreadContext fetches the full thread history and returns formatted
// context. The caller decides whether to process the message — this function
// always returns the thread body when the API call succeeds.
func FetchThreadContext(ctx context.Context, api *goslack.Client, channelID, threadTS, botUserID string) (string, error) {
	threadCtx, cancel := context.WithTimeout(ctx, threadFetchTimeout)
	defer cancel()

	msgs, _, _, err := api.GetConversationRepliesContext(threadCtx,
		&goslack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
		})
	if err != nil {
		return "", fmt.Errorf("fetching thread replies: %w", err)
	}

	return FormatThreadContext(msgs, botUserID), nil
}
