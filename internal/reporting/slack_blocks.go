package reporting

import (
	"regexp"
	"strings"
	"unicode/utf8"

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

	// reInlineFormatting matches all inline formatting tokens that need
	// special handling inside rich-text contexts (tables, lists).
	// Groups:
	//   1 — bold content        (**…**)
	//   2 — strikethrough       (~~…~~)
	//   3 — inline code         (`…`)
	//   4 — md link text        [text](url)
	//   5 — md link url
	//   6 — slack link url      <url|text>
	//   7 — slack link text
	reInlineFormatting = regexp.MustCompile(
		`\*\*(.+?)\*\*` + // bold
			`|~~(.+?)~~` + // strikethrough
			"|`([^`]+)`" + // inline code
			`|\[([^\]]+)\]\(([^)]*(?:\([^)]*\))[^)]*|[^)]+)\)` + // [text](url)
			`|<((?:https?://)[^|>]+)\|([^>]+)>`, // <url|text>
	)
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
	segDivider
)

// reInlineCode matches `code` spans.
var reInlineCode = regexp.MustCompile("`([^`]+)`")

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
	// unorderedListRe matches unordered list items: - item, * item, • item
	unorderedListRe = regexp.MustCompile(`^(\s*)[-*•]\s+(.+)$`)
	// orderedListRe matches ordered list items: 1. item, 2. item
	orderedListRe = regexp.MustCompile(`^(\s*)\d+\.\s+(.+)$`)
	// dividerRe matches horizontal rule lines: ---, ***, ___  (three or more of the same character)
	dividerRe = regexp.MustCompile(`^\s*(?:---+|___+|\*\*\*+)\s*$`)
)

// parseMarkdownSegments splits markdown text into typed segments.
func parseMarkdownSegments(text string) []segment {
	lines := strings.Split(text, "\n")
	var segments []segment

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Check for divider (---, ***, ___).
		// NOTE: This intentionally does not support CommonMark setext headings
		// (e.g. "Header\n---"), which would render as a paragraph + divider.
		if dividerRe.MatchString(line) {
			segments = append(segments, segment{typ: segDivider})
			i++
			continue
		}

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
			for j < len(lines) {
				if unorderedListRe.MatchString(lines[j]) {
					listLines = append(listLines, lines[j])
					j++
				} else if strings.TrimSpace(lines[j]) == "" && j+1 < len(lines) && unorderedListRe.MatchString(lines[j+1]) {
					// Skip blank lines between list items.
					j++
				} else {
					break
				}
			}
			segments = append(segments, segment{typ: segList, lines: listLines})
			i = j
			continue
		}

		// Check for ordered list.
		if orderedListRe.MatchString(line) {
			var listLines []string
			j := i
			for j < len(lines) {
				if orderedListRe.MatchString(lines[j]) {
					listLines = append(listLines, lines[j])
					j++
				} else if strings.TrimSpace(lines[j]) == "" && j+1 < len(lines) && orderedListRe.MatchString(lines[j+1]) {
					// Skip blank lines between list items.
					j++
				} else {
					break
				}
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
			if dividerRe.MatchString(l) ||
				headerRe.MatchString(l) ||
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

const (
	// SlackBlockLimit is the maximum number of blocks Slack allows per message.
	SlackBlockLimit = 50
	// slackSectionTextLimit is the maximum character length for a section block's text.
	slackSectionTextLimit = 3000
	// slackHeaderTextLimit is the maximum character length for a header block's text.
	slackHeaderTextLimit = 150
)

// responseToBlocks converts a markdown response string into Slack blocks.
func responseToBlocks(text string) []slack.Block {
	segments := parseMarkdownSegments(text)
	var blocks []slack.Block

	for _, seg := range segments {
		switch seg.typ {
		case segDivider:
			blocks = append(blocks, slack.NewDividerBlock())
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
				for _, chunk := range splitText(convertInlineMarkdown(joined), slackSectionTextLimit) {
					if chunk == "" {
						continue
					}
					blocks = append(blocks, slack.NewSectionBlock(
						slack.NewTextBlockObject(slack.MarkdownType, chunk, false, false),
						nil, nil,
					))
				}
			}
		}
	}

	return blocks
}

// headerBlock creates a Slack HeaderBlock from header text.
// HeaderBlocks only support plain text, so backtick code spans are stripped.
func headerBlock(text string) *slack.HeaderBlock {
	text = reInlineCode.ReplaceAllString(text, "$1")
	if utf8.RuneCountInString(text) > slackHeaderTextLimit {
		text = string([]rune(text)[:slackHeaderTextLimit-1]) + "…"
	}
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

	// Parse header row to determine column count.
	headerCells := parseCells(lines[0])
	numCols := len(headerCells)

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
// Inline formatting (bold, code, links, strikethrough) is parsed and rendered
// as styled rich-text elements.
func cellsToRichText(cells []string) []*slack.RichTextBlock {
	blocks := make([]*slack.RichTextBlock, len(cells))
	for i, cell := range cells {
		if cell == "" {
			// Slack rejects empty text elements with invalid_blocks.
			blocks[i] = slack.NewRichTextBlock("",
				slack.NewRichTextSection(
					slack.NewRichTextSectionTextElement("\u00a0", nil),
				),
			)
			continue
		}
		elements := parseRichTextElements(cell)
		blocks[i] = slack.NewRichTextBlock("",
			slack.NewRichTextSection(elements...),
		)
	}
	return blocks
}

// parseRichTextElements parses inline formatting (bold, strikethrough, code,
// markdown links, slack links) and returns a slice of RichTextSectionElement
// with appropriate styling. Nested formatting (e.g. **`code`**) is supported
// by recursively parsing bold/strikethrough content.
func parseRichTextElements(text string) []slack.RichTextSectionElement {
	return parseRichTextElementsWithStyle(text, nil)
}

// parseRichTextElementsWithStyle is the recursive inner function that carries
// an inherited style from an outer formatting context (e.g. bold wrapping code).
func parseRichTextElementsWithStyle(text string, inherited *slack.RichTextSectionTextStyle) []slack.RichTextSectionElement {
	var elements []slack.RichTextSectionElement

	matches := reInlineFormatting.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		if text != "" {
			elements = append(elements, slack.NewRichTextSectionTextElement(text, inherited))
		}
		return elements
	}

	pos := 0
	for _, loc := range matches {
		// Plain text before this match.
		if loc[0] > pos {
			elements = append(elements,
				slack.NewRichTextSectionTextElement(text[pos:loc[0]], inherited))
		}

		switch {
		case loc[2] >= 0: // group 1: bold **…**
			inner := text[loc[2]:loc[3]]
			style := mergeStyle(inherited, &slack.RichTextSectionTextStyle{Bold: true})
			elements = append(elements, parseRichTextElementsWithStyle(inner, style)...)

		case loc[4] >= 0: // group 2: strikethrough ~~…~~
			inner := text[loc[4]:loc[5]]
			style := mergeStyle(inherited, &slack.RichTextSectionTextStyle{Strike: true})
			elements = append(elements, parseRichTextElementsWithStyle(inner, style)...)

		case loc[6] >= 0: // group 3: inline code `…`
			code := text[loc[6]:loc[7]]
			style := mergeStyle(inherited, &slack.RichTextSectionTextStyle{Code: true})
			elements = append(elements, slack.NewRichTextSectionTextElement(code, style))

		case loc[8] >= 0: // groups 4,5: markdown link [text](url)
			linkText := text[loc[8]:loc[9]]
			linkURL := text[loc[10]:loc[11]]
			elements = append(elements, slack.NewRichTextSectionLinkElement(linkURL, linkText, inherited))

		case loc[12] >= 0: // groups 6,7: slack link <url|text>
			linkURL := text[loc[12]:loc[13]]
			linkText := text[loc[14]:loc[15]]
			elements = append(elements, slack.NewRichTextSectionLinkElement(linkURL, linkText, inherited))
		}

		pos = loc[1]
	}

	// Trailing text after last match.
	if pos < len(text) {
		elements = append(elements,
			slack.NewRichTextSectionTextElement(text[pos:], inherited))
	}

	return elements
}

// mergeStyle combines two RichTextSectionTextStyle values, producing a new
// style with all flags from both. Either argument may be nil.
func mergeStyle(a, b *slack.RichTextSectionTextStyle) *slack.RichTextSectionTextStyle {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &slack.RichTextSectionTextStyle{
		Bold:   a.Bold || b.Bold,
		Italic: a.Italic || b.Italic,
		Strike: a.Strike || b.Strike,
		Code:   a.Code || b.Code,
	}
}

// splitText splits s into chunks of at most maxLen runes, breaking at the
// last newline before the limit when possible.
func splitText(s string, maxLen int) []string {
	if utf8.RuneCountInString(s) <= maxLen {
		return []string{s}
	}
	runes := []rune(s)
	var chunks []string
	for len(runes) > 0 {
		end := maxLen
		if end > len(runes) {
			end = len(runes)
		}
		chunk := runes[:end]
		if end < len(runes) {
			if idx := lastIndexRune(chunk, '\n'); idx > 0 {
				end = idx + 1
				chunk = runes[:end]
			}
		}
		trimmed := strings.TrimRight(string(chunk), "\n")
		if trimmed != "" {
			chunks = append(chunks, trimmed)
		}
		runes = runes[end:]
	}
	return chunks
}

func lastIndexRune(runes []rune, r rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == r {
			return i
		}
	}
	return -1
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
			parseRichTextElements(text)...,
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
