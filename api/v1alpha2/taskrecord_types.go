package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TaskReference identifies a Task by name and UID. It is used by the resources
// that outlive a Task and refer back to it, such as TaskRecord and TaskScore.
type TaskReference struct {
	// Name is the Task name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// UID is the Task UID.
	UID types.UID `json:"uid"`
}

// TaskRecordSpec is the immutable terminal record for one completed Task.
type TaskRecordSpec struct {
	// TaskRef identifies the Task this record was created from.
	// +kubebuilder:validation:Required
	TaskRef TaskReference `json:"taskRef"`

	// Type is the effective agent type of the Task, resolved from
	// Task.spec.worker.type with a fallback to the legacy Task.spec.type.
	// +optional
	// +kubebuilder:validation:Enum=claude-code;codex;gemini;opencode;cursor
	Type string `json:"type,omitempty"`

	// Model is the effective model of the Task, resolved from
	// Task.spec.worker.model with a fallback to the legacy Task.spec.model, if set.
	// +optional
	Model string `json:"model,omitempty"`

	// Phase is the terminal Task phase.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Succeeded;Failed
	Phase TaskPhase `json:"phase"`

	// StartTime is when the Task started running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the Task completed.
	// +kubebuilder:validation:Required
	CompletionTime *metav1.Time `json:"completionTime"`

	// Usage contains structured token and cost usage reported by the Task.
	// +optional
	Usage *TaskUsage `json:"usage,omitempty"`

	// TTLSecondsAfterCompletion is the number of seconds after CompletionTime
	// before the TaskRecord is eligible for automatic deletion. If unset,
	// the record is retained indefinitely. The controller garbage-collects
	// expired records during reconciliation.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterCompletion *int32 `json:"ttlSecondsAfterCompletion,omitempty"`
}

// TaskScoreSummary aggregates the TaskScores recorded against one Task. It is
// a derived view maintained by the controller; TaskScore objects remain the
// source of truth.
type TaskScoreSummary struct {
	// Positive is the number of TaskScores with a Positive verdict.
	//
	// Serialized even when zero: the summary is absent entirely for an unscored
	// Task, so a present summary with zero here means "scored, none positive". With
	// omitempty the print column would render that as <none>, which reads as
	// unknown rather than zero. Still optional: only Kelos writes this status.
	// +optional
	Positive int32 `json:"positive"`

	// Negative is the number of TaskScores with a Negative verdict. Serialized
	// even when zero, for the same reason as Positive.
	// +optional
	Negative int32 `json:"negative"`

	// Total is the number of TaskScores recorded against the Task. It counts every
	// score, including any whose verdict this controller does not recognize, so
	// Positive + Negative <= Total rather than == Total.
	//
	// Consumers should treat rates computed over a small Total as unreliable:
	// most Tasks are never scored, and the actors who do score self-select.
	// +optional
	Total int32 `json:"total"`

	// LastObservedAt is the most recent ObservedAt across the aggregated scores.
	// +optional
	LastObservedAt *metav1.Time `json:"lastObservedAt,omitempty"`
}

// TaskRecordStatus holds observations about a completed Task that arrive after
// the terminal record was written.
type TaskRecordStatus struct {
	// Scores aggregates the TaskScores recorded against this Task.
	// +optional
	Scores *TaskScoreSummary `json:"scores,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tr
// +kubebuilder:printcolumn:name="Task",type=string,JSONPath=`.spec.taskRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.spec.phase`
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.spec.usage.costUSD`,priority=1
// +kubebuilder:printcolumn:name="Positive",type=integer,JSONPath=`.status.scores.positive`,priority=1
// +kubebuilder:printcolumn:name="Negative",type=integer,JSONPath=`.status.scores.negative`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskRecord is an immutable terminal record for one completed Task.
// It preserves accounting data after the Task itself is deleted by TTL.
// The name is derived from the Task UID to guarantee uniqueness.
// No ownerReference is set so garbage collection does not remove it.
// Records with spec.ttlSecondsAfterCompletion set are automatically deleted
// by the controller after the specified duration.
type TaskRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="TaskRecord spec is immutable after creation"
	Spec TaskRecordSpec `json:"spec"`

	// +optional
	Status TaskRecordStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskRecordList contains a list of TaskRecord.
type TaskRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskRecord `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskRecord{}, &TaskRecordList{})
}
