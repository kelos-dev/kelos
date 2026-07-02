package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TaskReference identifies the Task a TaskRecord was created from.
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

	// Type is the Task.spec.type value.
	// +optional
	// +kubebuilder:validation:Enum=claude-code;codex;gemini;opencode;cursor
	Type string `json:"type,omitempty"`

	// Model is the Task.spec.model value, if set.
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
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

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

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:shortName=tr
// +kubebuilder:printcolumn:name="Task",type=string,JSONPath=`.spec.taskRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.spec.phase`
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.spec.usage.costUSD`,priority=1
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

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="TaskRecord spec is immutable after creation"
	Spec TaskRecordSpec `json:"spec,omitempty"`
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
