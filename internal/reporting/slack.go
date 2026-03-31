package reporting

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"
)

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
// When results contain a PR URL, it is included in the message.
func FormatSlackSucceeded(taskName string, results map[string]string) string {
	if pr := results["pr"]; pr != "" {
		return fmt.Sprintf("Done! PR: %s (Task: %s)", pr, taskName)
	}
	return fmt.Sprintf("Done! (Task: %s)", taskName)
}

// FormatSlackFailed returns the thread reply text for a failed task.
// When a status message is available, it is included in the reply.
func FormatSlackFailed(taskName, message string) string {
	if message != "" {
		return fmt.Sprintf("Failed: %s (Task: %s)", message, taskName)
	}
	return fmt.Sprintf("Failed. (Task: %s)", taskName)
}
