package logview

import (
	"encoding/json"
	"strings"
)

// diffContextLines is how many unchanged lines are kept around a change
// when a diff is computed from an edit's old and new text.
const diffContextLines = 3

// DiffOp is the kind of a diff line.
type DiffOp byte

// Diff line kinds, matching the unified diff prefixes.
const (
	DiffContext DiffOp = ' '
	DiffAdd     DiffOp = '+'
	DiffRemove  DiffOp = '-'
)

// Hunk is a contiguous block of a diff.
type Hunk struct {
	// OldStart and NewStart are 1-based file line numbers, or 0 when the
	// position in the file is unknown.
	OldStart int
	NewStart int
	Lines    []DiffLine
}

// DiffLine is one line of a Hunk.
type DiffLine struct {
	Op   DiffOp
	Text string
}

// replacementHunk builds a hunk for an edit that replaced oldText with
// newText. Lines shared at both ends become context; everything between
// them is shown as removed and then added.
func replacementHunk(oldText, newText string) Hunk {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	var hunk Hunk
	for _, line := range oldLines[max(0, prefix-diffContextLines):prefix] {
		hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffContext, Text: line})
	}
	for _, line := range oldLines[prefix : len(oldLines)-suffix] {
		hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffRemove, Text: line})
	}
	for _, line := range newLines[prefix : len(newLines)-suffix] {
		hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffAdd, Text: line})
	}
	tail := oldLines[len(oldLines)-suffix:]
	for _, line := range tail[:min(len(tail), diffContextLines)] {
		hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffContext, Text: line})
	}
	return hunk
}

// structuredPatch is Claude Code's diff of an edit, reported in the
// tool_use_result of Edit, MultiEdit and Write.
type structuredPatch struct {
	OldStart int      `json:"oldStart"`
	NewStart int      `json:"newStart"`
	Lines    []string `json:"lines"`
}

// parseStructuredPatch converts Claude's structuredPatch into hunks. It
// returns nil when raw is absent or malformed.
func parseStructuredPatch(raw json.RawMessage) []Hunk {
	var patches []structuredPatch
	if len(raw) == 0 || json.Unmarshal(raw, &patches) != nil {
		return nil
	}
	hunks := make([]Hunk, 0, len(patches))
	for _, patch := range patches {
		hunk := Hunk{OldStart: patch.OldStart, NewStart: patch.NewStart}
		for _, line := range patch.Lines {
			hunk.Lines = append(hunk.Lines, parseDiffLine(line))
		}
		hunks = append(hunks, hunk)
	}
	return hunks
}

func parseDiffLine(line string) DiffLine {
	if line == "" {
		return DiffLine{Op: DiffContext}
	}
	// The first byte is the unified diff prefix of the line.
	switch op := DiffOp(line[0]); op {
	case DiffAdd, DiffRemove, DiffContext:
		return DiffLine{Op: op, Text: line[1:]}
	default:
		return DiffLine{Op: DiffContext, Text: line}
	}
}

// diffStats counts the added and removed lines of hunks.
func diffStats(hunks []Hunk) (added, removed int) {
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			switch line.Op {
			case DiffAdd:
				added++
			case DiffRemove:
				removed++
			}
		}
	}
	return added, removed
}

// splitLines splits text into lines, ignoring one trailing newline.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
