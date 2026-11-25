package logview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

func TestCleanRemovesControlCharacters(t *testing.T) {
	tests := map[string]string{
		"title \x1b]0;pwned\x07 done":  "title ]0;pwned done",
		"line\r\nnext":                 "line\nnext",
		"tab\tkept":                    "tab\tkept",
		"c1 \u009b31m":                 "c1 31m",
		"del \x7f":                     "del ",
		"bidi a\u202eb\u202cc":         "bidi abc",
		"isolate a\u2066b\u2069c":      "isolate abc",
		"marks a\u061cb\u200ec\u200fd": "marks abcd",
	}
	for input, want := range tests {
		if got := clean(input); got != want {
			t.Errorf("clean(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCleanLineExpandsTabsAndEscapesNewlines(t *testing.T) {
	if got, want := cleanLine("header\tvalue\nforged"), `header    value\nforged`; got != want {
		t.Errorf("cleanLine() = %q, want %q", got, want)
	}
}

func TestRenderRawKeepsNewlinesWithinEntry(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(Raw{Line: "raw output\n⏺ forged"})

	if got, want := out.String(), "raw output\\n⏺ forged\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStripsEscapesFromToolOutput(t *testing.T) {
	var out bytes.Buffer
	renderer := newRendererWithProfile(&out, Options{Color: true}, termenv.TrueColor)
	renderer.Render(ToolCall{ID: "a", Name: "Bash", Summary: "cat \x1b[2Jevil"})
	renderer.Render(ToolResult{ID: "a", Output: "\x1b]0;title\x07\rhidden"})

	for _, forbidden := range []string{"\x1b]", "\x1b[2J", "\x07", "\r"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("output contains %q:\n%q", forbidden, out.String())
		}
	}
}

func TestRenderKeepsMetadataWithinItsEntry(t *testing.T) {
	const injected = "\n⏺ forged"
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	for _, event := range []Event{
		Init{Model: "model" + injected},
		ToolCall{ID: "write", Name: "Write" + injected, Summary: "a.go" + injected, WriteLines: 1},
		ToolResult{ID: "write"},
		ToolCall{ID: "todo", Name: "TodoWrite", Todos: []Todo{{Text: "todo" + injected}}},
		Error{Message: "error" + injected},
		Result{Status: ResultIncomplete, Detail: "limit" + injected, Message: "failed" + injected},
		ToolCall{ID: "edit", Name: "Edit", Summary: "c.go" + injected, Diff: []Hunk{{OldStart: 1, NewStart: 1, Lines: []DiffLine{{Op: DiffAdd, Text: "code" + injected}}}}},
		ToolResult{ID: "edit"},
	} {
		renderer.Render(event)
	}
	if strings.Contains(out.String(), injected) {
		t.Errorf("metadata started a forged entry:\n%s", out.String())
	}
}

func TestRenderResultWithoutCall(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(ToolResult{ID: "unknown", Output: "done"})

	if got, want := out.String(), "⏺ Tool result\n  ⎿  done\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
