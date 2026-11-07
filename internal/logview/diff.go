package logview

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// diffContextLines is how many unchanged lines are kept around a change
// when a diff is computed from an edit's old and new text.
const diffContextLines = 3

// Opcode kinds returned by difflib's line matcher.
const (
	diffEqual   = 'e'
	diffInsert  = 'i'
	diffDelete  = 'd'
	diffReplace = 'r'
)

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

// replacementHunks builds line-level hunks for an edit. Unchanged lines
// between separate edits remain context instead of counting as changes.
func replacementHunks(oldText, newText string) []Hunk {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	if slices.Equal(oldLines, newLines) {
		return nil
	}

	groups := difflib.NewMatcher(oldLines, newLines).GetGroupedOpCodes(diffContextLines)
	hunks := make([]Hunk, 0, len(groups))
	for _, group := range groups {
		hunks = append(hunks, replacementGroup(oldLines, newLines, group))
	}
	return hunks
}

func replacementGroup(oldLines, newLines []string, group []difflib.OpCode) Hunk {
	var hunk Hunk
	for _, code := range group {
		if code.Tag == diffEqual {
			for _, line := range oldLines[code.I1:code.I2] {
				hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffContext, Text: line})
			}
			continue
		}
		if code.Tag == diffDelete || code.Tag == diffReplace {
			for _, line := range oldLines[code.I1:code.I2] {
				hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffRemove, Text: line})
			}
		}
		if code.Tag == diffInsert || code.Tag == diffReplace {
			for _, line := range newLines[code.J1:code.J2] {
				hunk.Lines = append(hunk.Lines, DiffLine{Op: DiffAdd, Text: line})
			}
		}
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
// returns nil when raw is absent, null or malformed, and an empty slice
// when Claude reported an empty patch.
func parseStructuredPatch(raw json.RawMessage) []Hunk {
	var patches []structuredPatch
	if len(raw) == 0 || json.Unmarshal(raw, &patches) != nil || patches == nil {
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
