package slack

import (
	"strconv"
	"strings"

	goslack "github.com/slack-go/slack"
)

// blocksText returns the text of a message's Block Kit blocks, one line per
// paragraph, list item, quote line and table row. It covers every block type
// the Kelos reporter posts (section, header, rich_text, table, context and
// divider). ok is false when a block or rich text element of any other type is
// present, because its text would otherwise be silently left out.
func blocksText(blocks []goslack.Block) (text string, ok bool) {
	var lines []string
	for _, block := range blocks {
		switch b := block.(type) {
		case *goslack.SectionBlock:
			if b.Text != nil {
				lines = appendText(lines, b.Text.Text)
			}
			for _, field := range b.Fields {
				if field != nil {
					lines = appendText(lines, field.Text)
				}
			}
		case *goslack.HeaderBlock:
			if b.Text != nil {
				lines = appendText(lines, b.Text.Text)
			}
		case *goslack.ContextBlock:
			for _, element := range b.ContextElements.Elements {
				if t, isText := element.(*goslack.TextBlockObject); isText {
					lines = appendText(lines, t.Text)
				}
			}
		case *goslack.RichTextBlock:
			var richOK bool
			lines, richOK = appendRichText(lines, b.Elements, 0)
			if !richOK {
				return "", false
			}
		case *goslack.TableBlock:
			for _, row := range b.Rows {
				cells := make([]string, 0, len(row))
				for _, cell := range row {
					if cell == nil {
						cells = append(cells, "")
						continue
					}
					cellLines, cellOK := appendRichText(nil, cell.Elements, 0)
					if !cellOK {
						return "", false
					}
					cells = append(cells, strings.Join(cellLines, " "))
				}
				lines = appendText(lines, strings.Join(cells, " | "))
			}
		case *goslack.DividerBlock:
		default:
			return "", false
		}
	}
	return strings.Join(lines, "\n"), true
}

// appendRichText appends the lines of rich text elements. List items are
// written as markdown list lines ("- item", "1. item"), indented two spaces
// per nesting level, and quote lines are prefixed with "> ".
func appendRichText(lines []string, elements []goslack.RichTextElement, indent int) ([]string, bool) {
	for _, element := range elements {
		switch e := element.(type) {
		case *goslack.RichTextSection:
			lines = appendText(lines, strings.TrimRight(sectionElementsText(e.Elements), "\n"))
		case *goslack.RichTextPreformatted:
			lines = appendText(lines, strings.TrimRight(sectionElementsText(e.Elements), "\n"))
		case *goslack.RichTextQuote:
			for _, line := range strings.Split(strings.Trim(sectionElementsText(e.Elements), "\n"), "\n") {
				lines = append(lines, "> "+line)
			}
		case *goslack.RichTextList:
			prefix := strings.Repeat("  ", indent+e.Indent)
			for i, item := range e.Elements {
				section, isSection := item.(*goslack.RichTextSection)
				if !isSection {
					var nestedOK bool
					lines, nestedOK = appendRichText(lines, []goslack.RichTextElement{item}, indent+e.Indent+1)
					if !nestedOK {
						return nil, false
					}
					continue
				}
				marker := "- "
				if e.Style == goslack.RTEListOrdered {
					marker = strconv.Itoa(e.Offset+i+1) + ". "
				}
				lines = append(lines, prefix+marker+strings.TrimSpace(sectionElementsText(section.Elements)))
			}
		default:
			return nil, false
		}
	}
	return lines, true
}

// sectionElementsText renders inline rich text elements the way Slack writes
// them in a message's text field: mentions as <@U…>, channels as <#C…>, emoji
// as :name:. Elements that carry no text, such as dates and colors, are left
// out.
func sectionElementsText(elements []goslack.RichTextSectionElement) string {
	var sb strings.Builder
	for _, element := range elements {
		switch e := element.(type) {
		case *goslack.RichTextSectionTextElement:
			sb.WriteString(e.Text)
		case *goslack.RichTextSectionLinkElement:
			if e.Text != "" {
				sb.WriteString(e.Text)
			} else {
				sb.WriteString(e.URL)
			}
		case *goslack.RichTextSectionUserElement:
			sb.WriteString("<@" + e.UserID + ">")
		case *goslack.RichTextSectionChannelElement:
			sb.WriteString("<#" + e.ChannelID + ">")
		case *goslack.RichTextSectionUserGroupElement:
			sb.WriteString("<!subteam^" + e.UsergroupID + ">")
		case *goslack.RichTextSectionBroadcastElement:
			sb.WriteString("<!" + e.Range + ">")
		case *goslack.RichTextSectionEmojiElement:
			sb.WriteString(":" + e.Name + ":")
		}
	}
	return sb.String()
}

// appendText appends s when it is not empty.
func appendText(lines []string, s string) []string {
	if s == "" {
		return lines
	}
	return append(lines, s)
}
