package logview

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/muesli/termenv"
)

// Layout of a rendered entry, after Claude Code.
const (
	bulletMark    = "⏺ "
	thinkingMark  = "✻ "
	resultPrefix  = "  ⎿  "
	resultIndent  = "     "
	textIndent    = "  "
	ellipsis      = "…"
	noOutputLabel = "(no output)"
	// unknownCallName heads a result whose tool call was not seen.
	unknownCallName = "Tool result"
	// unknownPath names an edited file whose path was not reported.
	unknownPath = "file"
)

// Preview limits for pretty output.
const (
	maxSummaryRunes = 160
	// resultRestPreviewLines is how many output lines are shown after the
	// first one, which sits on the result line itself.
	resultRestPreviewLines = resultPreviewLines - 1
	maxLineRunes           = 200
	resultPreviewLines     = 3
	thinkingPreviewLines   = 3
	maxDiffLines           = 40
)

// Options configures a Renderer.
type Options struct {
	// Color enables ANSI styling.
	Color bool
	// Width is the terminal width in columns, or 0 when unknown. Tinted
	// diff lines are padded to it.
	Width int
}

// Renderer writes events in a Claude Code-like layout.
type Renderer struct {
	w      io.Writer
	styles styles
	width  int
	// calls holds tool calls waiting for their result, by ID.
	calls map[string]ToolCall
	// started is set once anything was written; entries after the first
	// are separated by a blank line.
	started bool
	// inDelta is set while streamed text is being continued on one line.
	inDelta bool
	// deltaLineStart tracks a newline at the end of a streamed fragment.
	deltaLineStart bool
	// lastCallID is the ID of the tool call whose header was written last,
	// or "" when another entry followed it.
	lastCallID string
	// inRaw is set while consecutive raw lines are written as one block.
	inRaw bool
}

// NewRenderer returns a Renderer writing to w.
func NewRenderer(w io.Writer, opts Options) *Renderer {
	profile := termenv.Ascii
	if opts.Color {
		profile = detectProfile(w)
	}
	return newRendererWithProfile(w, opts, profile)
}

// newRendererWithProfile returns a Renderer using profile instead of
// detecting it from the terminal.
func newRendererWithProfile(w io.Writer, opts Options, profile termenv.Profile) *Renderer {
	return &Renderer{
		w:      w,
		styles: newStyles(w, profile),
		width:  opts.Width,
		calls:  map[string]ToolCall{},
	}
}

// Finish ends a line left open by streamed text. Call it once the stream
// is exhausted.
func (r *Renderer) Finish() {
	if r.inDelta {
		if !r.deltaLineStart {
			fmt.Fprintln(r.w)
		}
		r.inDelta = false
		r.deltaLineStart = false
	}
}

// Render writes one event.
func (r *Renderer) Render(event Event) {
	event = valueEvent(event)
	if event == nil {
		return
	}
	if isNested(event) {
		return
	}
	event = sanitize(event)
	if text, ok := event.(Text); ok && text.Delta && r.inDelta {
		r.writeDelta(text)
		return
	}
	r.Finish()
	r.render(event)
}

func (r *Renderer) render(event Event) {
	switch e := event.(type) {
	case Init:
		r.entry(r.styles.muted.Render("model: " + e.Model))
	case Text:
		r.renderText(e)
	case Thinking:
		r.renderThinking(e)
	case ToolCall:
		r.renderToolCall(e)
	case ToolResult:
		r.renderToolResult(e)
	case Error:
		r.entry(r.styles.failure.Render(bulletMark+"Error: ") + e.Message)
	case Result:
		r.renderResult(e)
	case Raw:
		r.renderRaw(e)
	}
}

// entry starts a new entry, separated from the previous one by a blank
// line, and writes its first line.
func (r *Renderer) entry(line string) {
	r.begin()
	r.line(line)
}

// begin separates a new entry from the previous one.
func (r *Renderer) begin() {
	if r.started {
		fmt.Fprintln(r.w)
	}
	r.started = true
	r.lastCallID = ""
	r.inRaw = false
}

// renderRaw writes non-JSON lines muted, grouping consecutive ones.
func (r *Renderer) renderRaw(raw Raw) {
	if strings.TrimSpace(raw.Line) == "" {
		return
	}
	line := r.styles.muted.Render(r.clip(raw.Line))
	if r.inRaw {
		r.line(line)
		return
	}
	r.entry(line)
	r.inRaw = true
}

// line writes one line of the current entry.
func (r *Renderer) line(line string) {
	fmt.Fprintln(r.w, line)
}

func (r *Renderer) bullet() string {
	return r.styles.bullet.Render(bulletMark)
}

func (r *Renderer) renderText(text Text) {
	lines := splitLines(text.Text)
	if len(lines) == 0 {
		return
	}
	if text.Delta {
		r.begin()
		fmt.Fprint(r.w, r.bullet())
		r.inDelta = true
		r.writeDelta(text)
		return
	}
	r.entry(r.bullet() + lines[0])
	for _, line := range lines[1:] {
		r.line(textIndent + line)
	}
}

func (r *Renderer) writeDelta(text Text) {
	for i, line := range strings.Split(text.Text, "\n") {
		if i > 0 {
			fmt.Fprintln(r.w)
			r.deltaLineStart = true
		}
		if line == "" {
			continue
		}
		if r.deltaLineStart {
			fmt.Fprint(r.w, textIndent)
		}
		fmt.Fprint(r.w, line)
		r.deltaLineStart = false
	}
}

func (r *Renderer) renderThinking(thinking Thinking) {
	lines := splitLines(thinking.Text)
	if len(lines) == 0 {
		return
	}
	shown, hidden := r.preview(lines, thinkingPreviewLines)
	r.entry(r.styles.thinking.Render(thinkingMark + shown[0]))
	for _, line := range shown[1:] {
		r.line(r.styles.thinking.Render(textIndent + line))
	}
	if hidden > 0 {
		r.line(r.styles.muted.Render(textIndent + moreLines(hidden)))
	}
}

func (r *Renderer) renderToolCall(call ToolCall) {
	r.entry(r.bullet() + r.callTitle(call.Name, call.Summary))
	if len(call.Todos) > 0 {
		r.renderTodos(call.Todos)
	}
	if call.ID == "" {
		return
	}
	r.calls[call.ID] = call
	r.lastCallID = call.ID
}

// resultHeader repeats a call's header when other entries were written
// since it, as happens with parallel tool calls, so the result is not
// shown under the wrong call.
// A result whose call was never seen gets a generic header.
func (r *Renderer) resultHeader(result ToolResult, call ToolCall, known bool) {
	switch {
	case !known:
		r.entry(r.styles.muted.Render(bulletMark + unknownCallName))
		r.lastCallID = ""
	case result.ID != r.lastCallID:
		r.entry(r.styles.muted.Render(bulletMark + call.Name + r.summarySuffix(call.Summary)))
		r.lastCallID = result.ID
	}
}

// callTitle renders "Name(summary)" with the summary on one line.
func (r *Renderer) callTitle(name, summary string) string {
	return r.styles.tool.Render(name) + r.summarySuffix(summary)
}

func (r *Renderer) summarySuffix(summary string) string {
	if summary == "" {
		return ""
	}
	return "(" + r.oneLine(summary) + ")"
}

func (r *Renderer) oneLine(summary string) string {
	lines := splitLines(summary)
	if len(lines) == 0 {
		return ""
	}
	line := lines[0]
	if len(lines) > 1 {
		line += " " + ellipsis
	}
	if utf8.RuneCountInString(line) > maxSummaryRunes {
		line = string([]rune(line)[:maxSummaryRunes]) + ellipsis
	}
	return line
}

// clip shortens one line of output.
func (r *Renderer) clip(line string) string {
	if utf8.RuneCountInString(line) <= maxLineRunes {
		return line
	}
	return string([]rune(line)[:maxLineRunes]) + ellipsis
}

// preview returns the lines to show and how many were left out.
func (r *Renderer) preview(lines []string, limit int) ([]string, int) {
	if len(lines) <= limit {
		return lines, 0
	}
	return lines[:limit], len(lines) - limit
}

func moreLines(hidden int) string {
	return fmt.Sprintf("%s +%d lines", ellipsis, hidden)
}

func isNested(event Event) bool {
	switch e := event.(type) {
	case Text:
		return e.Nested
	case Thinking:
		return e.Nested
	case ToolCall:
		return e.Nested
	case ToolResult:
		return e.Nested
	}
	return false
}
