// Package logview turns Claude Code's NDJSON stream into events and renders
// them in a Claude Code-like style. Other agents can use the same events.
package logview

// Event is one renderable item of an agent's log stream.
type Event interface {
	isEvent()
}

// Init reports the model the agent started with.
type Init struct {
	Model string
}

// Text is assistant prose. Delta marks a streamed fragment that continues
// the previous Text instead of starting a new message.
type Text struct {
	Text  string
	Delta bool
	// Nested marks output of a sub-agent rather than the main agent.
	Nested bool
}

// Thinking is the agent's reasoning, when the agent emits it.
type Thinking struct {
	Text   string
	Nested bool
}

// ToolCall is the agent invoking a tool.
type ToolCall struct {
	ID      string
	Name    string
	Summary string
	// Diff holds the edit for file-editing tools whose input carries the
	// replaced and replacement text.
	Diff []Hunk
	// WriteLines is the number of lines a file-writing tool wrote.
	WriteLines int
	Todos      []Todo
	Nested     bool
}

// ToolResult is the outcome of a ToolCall, matched by ID when the agent
// provides one.
type ToolResult struct {
	ID      string
	Output  string
	IsError bool
	// Diff, when set, supersedes the diff of the matching ToolCall; Claude
	// reports it with real file line numbers.
	Diff   []Hunk
	Nested bool
}

// Todo is one entry of an agent's todo list.
type Todo struct {
	Text   string
	Status TodoStatus
}

// TodoStatus is the progress of a Todo.
type TodoStatus string

// Todo statuses.
const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

// Error is an error the agent or its runtime reported.
type Error struct {
	Message string
}

// Result summarizes a finished run or turn.
type Result struct {
	Status  ResultStatus
	Detail  string
	Turns   int
	CostUSD float64
	// InputTokens counts all input tokens, CachedTokens the part of them
	// read from the prompt cache.
	InputTokens  int
	CachedTokens int
	OutputTokens int
	DurationMS   int64
	// Message is the agent's final error text, shown for failed runs.
	Message string
}

// ResultStatus is how a run ended.
type ResultStatus string

// Result statuses.
const (
	ResultCompleted  ResultStatus = "completed"
	ResultIncomplete ResultStatus = "incomplete"
	ResultError      ResultStatus = "error"
)

// Raw is a log line that is not agent JSON; it is passed through as-is.
type Raw struct {
	Line string
}

func (Init) isEvent()       {}
func (Text) isEvent()       {}
func (Thinking) isEvent()   {}
func (ToolCall) isEvent()   {}
func (ToolResult) isEvent() {}
func (Error) isEvent()      {}
func (Result) isEvent()     {}
func (Raw) isEvent()        {}

// valueEvent normalizes pointers accepted by Event's value-receiver methods.
func valueEvent(event Event) Event {
	switch e := event.(type) {
	case *Init:
		return derefEvent(e)
	case *Text:
		return derefEvent(e)
	case *Thinking:
		return derefEvent(e)
	case *ToolCall:
		return derefEvent(e)
	case *ToolResult:
		return derefEvent(e)
	case *Error:
		return derefEvent(e)
	case *Result:
		return derefEvent(e)
	case *Raw:
		return derefEvent(e)
	default:
		return event
	}
}

func derefEvent[T Event](event *T) Event {
	if event == nil {
		return nil
	}
	return *event
}
