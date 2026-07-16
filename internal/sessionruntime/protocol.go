package sessionruntime

import "context"

const (
	EventHistoryStart     = "history.start"
	EventHistoryEnd       = "history.end"
	EventRequestAccepted  = "request.accepted"
	EventRuntimeRecovered = "runtime.recovered"
	EventUserMessage      = "user.message"
	EventTurnStarted      = "turn.started"
	EventTurnInterrupting = "turn.interrupting"
	EventAssistantDelta   = "assistant.delta"
	EventAssistantMessage = "assistant.message"
	EventToolStarted      = "tool.started"
	EventToolCompleted    = "tool.completed"
	EventInputRequested   = "input.requested"
	EventInputResolved    = "input.resolved"
	EventFileDiff         = "file.diff"
	EventTurnCompleted    = "turn.completed"
	EventError            = "error"
)

// Event is one conversation event exposed through the shared Session control interface.
type Event struct {
	ID           int64           `json:"id,omitempty"`
	Type         string          `json:"type"`
	RequestID    string          `json:"requestId,omitempty"`
	TurnID       string          `json:"turnId,omitempty"`
	Text         string          `json:"text,omitempty"`
	ToolID       string          `json:"toolId,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	Status       string          `json:"status,omitempty"`
	InputID      string          `json:"inputId,omitempty"`
	Questions    []InputQuestion `json:"questions,omitempty"`
	Diff         string          `json:"diff,omitempty"`
	FirstEventID int64           `json:"firstEventId,omitempty"`
	LastEventID  int64           `json:"lastEventId,omitempty"`
	JournalID    string          `json:"journalId,omitempty"`
	Reset        bool            `json:"reset,omitempty"`
}

// ClientRequest is a command sent by a web or terminal client.
type ClientRequest struct {
	Type          string              `json:"type"`
	RequestID     string              `json:"requestId,omitempty"`
	Since         int64               `json:"since,omitempty"`
	JournalID     string              `json:"journalId,omitempty"`
	HistoryBounds bool                `json:"historyBounds,omitempty"`
	Text          string              `json:"text,omitempty"`
	InputID       string              `json:"inputId,omitempty"`
	Answers       map[string][]string `json:"answers,omitempty"`
	Cancel        bool                `json:"cancel,omitempty"`
}

// InputOption describes one structured answer offered by a provider.
type InputOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// InputQuestion describes one question that blocks the active provider turn.
type InputQuestion struct {
	ID          string        `json:"id"`
	Header      string        `json:"header,omitempty"`
	Question    string        `json:"question"`
	Options     []InputOption `json:"options,omitempty"`
	MultiSelect bool          `json:"multiSelect,omitempty"`
	Secret      bool          `json:"secret,omitempty"`
}

// InputRequest asks a Session client to answer provider questions.
type InputRequest struct {
	ID        string
	Questions []InputQuestion
}

// EventSink receives provider events for the active turn.
type EventSink interface {
	Emit(Event)
	RequestInput(ctx context.Context, request InputRequest) (map[string][]string, error)
}
