package logview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestContentTextJoinsTextBlocks(t *testing.T) {
	input := json.RawMessage(`[{"type":"text","text":"first"},{"type":"image","text":"ignored"},{"type":"text","text":""},{"type":"text","text":"last"}]`)
	if got, want := contentText(input), "first\n\nlast"; got != want {
		t.Errorf("contentText = %q, want %q", got, want)
	}
}

func collect(t *testing.T, input string) []Event {
	t.Helper()
	var events []Event
	if err := Parse(strings.NewReader(input), func(e Event) { events = append(events, e) }); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return events
}

func TestParsePassesNonJSONThrough(t *testing.T) {
	events := collect(t, "plain line\n\n")
	if want := []Event{Raw{Line: "plain line"}}; !reflect.DeepEqual(events, want) {
		t.Errorf("got %+v, want %+v", events, want)
	}
}

func TestParseClaudeAttributesPatchOnlyToSingleResult(t *testing.T) {
	patch := `"tool_use_result":{"structuredPatch":[{"oldStart":1,"newStart":1,"lines":["+a"]}]}`
	single := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]},` + patch + `}`
	double := `{"type":"user","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"a","content":"ok"},` +
		`{"type":"tool_result","tool_use_id":"b","content":"ok"}]},` + patch + `}`

	if got := collect(t, single); len(got) != 1 || len(got[0].(ToolResult).Diff) != 1 {
		t.Errorf("single result: got %+v, want the patch attached", got)
	}
	results := collect(t, double)
	if len(results) != 2 {
		t.Fatalf("two results: got %+v, want two tool results", results)
	}
	for _, event := range results {
		result, ok := event.(ToolResult)
		if !ok {
			t.Fatalf("two results: got event %T, want ToolResult", event)
		}
		if result.Diff != nil {
			t.Errorf("two results: patch attached to %+v", result)
		}
	}
}

func TestParseClaudeMarksSubAgentEvents(t *testing.T) {
	input := `{"type":"assistant","parent_tool_use_id":"t1","message":{"content":[{"type":"text","text":"hi"}]}}`
	if got := collect(t, input); !reflect.DeepEqual(got, []Event{Text{Text: "hi", Nested: true}}) {
		t.Errorf("got %+v, want a nested Text", got)
	}
}
