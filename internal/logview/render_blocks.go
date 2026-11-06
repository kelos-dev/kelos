package logview

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Glyphs of the todo list and the run footer.
const (
	todoOpen      = "☐ "
	todoDone      = "☒ "
	footerRule    = "──────────"
	footerSep     = " · "
	successMark   = "✓ "
	warningMark   = "⚠ "
	failureMark   = "✗ "
	lineNumberFmt = "%5d "
	noLineNumber  = "      "
	// tabSpaces replaces tabs in displayed lines so widths stay predictable.
	tabSpaces = "    "
)

// Number formats of the footer.
const (
	hundred      = 100
	thousand     = 1_000
	million      = 1_000_000
	thousandsFmt = "%.1fk"
	millionsFmt  = "%.2fM"
	costFmt      = "$%.4f"
)

func (r *Renderer) renderToolResult(result ToolResult) {
	call, known := r.calls[result.ID]
	delete(r.calls, result.ID)
	r.resultHeader(result, call, known)

	// An explicitly empty result diff means the tool reported no patch, so
	// only a missing one falls back to the call's.
	diff := result.Diff
	if diff == nil {
		diff = call.Diff
	}
	switch {
	case result.IsError:
		r.renderOutput(firstLine(result.Output), restLines(result.Output), true)
	case len(diff) > 0:
		path := r.oneLine(call.Summary)
		if path == "" {
			path = unknownPath
		}
		r.renderDiff(path, diff)
	case call.WriteLines > 0:
		r.line(resultPrefix + fmt.Sprintf("Wrote %d lines to %s", call.WriteLines, r.oneLine(call.Summary)))
	case len(call.Todos) > 0:
		// The list was shown with the call.
	case strings.TrimSpace(result.Output) == "":
		r.line(resultPrefix + r.styles.muted.Render(noOutputLabel))
	default:
		r.renderOutput(firstLine(result.Output), restLines(result.Output), false)
	}
}

// renderOutput writes a tool output under its call: first, then a
// preview of the following lines.
func (r *Renderer) renderOutput(first string, rest []string, isError bool) {
	first = r.clip(first)
	if isError {
		first = r.styles.failure.Render("Error: ") + first
	} else {
		first = r.styles.muted.Render(first)
	}
	r.line(resultPrefix + first)
	shown, hidden := r.preview(rest, resultRestPreviewLines)
	for _, line := range shown {
		r.line(resultIndent + r.styles.muted.Render(r.clip(line)))
	}
	if hidden > 0 {
		r.line(resultIndent + r.styles.muted.Render(moreLines(hidden)))
	}
}

func (r *Renderer) renderDiff(path string, hunks []Hunk) {
	added, removed := diffStats(hunks)
	r.line(resultPrefix + fmt.Sprintf("Updated %s with %s and %s",
		path, plural(added, "addition"), plural(removed, "removal")))

	var lines []string
	for i, hunk := range hunks {
		if i > 0 {
			lines = append(lines, r.styles.muted.Render(noLineNumber+ellipsis))
		}
		lines = append(lines, r.hunkLines(hunk)...)
	}
	shown, hidden := r.preview(lines, maxDiffLines)
	for _, line := range shown {
		r.line(resultIndent + line)
	}
	if hidden > 0 {
		r.line(resultIndent + r.styles.muted.Render(moreLines(hidden)))
	}
}

// hunkLines renders a hunk, numbering lines when its position is known:
// removed lines carry their old number, the others their new one.
func (r *Renderer) hunkLines(hunk Hunk) []string {
	oldLine, newLine := hunk.OldStart, hunk.NewStart
	oldKnown, newKnown := oldLine > 0, newLine > 0
	lines := make([]string, 0, len(hunk.Lines))
	for _, line := range hunk.Lines {
		var number, styled string
		switch line.Op {
		case DiffRemove:
			number = lineNumber(oldKnown, oldLine)
			styled = r.styles.removed.Render(r.padDiffLine(r.clip("- " + line.Text)))
			oldLine++
		case DiffAdd:
			number = lineNumber(newKnown, newLine)
			styled = r.styles.added.Render(r.padDiffLine(r.clip("+ " + line.Text)))
			newLine++
		default:
			number = lineNumber(newKnown, newLine)
			styled = r.styles.muted.Render(r.clip("  " + line.Text))
			oldLine++
			newLine++
		}
		lines = append(lines, r.styles.muted.Render(number)+styled)
	}
	return lines
}

// padDiffLine expands tabs and, when lines are tinted and the terminal
// width is known, pads text so the tint spans the whole line.
func (r *Renderer) padDiffLine(text string) string {
	text = strings.ReplaceAll(text, "\t", tabSpaces)
	if !r.styles.tinted || r.width == 0 {
		return text
	}
	available := r.width - len(resultIndent) - len(noLineNumber)
	if gap := available - lipgloss.Width(text); gap > 0 {
		text += strings.Repeat(" ", gap)
	}
	return text
}

func lineNumber(numbered bool, n int) string {
	if !numbered {
		return noLineNumber
	}
	return fmt.Sprintf(lineNumberFmt, n)
}

func (r *Renderer) renderTodos(todos []Todo) {
	for i, todo := range todos {
		prefix := resultIndent
		if i == 0 {
			prefix = resultPrefix
		}
		var item string
		switch todo.Status {
		case TodoCompleted:
			item = r.styles.done.Render(todoDone + todo.Text)
		case TodoInProgress:
			item = r.styles.active.Render(todoOpen + todo.Text)
		default:
			item = todoOpen + todo.Text
		}
		r.line(prefix + item)
	}
}

func (r *Renderer) renderResult(result Result) {
	r.entry(r.styles.muted.Render(footerRule))
	r.line(r.resultStatus(result) + r.styles.muted.Render(resultFacts(result)))
	if result.Message != "" {
		r.line(r.styles.failure.Render(result.Message))
	}
}

func (r *Renderer) resultStatus(result Result) string {
	switch result.Status {
	case ResultError:
		return r.styles.failure.Render(failureMark + withDetail("Error", result.Detail))
	case ResultIncomplete:
		return r.styles.warning.Render(warningMark + withDetail("Incomplete", result.Detail))
	default:
		return r.styles.success.Render(successMark + "Completed")
	}
}

// resultFacts lists the run's turns, cost, tokens and duration, skipping
// what the agent did not report.
func resultFacts(result Result) string {
	var facts []string
	if result.Turns > 0 {
		facts = append(facts, plural(result.Turns, "turn"))
	}
	if result.CostUSD > 0 {
		facts = append(facts, fmt.Sprintf(costFmt, result.CostUSD))
	}
	if result.InputTokens > 0 || result.OutputTokens > 0 {
		tokens := humanCount(result.InputTokens) + " in"
		if result.CachedTokens > 0 {
			tokens += " (" + humanCount(result.CachedTokens) + " cached)"
		}
		facts = append(facts, tokens+", "+humanCount(result.OutputTokens)+" out")
	}
	if result.DurationMS > 0 {
		facts = append(facts, (time.Duration(result.DurationMS) * time.Millisecond).Round(time.Second).String())
	}
	if len(facts) == 0 {
		return ""
	}
	return footerSep + strings.Join(facts, footerSep)
}

// humanCount abbreviates a token count: 950, 18.5k, 2.37M.
func humanCount(n int) string {
	// thousandsFmt shows one decimal, so round to the hundred before
	// choosing the unit: 999_950 is 1.00M, not 1000.0k.
	rounded := (n + hundred/2) / hundred * hundred
	switch {
	case rounded >= million:
		return fmt.Sprintf(millionsFmt, float64(n)/million)
	case n >= thousand:
		return fmt.Sprintf(thousandsFmt, float64(rounded)/thousand)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func withDetail(label, detail string) string {
	if detail == "" {
		return label
	}
	return label + " (" + detail + ")"
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func firstLine(text string) string {
	lines := splitLines(strings.TrimRight(text, "\n"))
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func restLines(text string) []string {
	lines := splitLines(strings.TrimRight(text, "\n"))
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}
