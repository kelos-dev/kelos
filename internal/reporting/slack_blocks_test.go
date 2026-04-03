package reporting

import (
	"testing"

	"github.com/slack-go/slack"
)

func TestParseMarkdownSegments(t *testing.T) {
	t.Run("plain text only", func(t *testing.T) {
		segs := parseMarkdownSegments("Hello world\nSecond line")
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segPlain {
			t.Errorf("expected segPlain, got %d", segs[0].typ)
		}
	})

	t.Run("header", func(t *testing.T) {
		segs := parseMarkdownSegments("# My Header")
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segHeader {
			t.Errorf("expected segHeader, got %d", segs[0].typ)
		}
		if segs[0].lines[0] != "My Header" {
			t.Errorf("expected header text %q, got %q", "My Header", segs[0].lines[0])
		}
		if segs[0].level != 1 {
			t.Errorf("expected level 1, got %d", segs[0].level)
		}
	})

	t.Run("table", func(t *testing.T) {
		input := "| Name | Age |\n| --- | --- |\n| Alice | 30 |\n| Bob | 25 |"
		segs := parseMarkdownSegments(input)
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segTable {
			t.Errorf("expected segTable, got %d", segs[0].typ)
		}
		// Header + 2 data rows (separator is excluded).
		if len(segs[0].lines) != 3 {
			t.Errorf("expected 3 table lines, got %d", len(segs[0].lines))
		}
	})

	t.Run("unordered list", func(t *testing.T) {
		input := "- item one\n- item two\n- item three"
		segs := parseMarkdownSegments(input)
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segList {
			t.Errorf("expected segList, got %d", segs[0].typ)
		}
	})

	t.Run("ordered list", func(t *testing.T) {
		input := "1. first\n2. second\n3. third"
		segs := parseMarkdownSegments(input)
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segList {
			t.Errorf("expected segList, got %d", segs[0].typ)
		}
	})

	t.Run("divider", func(t *testing.T) {
		for _, input := range []string{"---", "***", "___", "-----", "  ---  "} {
			segs := parseMarkdownSegments(input)
			if len(segs) != 1 {
				t.Fatalf("input %q: expected 1 segment, got %d", input, len(segs))
			}
			if segs[0].typ != segDivider {
				t.Errorf("input %q: expected segDivider, got %d", input, segs[0].typ)
			}
		}
	})

	t.Run("ordered list with blank lines", func(t *testing.T) {
		input := "1. first\n\n2. second\n\n3. third"
		segs := parseMarkdownSegments(input)
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segList {
			t.Errorf("expected segList, got %d", segs[0].typ)
		}
		if len(segs[0].lines) != 3 {
			t.Errorf("expected 3 list lines, got %d", len(segs[0].lines))
		}
	})

	t.Run("unordered list with blank lines", func(t *testing.T) {
		input := "- first\n\n- second\n\n- third"
		segs := parseMarkdownSegments(input)
		if len(segs) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segs))
		}
		if segs[0].typ != segList {
			t.Errorf("expected segList, got %d", segs[0].typ)
		}
		if len(segs[0].lines) != 3 {
			t.Errorf("expected 3 list lines, got %d", len(segs[0].lines))
		}
	})

	t.Run("divider between content", func(t *testing.T) {
		input := "Some text\n---\nMore text"
		segs := parseMarkdownSegments(input)
		if len(segs) != 3 {
			t.Fatalf("expected 3 segments, got %d", len(segs))
		}
		if segs[0].typ != segPlain {
			t.Errorf("segment 0: expected segPlain, got %d", segs[0].typ)
		}
		if segs[1].typ != segDivider {
			t.Errorf("segment 1: expected segDivider, got %d", segs[1].typ)
		}
		if segs[2].typ != segPlain {
			t.Errorf("segment 2: expected segPlain, got %d", segs[2].typ)
		}
	})

	t.Run("mixed content", func(t *testing.T) {
		input := "## Summary\nSome text here.\n\n| Col1 | Col2 |\n| --- | --- |\n| a | b |\n\n- item 1\n- item 2"
		segs := parseMarkdownSegments(input)
		if len(segs) < 4 {
			t.Fatalf("expected at least 4 segments, got %d", len(segs))
		}
		if segs[0].typ != segHeader {
			t.Errorf("segment 0: expected segHeader, got %d", segs[0].typ)
		}
		if segs[1].typ != segPlain {
			t.Errorf("segment 1: expected segPlain, got %d", segs[1].typ)
		}
		if segs[2].typ != segTable {
			t.Errorf("segment 2: expected segTable, got %d", segs[2].typ)
		}
		if segs[3].typ != segList {
			t.Errorf("segment 3: expected segList, got %d", segs[3].typ)
		}
	})
}

func TestResponseToBlocks(t *testing.T) {
	t.Run("plain text becomes SectionBlock", func(t *testing.T) {
		blocks := responseToBlocks("Hello world")
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		section, ok := blocks[0].(*slack.SectionBlock)
		if !ok {
			t.Fatalf("expected *SectionBlock, got %T", blocks[0])
		}
		if section.Text.Text != "Hello world" {
			t.Errorf("text = %q, want %q", section.Text.Text, "Hello world")
		}
	})

	t.Run("header becomes HeaderBlock", func(t *testing.T) {
		blocks := responseToBlocks("# My Title")
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		header, ok := blocks[0].(*slack.HeaderBlock)
		if !ok {
			t.Fatalf("expected *HeaderBlock, got %T", blocks[0])
		}
		if header.Text.Text != "My Title" {
			t.Errorf("text = %q, want %q", header.Text.Text, "My Title")
		}
	})

	t.Run("table becomes TableBlock", func(t *testing.T) {
		input := "| Name | Age |\n| --- | --- |\n| Alice | 30 |"
		blocks := responseToBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		table, ok := blocks[0].(*slack.TableBlock)
		if !ok {
			t.Fatalf("expected *TableBlock, got %T", blocks[0])
		}
		if len(table.Rows) != 2 {
			t.Errorf("expected 2 rows (header + 1 data), got %d", len(table.Rows))
		}
	})

	t.Run("unordered list becomes RichTextBlock", func(t *testing.T) {
		input := "- apple\n- banana\n- cherry"
		blocks := responseToBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		rt, ok := blocks[0].(*slack.RichTextBlock)
		if !ok {
			t.Fatalf("expected *RichTextBlock, got %T", blocks[0])
		}
		if len(rt.Elements) != 1 {
			t.Fatalf("expected 1 element, got %d", len(rt.Elements))
		}
		list, ok := rt.Elements[0].(*slack.RichTextList)
		if !ok {
			t.Fatalf("expected *RichTextList, got %T", rt.Elements[0])
		}
		if list.Style != slack.RTEListBullet {
			t.Errorf("expected bullet style, got %q", list.Style)
		}
		if len(list.Elements) != 3 {
			t.Errorf("expected 3 list items, got %d", len(list.Elements))
		}
	})

	t.Run("ordered list becomes RichTextBlock", func(t *testing.T) {
		input := "1. first\n2. second"
		blocks := responseToBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		rt, ok := blocks[0].(*slack.RichTextBlock)
		if !ok {
			t.Fatalf("expected *RichTextBlock, got %T", blocks[0])
		}
		list := rt.Elements[0].(*slack.RichTextList)
		if list.Style != slack.RTEListOrdered {
			t.Errorf("expected ordered style, got %q", list.Style)
		}
	})

	t.Run("mixed content produces correct block types", func(t *testing.T) {
		input := "## Report\nHere are the results:\n\n| Name | Score |\n| --- | --- |\n| Alice | 95 |\n\n- Note 1\n- Note 2"
		blocks := responseToBlocks(input)
		if len(blocks) < 4 {
			t.Fatalf("expected at least 4 blocks, got %d", len(blocks))
		}
		if _, ok := blocks[0].(*slack.HeaderBlock); !ok {
			t.Errorf("block 0: expected *HeaderBlock, got %T", blocks[0])
		}
		if _, ok := blocks[1].(*slack.SectionBlock); !ok {
			t.Errorf("block 1: expected *SectionBlock, got %T", blocks[1])
		}
		if _, ok := blocks[2].(*slack.TableBlock); !ok {
			t.Errorf("block 2: expected *TableBlock, got %T", blocks[2])
		}
		if _, ok := blocks[3].(*slack.RichTextBlock); !ok {
			t.Errorf("block 3: expected *RichTextBlock, got %T", blocks[3])
		}
	})

	t.Run("empty string produces no blocks", func(t *testing.T) {
		blocks := responseToBlocks("")
		if len(blocks) != 0 {
			t.Errorf("expected 0 blocks, got %d", len(blocks))
		}
	})

	t.Run("divider becomes DividerBlock", func(t *testing.T) {
		blocks := responseToBlocks("Text above\n---\nText below")
		if len(blocks) != 3 {
			t.Fatalf("expected 3 blocks, got %d", len(blocks))
		}
		if _, ok := blocks[1].(*slack.DividerBlock); !ok {
			t.Errorf("block 1: expected *DividerBlock, got %T", blocks[1])
		}
	})

	t.Run("header strips backtick code spans", func(t *testing.T) {
		blocks := responseToBlocks("# The `start_data_complete_jobs` function")
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		header, ok := blocks[0].(*slack.HeaderBlock)
		if !ok {
			t.Fatalf("expected *HeaderBlock, got %T", blocks[0])
		}
		want := "The start_data_complete_jobs function"
		if header.Text.Text != want {
			t.Errorf("text = %q, want %q", header.Text.Text, want)
		}
	})

	t.Run("list items with inline code", func(t *testing.T) {
		input := "- Run `make test` first\n- Then `make build`"
		blocks := responseToBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		rt, ok := blocks[0].(*slack.RichTextBlock)
		if !ok {
			t.Fatalf("expected *RichTextBlock, got %T", blocks[0])
		}
		list, ok := rt.Elements[0].(*slack.RichTextList)
		if !ok {
			t.Fatalf("expected *RichTextList, got %T", rt.Elements[0])
		}
		// First list item should have 3 elements: "Run ", "make test" (code), " first"
		section, ok := list.Elements[0].(*slack.RichTextSection)
		if !ok {
			t.Fatalf("expected *RichTextSection, got %T", list.Elements[0])
		}
		if len(section.Elements) != 3 {
			t.Fatalf("expected 3 elements in first item, got %d", len(section.Elements))
		}
		codeElem, ok := section.Elements[1].(*slack.RichTextSectionTextElement)
		if !ok {
			t.Fatalf("expected *RichTextSectionTextElement, got %T", section.Elements[1])
		}
		if codeElem.Style == nil || !codeElem.Style.Code {
			t.Error("expected code style on middle element")
		}
		if codeElem.Text != "make test" {
			t.Errorf("code text = %q, want %q", codeElem.Text, "make test")
		}
	})

	t.Run("asterisk list items", func(t *testing.T) {
		input := "* foo\n* bar"
		blocks := responseToBlocks(input)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		if _, ok := blocks[0].(*slack.RichTextBlock); !ok {
			t.Errorf("expected *RichTextBlock, got %T", blocks[0])
		}
	})
}

func TestParseRichTextElements(t *testing.T) {
	t.Run("no code spans", func(t *testing.T) {
		elems := parseRichTextElements("plain text")
		if len(elems) != 1 {
			t.Fatalf("expected 1 element, got %d", len(elems))
		}
	})

	t.Run("code span in middle", func(t *testing.T) {
		elems := parseRichTextElements("run `make test` now")
		if len(elems) != 3 {
			t.Fatalf("expected 3 elements, got %d", len(elems))
		}
		te, ok := elems[0].(*slack.RichTextSectionTextElement)
		if !ok {
			t.Fatalf("expected text element, got %T", elems[0])
		}
		if te.Text != "run " {
			t.Errorf("elem 0 text = %q, want %q", te.Text, "run ")
		}
		code, ok := elems[1].(*slack.RichTextSectionTextElement)
		if !ok {
			t.Fatalf("expected text element, got %T", elems[1])
		}
		if code.Text != "make test" {
			t.Errorf("elem 1 text = %q, want %q", code.Text, "make test")
		}
		if code.Style == nil || !code.Style.Code {
			t.Error("expected code style on elem 1")
		}
	})

	t.Run("multiple code spans", func(t *testing.T) {
		elems := parseRichTextElements("`foo` and `bar`")
		if len(elems) != 3 {
			t.Fatalf("expected 3 elements, got %d", len(elems))
		}
		// First should be code
		first, _ := elems[0].(*slack.RichTextSectionTextElement)
		if first.Style == nil || !first.Style.Code {
			t.Error("expected code style on first element")
		}
		// Middle should be plain text " and "
		mid, _ := elems[1].(*slack.RichTextSectionTextElement)
		if mid.Text != " and " {
			t.Errorf("middle text = %q, want %q", mid.Text, " and ")
		}
	})
}

func TestParseCells(t *testing.T) {
	cells := parseCells("| Alice | 30 | Engineer |")
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells, got %d", len(cells))
	}
	if cells[0] != "Alice" {
		t.Errorf("cell 0 = %q, want %q", cells[0], "Alice")
	}
	if cells[1] != "30" {
		t.Errorf("cell 1 = %q, want %q", cells[1], "30")
	}
	if cells[2] != "Engineer" {
		t.Errorf("cell 2 = %q, want %q", cells[2], "Engineer")
	}
}
