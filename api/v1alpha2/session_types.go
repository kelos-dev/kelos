package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// SessionPhase represents the infrastructure lifecycle of a Session.
type SessionPhase string

const (
	// SessionPhasePending means the Session Pod is being prepared.
	SessionPhasePending SessionPhase = "Pending"
	// SessionPhaseReady means the Session runtime is ready for clients.
	SessionPhaseReady SessionPhase = "Ready"
	// SessionPhaseFailed means the Session cannot accept clients.
	SessionPhaseFailed SessionPhase = "Failed"
)

const (
	// SessionConditionReady indicates whether the Session infrastructure is ready for clients.
	SessionConditionReady = "Ready"
	// SessionConditionActive indicates whether the Session runtime has an unfinished turn.
	SessionConditionActive = "Active"
)

// SessionPullRequestState represents the lifecycle state of a Session pull request.
type SessionPullRequestState string

const (
	// SessionPullRequestStateDraft means the pull request is open as a draft.
	SessionPullRequestStateDraft SessionPullRequestState = "Draft"
	// SessionPullRequestStateOpen means the pull request is open for review.
	SessionPullRequestStateOpen SessionPullRequestState = "Open"
	// SessionPullRequestStateMerged means the pull request has been merged.
	SessionPullRequestStateMerged SessionPullRequestState = "Merged"
	// SessionPullRequestStateClosed means the pull request was closed without merging.
	SessionPullRequestStateClosed SessionPullRequestState = "Closed"
)

// SessionPullRequest describes the pull request associated with a Session branch.
type SessionPullRequest struct {
	// URL is the pull request web URL.
	URL string `json:"url"`

	// State is the pull request lifecycle state.
	// +kubebuilder:validation:Enum=Draft;Open;Merged;Closed
	State SessionPullRequestState `json:"state"`
}

// SessionSpec defines the desired state of a Session.
//
// +kubebuilder:validation:XValidation:rule="has(self.worker.type) && self.worker.type in ['claude-code', 'codex', 'opencode']",message="worker.type must be claude-code, codex, or opencode"
// +kubebuilder:validation:XValidation:rule="has(self.worker.credentials)",message="worker.credentials is required"
type SessionSpec struct {
	// Worker defines the agent and execution environment for this Session.
	Worker WorkerSpec `json:"worker"`

	// VolumeClaimTemplate defines persistent storage for the Session workspace.
	// Omit this field to use an emptyDir workspace.
	// +optional
	VolumeClaimTemplate *corev1.PersistentVolumeClaimSpec `json:"volumeClaimTemplate,omitempty"`
}

// SessionStatus defines the observed state of a Session.
type SessionStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current infrastructure phase of the Session.
	// +optional
	Phase SessionPhase `json:"phase,omitempty"`

	// PodName is the name of the Pod that hosts the Session runtime.
	// +optional
	PodName string `json:"podName,omitempty"`

	// PodUID identifies the Pod that owns the live conversation process.
	// +optional
	PodUID types.UID `json:"podUID,omitempty"`

	// Message provides additional information about the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Branch is the currently checked-out git branch in the Session workspace.
	// +optional
	Branch string `json:"branch,omitempty"`

	// PullRequest describes the pull request associated with Branch, when one exists.
	// +optional
	PullRequest *SessionPullRequest `json:"pullRequest,omitempty"`

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
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sess
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.worker.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.podName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Session is the Schema for the sessions API.
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Session spec is immutable after creation"
	Spec   SessionSpec   `json:"spec"`
	Status SessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SessionList contains a list of Session.
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Session{}, &SessionList{})
}
