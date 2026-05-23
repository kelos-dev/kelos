package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AgentTurnPhase represents the lifecycle state of an AgentTurn.
type AgentTurnPhase string

const (
	AgentTurnPhaseQueued    AgentTurnPhase = "Queued"
	AgentTurnPhaseRunning   AgentTurnPhase = "Running"
	AgentTurnPhaseSucceeded AgentTurnPhase = "Succeeded"
	AgentTurnPhaseFailed    AgentTurnPhase = "Failed"
	AgentTurnPhaseCanceled  AgentTurnPhase = "Canceled"
)

// AgentTurnSource identifies the external message that created a turn.
type AgentTurnSource struct {
	// Type identifies the source shape. The first implementation supports
	// SlackMessage.
	Type string `json:"type"`

	// TeamID is the Slack workspace/team ID when available.
	// +optional
	TeamID string `json:"teamID,omitempty"`

	// ChannelID is the Slack channel ID.
	ChannelID string `json:"channelID"`

	// RootTS is the Slack root thread timestamp.
	RootTS string `json:"rootTS"`

	// MessageTS is the Slack timestamp of the explicit mention.
	MessageTS string `json:"messageTS"`

	// UserID is the Slack user ID of the message author.
	// +optional
	UserID string `json:"userID,omitempty"`

	// BotID is the Slack bot ID when the author is a bot.
	// +optional
	BotID string `json:"botID,omitempty"`

	// Permalink is a Slack permalink to the message.
	// +optional
	Permalink string `json:"permalink,omitempty"`
}

// AgentTurnInput is the explicit request that created a turn.
type AgentTurnInput struct {
	// Text is the raw Slack message text.
	Text string `json:"text"`

	// Body is the semantic request body.
	Body string `json:"body"`
}

// AgentTurnContext is the Slack context materialized for a turn.
type AgentTurnContext struct {
	// Mode records how the transcript window was selected.
	Mode SlackSessionContextWindow `json:"mode"`

	// FromTSExclusive is the lower Slack timestamp bound.
	// +optional
	FromTSExclusive string `json:"fromTSExclusive,omitempty"`

	// ToTSInclusive is the upper Slack timestamp bound.
	ToTSInclusive string `json:"toTSInclusive"`

	// Transcript contains the side conversation delta for this turn.
	// +optional
	Transcript string `json:"transcript,omitempty"`

	// TranscriptBytes is the size of Transcript in bytes.
	// +optional
	TranscriptBytes int32 `json:"transcriptBytes,omitempty"`
}

// AgentTurnSpec defines desired AgentTurn state.
type AgentTurnSpec struct {
	// SessionRef identifies the AgentSession this turn belongs to.
	SessionRef AgentSessionReference `json:"sessionRef"`

	// Sequence is the FIFO sequence number within the session.
	Sequence int32 `json:"sequence"`

	// Source identifies the external message that created this turn.
	Source AgentTurnSource `json:"source"`

	// Input is the explicit request.
	Input AgentTurnInput `json:"input"`

	// Context is the materialized side-conversation delta.
	Context AgentTurnContext `json:"context"`
}

// AgentTurnStatus defines observed AgentTurn state.
type AgentTurnStatus struct {
	// Phase represents the current turn phase.
	// +optional
	Phase AgentTurnPhase `json:"phase,omitempty"`

	// StartedAt is when the runner began the turn.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the turn reached a terminal phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// CodexTurnID is the Codex App Server turn ID.
	// +optional
	CodexTurnID string `json:"codexTurnID,omitempty"`

	// SlackProgressMessageTS is the Slack timestamp of the progress message.
	// +optional
	SlackProgressMessageTS string `json:"slackProgressMessageTS,omitempty"`

	// SlackAgentMessageTS is the Slack timestamp of Cody's terminal response.
	// +optional
	SlackAgentMessageTS string `json:"slackAgentMessageTS,omitempty"`

	// ResultText is the terminal Cody response text.
	// +optional
	ResultText string `json:"resultText,omitempty"`

	// Activity is a short live activity message.
	// +optional
	Activity string `json:"activity,omitempty"`

	// Message provides additional status detail.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions provides detailed status information.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Session",type=string,JSONPath=`.spec.sessionRef.name`
// +kubebuilder:printcolumn:name="Seq",type=integer,JSONPath=`.spec.sequence`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentTurn is the Schema for explicit turns inside an AgentSession.
type AgentTurn struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentTurnSpec   `json:"spec,omitempty"`
	Status AgentTurnStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentTurnList contains a list of AgentTurn.
type AgentTurnList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentTurn `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentTurn{}, &AgentTurnList{})
}
