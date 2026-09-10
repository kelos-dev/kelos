package sessionruntime

import (
	"context"
	"time"
)

const (
	EventHistoryStart       = "history.start"
	EventHistoryEnd         = "history.end"
	EventPrompts            = "prompts"
	EventRuntimeStatus      = "runtime.status"
	EventRequestAccepted    = "request.accepted"
	EventRuntimeRecovered   = "runtime.recovered"
	EventUserMessage        = "user.message"
	EventUserMessageUpdated = "user.message.updated"
	EventUserMessageRemoved = "user.message.removed"
	EventTurnStarted        = "turn.started"
	EventTurnInterrupting   = "turn.interrupting"
	EventAssistantDelta     = "assistant.delta"
	EventAssistantMessage   = "assistant.message"
	EventToolStarted        = "tool.started"
	EventToolDelta          = "tool.delta"
	EventToolCompleted      = "tool.completed"
	EventGoalUpdated        = "goal.updated"
	EventInputRequested     = "input.requested"
	EventInputResolved      = "input.resolved"
	EventFileDiff           = "file.diff"
	EventTurnCompleted      = "turn.completed"
	EventError              = "error"

	DefaultHistoryItemLimit = 20
	DefaultHistoryByteLimit = 128 * 1024
)

// Event is one conversation event exposed through the shared Session control interface.
type Event struct {
	ID   int64  `json:"id,omitempty"`
	Type string `json:"type"`
	// Timestamp records when a durable conversation event was first appended.
	Timestamp      *time.Time      `json:"timestamp,omitempty"`
	RequestID      string          `json:"requestId,omitempty"`
	TurnID         string          `json:"turnId,omitempty"`
	Text           string          `json:"text,omitempty"`
	Revision       int64           `json:"revision,omitempty"`
	SessionCommand bool            `json:"sessionCommand,omitempty"`
	ToolID         string          `json:"toolId,omitempty"`
	ToolName       string          `json:"toolName,omitempty"`
	Output         string          `json:"output,omitempty"`
	Status         string          `json:"status,omitempty"`
	InputID        string          `json:"inputId,omitempty"`
	Questions      []InputQuestion `json:"questions,omitempty"`
	Diff           string          `json:"diff,omitempty"`
	FirstEventID   int64           `json:"firstEventId,omitempty"`
	LastEventID    int64           `json:"lastEventId,omitempty"`
	JournalID      string          `json:"journalId,omitempty"`
	Reset          bool            `json:"reset,omitempty"`
	HistoryLimited bool            `json:"historyLimited,omitempty"`
	HistoryPage    bool            `json:"historyPage,omitempty"`
	HistoryCursor  string          `json:"historyCursor,omitempty"`
	HistoryState   *HistoryState   `json:"historyState,omitempty"`
	Runtime        *RuntimeStatus  `json:"runtime,omitempty"`
	Goal           *Goal           `json:"goal,omitempty"`
	Attachments    []Attachment    `json:"attachments,omitempty"`
	Prompts        []Prompt        `json:"prompts,omitempty"`
}

// Prompt is a retained user submission, with its latest accepted text.
type Prompt struct {
	ID          int64        `json:"id"`
	TurnID      string       `json:"turnId,omitempty"`
	Text        string       `json:"text"`
	Timestamp   *time.Time   `json:"timestamp,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Goal describes the persisted objective owned by a Codex Session.
type Goal struct {
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget,omitempty"`
	TokensUsed      int64  `json:"tokensUsed,omitempty"`
	TimeUsedSeconds int64  `json:"timeUsedSeconds,omitempty"`
}

// HistoryState describes conversation state that is independent of transcript paging.
type HistoryState struct {
	ActiveTurnID      string              `json:"activeTurnId,omitempty"`
	ActiveTurnStarted *time.Time          `json:"activeTurnStarted,omitempty"`
	TurnInterrupting  bool                `json:"turnInterrupting,omitempty"`
	WaitingForInput   bool                `json:"waitingForInput,omitempty"`
	PendingTurn       *HistoryPendingTurn `json:"pendingTurn,omitempty"`
	FileDiff          string              `json:"fileDiff,omitempty"`
}

// HistoryPendingTurn describes the user message waiting to run.
type HistoryPendingTurn struct {
	TurnID      string       `json:"turnId"`
	Text        string       `json:"text"`
	Revision    int64        `json:"revision"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// RuntimeStatus describes the current Session and workspace for connected clients.
type RuntimeStatus struct {
	SessionName       string            `json:"sessionName,omitempty"`
	AgentType         string            `json:"agentType,omitempty"`
	Model             string            `json:"model,omitempty"`
	Effort            string            `json:"effort,omitempty"`
	WorkingDir        string            `json:"workingDir,omitempty"`
	HomeDir           string            `json:"homeDir,omitempty"`
	Branch            string            `json:"branch,omitempty"`
	PullRequestNumber int               `json:"pullRequestNumber,omitempty"`
	Usage             *RuntimeUsage     `json:"usage,omitempty"`
	WeeklyLimit       *RuntimeRateLimit `json:"weeklyLimit,omitempty"`
}

// RuntimeUsage describes cumulative provider token use and the model context window.
type RuntimeUsage struct {
	InputTokens   int64 `json:"inputTokens"`
	OutputTokens  int64 `json:"outputTokens"`
	TotalTokens   int64 `json:"totalTokens"`
	ContextTokens int64 `json:"contextTokens,omitempty"`
	ContextWindow int64 `json:"contextWindow,omitempty"`
}

// RuntimeRateLimit describes one provider usage window.
type RuntimeRateLimit struct {
	UsedPercent int `json:"usedPercent"`
}

// ClientRequest is a command sent by a web or terminal client.
type ClientRequest struct {
	Type             string              `json:"type"`
	RequestID        string              `json:"requestId,omitempty"`
	Since            int64               `json:"since,omitempty"`
	JournalID        string              `json:"journalId,omitempty"`
	HistoryBounds    bool                `json:"historyBounds,omitempty"`
	HistoryItems     int                 `json:"historyItems,omitempty"`
	HistoryBytes     int                 `json:"historyBytes,omitempty"`
	HistoryCursor    string              `json:"historyCursor,omitempty"`
	TurnID           string              `json:"turnId,omitempty"`
	Text             string              `json:"text,omitempty"`
	ExpectedRevision int64               `json:"expectedRevision,omitempty"`
	AttachmentIDs    []string            `json:"attachmentIds,omitempty"`
	InputID          string              `json:"inputId,omitempty"`
	Answers          map[string][]string `json:"answers,omitempty"`
	Cancel           bool                `json:"cancel,omitempty"`
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
