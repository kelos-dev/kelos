package reporting

import (
	"regexp"
	"strings"

	"github.com/slack-go/slack"
)

var (
	// reBold matches **bold** syntax.
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*`)
	// reLink matches [text](url) syntax, allowing one level of balanced parentheses
	// in the URL (e.g. Wikipedia/RFC links like https://en.wikipedia.org/wiki/Go_(language)).
	reLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*(?:\([^)]*\))[^)]*|[^)]+)\)`)
	// reStrikethrough matches ~~text~~ syntax.
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
)

// convertInlineMarkdown converts standard inline Markdown (bold, links,
// strikethrough) to Slack mrkdwn format. Headings are handled separately
// as HeaderBlocks, so they are not converted here.
func convertInlineMarkdown(s string) string {
	s = reBold.ReplaceAllString(s, "*$1*")
	s = reLink.ReplaceAllString(s, "<$2|$1>")
	s = reStrikethrough.ReplaceAllString(s, "~$1~")
	return s
}

// segmentType identifies a markdown content segment.
type segmentType int

const (
	segPlain segmentType = iota
	segTable
	segList
	segHeader
)

// segment is a contiguous chunk of parsed markdown content.
type segment struct {
	typ   segmentType
	lines []string
	// header level (1-3) for segHeader segments.
	level int
}

var (
	// tableRowRe matches markdown table rows: | col1 | col2 |
	tableRowRe = regexp.MustCompile(`^\s*\|(.+)\|\s*$`)
	// tableSepRe matches the separator row: | --- | --- |
	tableSepRe = regexp.MustCompile(`^\s*\|[\s\-:|]+\|\s*$`)
	// headerRe matches markdown headers: # Header, ## Header, ### Header
	headerRe = regexp.MustCompile(`^(#{1,3})\s+(.+)$`)
	// unorderedListRe matches unordered list items: - item, * item
	unorderedListRe = regexp.MustCompile(`^(\s*)[-*]\s+(.+)$`)
	// orderedListRe matches ordered list items: 1. item, 2. item
	orderedListRe = regexp.MustCompile(`^(\s*)\d+\.\s+(.+)$`)
)

// parseMarkdownSegments splits markdown text into typed segments.
func parseMarkdownSegments(text string) []segment {
	lines := strings.Split(text, "\n")
	var segments []segment

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Check for header.
		if m := headerRe.FindStringSubmatch(line); m != nil {
			segments = append(segments, segment{
				typ:   segHeader,
				lines: []string{m[2]},
				level: len(m[1]),
			})
			i++
			continue
		}

		// Check for table start (need at least header row + separator + one data row).
		if tableRowRe.MatchString(line) && i+1 < len(lines) && tableSepRe.MatchString(lines[i+1]) {
			var tableLines []string
			tableLines = append(tableLines, line)
			j := i + 1
			// Skip separator.
			j++
			// Collect data rows.
			for j < len(lines) && tableRowRe.MatchString(lines[j]) && !tableSepRe.MatchString(lines[j]) {
				tableLines = append(tableLines, lines[j])
				j++
			}
			segments = append(segments, segment{typ: segTable, lines: tableLines})
			i = j
			continue
		}

		// Check for unordered list.
		if unorderedListRe.MatchString(line) {
			var listLines []string
			j := i
			for j < len(lines) && unorderedListRe.MatchString(lines[j]) {
				listLines = append(listLines, lines[j])
				j++
			}
			segments = append(segments, segment{typ: segList, lines: listLines})
			i = j
			continue
		}

		// Check for ordered list.
		if orderedListRe.MatchString(line) {
			var listLines []string
			j := i
			for j < len(lines) && orderedListRe.MatchString(lines[j]) {
				listLines = append(listLines, lines[j])
				j++
			}
			segments = append(segments, segment{typ: segList, lines: listLines})
			i = j
			continue
		}

		// Plain text — accumulate consecutive non-special lines.
		var plainLines []string
		j := i
		for j < len(lines) {
			l := lines[j]
			if headerRe.MatchString(l) ||
				unorderedListRe.MatchString(l) ||
				orderedListRe.MatchString(l) {
				break
			}
			// Check if this starts a table.
			if tableRowRe.MatchString(l) && j+1 < len(lines) && tableSepRe.MatchString(lines[j+1]) {
				break
			}
			plainLines = append(plainLines, l)
			j++
		}
		// Trim trailing empty lines from plain text.
		for len(plainLines) > 0 && strings.TrimSpace(plainLines[len(plainLines)-1]) == "" {
			plainLines = plainLines[:len(plainLines)-1]
		}
		if len(plainLines) > 0 {
			segments = append(segments, segment{typ: segPlain, lines: plainLines})
		}
		// If nothing was consumed (blank lines between segments), advance past them.
		if j == i {
			i++
		} else {
			i = j
		}
	}

	return segments
}

// responseToBlocks converts a markdown response string into Slack blocks.
func responseToBlocks(text string) []slack.Block {
	segments := parseMarkdownSegments(text)
	var blocks []slack.Block

	for _, seg := range segments {
		switch seg.typ {
		case segHeader:
			blocks = append(blocks, headerBlock(seg.lines[0]))
		case segTable:
			if b := tableBlock(seg.lines); b != nil {
				blocks = append(blocks, b)
			}
		case segList:
			blocks = append(blocks, listBlock(seg.lines)...)
		case segPlain:
			joined := strings.Join(seg.lines, "\n")
			if strings.TrimSpace(joined) != "" {
				blocks = append(blocks, slack.NewSectionBlock(
					slack.NewTextBlockObject(slack.MarkdownType, convertInlineMarkdown(joined), false, false),
					nil, nil,
				))
			}
		}
	}

	return blocks
}

// headerBlock creates a Slack HeaderBlock from header text.
func headerBlock(text string) *slack.HeaderBlock {
	return slack.NewHeaderBlock(
		slack.NewTextBlockObject(slack.PlainTextType, text, false, false),
	)
}

// tableBlock creates a Slack TableBlock from markdown table lines.
// The first line is the header row; subsequent lines are data rows.
func tableBlock(lines []string) *slack.TableBlock {
	if len(lines) < 1 {
		return nil
	}

	table := slack.NewTableBlock("")

	// Parse header row to determine column count and settings.
	headerCells := parseCells(lines[0])
	numCols := len(headerCells)
	settings := make([]slack.ColumnSetting, numCols)
	for i := range settings {
		settings[i] = slack.ColumnSetting{IsWrapped: true}
	}
	table.WithColumnSettings(settings...)

	// Add header row.
	table.AddRow(cellsToRichText(headerCells)...)

	// Add data rows.
	for _, line := range lines[1:] {
		cells := parseCells(line)
		// Pad or truncate to match column count.
		for len(cells) < numCols {
			cells = append(cells, "")
		}
		cells = cells[:numCols]
		table.AddRow(cellsToRichText(cells)...)
	}

	return table
}

// parseCells extracts cell text from a markdown table row.
func parseCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.Trim(row, "|")
	parts := strings.Split(row, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// cellsToRichText converts cell strings to RichTextBlock pointers for table rows.
func cellsToRichText(cells []string) []*slack.RichTextBlock {
	blocks := make([]*slack.RichTextBlock, len(cells))
	for i, cell := range cells {
		blocks[i] = slack.NewRichTextBlock("",
			slack.NewRichTextSection(
				slack.NewRichTextSectionTextElement(cell, nil),
			),
		)
	}
	return blocks
}

// listBlock creates Slack RichTextBlock(s) from markdown list lines.
func listBlock(lines []string) []slack.Block {
	if len(lines) == 0 {
		return nil
	}

	// Determine list style from first line.
	style := slack.RTEListBullet
	if orderedListRe.MatchString(lines[0]) {
		style = slack.RTEListOrdered
	}

	var items []slack.RichTextElement
	for _, line := range lines {
		var text string
		if m := unorderedListRe.FindStringSubmatch(line); m != nil {
			text = m[2]
		} else if m := orderedListRe.FindStringSubmatch(line); m != nil {
			text = m[2]
		} else {
			continue
		}
		items = append(items, slack.NewRichTextSection(
			slack.NewRichTextSectionTextElement(text, nil),
		))
	}

	if len(items) == 0 {
		return nil
	}

	return []slack.Block{
		slack.NewRichTextBlock("",
			slack.NewRichTextList(style, 0, items...),
		),
	}
}
