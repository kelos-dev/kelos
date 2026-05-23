package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AgentSessionPhase represents the lifecycle state of an AgentSession.
type AgentSessionPhase string

const (
	AgentSessionPhasePending  AgentSessionPhase = "Pending"
	AgentSessionPhaseStarting AgentSessionPhase = "Starting"
	AgentSessionPhaseIdle     AgentSessionPhase = "Idle"
	AgentSessionPhaseRunning  AgentSessionPhase = "Running"
	AgentSessionPhaseClosing  AgentSessionPhase = "Closing"
	AgentSessionPhaseClosed   AgentSessionPhase = "Closed"
	AgentSessionPhaseError    AgentSessionPhase = "Error"
)

// TaskSpawnerReference refers to the TaskSpawner that owns a session.
type TaskSpawnerReference struct {
	// Name is the name of the TaskSpawner resource.
	Name string `json:"name"`
}

// AgentSessionReference refers to an AgentSession by name.
type AgentSessionReference struct {
	// Name is the name of the AgentSession resource.
	Name string `json:"name"`
}

// AgentSessionSource identifies the external conversation backing a session.
type AgentSessionSource struct {
	// Type identifies the source shape. The first implementation supports
	// SlackThread.
	Type string `json:"type"`

	// TeamID is the Slack workspace/team ID when available.
	// +optional
	TeamID string `json:"teamID,omitempty"`

	// ChannelID is the Slack channel ID.
	ChannelID string `json:"channelID"`

	// RootTS is the Slack root thread timestamp.
	RootTS string `json:"rootTS"`

	// ThreadURL is a Slack permalink to the thread.
	// +optional
	ThreadURL string `json:"threadURL,omitempty"`
}

// AgentSessionRoute records the Slack route that created the session.
type AgentSessionRoute struct {
	// TriggerIndex is the matched trigger index when known.
	// +optional
	TriggerIndex *int32 `json:"triggerIndex,omitempty"`

	// TriggerPattern is the matched trigger regex when known.
	// +optional
	TriggerPattern string `json:"triggerPattern,omitempty"`

	// InitialText is the original first-turn Slack text.
	// +optional
	InitialText string `json:"initialText,omitempty"`
}

// AgentSessionSpec defines desired AgentSession state.
type AgentSessionSpec struct {
	// Source identifies the external conversation for this session.
	Source AgentSessionSource `json:"source"`

	// TaskSpawnerRef identifies the TaskSpawner whose template and route own
	// this session.
	TaskSpawnerRef TaskSpawnerReference `json:"taskSpawnerRef"`

	// TaskTemplateSnapshot is copied from the originating TaskSpawner when the
	// session is created. It remains stable for the session lifetime.
	TaskTemplateSnapshot TaskTemplate `json:"taskTemplateSnapshot"`

	// Route records the first-turn route.
	// +optional
	Route AgentSessionRoute `json:"route,omitempty"`

	// IdleTimeout closes an idle session after this duration.
	IdleTimeout metav1.Duration `json:"idleTimeout"`

	// ContextWindow controls how much Slack thread context is materialized for
	// each follow-up turn.
	// +optional
	ContextWindow SlackSessionContextWindow `json:"contextWindow,omitempty"`

	// MaxQueuedTurns limits queued turns for this session.
	MaxQueuedTurns int32 `json:"maxQueuedTurns"`
}

// AgentSessionStatus defines observed AgentSession state.
type AgentSessionStatus struct {
	// Phase represents the current session phase.
	// +optional
	Phase AgentSessionPhase `json:"phase,omitempty"`

	// CodexThreadID is the Codex App Server thread ID backing this session.
	// +optional
	CodexThreadID string `json:"codexThreadID,omitempty"`

	// RunnerJobName is the Kubernetes Job running the session runner.
	// +optional
	RunnerJobName string `json:"runnerJobName,omitempty"`

	// RunnerPodName is the current runner Pod name when known.
	// +optional
	RunnerPodName string `json:"runnerPodName,omitempty"`

	// CurrentTurn is the AgentTurn currently being executed.
	// +optional
	CurrentTurn string `json:"currentTurn,omitempty"`

	// LastCompletedTurn is the last terminal AgentTurn.
	// +optional
	LastCompletedTurn string `json:"lastCompletedTurn,omitempty"`

	// LastAgentMessageTS is the Slack timestamp of Cody's last terminal reply.
	// +optional
	LastAgentMessageTS string `json:"lastAgentMessageTS,omitempty"`

	// LastActivityAt is the last observed session activity time.
	// +optional
	LastActivityAt *metav1.Time `json:"lastActivityAt,omitempty"`

	// QueuedTurns is the number of queued turns observed for this session.
	// +optional
	QueuedTurns int32 `json:"queuedTurns,omitempty"`

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
// +kubebuilder:printcolumn:name="TaskSpawner",type=string,JSONPath=`.spec.taskSpawnerRef.name`
// +kubebuilder:printcolumn:name="Channel",type=string,JSONPath=`.spec.source.channelID`,priority=1
// +kubebuilder:printcolumn:name="Root TS",type=string,JSONPath=`.spec.source.rootTS`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentSession is the Schema for thread-scoped agent sessions.
type AgentSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSessionSpec   `json:"spec,omitempty"`
	Status AgentSessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentSessionList contains a list of AgentSession.
type AgentSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentSession{}, &AgentSessionList{})
}
