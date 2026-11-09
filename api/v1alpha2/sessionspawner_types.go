package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// SessionSpawnerConditionLastDeliverySucceeded reports the result of the most recent matching
	// GitHub webhook delivery. Only a spawner with when.githubWebhook currently sets it: the
	// Slack path reports a failure into the Slack thread instead, so the condition stays absent
	// there. The condition is absent until a webhook delivery has been attempted.
	SessionSpawnerConditionLastDeliverySucceeded = "LastDeliverySucceeded"
)

// SessionSpawnerWhen defines the event source that triggers Session creation.
type SessionSpawnerWhen struct {
	// GitHubWebhook receives GitHub events whose matching deliveries create
	// Sessions. GitHub reporting is not supported for SessionSpawner.
	// +optional
	GitHubWebhook *GitHubWebhook `json:"githubWebhook,omitempty"`

	// Note for contributors, kept above the godoc and separated by a blank line
	// so controller-gen leaves it out of the CRD description: this is the same
	// type TaskSpawner uses, so a field added there appears here automatically.
	// A field that only a TaskSpawner can honour must be rejected with a CEL
	// rule on SessionSpawnerSpec, the way githubWebhook.reporting is.

	// Slack receives Slack messages whose matching deliveries create Sessions.
	// One Session is created per Slack thread and reused for every later matching
	// message in that thread, so the conversation retains its history. The reply
	// is posted back into the thread.
	// +optional
	Slack *Slack `json:"slack,omitempty"`
}

// SessionTemplate defines the Session spec copied to each spawned Session.
type SessionTemplate struct {
	SessionSpec `json:",inline"`
}

// SessionSpawnerSpec defines the desired state of a SessionSpawner.
//
// +kubebuilder:validation:XValidation:rule="[has(self.when.githubWebhook), has(self.when.slack)].filter(source, source).size() == 1",message="exactly one of when.githubWebhook or when.slack must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.when.githubWebhook) || !has(self.when.githubWebhook.reporting)",message="when.githubWebhook.reporting is not supported"
// +kubebuilder:validation:XValidation:rule="has(self.sessionTemplate.worker.workspaceRef) && size(self.sessionTemplate.worker.workspaceRef.name) > 0",message="sessionTemplate.worker.workspaceRef.name is required"
// +kubebuilder:validation:XValidation:rule="!has(self.when.githubWebhook) || (has(self.sessionTemplate.initialPrompt) && size(self.sessionTemplate.initialPrompt) > 0)",message="sessionTemplate.initialPrompt is required for when.githubWebhook"
// +kubebuilder:validation:XValidation:rule="!has(self.when.slack) || !has(self.sessionTemplate.initialPrompt) || size(self.sessionTemplate.initialPrompt) == 0",message="sessionTemplate.initialPrompt is not supported for when.slack"
// +kubebuilder:validation:XValidation:rule="!has(self.when.slack) || !has(self.sessionTemplate.suspend) || !self.sessionTemplate.suspend",message="sessionTemplate.suspend is not supported for when.slack"
// +kubebuilder:validation:XValidation:rule="!has(self.when.slack) || (has(self.sessionTemplate.idlePolicy) && (has(self.sessionTemplate.idlePolicy.suspendAfterSeconds) || has(self.sessionTemplate.idlePolicy.deleteAfterSeconds)))",message="sessionTemplate.idlePolicy must set suspendAfterSeconds or deleteAfterSeconds for when.slack"
// +kubebuilder:validation:XValidation:rule="has(self.sessionTemplate.worker.credentials) || has(self.credentials)",message="sessionTemplate.worker.credentials or spec.credentials is required"
// +kubebuilder:validation:XValidation:rule="!has(self.credentials) || !has(self.sessionTemplate.worker.credentials)",message="spec.credentials is mutually exclusive with sessionTemplate.worker.credentials"
type SessionSpawnerSpec struct {
	// When defines the event source and filters. Exactly one of githubWebhook
	// or slack must be set.
	// +kubebuilder:validation:Required
	When SessionSpawnerWhen `json:"when"`

	// SessionTemplate defines Sessions created for matching deliveries. The
	// initialBranch field is a Go text/template rendered with the matching
	// GitHub webhook or Slack message context; initialPrompt is the same for a
	// GitHub webhook source, and is rejected for a Slack one.
	//
	// A Slack spawner drives every matching thread message as its own
	// conversation turn, so an initialPrompt would run as an extra first turn
	// whose reply nothing posts, delaying the message that actually asked for
	// something. Put standing instructions in an AgentConfig instead. A Slack
	// spawner also rejects suspend, because a suspended Session cannot answer
	// the thread that created it.
	//
	// A Slack spawner must set idlePolicy.suspendAfterSeconds or
	// idlePolicy.deleteAfterSeconds. Every thread it matches gets its own Pod,
	// and a persistent volume when volumeClaimTemplate is set, so without an
	// idle policy those accumulate for as long as the spawner exists.
	// +kubebuilder:validation:Required
	SessionTemplate SessionTemplate `json:"sessionTemplate"`

	// Credentials lists named credentials available to generated Sessions. The
	// spawner selects one credential at random and copies it to the generated
	// Session. Mutually exclusive with credentials configured in sessionTemplate.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Credentials []SpawnerCredential `json:"credentials,omitempty"`
}

// SessionSpawnerStatus defines the observed state of a SessionSpawner.
type SessionSpawnerStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// TotalSessions is the number of Sessions currently associated with this spawner.
	// +optional
	TotalSessions int32 `json:"totalSessions,omitempty"`

	// LastSessionName identifies the Session most recently created or confirmed to exist.
	// Only a spawner with when.githubWebhook currently sets it; see Conditions.
	// +optional
	LastSessionName string `json:"lastSessionName,omitempty"`

	// LastDeliveryTime is when a matching GitHub webhook delivery was most recently attempted.
	// Only a spawner with when.githubWebhook currently sets it; see Conditions.
	// +optional
	LastDeliveryTime *metav1.Time `json:"lastDeliveryTime,omitempty"`

	// Conditions report the result of processing matching GitHub webhook deliveries. A spawner
	// with when.slack does not currently set them, because the Slack bridge reports a failed turn
	// into the thread that caused it rather than onto the spawner. Use status.totalSessions, which
	// both sources maintain, to see whether a Slack spawner is producing Sessions.
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
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.sessionTemplate.worker.workspaceRef.name`
// +kubebuilder:printcolumn:name="Sessions",type=integer,JSONPath=`.status.totalSessions`
// +kubebuilder:printcolumn:name="Last Session",type=string,JSONPath=`.status.lastSessionName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SessionSpawner creates Sessions from matching GitHub webhook deliveries or
// Slack messages.
type SessionSpawner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   SessionSpawnerSpec   `json:"spec"`
	Status SessionSpawnerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SessionSpawnerList contains a list of SessionSpawner.
type SessionSpawnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SessionSpawner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SessionSpawner{}, &SessionSpawnerList{})
}
