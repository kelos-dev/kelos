package reporting

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// decodeResponse decodes a base64-encoded agent response from task results.
// Returns the raw string if decoding fails (backward compatibility).
func decodeResponse(encoded string) string {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}
	return string(decoded)
}

// SlackReporter posts and updates thread replies in Slack channels.
type SlackReporter struct {
	// BotToken is the Bot User OAuth Token (xoxb-...).
	BotToken string
	client   *slack.Client
}

func (r *SlackReporter) api() *slack.Client {
	if r.client == nil {
		r.client = slack.New(r.BotToken)
	}
	return r.client
}

// PostThreadReply posts a new message as a thread reply and returns the
// reply's message timestamp.
func (r *SlackReporter) PostThreadReply(ctx context.Context, channel, threadTS, text string) (string, error) {
	_, ts, err := r.api().PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		return "", fmt.Errorf("posting Slack thread reply: %w", err)
	}
	return ts, nil
}

// UpdateMessage updates an existing Slack message in place.
func (r *SlackReporter) UpdateMessage(ctx context.Context, channel, messageTS, text string) error {
	_, _, _, err := r.api().UpdateMessageContext(ctx, channel, messageTS,
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		return fmt.Errorf("updating Slack message: %w", err)
	}
	return nil
}

// FormatSlackAccepted returns the thread reply text for an accepted task.
func FormatSlackAccepted(taskName string) string {
	return fmt.Sprintf("Working on your request... (Task: %s)", taskName)
}

// FormatSlackSucceeded returns the thread reply text for a succeeded task.
// When results contain an agent response or PR URL, they are included.
func FormatSlackSucceeded(taskName string, results map[string]string) string {
	var parts []string
	if resp := results["response"]; resp != "" {
		parts = append(parts, decodeResponse(resp))
	}
	if pr := results["pr"]; pr != "" {
		parts = append(parts, "PR: "+pr)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Done! (Task: %s)", taskName)
	}
	return fmt.Sprintf("%s (Task: %s)", strings.Join(parts, "\n"), taskName)
}

// FormatSlackFailed returns the thread reply text for a failed task.
// When a status message or agent response is available, it is included.
func FormatSlackFailed(taskName, message string, results map[string]string) string {
	var parts []string
	if resp := results["response"]; resp != "" {
		parts = append(parts, decodeResponse(resp))
	}
	if message != "" {
		parts = append(parts, "Error: "+message)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Failed. (Task: %s)", taskName)
	}
	return fmt.Sprintf("%s (Task: %s)", strings.Join(parts, "\n"), taskName)
}
