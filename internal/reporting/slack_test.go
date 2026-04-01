package reporting

import (
	"context"
	"encoding/base64"
	"testing"
)

func TestFormatSlackMessages(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		got := FormatSlackAccepted("spawner-1234567890.123456")
		want := "Working on your request... (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded with PR", func(t *testing.T) {
		results := map[string]string{"pr": "https://github.com/org/repo/pull/42"}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		want := "PR: https://github.com/org/repo/pull/42 (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded without results", func(t *testing.T) {
		got := FormatSlackSucceeded("spawner-1234567890.123456", nil)
		want := "Done! (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded with empty results", func(t *testing.T) {
		got := FormatSlackSucceeded("spawner-1234567890.123456", map[string]string{})
		want := "Done! (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded with response", func(t *testing.T) {
		results := map[string]string{"response": b64("I need your GitHub username to proceed.")}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		want := "I need your GitHub username to proceed. (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded with response and PR", func(t *testing.T) {
		results := map[string]string{
			"response": b64("Added CODEOWNERS entry."),
			"pr":       "https://github.com/org/repo/pull/42",
		}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		want := "Added CODEOWNERS entry.\nPR: https://github.com/org/repo/pull/42 (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded with multiline response", func(t *testing.T) {
		results := map[string]string{"response": b64("Line one.\nLine two.\nLine three.")}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		want := "Line one.\nLine two.\nLine three. (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("succeeded with non-base64 response fallback", func(t *testing.T) {
		results := map[string]string{"response": "plain text response"}
		got := FormatSlackSucceeded("spawner-1234567890.123456", results)
		want := "plain text response (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("failed with message", func(t *testing.T) {
		got := FormatSlackFailed("spawner-1234567890.123456", "pod OOMKilled", nil)
		want := "Error: pod OOMKilled (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("failed without message", func(t *testing.T) {
		got := FormatSlackFailed("spawner-1234567890.123456", "", nil)
		want := "Failed. (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("failed with response", func(t *testing.T) {
		results := map[string]string{"response": b64("Could not find the file.")}
		got := FormatSlackFailed("spawner-1234567890.123456", "Task failed", results)
		want := "Could not find the file.\nError: Task failed (Task: spawner-1234567890.123456)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
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
	_, err := reporter.PostThreadReply(context.Background(), "C123", "1234.5678", "test")
	if err == nil {
		t.Error("expected error with invalid token, got nil")
	}
}

func TestSlackReporter_UpdateMessageError(t *testing.T) {
	reporter := &SlackReporter{BotToken: "xoxb-invalid"}
	err := reporter.UpdateMessage(context.Background(), "C123", "1234.5678", "test")
	if err == nil {
		t.Error("expected error with invalid token, got nil")
	}
}
