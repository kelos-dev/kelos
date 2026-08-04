package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScoreVerdict is the normalized judgement carried by a TaskScore. Every
// integration maps its own vocabulary (a Slack reaction, a GitHub review state)
// onto this enum, so consumers aggregate scores without understanding any
// source-specific signal.
// +kubebuilder:validation:Enum=Positive;Negative
type ScoreVerdict string

const (
	// ScoreVerdictPositive indicates the external actor judged the result good.
	ScoreVerdictPositive ScoreVerdict = "Positive"

	// ScoreVerdictNegative indicates the external actor judged the result bad.
	ScoreVerdictNegative ScoreVerdict = "Negative"
)

// ScoreSourceType identifies the integration that observed a score.
// +kubebuilder:validation:Enum=SlackReaction
type ScoreSourceType string

const (
	// ScoreSourceSlackReaction is an emoji reaction added to the message the
	// agent posted with its result.
	ScoreSourceSlackReaction ScoreSourceType = "SlackReaction"
)

// ScoreSource records where a score came from and preserves the source's own
// signal so verdicts can be re-derived if the mapping changes.
type ScoreSource struct {
	// Type identifies the integration that observed the score.
	// +kubebuilder:validation:Required
	Type ScoreSourceType `json:"type"`

	// Actor is the external identity that scored the result, in whatever form the
	// source system uses (e.g. a Slack user ID).
	//
	// It is part of the score's identity: one actor's score for a given signal is
	// recorded once rather than accumulating, and a redelivery keeps the score
	// already stored. Omit it when the source has no distinct actor — a
	// machine-generated outcome signal, say — in which case identity collapses to
	// the Task, source type, and signal, which is the idempotency such a source
	// wants. The empty string is rejected; omission is the way to express absence.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Actor string `json:"actor,omitempty"`

	// Signal identifies the score in the source system's own vocabulary, before
	// it was normalized into a Verdict (e.g. the Slack reaction name "+1"). It
	// lets verdicts be re-derived if the mapping from signal to verdict changes.
	//
	// Cosmetic variants the source treats as the same signal are folded first —
	// a Slack skin-tone modifier is stripped — so that one actor expressing one
	// sentiment produces one score rather than one per variant. It is therefore
	// the source's canonical signal name, not the exact bytes received.
	//
	// Required because it is part of the score's identity: two scores that differ
	// only by a signal neither records would collide, and the second would be
	// silently discarded as a duplicate of the first.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Signal string `json:"signal"`

	// URI locates the scored artifact in the source system, for example
	// "slack://C0123456789/1712345678.123456".
	//
	// The value is opaque: its format varies by source and is not part of the API
	// contract, so clients must not parse it. It is named URI rather than Ref
	// because it is a free-form string, not a reference to a Kubernetes object as
	// the sibling TaskRef is.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	URI string `json:"uri,omitempty"`
}

// TaskScoreSpec is the immutable record of one scoring event observed from
// outside the cluster.
//
// taskRef.uid is rejected when empty rather than only when absent. The UID is
// what ties a score to its Task after the Task is gone, so a score without one
// could never be summarized, adopted, or garbage-collected. The rule lives here
// rather than as a MinLength on the shared TaskReference, because tightening that
// type would also tighten TaskRecord, where the field already ships.
// +kubebuilder:validation:XValidation:rule="size(self.taskRef.uid) > 0",message="taskRef.uid must not be empty"
type TaskScoreSpec struct {
	// TaskRef identifies the Task whose result was scored.
	// +kubebuilder:validation:Required
	TaskRef TaskReference `json:"taskRef"`

	// Verdict is the normalized judgement.
	// +kubebuilder:validation:Required
	Verdict ScoreVerdict `json:"verdict"`

	// Source records the origin of the score.
	// +kubebuilder:validation:Required
	Source ScoreSource `json:"source"`

	// ObservedAt is when the scoring event happened in the source system.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:shortName=tsc
// +kubebuilder:printcolumn:name="Task",type=string,JSONPath=`.spec.taskRef.name`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.spec.verdict`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="Actor",type=string,JSONPath=`.spec.source.actor`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskScore is an immutable record of one external actor scoring one Task
// result. A Task may accumulate many TaskScores from different actors and
// different sources.
//
// The name is derived from the Task UID, source type, actor, and signal, so a
// repeated delivery of the same scoring event is a no-op and a retraction (e.g.
// removing a Slack reaction) deletes the object it created.
//
// The TaskSpawner that created the scored Task is recorded in the
// kelos.dev/taskspawner label rather than in the spec, so scores can be selected
// per spawner without a full scan.
//
// A TaskScore is adopted by the TaskRecord for its Task once that record
// exists, so scores are garbage-collected with the record they describe.
type TaskScore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="TaskScore spec is immutable after creation"
	Spec TaskScoreSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// TaskScoreList contains a list of TaskScore.
type TaskScoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskScore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskScore{}, &TaskScoreList{})
}
