package logview

import (
	"encoding/json"

	"github.com/kelos-dev/kelos/internal/claudecode"
)

// claudeEvent is one line of claude --output-format stream-json.
type claudeEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	Model           string          `json:"model"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         *claudeMessage  `json:"message"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	Result          string          `json:"result"`
	IsError         bool            `json:"is_error"`
	StopReason      string          `json:"stop_reason"`
	TerminalReason  string          `json:"terminal_reason"`
	NumTurns        int             `json:"num_turns"`
	TotalCostUSD    float64         `json:"total_cost_usd"`
	DurationMS      int64           `json:"duration_ms"`
	Usage           *claudeUsage    `json:"usage"`
}

type claudeMessage struct {
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// claudeUsage splits input tokens into uncached, cache-read and
// cache-write counts.
type claudeUsage struct {
	InputTokens      int `json:"input_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

// claudeToolUseResult is the part of a user event's tool_use_result that
// carries Claude Code's own diff of a file edit.
type claudeToolUseResult struct {
	StructuredPatch json.RawMessage `json:"structuredPatch"`
}

// parseClaudeLine handles one stream-json line, reporting false when it is
// not JSON.
func parseClaudeLine(line []byte, emit func(Event)) bool {
	var event claudeEvent
	if json.Unmarshal(line, &event) != nil {
		return false
	}
	nested := event.ParentToolUseID != nil && *event.ParentToolUseID != ""
	switch event.Type {
	case "system":
		if event.Subtype == "init" && event.Model != "" {
			emit(Init{Model: event.Model})
		}
	case "assistant":
		emitClaudeAssistant(event.Message, nested, emit)
	case "user":
		emitClaudeToolResults(event, nested, emit)
	case "result":
		emit(claudeResult(event))
	}
	return true
}

func emitClaudeAssistant(message *claudeMessage, nested bool, emit func(Event)) {
	if message == nil {
		return
	}
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				emit(Text{Text: block.Text, Nested: nested})
			}
		case "thinking":
			if block.Thinking != "" {
				emit(Thinking{Text: block.Thinking, Nested: nested})
			}
		case "tool_use":
			call := newToolCall(block.ID, block.Name, block.Input)
			call.Nested = nested
			emit(call)
		}
	}
}

func emitClaudeToolResults(event claudeEvent, nested bool, emit func(Event)) {
	if event.Message == nil {
		return
	}
	var results []claudeBlock
	for _, block := range event.Message.Content {
		if block.Type == "tool_result" {
			results = append(results, block)
		}
	}
	for _, block := range results {
		result := ToolResult{
			ID:      block.ToolUseID,
			Output:  contentText(block.Content),
			IsError: block.IsError,
			Nested:  nested,
		}
		// tool_use_result describes the event as a whole, so it can only
		// be attributed when the event carries a single result.
		if len(results) == 1 {
			result.Diff = claudePatch(event.ToolUseResult)
		}
		emit(result)
	}
}

func claudePatch(raw json.RawMessage) []Hunk {
	var result claudeToolUseResult
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return parseStructuredPatch(result.StructuredPatch)
}

func claudeResult(event claudeEvent) Result {
	completion := claudecode.Result{
		Subtype:        event.Subtype,
		IsError:        event.IsError,
		StopReason:     event.StopReason,
		TerminalReason: event.TerminalReason,
		Text:           event.Result,
		NumTurns:       event.NumTurns,
	}
	result := Result{
		Status:     ResultStatus(completion.Status()),
		Turns:      event.NumTurns,
		CostUSD:    event.TotalCostUSD,
		DurationMS: event.DurationMS,
	}
	if result.Status != ResultCompleted {
		result.Detail = completion.Details()
	}
	if result.Status == ResultError {
		result.Message = event.Result
	}
	if event.Usage != nil {
		result.InputTokens = event.Usage.InputTokens + event.Usage.CacheReadTokens + event.Usage.CacheWriteTokens
		result.CachedTokens = event.Usage.CacheReadTokens
		result.OutputTokens = event.Usage.OutputTokens
	}
	return result
}
