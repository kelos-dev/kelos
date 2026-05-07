package reporting

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

// Slack's chat.postMessage text field limit is 40,000 characters.
const slackFallbackTextLimit = 40000

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
	once     sync.Once
}

func (r *SlackReporter) api() *slack.Client {
	r.once.Do(func() {
		r.client = slack.New(r.BotToken)
	})
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

// phaseHeaderText maps each phase to its leading Block Kit section text.
// Phases without an entry (e.g. "succeeded") get no header block.
var phaseHeaderText = map[string]string{
	"accepted": ":hourglass_flowing_sand: *Working on your request...*",
	"failed":   ":x: *Something went wrong*",
}

// phaseFallbackText maps each phase to its default fallback text (before the
// "(Task: ...)" suffix) when no richer content is available.
var phaseFallbackText = map[string]string{
	"accepted":  "Working on your request...",
	"succeeded": "Done!",
	"failed":    "Failed.",
}

// FormatProgressMessage returns a Slack message with Block Kit blocks for
// an in-progress agent snapshot. Using blocks (rather than text-only) allows
// the activity indicator loop to append a context element showing the agent's
// current action.
func FormatProgressMessage(text, taskName string) SlackMessage {
	blocks := responseToBlocks(text)
	// Reserve 1 block for the trailing context block.
	if len(blocks) > SlackBlockLimit-1 {
		blocks = blocks[:SlackBlockLimit-1]
	}
	blocks = append(blocks, contextBlock(taskName))
	return SlackMessage{
		Text:   truncateFallbackText(text),
		Blocks: blocks,
	}
}

// FormatSlackTransitionMessage returns one or more rich Slack messages for a
// task phase transition. When the agent response is short enough to fit in a
// single message (≤ SlackBlockLimit blocks), a single SlackMessage is returned.
// For longer responses, the response blocks are split across multiple messages
// so that no individual message exceeds the Slack block limit, and each chunk
// is posted as a separate thread reply.
func FormatSlackTransitionMessage(phase, taskName, message string, results map[string]string) []SlackMessage {
	// Build the optional header block (e.g. "Working on your request…").
	var headerBlocks []slack.Block
	if header, ok := phaseHeaderText[phase]; ok {
		headerBlocks = append(headerBlocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, header, false, false),
			nil, nil,
		))
	}

	// Build the response blocks from the agent output.
	resp := results["response"]
	decoded := decodeResponse(resp)
	var responseBlocks []slack.Block
	if resp != "" {
		responseBlocks = responseToBlocks(decoded)
	}

	// Build the trailing blocks (PR link, error, context).
	var trailingBlocks []slack.Block
	pr := results["pr"]
	if pr != "" {
		trailingBlocks = append(trailingBlocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf(":link: *Pull Request:* <%s>", pr), false, false),
			nil, nil,
		))
	}

	if message != "" && phase == "failed" {
		trailingBlocks = append(trailingBlocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf(":warning: *Error:* %s", message), false, false),
			nil, nil,
		))
	}

	trailingBlocks = append(trailingBlocks, contextBlock(taskName))

	fallbackText := buildFallbackText(decoded, pr, message, phase, taskName)

	// Check if everything fits in a single message.
	totalBlocks := len(headerBlocks) + len(responseBlocks) + len(trailingBlocks)
	if totalBlocks <= SlackBlockLimit {
		var blocks []slack.Block
		blocks = append(blocks, headerBlocks...)
		blocks = append(blocks, responseBlocks...)
		blocks = append(blocks, trailingBlocks...)
		return []SlackMessage{{Text: fallbackText, Blocks: blocks}}
	}

	// Split response blocks across multiple messages.
	return splitResponseMessages(headerBlocks, responseBlocks, trailingBlocks, fallbackText, taskName)
}

// splitResponseMessages distributes response blocks across multiple messages
// so that each message stays within the SlackBlockLimit. The first message
// includes the header blocks, and the last message includes the trailing
// blocks (PR link, error, context). Continuation messages get a part number
// context block.
func splitResponseMessages(headerBlocks, responseBlocks, trailingBlocks []slack.Block, fallbackText, taskName string) []SlackMessage {
	// Reserve space in the first chunk for header blocks and either a
	// continuation context block (1) or trailing blocks, whichever is larger.
	firstReserve := 1
	if len(trailingBlocks) > firstReserve {
		firstReserve = len(trailingBlocks)
	}
	firstChunkCap := SlackBlockLimit - len(headerBlocks) - firstReserve
	// Reserve space in the last chunk for trailing blocks.
	lastChunkTrailing := len(trailingBlocks)

	// Split the response blocks into chunks.
	chunks := splitBlocks(responseBlocks, firstChunkCap, lastChunkTrailing)

	if len(chunks) == 0 {
		// No response blocks — single message with header + trailing.
		var blocks []slack.Block
		blocks = append(blocks, headerBlocks...)
		blocks = append(blocks, trailingBlocks...)
		return []SlackMessage{{Text: fallbackText, Blocks: blocks}}
	}

	totalParts := len(chunks)
	var messages []SlackMessage

	for i, chunk := range chunks {
		var blocks []slack.Block
		partNum := i + 1

		if i == 0 {
			// First message: header + first chunk of response blocks.
			blocks = append(blocks, headerBlocks...)
			blocks = append(blocks, chunk...)
			if totalParts > 1 {
				blocks = append(blocks, continuationContextBlock(taskName, partNum, totalParts))
			} else {
				blocks = append(blocks, trailingBlocks...)
			}
		} else if i == totalParts-1 {
			// Last message: remaining response blocks + trailing blocks.
			blocks = append(blocks, chunk...)
			blocks = append(blocks, trailingBlocks...)
		} else {
			// Middle message: response blocks + continuation context.
			blocks = append(blocks, chunk...)
			blocks = append(blocks, continuationContextBlock(taskName, partNum, totalParts))
		}

		text := fallbackText
		if i > 0 {
			text = fmt.Sprintf("(continued, part %d/%d) (Task: %s)", partNum, totalParts, taskName)
		}

		messages = append(messages, SlackMessage{Text: text, Blocks: blocks})
	}

	return messages
}

// splitBlocks divides blocks into chunks. The first chunk has capacity
// firstCap and the last chunk reserves lastReserve slots for trailing
// blocks. Middle chunks use SlackBlockLimit minus max(1, lastReserve) to
// ensure that even if a middle chunk ends up being the final one, the
// trailing blocks still fit within the limit.
func splitBlocks(blocks []slack.Block, firstCap, lastReserve int) [][]slack.Block {
	if len(blocks) == 0 {
		return nil
	}

	var chunks [][]slack.Block
	pos := 0

	// First chunk.
	end := pos + firstCap
	if end > len(blocks) {
		end = len(blocks)
	}
	chunks = append(chunks, blocks[pos:end])
	pos = end

	if pos >= len(blocks) {
		return chunks
	}

	// Middle and last chunks.
	for pos < len(blocks) {
		remaining := len(blocks) - pos

		// Check if remaining blocks fit in a final chunk with trailing
		// blocks. Reserve 1 extra for the continuation context on middle
		// chunks, but the last chunk uses trailing blocks instead.
		if remaining <= SlackBlockLimit-lastReserve {
			chunks = append(chunks, blocks[pos:])
			break
		}

		// Middle chunk: full limit minus max(1, lastReserve) so that if
		// this chunk ends up being the last one (consumed by
		// splitResponseMessages as the final part), the trailing blocks
		// still fit within SlackBlockLimit.
		chunkSize := SlackBlockLimit - max(1, lastReserve)
		end := pos + chunkSize
		if end > len(blocks) {
			end = len(blocks)
		}
		chunks = append(chunks, blocks[pos:end])
		pos = end
	}

	return chunks
}

func buildFallbackText(decoded, pr, message, phase, taskName string) string {
	var s string
	switch {
	case decoded != "" && pr != "" && message != "" && phase == "failed":
		s = fmt.Sprintf("%s\nPR: %s\nError: %s (Task: %s)", decoded, pr, message, taskName)
	case decoded != "" && pr != "":
		s = fmt.Sprintf("%s\nPR: %s (Task: %s)", decoded, pr, taskName)
	case decoded != "" && message != "" && phase == "failed":
		s = fmt.Sprintf("%s\nError: %s (Task: %s)", decoded, message, taskName)
	case decoded != "":
		s = fmt.Sprintf("%s (Task: %s)", decoded, taskName)
	case pr != "":
		s = fmt.Sprintf("PR: %s (Task: %s)", pr, taskName)
	case message != "" && phase == "failed":
		s = fmt.Sprintf("Error: %s (Task: %s)", message, taskName)
	default:
		s = fmt.Sprintf("%s (Task: %s)", phaseFallbackText[phase], taskName)
	}
	return truncateFallbackText(s)
}

func truncateFallbackText(s string) string {
	if utf8.RuneCountInString(s) <= slackFallbackTextLimit {
		return s
	}
	return string([]rune(s)[:slackFallbackTextLimit-1]) + "…"
}

// continuationContextBlock returns a context block indicating a multi-part
// message with the current part number and total.
func continuationContextBlock(taskName string, part, total int) *slack.ContextBlock {
	return slack.NewContextBlock("",
		slack.NewTextBlockObject(slack.MarkdownType,
			fmt.Sprintf("Task: `%s` · Part %d/%d", taskName, part, total), false, false),
	)
}
