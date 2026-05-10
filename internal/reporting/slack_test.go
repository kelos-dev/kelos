package reporting

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

// firstMsg is a helper that returns the first (and usually only) message
// from FormatSlackTransitionMessage, failing the test if the slice is empty.
func firstMsg(t *testing.T, msgs []SlackMessage) SlackMessage {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("FormatSlackTransitionMessage returned 0 messages")
	}
	return msgs[0]
}

func TestFormatSlackTransitionMessages(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		got := firstMsg(t, FormatSlackTransitionMessage("accepted", "spawner-1234567890.123456", "", nil))
		if got.Text != "Working on your request... (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // section + context
		assertSectionText(t, got.Blocks[0], ":hourglass_flowing_sand: *Working on your request...*")
		assertContextContains(t, got.Blocks[1], "spawner-1234567890.123456")
	})

	t.Run("succeeded with PR", func(t *testing.T) {
		results := map[string]string{"pr": "https://github.com/org/repo/pull/42"}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		if got.Text != "PR: https://github.com/org/repo/pull/42 (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // PR + context
		assertSectionContains(t, got.Blocks[0], "https://github.com/org/repo/pull/42")
	})

	t.Run("succeeded without results", func(t *testing.T) {
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", nil))
		if got.Text != "Done! (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 1) // context only
	})

	t.Run("succeeded with empty results", func(t *testing.T) {
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", map[string]string{}))
		if got.Text != "Done! (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 1) // context only
	})

	t.Run("succeeded ignores status message", func(t *testing.T) {
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "Task completed successfully", nil))
		if got.Text != "Done! (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 1) // context only, no error block
	})

	t.Run("succeeded with response", func(t *testing.T) {
		results := map[string]string{"response": b64("I need your GitHub username to proceed.")}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		if got.Text != "I need your GitHub username to proceed. (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // response + context
		assertSectionContains(t, got.Blocks[0], "I need your GitHub username to proceed.")
	})

	t.Run("succeeded with response and PR", func(t *testing.T) {
		results := map[string]string{
			"response": b64("Added CODEOWNERS entry."),
			"pr":       "https://github.com/org/repo/pull/42",
		}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		if got.Text != "Added CODEOWNERS entry.\nPR: https://github.com/org/repo/pull/42 (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 3) // response + PR + context
		assertSectionContains(t, got.Blocks[0], "Added CODEOWNERS entry.")
		assertSectionContains(t, got.Blocks[1], "https://github.com/org/repo/pull/42")
	})

	t.Run("succeeded with multiline response", func(t *testing.T) {
		results := map[string]string{"response": b64("Line one.\nLine two.\nLine three.")}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		if got.Text != "Line one.\nLine two.\nLine three. (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // response + context
	})

	t.Run("succeeded with non-base64 response fallback", func(t *testing.T) {
		results := map[string]string{"response": "plain text response"}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		if got.Text != "plain text response (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // response + context
	})

	t.Run("failed with message", func(t *testing.T) {
		got := firstMsg(t, FormatSlackTransitionMessage("failed", "spawner-1234567890.123456", "pod OOMKilled", nil))
		if got.Text != "Error: pod OOMKilled (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 3) // header + error + context
		assertSectionContains(t, got.Blocks[1], "pod OOMKilled")
	})

	t.Run("failed without message", func(t *testing.T) {
		got := firstMsg(t, FormatSlackTransitionMessage("failed", "spawner-1234567890.123456", "", nil))
		if got.Text != "Failed. (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // header + context
	})

	t.Run("failed with response", func(t *testing.T) {
		results := map[string]string{"response": b64("Could not find the file.")}
		got := firstMsg(t, FormatSlackTransitionMessage("failed", "spawner-1234567890.123456", "Task failed", results))
		if got.Text != "Could not find the file.\nError: Task failed (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 4) // header + response + error + context
		assertSectionContains(t, got.Blocks[1], "Could not find the file.")
		assertSectionContains(t, got.Blocks[2], "Task failed")
	})

	t.Run("succeeded with table response", func(t *testing.T) {
		resp := "| Name | Age |\n| --- | --- |\n| Alice | 30 |"
		results := map[string]string{"response": b64(resp)}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		// table + context
		assertBlockCount(t, got.Blocks, 2)
		if _, ok := got.Blocks[0].(*slack.TableBlock); !ok {
			t.Errorf("block 0: expected *TableBlock, got %T", got.Blocks[0])
		}
	})

	t.Run("succeeded with list response", func(t *testing.T) {
		resp := "- item one\n- item two\n- item three"
		results := map[string]string{"response": b64(resp)}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		// rich_text list + context
		assertBlockCount(t, got.Blocks, 2)
		if _, ok := got.Blocks[0].(*slack.RichTextBlock); !ok {
			t.Errorf("block 0: expected *RichTextBlock, got %T", got.Blocks[0])
		}
	})

	t.Run("succeeded with header response", func(t *testing.T) {
		resp := "# Summary\nEverything looks good."
		results := map[string]string{"response": b64(resp)}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		// HeaderBlock + SectionBlock + context
		assertBlockCount(t, got.Blocks, 3)
		if hdr, ok := got.Blocks[0].(*slack.HeaderBlock); !ok {
			t.Errorf("block 0: expected *HeaderBlock, got %T", got.Blocks[0])
		} else if hdr.Text.Text != "Summary" {
			t.Errorf("header text = %q, want %q", hdr.Text.Text, "Summary")
		}
		assertSectionContains(t, got.Blocks[1], "Everything looks good.")
	})

	t.Run("succeeded with mixed rich content", func(t *testing.T) {
		resp := "## Report\nResults below:\n\n| Col | Val |\n| --- | --- |\n| a | 1 |\n\n- note 1\n- note 2"
		results := map[string]string{"response": b64(resp)}
		got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
		// header + section + table + list + context
		assertBlockCount(t, got.Blocks, 5)
		if _, ok := got.Blocks[0].(*slack.HeaderBlock); !ok {
			t.Errorf("block 0: expected *HeaderBlock, got %T", got.Blocks[0])
		}
		if _, ok := got.Blocks[1].(*slack.SectionBlock); !ok {
			t.Errorf("block 1: expected *SectionBlock, got %T", got.Blocks[1])
		}
		if _, ok := got.Blocks[2].(*slack.TableBlock); !ok {
			t.Errorf("block 2: expected *TableBlock, got %T", got.Blocks[2])
		}
		if _, ok := got.Blocks[3].(*slack.RichTextBlock); !ok {
			t.Errorf("block 3: expected *RichTextBlock, got %T", got.Blocks[3])
		}
	})
}

func TestFormatSlackTransitionMessage_SplitsLongResponse(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("### Header\nSome content here.")
	}
	results := map[string]string{"response": b64(sb.String())}

	msgs := FormatSlackTransitionMessage("succeeded", "test-task", "", results)

	if len(msgs) < 2 {
		t.Fatalf("expected multiple messages for long response, got %d", len(msgs))
	}

	// Every message must be within the block limit.
	for i, msg := range msgs {
		if len(msg.Blocks) > SlackBlockLimit {
			t.Errorf("message %d has %d blocks, must be <= %d", i, len(msg.Blocks), SlackBlockLimit)
		}
	}

	// Last block of the last message must be the context block (task name).
	lastMsg := msgs[len(msgs)-1]
	last := lastMsg.Blocks[len(lastMsg.Blocks)-1]
	if _, ok := last.(*slack.ContextBlock); !ok {
		t.Errorf("last block of last message should be ContextBlock, got %T", last)
	}

	// First message should have a continuation context with part info.
	firstMsg := msgs[0]
	firstLast := firstMsg.Blocks[len(firstMsg.Blocks)-1]
	if ctx, ok := firstLast.(*slack.ContextBlock); ok {
		found := false
		for _, elem := range ctx.ContextElements.Elements {
			if txt, ok := elem.(*slack.TextBlockObject); ok {
				if strings.Contains(txt.Text, "Part 1/") {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected first message to have a Part 1/N continuation indicator")
		}
	} else {
		t.Errorf("first message last block: expected *ContextBlock, got %T", firstLast)
	}

	// Continuation messages should have part indicators in their fallback text.
	for i := 1; i < len(msgs); i++ {
		if !strings.Contains(msgs[i].Text, "continued") {
			t.Errorf("message %d fallback text should contain 'continued', got %q", i, msgs[i].Text)
		}
	}
}

func TestFormatSlackTransitionMessage_SplitsWithPRLink(t *testing.T) {
	// Verify that the PR link ends up in the last message.
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("### Header\nSome content here.")
	}
	results := map[string]string{
		"response": b64(sb.String()),
		"pr":       "https://github.com/org/repo/pull/99",
	}

	msgs := FormatSlackTransitionMessage("succeeded", "test-task", "", results)

	if len(msgs) < 2 {
		t.Fatalf("expected multiple messages, got %d", len(msgs))
	}

	// PR link should be in the last message.
	lastMsg := msgs[len(msgs)-1]
	foundPR := false
	for _, b := range lastMsg.Blocks {
		if sec, ok := b.(*slack.SectionBlock); ok && sec.Text != nil {
			if strings.Contains(sec.Text.Text, "Pull Request") {
				foundPR = true
				break
			}
		}
	}
	if !foundPR {
		t.Error("expected PR link in last message")
	}
}

func TestFormatSlackTransitionMessage_MiddleChunkBecomesLast(t *testing.T) {
	// Regression: when the response block count lands in
	// (SlackBlockLimit-lastReserve, SlackBlockLimit-1] for a middle chunk,
	// the middle chunk consumed all remaining blocks and trailing blocks
	// were appended on top, exceeding 50. Verify that every message stays
	// within the limit for pathological response sizes with 3 trailing
	// blocks (failed + PR link + context).
	//
	// Use headers to produce exactly 1 block per line.
	for _, numHeaders := range []int{95, 96, 97, 144, 145, 146, 193, 194, 195} {
		var sb strings.Builder
		for i := 0; i < numHeaders; i++ {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("# Header\n")
		}
		results := map[string]string{
			"response": b64(sb.String()),
			"pr":       "https://github.com/org/repo/pull/1",
		}

		msgs := FormatSlackTransitionMessage("failed", "test-task", "something broke", results)

		if len(msgs) < 2 {
			t.Errorf("numHeaders=%d: expected multiple messages, got %d", numHeaders, len(msgs))
			continue
		}

		for i, msg := range msgs {
			if len(msg.Blocks) > SlackBlockLimit {
				t.Errorf("numHeaders=%d message %d has %d blocks, must be <= %d",
					numHeaders, i, len(msg.Blocks), SlackBlockLimit)
			}
		}

		// The last message must contain the trailing blocks.
		lastMsg := msgs[len(msgs)-1]
		foundPR := false
		foundError := false
		for _, b := range lastMsg.Blocks {
			if sec, ok := b.(*slack.SectionBlock); ok && sec.Text != nil {
				if strings.Contains(sec.Text.Text, "Pull Request") {
					foundPR = true
				}
				if strings.Contains(sec.Text.Text, "Error") {
					foundError = true
				}
			}
		}
		if !foundPR {
			t.Errorf("numHeaders=%d: expected PR link in last message", numHeaders)
		}
		if !foundError {
			t.Errorf("numHeaders=%d: expected error in last message", numHeaders)
		}
	}
}

func TestSplitBlocks_MiddleChunkDoesNotExceedLimit(t *testing.T) {
	// Direct test of splitBlocks to ensure that appending lastReserve
	// trailing blocks to the last chunk never exceeds SlackBlockLimit.
	dummyBlock := slack.NewDividerBlock()

	for _, tc := range []struct {
		name        string
		total       int
		firstCap    int
		lastReserve int
	}{
		{"95 blocks reserve 3", 95, 46, 3},
		{"96 blocks reserve 3", 96, 46, 3},
		{"97 blocks reserve 3", 97, 46, 3},
		{"144 blocks reserve 3", 144, 46, 3},
		{"48 blocks reserve 2", 48, 47, 2},
		{"49 blocks reserve 2", 49, 47, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := make([]slack.Block, tc.total)
			for i := range blocks {
				blocks[i] = dummyBlock
			}

			chunks := splitBlocks(blocks, tc.firstCap, tc.lastReserve)

			totalEmitted := 0
			for i, chunk := range chunks {
				totalEmitted += len(chunk)
				if i == len(chunks)-1 {
					// Simulate appending trailing blocks.
					if len(chunk)+tc.lastReserve > SlackBlockLimit {
						t.Errorf("last chunk size %d + lastReserve %d = %d > %d",
							len(chunk), tc.lastReserve, len(chunk)+tc.lastReserve, SlackBlockLimit)
					}
				} else {
					// Middle chunks get 1 continuation context block.
					if len(chunk)+1 > SlackBlockLimit {
						t.Errorf("middle chunk %d size %d + 1 = %d > %d",
							i, len(chunk), len(chunk)+1, SlackBlockLimit)
					}
				}
			}
			if totalEmitted != tc.total {
				t.Errorf("emitted %d blocks, want %d", totalEmitted, tc.total)
			}
		})
	}
}

func TestFormatSlackTransitionMessage_SingleChunkWithTrailing(t *testing.T) {
	// When response blocks barely exceed the limit (e.g. 49 response blocks
	// + 2 trailing = 51), splitBlocks may return a single chunk. The trailing
	// blocks (PR link, context) must still be included.
	var sb strings.Builder
	for i := 0; i < 49; i++ {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Short paragraph.")
	}
	results := map[string]string{
		"response": b64(sb.String()),
		"pr":       "https://github.com/org/repo/pull/77",
	}

	msgs := FormatSlackTransitionMessage("succeeded", "test-task", "", results)

	// All blocks should fit in a single message after splitting.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	msg := msgs[0]
	if len(msg.Blocks) > SlackBlockLimit {
		t.Errorf("message has %d blocks, must be <= %d", len(msg.Blocks), SlackBlockLimit)
	}

	// PR link must be present.
	foundPR := false
	for _, b := range msg.Blocks {
		if sec, ok := b.(*slack.SectionBlock); ok && sec.Text != nil {
			if strings.Contains(sec.Text.Text, "Pull Request") {
				foundPR = true
				break
			}
		}
	}
	if !foundPR {
		t.Error("expected PR link in message")
	}

	// Context block must be present as the last block.
	last := msg.Blocks[len(msg.Blocks)-1]
	if _, ok := last.(*slack.ContextBlock); !ok {
		t.Errorf("last block should be ContextBlock, got %T", last)
	}
}

func TestFormatSlackTransitionMessage_UnicodeBulletResponse(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# Weekly Update\n\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("• *Status:* On track — lots of progress this week with many issues completed across the board.\n")
	}
	results := map[string]string{"response": b64(sb.String())}
	msgs := FormatSlackTransitionMessage("succeeded", "test-task", "", results)

	for i, msg := range msgs {
		if len(msg.Blocks) > SlackBlockLimit {
			t.Errorf("message %d has %d blocks, must be <= %d", i, len(msg.Blocks), SlackBlockLimit)
		}
		for j, b := range msg.Blocks {
			if sec, ok := b.(*slack.SectionBlock); ok && sec.Text != nil {
				if len([]rune(sec.Text.Text)) > slackSectionTextLimit {
					t.Errorf("message %d block %d section text length %d exceeds %d", i, j, len([]rune(sec.Text.Text)), slackSectionTextLimit)
				}
			}
		}
		if len([]rune(msg.Text)) > slackFallbackTextLimit {
			t.Errorf("message %d fallback text length %d exceeds %d", i, len([]rune(msg.Text)), slackFallbackTextLimit)
		}
	}
}

func TestFormatSlackTransitionMessage_FallbackTextTruncated(t *testing.T) {
	long := strings.Repeat("A", 50000)
	results := map[string]string{"response": b64(long)}
	msgs := FormatSlackTransitionMessage("succeeded", "test-task", "", results)
	for i, msg := range msgs {
		if len([]rune(msg.Text)) > slackFallbackTextLimit {
			t.Errorf("message %d fallback text length %d exceeds limit %d", i, len([]rune(msg.Text)), slackFallbackTextLimit)
		}
	}
}

func TestFormatProgressMessage_FallbackTextTruncated(t *testing.T) {
	long := strings.Repeat("x", 50000)
	got := FormatProgressMessage(long, "test-task")
	if len([]rune(got.Text)) > slackFallbackTextLimit {
		t.Errorf("fallback text length %d exceeds limit %d", len([]rune(got.Text)), slackFallbackTextLimit)
	}
}

func TestFormatProgressMessage(t *testing.T) {
	t.Run("includes blocks and context", func(t *testing.T) {
		got := FormatProgressMessage("Looking at the config files...", "test-task")
		if got.Text != "Looking at the config files..." {
			t.Errorf("text = %q, want progress text", got.Text)
		}
		if len(got.Blocks) == 0 {
			t.Fatal("expected blocks to be present")
		}
		// Last block should be a context block with the task name.
		lastBlock := got.Blocks[len(got.Blocks)-1]
		assertContextContains(t, lastBlock, "test-task")
	})

	t.Run("appendActivityContext works", func(t *testing.T) {
		base := FormatProgressMessage("Investigating the issue...", "test-task")
		result := appendActivityContext(base, "Reading `main.go`...")
		// Should have blocks (not skipped like text-only messages).
		if len(result.Blocks) == 0 {
			t.Fatal("expected blocks after appending activity context")
		}
		// The activity text should be in the last context block.
		lastBlock := result.Blocks[len(result.Blocks)-1]
		ctx, ok := lastBlock.(*slack.ContextBlock)
		if !ok {
			t.Fatalf("last block: expected *ContextBlock, got %T", lastBlock)
		}
		// Should have 2 elements: task name + activity.
		if len(ctx.ContextElements.Elements) != 2 {
			t.Errorf("context elements = %d, want 2 (task name + activity)", len(ctx.ContextElements.Elements))
		}
	})
}

func TestConvertInlineMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"bold", "this is **important**", "this is *important*"},
		{"link", "see [docs](https://example.com)", "see <https://example.com|docs>"},
		{"link with parens", "see [Go](https://en.wikipedia.org/wiki/Go_(language))", "see <https://en.wikipedia.org/wiki/Go_(language)|Go>"},
		{"link with rfc parens", "[RFC](https://tools.ietf.org/html/rfc3986_(URI))", "<https://tools.ietf.org/html/rfc3986_(URI)|RFC>"},
		{"strikethrough", "~~removed~~", "~removed~"},
		{"mixed", "**Bold** and [link](https://x.com) ~~old~~", "*Bold* and <https://x.com|link> ~old~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertInlineMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("convertInlineMarkdown(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatSlackSucceeded_MarkdownConversion(t *testing.T) {
	results := map[string]string{"response": b64("## Summary\nI updated the **CODEOWNERS** file.")}
	got := firstMsg(t, FormatSlackTransitionMessage("succeeded", "spawner-1234567890.123456", "", results))
	// ## Summary becomes a HeaderBlock, text becomes a SectionBlock with inline markdown converted
	if hdr, ok := got.Blocks[0].(*slack.HeaderBlock); !ok {
		t.Errorf("block 0: expected *HeaderBlock, got %T", got.Blocks[0])
	} else if hdr.Text.Text != "Summary" {
		t.Errorf("header text = %q, want %q", hdr.Text.Text, "Summary")
	}
	assertSectionContains(t, got.Blocks[1], "*CODEOWNERS*")
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestSlackReporterConstruction(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-test-token"}
	if reporter.BotToken != "xoxb-test-token" {
		t.Errorf("BotToken = %q, want %q", reporter.BotToken, "xoxb-test-token")
	}
}

func TestSlackReporter_PostThreadReplyError(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-invalid"}
	_, err := reporter.PostThreadReply(context.Background(), "C123", "1234.5678", SlackMessage{Text: "test"})
	if err == nil {
		t.Error("expected error with invalid token, got nil")
	}
}

// assertBlockCount checks that the blocks slice has the expected length.
func assertBlockCount(t *testing.T, blocks []slack.Block, want int) {
	t.Helper()
	if len(blocks) != want {
		t.Errorf("block count = %d, want %d", len(blocks), want)
	}
}

// assertSectionText checks that a block is a SectionBlock with the exact text.
func assertSectionText(t *testing.T, block slack.Block, want string) {
	t.Helper()
	section, ok := block.(*slack.SectionBlock)
	if !ok {
		t.Errorf("expected SectionBlock, got %T", block)
		return
	}
	if section.Text == nil || section.Text.Text != want {
		got := ""
		if section.Text != nil {
			got = section.Text.Text
		}
		t.Errorf("section text = %q, want %q", got, want)
	}
}

// assertSectionContains checks that a block is a SectionBlock containing the substring.
func assertSectionContains(t *testing.T, block slack.Block, substr string) {
	t.Helper()
	section, ok := block.(*slack.SectionBlock)
	if !ok {
		t.Errorf("expected SectionBlock, got %T", block)
		return
	}
	if section.Text == nil {
		t.Errorf("section text is nil, want to contain %q", substr)
		return
	}
	if !strings.Contains(section.Text.Text, substr) {
		t.Errorf("section text %q does not contain %q", section.Text.Text, substr)
	}
}

// assertContextContains checks that a block is a ContextBlock containing the substring.
func assertContextContains(t *testing.T, block slack.Block, substr string) {
	t.Helper()
	ctx, ok := block.(*slack.ContextBlock)
	if !ok {
		t.Errorf("expected ContextBlock, got %T", block)
		return
	}
	for _, elem := range ctx.ContextElements.Elements {
		if txt, ok := elem.(*slack.TextBlockObject); ok {
			if strings.Contains(txt.Text, substr) {
				return
			}
		}
	}
	t.Errorf("context block does not contain %q", substr)
}
