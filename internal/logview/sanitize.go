package logview

import "strings"

// Control character ranges that must not reach the terminal: agent and
// tool output is untrusted, and escape sequences in it could retitle the
// window, move the cursor or overwrite earlier lines.
const (
	c0End             = 0x1f
	del               = 0x7f
	c1Low             = 0x80
	c1High            = 0x9f
	arabicLetterMark  = 0x061c
	directionMarkLow  = 0x200e
	directionMarkHigh = 0x200f
	bidiEmbeddingLow  = 0x202a
	bidiEmbeddingHigh = 0x202e
	bidiIsolateLow    = 0x2066
	bidiIsolateHigh   = 0x2069
)

// clean removes control characters from text, keeping newlines and tabs.
// A carriage return ending a line is dropped with the rest.
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r <= c0End, r == del, r >= c1Low && r <= c1High,
			r == arabicLetterMark,
			r >= directionMarkLow && r <= directionMarkHigh,
			r >= bidiEmbeddingLow && r <= bidiEmbeddingHigh,
			r >= bidiIsolateLow && r <= bidiIsolateHigh:
			return -1
		default:
			return r
		}
	}, text)
}

// cleanLine keeps untrusted metadata within its rendered entry.
func cleanLine(text string) string {
	return strings.NewReplacer("\t", tabSpaces, "\n", `\n`).Replace(clean(text))
}

func cleanHunks(hunks []Hunk) []Hunk {
	cleaned := make([]Hunk, len(hunks))
	for i, hunk := range hunks {
		cleaned[i] = Hunk{OldStart: hunk.OldStart, NewStart: hunk.NewStart, Lines: make([]DiffLine, len(hunk.Lines))}
		for j, line := range hunk.Lines {
			cleaned[i].Lines[j] = DiffLine{Op: line.Op, Text: cleanLine(line.Text)}
		}
	}
	return cleaned
}

func cleanTodos(todos []Todo) []Todo {
	cleaned := make([]Todo, len(todos))
	for i, todo := range todos {
		cleaned[i] = Todo{Text: cleanLine(todo.Text), Status: todo.Status}
	}
	return cleaned
}

// sanitize returns event with every displayed string cleaned.
func sanitize(event Event) Event {
	switch e := event.(type) {
	case Init:
		e.Model = cleanLine(e.Model)
		return e
	case Text:
		e.Text = clean(e.Text)
		return e
	case Thinking:
		e.Text = clean(e.Text)
		return e
	case ToolCall:
		e.Name, e.Summary = cleanLine(e.Name), clean(e.Summary)
		e.Diff, e.Todos = cleanHunks(e.Diff), cleanTodos(e.Todos)
		return e
	case ToolResult:
		e.Output, e.Diff = clean(e.Output), cleanHunks(e.Diff)
		return e
	case Error:
		e.Message = cleanLine(e.Message)
		return e
	case Result:
		e.Detail, e.Message = cleanLine(e.Detail), cleanLine(e.Message)
		return e
	case Raw:
		e.Line = cleanLine(e.Line)
		return e
	}
	return event
}
