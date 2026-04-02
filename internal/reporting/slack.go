package reporting

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
)

var (
	// reHeading matches Markdown headings (# through ######).
	reHeading = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	// reBold matches **bold** syntax.
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*`)
	// reLink matches [text](url) syntax, allowing one level of balanced parentheses
	// in the URL (e.g. Wikipedia/RFC links like https://en.wikipedia.org/wiki/Go_(language)).
	reLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*(?:\([^)]*\))[^)]*|[^)]+)\)`)
	// reStrikethrough matches ~~text~~ syntax.
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
)

// convertMarkdownToSlack converts standard Markdown to Slack mrkdwn format.
func convertMarkdownToSlack(md string) string {
	s := md
	// Convert headings to bold text, stripping any inner ** markers.
	s = reHeading.ReplaceAllStringFunc(s, func(m string) string {
		inner := reHeading.FindStringSubmatch(m)[1]
		inner = reBold.ReplaceAllString(inner, "$1") // strip ** before wrapping
		return "*" + inner + "*"
	})
	// Convert remaining **bold** outside headings.
	s = reBold.ReplaceAllString(s, "*$1*")
	// Convert [text](url) to <url|text>.
	s = reLink.ReplaceAllString(s, "<$2|$1>")
	// Convert ~~strikethrough~~ to ~strikethrough~.
	s = reStrikethrough.ReplaceAllString(s, "~$1~")
	// Collapse triple-newlines or more into double.
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// decodeResponse decodes a base64-encoded agent response from task results.
// Returns the raw string if decoding fails (backward compatibility).
func decodeResponse(encoded string) string {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}
	return string(decoded)
}

// SlackMessage holds the components of a rich Slack message.
type SlackMessage struct {
	// Text is the fallback text shown in notifications and accessibility contexts.
	Text string
	// Blocks are the Block Kit blocks for rich formatting.
	Blocks []slack.Block
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
func (r *SlackReporter) PostThreadReply(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
	opts := []slack.MsgOption{
		slack.MsgOptionText(msg.Text, false),
		slack.MsgOptionTS(threadTS),
	}
	if len(msg.Blocks) > 0 {
		opts = append(opts, slack.MsgOptionBlocks(msg.Blocks...))
	}
	_, ts, err := r.api().PostMessageContext(ctx, channel, opts...)
	if err != nil {
		return "", fmt.Errorf("posting Slack thread reply: %w", err)
	}
	return ts, nil
}

// UpdateMessage updates an existing Slack message in place.
func (r *SlackReporter) UpdateMessage(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
	opts := []slack.MsgOption{
		slack.MsgOptionText(msg.Text, false),
	}
	if len(msg.Blocks) > 0 {
		opts = append(opts, slack.MsgOptionBlocks(msg.Blocks...))
	}
	_, _, _, err := r.api().UpdateMessageContext(ctx, channel, messageTS, opts...)
	if err != nil {
		return fmt.Errorf("updating Slack message: %w", err)
	}
	return nil
}

// contextBlock returns a context block displaying the task name.
func contextBlock(taskName string) *slack.ContextBlock {
	return slack.NewContextBlock("",
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("Task: `%s`", taskName), false, false),
	)
}

// FormatSlackAccepted returns the Slack message for an accepted task.
func FormatSlackAccepted(taskName string) SlackMessage {
	return SlackMessage{
		Text: fmt.Sprintf("Working on your request... (Task: %s)", taskName),
		Blocks: []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject(slack.MarkdownType, ":hourglass_flowing_sand: *Working on your request...*", false, false),
				nil, nil,
			),
			contextBlock(taskName),
		},
	}
}

// FormatSlackSucceeded returns the Slack message for a succeeded task.
// When results contain an agent response or PR URL, they are included.
func FormatSlackSucceeded(taskName string, results map[string]string) SlackMessage {
	fallbackText := fmt.Sprintf("Done! (Task: %s)", taskName)
	var blocks []slack.Block

	resp := results["response"]
	pr := results["pr"]
	decoded := convertMarkdownToSlack(decodeResponse(resp))

	if resp != "" {
		fallbackText = fmt.Sprintf("%s (Task: %s)", decoded, taskName)
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, decoded, false, false),
			nil, nil,
		))
	}

	if pr != "" {
		if resp != "" {
			fallbackText = fmt.Sprintf("%s\nPR: %s (Task: %s)", decoded, pr, taskName)
		} else {
			fallbackText = fmt.Sprintf("PR: %s (Task: %s)", pr, taskName)
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf(":link: *Pull Request:* <%s>", pr), false, false),
			nil, nil,
		))
	}

	blocks = append(blocks, contextBlock(taskName))

	return SlackMessage{
		Text:   fallbackText,
		Blocks: blocks,
	}
}

// FormatSlackFailed returns the Slack message for a failed task.
// When a status message or agent response is available, it is included.
func FormatSlackFailed(taskName, message string, results map[string]string) SlackMessage {
	fallbackText := fmt.Sprintf("Failed. (Task: %s)", taskName)
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, ":x: *Something went wrong*", false, false),
			nil, nil,
		),
	}

	resp := ""
	if results != nil {
		resp = results["response"]
	}
	decoded := convertMarkdownToSlack(decodeResponse(resp))

	if resp != "" {
		fallbackText = fmt.Sprintf("%s (Task: %s)", decoded, taskName)
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, decoded, false, false),
			nil, nil,
		))
	}

	if message != "" {
		if resp != "" {
			fallbackText = fmt.Sprintf("%s\nError: %s (Task: %s)", decoded, message, taskName)
		} else {
			fallbackText = fmt.Sprintf("Error: %s (Task: %s)", message, taskName)
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf(":warning: *Error:* %s", message), false, false),
			nil, nil,
		))
	}

	blocks = append(blocks, contextBlock(taskName))

	return SlackMessage{
		Text:   fallbackText,
		Blocks: blocks,
	}
}
