package reporting

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

// Slack's chat.postMessage text field limit is 40,000 characters.
const slackFallbackTextLimit = 40000

const (
	stableSummaryRootLimit    = 1800
	stableSummaryStatusLimit  = 1400
	stableSummaryFinalLimit   = 1800
	sessionSummaryRootLimit   = 1800
	sessionSummaryLatestLimit = 1400
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

// PostMessage posts a new top-level message and returns its timestamp.
func (r *SlackReporter) PostMessage(ctx context.Context, channel string, msg SlackMessage) (string, error) {
	opts := []slack.MsgOption{
		slack.MsgOptionText(msg.Text, false),
	}
	if len(msg.Blocks) > 0 {
		opts = append(opts, slack.MsgOptionBlocks(msg.Blocks...))
	}
	_, ts, err := r.api().PostMessageContext(ctx, channel, opts...)
	if err != nil {
		return "", fmt.Errorf("posting Slack message: %w", err)
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

// FormatStableSummaryProgressMessage returns a compact root message that keeps
// the first material progress summary pinned while the latest status changes.
func FormatStableSummaryProgressMessage(stableSummary, currentProgress, taskName string) SlackMessage {
	stableSummary, _ = compactSlackText(stableSummary, stableSummaryRootLimit)
	currentProgress = strings.TrimSpace(currentProgress)

	blocks := []slack.Block{
		headerBlock("Infra health issue detected"),
		slackSection(fmt.Sprintf("*Detected issue*\n%s", stableSummary)),
	}

	if currentProgress != "" && currentProgress != stableSummary {
		currentProgress, _ = compactSlackText(currentProgress, stableSummaryStatusLimit)
		blocks = append(blocks, slackSection(fmt.Sprintf("*Current status*\n%s", currentProgress)))
	}

	blocks = append(blocks, contextBlock(taskName))

	text := buildStableSummaryFallback("Infra health issue detected", stableSummary, currentProgress, "", "", taskName)
	return SlackMessage{Text: text, Blocks: blocks}
}

// FormatStableSummaryFinalMessage returns a compact final root message. Full
// terminal details are posted separately with FormatSlackTransitionMessage.
func FormatStableSummaryFinalMessage(stableSummary, phase, taskName, message string, results map[string]string) SlackMessage {
	stableSummary, _ = compactSlackText(stableSummary, stableSummaryRootLimit)

	title := "Infra health investigation complete"
	if phase == "failed" {
		title = "Infra health investigation failed"
	}

	resp := decodeResponse(results["response"])
	outcome := strings.TrimSpace(resp)
	if outcome == "" && phase == "failed" {
		outcome = strings.TrimSpace(message)
	}
	if outcome == "" {
		outcome = phaseFallbackText[phase]
	}
	outcome, truncated := compactSlackText(outcome, stableSummaryFinalLimit)
	if truncated || strings.TrimSpace(resp) != "" {
		outcome = strings.TrimSpace(outcome + "\n\nFull details are posted in this message thread.")
	}

	pr := strings.TrimSpace(results["pr"])
	errText := ""
	if phase == "failed" && strings.TrimSpace(message) != "" && strings.TrimSpace(message) != outcome {
		errText, _ = compactSlackText(message, stableSummaryStatusLimit)
	}

	blocks := []slack.Block{
		headerBlock(title),
		slackSection(fmt.Sprintf("*Detected issue*\n%s", stableSummary)),
		slackSection(fmt.Sprintf("*Outcome*\n%s", outcome)),
	}
	if pr != "" {
		blocks = append(blocks, slackSection(fmt.Sprintf(":link: *Pull Request:* <%s>", pr)))
	}
	if errText != "" {
		blocks = append(blocks, slackSection(fmt.Sprintf(":warning: *Error:* %s", errText)))
	}
	blocks = append(blocks, contextBlock(taskName))

	text := buildStableSummaryFallback(title, stableSummary, outcome, pr, errText, taskName)
	return SlackMessage{Text: text, Blocks: blocks}
}

// FormatSessionSummaryRootMessage returns the persistent root message for a
// proactive AgentSession. It intentionally stays generic: a concise additive
// summary plus the latest in-flight status. Full turn details are posted in
// the message thread.
func FormatSessionSummaryRootMessage(title, summary, latest, taskName string) SlackMessage {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Cody session"
	}
	summary, _ = compactSlackText(summary, sessionSummaryRootLimit)
	latest = strings.TrimSpace(latest)
	if latest == summary {
		latest = ""
	}
	latest, _ = compactSlackText(latest, sessionSummaryLatestLimit)

	blocks := []slack.Block{headerBlock(title)}
	if summary != "" {
		blocks = append(blocks, slackSection(fmt.Sprintf("*Summary*\n%s", summary)))
	}
	if latest != "" {
		blocks = append(blocks, slackSection(fmt.Sprintf("*Latest*\n%s", latest)))
	}
	blocks = append(blocks, contextBlock(taskName))

	text := buildSessionSummaryFallback(title, summary, latest, taskName)
	return SlackMessage{Text: text, Blocks: blocks}
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

func slackSection(text string) *slack.SectionBlock {
	return slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
		nil, nil,
	)
}

func compactSlackText(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s, false
	}
	return strings.TrimSpace(string([]rune(s)[:limit-3])) + "...", true
}

func buildStableSummaryFallback(title, stableSummary, status, pr, errText, taskName string) string {
	parts := []string{title}
	if stableSummary != "" {
		parts = append(parts, stableSummary)
	}
	if status != "" {
		parts = append(parts, status)
	}
	if pr != "" {
		parts = append(parts, "PR: "+pr)
	}
	if errText != "" {
		parts = append(parts, "Error: "+errText)
	}
	parts = append(parts, fmt.Sprintf("(Task: %s)", taskName))
	return truncateFallbackText(strings.Join(parts, "\n"))
}

func buildSessionSummaryFallback(title, summary, latest, taskName string) string {
	parts := []string{title}
	if summary != "" {
		parts = append(parts, "Summary\n"+summary)
	}
	if latest != "" {
		parts = append(parts, "Latest\n"+latest)
	}
	parts = append(parts, fmt.Sprintf("(Task: %s)", taskName))
	return truncateFallbackText(strings.Join(parts, "\n\n"))
}

// continuationContextBlock returns a context block indicating a multi-part
// message with the current part number and total.
func continuationContextBlock(taskName string, part, total int) *slack.ContextBlock {
	return slack.NewContextBlock("",
		slack.NewTextBlockObject(slack.MarkdownType,
			fmt.Sprintf("Task: `%s` · Part %d/%d", taskName, part, total), false, false),
	)
}
