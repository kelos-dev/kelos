package reporting

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestFormatSlackMessages(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		got := FormatSlackAccepted("spawner-1234567890.123456")
		if got.Text != "Working on your request... (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // section + context
		assertSectionText(t, got.Blocks[0], ":hourglass_flowing_sand: *Working on your request...*")
		assertContextContains(t, got.Blocks[1], "spawner-1234567890.123456")
	})

	t.Run("succeeded with PR", func(t *testing.T) {
		results := map[string]string{"pr": "https://github.com/org/repo/pull/42"}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		if got.Text != "PR: https://github.com/org/repo/pull/42 (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // PR + context
		assertSectionContains(t, got.Blocks[0], "https://github.com/org/repo/pull/42")
	})

	t.Run("succeeded without results", func(t *testing.T) {
		got := FormatSlackSucceeded("spawner-1234567890.123456", nil)
		if got.Text != "Done! (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 1) // context only
	})

	t.Run("succeeded with empty results", func(t *testing.T) {
		got := FormatSlackSucceeded("spawner-1234567890.123456", map[string]string{})
		if got.Text != "Done! (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 1) // context only
	})

	t.Run("succeeded with response", func(t *testing.T) {
		results := map[string]string{"response": b64("I need your GitHub username to proceed.")}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
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
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		if got.Text != "Added CODEOWNERS entry.\nPR: https://github.com/org/repo/pull/42 (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 3) // response + PR + context
		assertSectionContains(t, got.Blocks[0], "Added CODEOWNERS entry.")
		assertSectionContains(t, got.Blocks[1], "https://github.com/org/repo/pull/42")
	})

	t.Run("succeeded with multiline response", func(t *testing.T) {
		results := map[string]string{"response": b64("Line one.\nLine two.\nLine three.")}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		if got.Text != "Line one.\nLine two.\nLine three. (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // response + context
	})

	t.Run("succeeded with non-base64 response fallback", func(t *testing.T) {
		results := map[string]string{"response": "plain text response"}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		if got.Text != "plain text response (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // response + context
	})

	t.Run("failed with message", func(t *testing.T) {
		got := FormatSlackFailed("spawner-1234567890.123456", "pod OOMKilled", nil)
		if got.Text != "Error: pod OOMKilled (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 3) // header + error + context
		assertSectionContains(t, got.Blocks[1], "pod OOMKilled")
	})

	t.Run("failed without message", func(t *testing.T) {
		got := FormatSlackFailed("spawner-1234567890.123456", "", nil)
		if got.Text != "Failed. (Task: spawner-1234567890.123456)" {
			t.Errorf("fallback text = %q", got.Text)
		}
		assertBlockCount(t, got.Blocks, 2) // header + context
	})

	t.Run("failed with response", func(t *testing.T) {
		results := map[string]string{"response": b64("Could not find the file.")}
		got := FormatSlackFailed("spawner-1234567890.123456", "Task failed", results)
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
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		// table + context
		assertBlockCount(t, got.Blocks, 2)
		if _, ok := got.Blocks[0].(*slack.TableBlock); !ok {
			t.Errorf("block 0: expected *TableBlock, got %T", got.Blocks[0])
		}
	})

	t.Run("succeeded with list response", func(t *testing.T) {
		resp := "- item one\n- item two\n- item three"
		results := map[string]string{"response": b64(resp)}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		// rich_text list + context
		assertBlockCount(t, got.Blocks, 2)
		if _, ok := got.Blocks[0].(*slack.RichTextBlock); !ok {
			t.Errorf("block 0: expected *RichTextBlock, got %T", got.Blocks[0])
		}
	})

	t.Run("succeeded with header response", func(t *testing.T) {
		resp := "# Summary\nEverything looks good."
		results := map[string]string{"response": b64(resp)}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
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
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
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
	got := FormatSlackSucceeded("spawner-1234567890.123456", results)
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

func TestSlackReporter_UpdateMessageError(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-invalid"}
	err := reporter.UpdateMessage(context.Background(), "C123", "1234.5678", SlackMessage{Text: "test"})
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
