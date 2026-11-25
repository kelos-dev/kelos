package logview

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// TestRenderGolden compares Claude Code's fixture with the expected output.
func TestRenderGolden(t *testing.T) {
	got := renderFixture(t, Options{})
	assertGolden(t, filepath.Join("testdata", "claude.golden"), got)
}

func renderFixture(t *testing.T, opts Options) string {
	t.Helper()
	input, err := os.Open(filepath.Join("testdata", "claude.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	var out bytes.Buffer
	renderer := NewRenderer(&out, opts)
	if err := Parse(input, renderer.Render); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	renderer.Finish()
	return out.String()
}

func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run go test -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (run go test -update to accept):\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func renderDiff(profile termenv.Profile, width int) string {
	var out bytes.Buffer
	renderer := newRendererWithProfile(&out, Options{Color: true, Width: width}, profile)
	renderer.Render(ToolCall{ID: "e", Name: "Update", Summary: "a.go", Diff: replacementHunks("x := 1", "\tx := 2")})
	renderer.Render(ToolResult{ID: "e"})
	return out.String()
}

func TestRenderColorsDiffTextOnBasicTerminals(t *testing.T) {
	const (
		ansiGreen = "\x1b[32m"
		ansiRed   = "\x1b[31m"
	)
	got := renderDiff(termenv.ANSI, 0)
	for _, want := range []string{ansiGreen + "+     x := 2", ansiRed + "- x := 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("colored output lacks %q:\n%q", want, got)
		}
	}
}

func TestRenderTintsDiffLinesToTerminalWidth(t *testing.T) {
	const (
		width          = 40
		trueColorTint  = "\x1b[48;2;"
		paddedAddition = "+     x := 2" + "                 "
	)
	got := renderDiff(termenv.TrueColor, width)
	if !strings.Contains(got, trueColorTint) {
		t.Errorf("output lacks a background tint:\n%q", got)
	}
	if !strings.Contains(got, paddedAddition) {
		t.Errorf("added line is not padded to %d columns:\n%q", width, got)
	}
}

func TestRenderWithoutColorHasNoEscapes(t *testing.T) {
	got := renderFixture(t, Options{})
	if strings.Contains(got, "\x1b[") {
		t.Errorf("uncolored output contains ANSI escapes:\n%s", got)
	}
}

func TestRenderRepeatsHeaderForOutOfOrderResult(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(ToolCall{ID: "a", Name: "Read", Summary: "a.go"})
	renderer.Render(ToolCall{ID: "b", Name: "Read", Summary: "b.go"})
	renderer.Render(ToolResult{ID: "a", Output: "package a"})
	renderer.Render(ToolResult{ID: "b", Output: "package b"})

	want := strings.Join([]string{
		"⏺ Read(a.go)",
		"",
		"⏺ Read(b.go)",
		"",
		"⏺ Read(a.go)",
		"  ⎿  package a",
		"",
		"⏺ Read(b.go)",
		"  ⎿  package b",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderStreamedTextContinuesOneLine(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(Text{Text: "Hello, ", Delta: true})
	renderer.Render(Text{Text: "world.", Delta: true})
	renderer.Finish()

	if got, want := out.String(), "⏺ Hello, world.\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderSkipsNestedActivity(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(Text{Text: "hidden", Nested: true})
	renderer.Render(ToolCall{ID: "a", Name: "Read", Nested: true})
	renderer.Render(ToolResult{ID: "a", Output: "hidden", Nested: true})
	renderer.Render(Text{Text: "shown"})
	if got, want := out.String(), "⏺ shown\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStreamedTextIndentsAcrossFragments(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(Text{Text: "first\n", Delta: true})
	renderer.Render(Text{Text: "second\nthird\n", Delta: true})
	renderer.Finish()
	if got, want := out.String(), "⏺ first\n  second\n  third\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderClipsToolOutputBeforeStyling(t *testing.T) {
	for _, isError := range []bool{false, true} {
		var out bytes.Buffer
		renderer := newRendererWithProfile(&out, Options{Color: true}, termenv.ANSI)
		renderer.Render(ToolCall{ID: "a", Name: "Bash"})
		renderer.Render(ToolResult{ID: "a", Output: strings.Repeat("x", maxLineRunes+1), IsError: isError})
		renderer.Render(Text{Text: "next"})

		first := strings.Repeat("x", maxLineRunes) + ellipsis
		if isError {
			first = renderer.styles.failure.Render("Error: ") + first
		} else {
			first = renderer.styles.muted.Render(first)
		}
		if !strings.Contains(out.String(), resultPrefix+first+"\n") {
			t.Errorf("isError=%v: clipped first line lost its style reset:\n%q", isError, out.String())
		}
	}
}

func TestRenderClipsDiffLines(t *testing.T) {
	longLine := strings.Repeat("x", maxLineRunes+1)
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Render(ToolCall{ID: "a", Name: "Update", Summary: "a.go"})
	renderer.Render(ToolResult{ID: "a", Diff: []Hunk{{Lines: []DiffLine{{Op: DiffAdd, Text: longLine}}}}})

	want := "+ " + strings.Repeat("x", maxLineRunes-len("+ ")) + ellipsis
	if !strings.Contains(out.String(), want) {
		t.Errorf("diff lacks %q:\n%s", want, out.String())
	}
}

func TestHunkLinesNumberKnownSide(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	lines := renderer.hunkLines(Hunk{NewStart: 5, Lines: []DiffLine{
		{Op: DiffRemove, Text: "old"},
		{Op: DiffAdd, Text: "new"},
	}})
	want := []string{"      - old", "    5 + new"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", lines, want)
	}
}

func TestRenderAcceptsPointerEvents(t *testing.T) {
	values := []Event{
		Init{Model: "claude"}, Text{Text: "hello"}, Thinking{Text: "thought"},
		ToolCall{ID: "a", Name: "Read"}, ToolResult{ID: "a", Output: "done"},
		Error{Message: "error"}, Result{Status: ResultCompleted}, Raw{Line: "raw"},
	}
	pointers := []Event{
		&Init{Model: "claude"}, &Text{Text: "hello"}, &Thinking{Text: "thought"},
		&ToolCall{ID: "a", Name: "Read"}, &ToolResult{ID: "a", Output: "done"},
		&Error{Message: "error"}, &Result{Status: ResultCompleted}, &Raw{Line: "raw"},
	}
	var want, got bytes.Buffer
	valueRenderer := NewRenderer(&want, Options{})
	pointerRenderer := NewRenderer(&got, Options{})
	for i := range values {
		valueRenderer.Render(values[i])
		pointerRenderer.Render(pointers[i])
	}
	pointerRenderer.Render((*Text)(nil))
	if got.String() != want.String() {
		t.Errorf("pointer events rendered differently:\ngot:\n%s\nwant:\n%s", got.String(), want.String())
	}
}

func TestRenderToolResultDiffFallback(t *testing.T) {
	callDiff := []Hunk{{OldStart: 1, NewStart: 1, Lines: []DiffLine{{DiffRemove, "old"}, {DiffAdd, "new"}}}}
	tests := []struct {
		name       string
		resultDiff []Hunk
		want       string
		notWant    string
	}{
		{name: "missing result diff uses the call's", want: "+ new", notWant: "updated"},
		{name: "empty result diff shows the output", resultDiff: []Hunk{}, want: "updated", notWant: "+ new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			renderer := NewRenderer(&out, Options{})
			renderer.Render(ToolCall{ID: "a", Name: "Edit", Summary: "a.go", Diff: callDiff})
			renderer.Render(ToolResult{ID: "a", Output: "updated", Diff: tt.resultDiff})
			got := out.String()
			if !strings.Contains(got, tt.want) || strings.Contains(got, tt.notWant) {
				t.Errorf("got:\n%s\nwant %q and not %q", got, tt.want, tt.notWant)
			}
		})
	}
}

func TestHumanCount(t *testing.T) {
	tests := map[int]string{
		0:         "0",
		950:       "950",
		1_000:     "1.0k",
		18_456:    "18.5k",
		999_949:   "999.9k",
		999_950:   "1.00M",
		2_374_433: "2.37M",
	}
	for n, want := range tests {
		if got := humanCount(n); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", n, got, want)
		}
	}
}
