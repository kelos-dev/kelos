package slack

import (
	"strings"
	"testing"

	goslack "github.com/slack-go/slack"
)

func TestFormatThreadContext(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{User: "U1", Text: "fix the bug please"}},
		{Msg: goslack.Msg{User: "UBOT", Text: "Working on it"}},
		{Msg: goslack.Msg{User: "U1", Text: "any updates?"}},
		{Msg: goslack.Msg{Text: ""}}, // empty text, should be skipped
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.HasPrefix(result, "Slack thread conversation:\n") {
		t.Error("Expected thread context to start with header")
	}
	if !strings.Contains(result, "User: fix the bug please") {
		t.Error("Expected user message to be labeled as User")
	}
	if !strings.Contains(result, "Agent: Working on it") {
		t.Error("Expected bot message to be labeled as Agent")
	}
	if !strings.Contains(result, "User: any updates?") {
		t.Error("Expected follow-up user message")
	}
	// Empty text message should be skipped
	if strings.Count(result, "User:") != 2 || strings.Count(result, "Agent:") != 1 {
		t.Errorf("Unexpected message count in output:\n%s", result)
	}
}

func TestFormatThreadContext_BotIDMessage(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{User: "U1", Text: "hello"}},
		{Msg: goslack.Msg{BotID: "B123", Text: "Bot response"}},
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.Contains(result, "Agent: Bot response") {
		t.Error("Expected message with BotID to be labeled as Agent")
	}
}

func TestFormatThreadContext_WithAttachments(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{
			User: "U1",
			Text: "This looks new in 379.0",
			Attachments: []goslack.Attachment{
				{Text: "Original forwarded message content"},
			},
		}},
		{Msg: goslack.Msg{User: "U1", Text: "<@UBOT> investigate this"}},
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.Contains(result, "User: This looks new in 379.0") {
		t.Error("Expected user text to appear")
	}
	if !strings.Contains(result, "> Original forwarded message content") {
		t.Errorf("Expected attachment text to appear blockquoted, got:\n%s", result)
	}
}

func TestFormatThreadContext_AttachmentOnly(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{
			User: "U1",
			Attachments: []goslack.Attachment{
				{Text: "Forwarded without commentary"},
			},
		}},
		{Msg: goslack.Msg{User: "U1", Text: "<@UBOT> look at this"}},
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.Contains(result, "User: [attachment]") {
		t.Errorf("Expected attachment-only message to have [attachment] label, got:\n%s", result)
	}
	if !strings.Contains(result, "> Forwarded without commentary") {
		t.Errorf("Expected attachment text to appear, got:\n%s", result)
	}
}

func TestFormatThreadContext_AttachmentWithPretext(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{
			User: "U1",
			Text: "Check this out",
			Attachments: []goslack.Attachment{
				{Pretext: "Message from #general", Text: "The actual content"},
			},
		}},
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.Contains(result, "User: Check this out") {
		t.Error("Expected user text")
	}
	if !strings.Contains(result, "Message from #general") {
		t.Errorf("Expected pretext to appear, got:\n%s", result)
	}
	if !strings.Contains(result, "> The actual content") {
		t.Errorf("Expected attachment text blockquoted, got:\n%s", result)
	}
}

func TestFormatThreadContext_FallbackOnly(t *testing.T) {
	msgs := []goslack.Message{
		{Msg: goslack.Msg{
			User: "U1",
			Attachments: []goslack.Attachment{
				{Fallback: "Fallback text for unsupported attachment"},
			},
		}},
	}

	result := FormatThreadContext(msgs, "UBOT")

	if !strings.Contains(result, "> Fallback text for unsupported attachment") {
		t.Errorf("Expected fallback text when no primary text, got:\n%s", result)
	}
}

func TestFormatAttachments(t *testing.T) {
	t.Run("empty attachments", func(t *testing.T) {
		result := formatAttachments(nil)
		if result != "" {
			t.Errorf("Expected empty string for nil attachments, got: %q", result)
		}
	})

	t.Run("attachment with text only", func(t *testing.T) {
		result := formatAttachments([]goslack.Attachment{
			{Text: "hello world"},
		})
		if result != "> hello world" {
			t.Errorf("Expected blockquoted text, got: %q", result)
		}
	})

	t.Run("attachment with multiline text", func(t *testing.T) {
		result := formatAttachments([]goslack.Attachment{
			{Text: "line one\nline two"},
		})
		if result != "> line one\n> line two" {
			t.Errorf("Expected each line blockquoted, got: %q", result)
		}
	})

	t.Run("fallback ignored when text present", func(t *testing.T) {
		result := formatAttachments([]goslack.Attachment{
			{Text: "primary", Fallback: "fallback"},
		})
		if strings.Contains(result, "fallback") {
			t.Errorf("Fallback should not appear when text is present, got: %q", result)
		}
	})

	t.Run("fallback used when no text", func(t *testing.T) {
		result := formatAttachments([]goslack.Attachment{
			{Fallback: "fallback only"},
		})
		if result != "> fallback only" {
			t.Errorf("Expected fallback to be used, got: %q", result)
		}
	})

	t.Run("multiline fallback blockquoted", func(t *testing.T) {
		result := formatAttachments([]goslack.Attachment{
			{Fallback: "line one\nline two"},
		})
		if result != "> line one\n> line two" {
			t.Errorf("Expected each fallback line blockquoted, got: %q", result)
		}
	})

	t.Run("attachment with no text fields", func(t *testing.T) {
		result := formatAttachments([]goslack.Attachment{
			{Color: "danger"},
		})
		if result != "" {
			t.Errorf("Expected empty string for non-text attachment, got: %q", result)
		}
	})
}
