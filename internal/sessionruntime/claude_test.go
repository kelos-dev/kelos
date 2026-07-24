package sessionruntime

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestClaudeProviderClosesEachTextBlock verifies that a streamed turn with two
// text blocks emits an assistant.message after each block's deltas, so clients
// render them as separate bubbles instead of concatenating the whole turn.
func TestClaudeProviderClosesEachTextBlock(t *testing.T) {
	provider := &ClaudeProvider{
		blockText: map[int]string{},
		seenTools: map[string]struct{}{},
	}
	sink := newOpenCodeTestSink(nil)

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Second block"}}`,
		`{"type":"content_block_stop","index":1}`,
	}
	for _, raw := range events {
		provider.emitClaudeStreamEvent(json.RawMessage(raw), sink)
	}

	want := []Event{
		{Type: EventAssistantDelta, Text: "Hello"},
		{Type: EventAssistantDelta, Text: " there"},
		{Type: EventAssistantMessage, Text: "Hello there"},
		{Type: EventAssistantDelta, Text: "Second block"},
		{Type: EventAssistantMessage, Text: "Second block"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}

// TestClaudeProviderStreamedToolBlockEmitsNoMessage verifies that closing a
// non-text content block (a tool_use) does not emit an empty assistant.message.
func TestClaudeProviderStreamedToolBlockEmitsNoMessage(t *testing.T) {
	provider := &ClaudeProvider{
		blockText: map[int]string{},
		seenTools: map[string]struct{}{},
	}
	sink := newOpenCodeTestSink(nil)

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"Bash"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, raw := range events {
		provider.emitClaudeStreamEvent(json.RawMessage(raw), sink)
	}

	want := []Event{
		{Type: EventToolStarted, ToolID: "tool-1", ToolName: "Bash", Status: "running"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}

// TestClaudeProviderEmitsMessageWithoutStreaming verifies that when no text
// deltas were streamed, the assembled assistant message still emits one
// assistant.message per text block.
func TestClaudeProviderEmitsMessageWithoutStreaming(t *testing.T) {
	provider := &ClaudeProvider{seenTools: map[string]struct{}{}}
	sink := newOpenCodeTestSink(nil)

	message := `{"content":[{"type":"text","text":"First"},{"type":"text","text":"Second"}]}`
	provider.emitClaudeMessage("assistant", json.RawMessage(message), sink)

	want := []Event{
		{Type: EventAssistantMessage, Text: "First"},
		{Type: EventAssistantMessage, Text: "Second"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}

// TestClaudeProviderStreamingSuppressesAssembledMessage verifies that once text
// deltas have streamed (and been closed per block), the assembled assistant
// message does not re-emit the same text as duplicate assistant.message events.
func TestClaudeProviderStreamingSuppressesAssembledMessage(t *testing.T) {
	provider := &ClaudeProvider{
		blockText: map[int]string{},
		seenTools: map[string]struct{}{},
	}
	sink := newOpenCodeTestSink(nil)

	stream := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Streamed"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, raw := range stream {
		provider.emitClaudeStreamEvent(json.RawMessage(raw), sink)
	}
	provider.emitClaudeMessage("assistant", json.RawMessage(`{"content":[{"type":"text","text":"Streamed"}]}`), sink)

	want := []Event{
		{Type: EventAssistantDelta, Text: "Streamed"},
		{Type: EventAssistantMessage, Text: "Streamed"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude events = %#v, want %#v", got, want)
	}
}
