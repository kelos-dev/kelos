package slack

import (
	"encoding/json"
	"testing"

	goslack "github.com/slack-go/slack"
)

func TestBlocksText(t *testing.T) {
	tests := []struct {
		name     string
		blocks   string
		wantText string
		wantOK   bool
	}{
		{
			name: "section, header, context and divider",
			blocks: `[{"type":"header","text":{"type":"plain_text","text":"Plan"}},` +
				`{"type":"section","text":{"type":"mrkdwn","text":"Two tickets."},"fields":[{"type":"mrkdwn","text":"*Owner:* Ada"}]},` +
				`{"type":"divider"},` +
				`{"type":"context","elements":[{"type":"image","image_url":"https://example.com/i.png","alt_text":"icon"},{"type":"mrkdwn","text":"Task: planner-1"}]}]`,
			wantText: "Plan\nTwo tickets.\n*Owner:* Ada\nTask: planner-1",
			wantOK:   true,
		},
		{
			name: "ordered list with offset and a nested bullet list",
			blocks: `[{"type":"rich_text","elements":[` +
				`{"type":"rich_text_list","style":"ordered","offset":2,"elements":[` +
				`{"type":"rich_text_section","elements":[{"type":"text","text":"Ticket C"}]},` +
				`{"type":"rich_text_section","elements":[{"type":"text","text":"Ticket D"}]}]},` +
				`{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` +
				`{"type":"rich_text_section","elements":[{"type":"text","text":"subtask"}]}]}]}]`,
			wantText: "3. Ticket C\n4. Ticket D\n  - subtask",
			wantOK:   true,
		},
		{
			name: "rich text sections, quotes, code and inline elements",
			blocks: `[{"type":"rich_text","elements":[` +
				`{"type":"rich_text_section","elements":[{"type":"text","text":"Ping "},{"type":"user","user_id":"U1"},` +
				`{"type":"text","text":" in "},{"type":"channel","channel_id":"C1"},{"type":"text","text":" "},` +
				`{"type":"emoji","name":"rocket"},{"type":"text","text":" "},{"type":"link","url":"https://example.com","text":"docs"},` +
				`{"type":"text","text":" "},{"type":"link","url":"https://example.com/raw"},{"type":"text","text":"\n"}]},` +
				`{"type":"rich_text_quote","elements":[{"type":"text","text":"first\nsecond"}]},` +
				`{"type":"rich_text_preformatted","elements":[{"type":"text","text":"go test ./..."}]}]}]`,
			wantText: "Ping <@U1> in <#C1> :rocket: docs https://example.com/raw\n> first\n> second\ngo test ./...",
			wantOK:   true,
		},
		{
			name: "table rows",
			blocks: `[{"type":"table","rows":[` +
				`[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Ticket"}]}]},` +
				`{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Owner"}]}]}],` +
				`[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"A"}]}]},` +
				`{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Ada"}]}]}]]}]`,
			wantText: "Ticket | Owner\nA | Ada",
			wantOK:   true,
		},
		{
			name:   "unread block type",
			blocks: `[{"type":"section","text":{"type":"mrkdwn","text":"Approve?"}},{"type":"actions","elements":[]}]`,
			wantOK: false,
		},
		{
			name:   "unread rich text element type",
			blocks: `[{"type":"rich_text","elements":[{"type":"rich_text_future","elements":[]}]}]`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var blocks goslack.Blocks
			if err := json.Unmarshal([]byte(tt.blocks), &blocks); err != nil {
				t.Fatalf("Unmarshal blocks: %v", err)
			}
			text, ok := blocksText(blocks.BlockSet)
			if ok != tt.wantOK {
				t.Fatalf("blocksText ok = %v, want %v (text %q)", ok, tt.wantOK, text)
			}
			if ok && text != tt.wantText {
				t.Errorf("blocksText text = %q, want %q", text, tt.wantText)
			}
		})
	}
}
