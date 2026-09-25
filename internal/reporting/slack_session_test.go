package reporting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestSessionWorkingMessage(t *testing.T) {
	got := SessionWorkingMessage("gravity-slack-abc123def456")
	if got.Text != "Working on your request... (Session: gravity-slack-abc123def456)" {
		t.Errorf("fallback text = %q", got.Text)
	}
	assertBlockCount(t, got.Blocks, 2) // section + context
	assertSectionText(t, got.Blocks[0], ":hourglass_flowing_sand: *Working on your request...*")
	assertContextContains(t, got.Blocks[1], "Session: `gravity-slack-abc123def456`")
}

func TestFormatSessionReplySingleMessage(t *testing.T) {
	msgs := FormatSessionReply("The build is green.", "", "", "gravity-slack-abc123def456")
	if len(msgs) != 1 {
		t.Fatalf("returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Text != "The build is green. (Session: gravity-slack-abc123def456)" {
		t.Errorf("fallback text = %q", msgs[0].Text)
	}
	// One rich text block for the paragraph, plus the trailing context block.
	assertBlockCount(t, msgs[0].Blocks, 2)
	assertContextContains(t, msgs[0].Blocks[1], "Session: `gravity-slack-abc123def456`")
}

func TestFormatSessionReplyIncludesErrorBlock(t *testing.T) {
	msgs := FormatSessionReply("I got partway.", "the turn ended as interrupted", "", "gravity-slack-abc123def456")
	if len(msgs) != 1 {
		t.Fatalf("returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Text != "I got partway.\nError: the turn ended as interrupted (Session: gravity-slack-abc123def456)" {
		t.Errorf("fallback text = %q", msgs[0].Text)
	}
	assertBlockCount(t, msgs[0].Blocks, 3) // response + error + context
	assertSectionText(t, msgs[0].Blocks[1], ":warning: *Error:* the turn ended as interrupted")
}

func TestFormatSessionReplyWithNoText(t *testing.T) {
	msgs := FormatSessionReply("", "", "", "gravity-slack-abc123def456")
	if len(msgs) != 1 {
		t.Fatalf("returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Text != "The agent returned no reply. (Session: gravity-slack-abc123def456)" {
		t.Errorf("fallback text = %q", msgs[0].Text)
	}
	// An empty reply still renders a body so the thread is not left blank.
	assertBlockCount(t, msgs[0].Blocks, 2)
}

func TestFormatSessionReplyErrorOnlyStillReportsTheError(t *testing.T) {
	msgs := FormatSessionReply("", "exec stream refused", "", "gravity-slack-abc123def456")
	if len(msgs) != 1 {
		t.Fatalf("returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Text != "Error: exec stream refused (Session: gravity-slack-abc123def456)" {
		t.Errorf("fallback text = %q", msgs[0].Text)
	}
	assertSectionText(t, msgs[0].Blocks[len(msgs[0].Blocks)-2], ":warning: *Error:* exec stream refused")
}

func TestFormatSessionReplySplitsLongReply(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "### Section %d\nSome content here.", i)
	}

	msgs := FormatSessionReply(sb.String(), "", "", "gravity-slack-abc123def456")
	if len(msgs) < 2 {
		t.Fatalf("returned %d messages, want the reply split across several", len(msgs))
	}
	for i, msg := range msgs {
		if len(msg.Blocks) > SlackBlockLimit {
			t.Errorf("message %d has %d blocks, want at most %d", i+1, len(msg.Blocks), SlackBlockLimit)
		}
		last, ok := msg.Blocks[len(msg.Blocks)-1].(*slack.ContextBlock)
		if !ok {
			t.Fatalf("message %d does not end with a context block", i+1)
		}
		assertContextContains(t, last, fmt.Sprintf("Session: `gravity-slack-abc123def456` · Part %d/%d", i+1, len(msgs)))
	}
	want := fmt.Sprintf("(continued, part 2/%d) (Session: gravity-slack-abc123def456)", len(msgs))
	if msgs[1].Text != want {
		t.Errorf("continuation text = %q, want %q", msgs[1].Text, want)
	}
}

func TestFormatSessionReplyTruncatesALongError(t *testing.T) {
	longError := strings.Repeat("stream refused. ", 500) // 8000 characters
	msgs := FormatSessionReply("", longError, "", "gravity-slack-abc123def456")
	if len(msgs) != 1 {
		t.Fatalf("returned %d messages, want 1", len(msgs))
	}
	errorBlock, ok := msgs[0].Blocks[len(msgs[0].Blocks)-2].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("block before the context block is %T, want a SectionBlock", msgs[0].Blocks[len(msgs[0].Blocks)-2])
	}
	if got := len([]rune(errorBlock.Text.Text)); got > slackSectionTextLimit {
		t.Errorf("error section is %d characters, want at most %d", got, slackSectionTextLimit)
	}
	if !strings.HasPrefix(errorBlock.Text.Text, ":warning: *Error:* stream refused.") {
		t.Errorf("error section = %q, want it to start with the error", errorBlock.Text.Text[:60])
	}
	if !strings.HasSuffix(errorBlock.Text.Text, "…") {
		t.Error("error section was not marked as truncated")
	}
}

func TestFormatSessionReplyIncludesADeclinedQuestionNote(t *testing.T) {
	msgs := FormatSessionReply("I assumed main.", "", "The agent asked a question. A thread cannot answer one mid-turn, so it was declined and the agent continued.", "gravity-slack-abc123def456")
	if len(msgs) != 1 {
		t.Fatalf("returned %d messages, want 1", len(msgs))
	}
	assertBlockCount(t, msgs[0].Blocks, 3) // response + note + context
	assertSectionContains(t, msgs[0].Blocks[1], ":grey_question: The agent asked a question.")
	if !strings.Contains(msgs[0].Text, "declined and the agent continued") {
		t.Errorf("fallback text = %q, want the note included", msgs[0].Text)
	}
}
