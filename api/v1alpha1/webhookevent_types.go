package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebhookEventSpec defines the desired state of WebhookEvent.
type WebhookEventSpec struct {
	// Source is the webhook source type (e.g., "github", "slack", "linear").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`

	// Payload is the raw webhook payload as JSON bytes.
	// +kubebuilder:validation:Required
	Payload []byte `json:"payload"`

	// ReceivedAt is the timestamp when the webhook was received.
	// +kubebuilder:validation:Required
	ReceivedAt metav1.Time `json:"receivedAt"`

	// TTLSecondsAfterProcessed limits the lifetime of a WebhookEvent after
	// it has been processed. If set, the event will be automatically deleted
	// after the given number of seconds once ProcessedAt is set.
	// Defaults to 7200 (2 hours) when created by the webhook receiver.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterProcessed *int32 `json:"ttlSecondsAfterProcessed,omitempty"`
}

// WebhookEventStatus defines the observed state of WebhookEvent.
type WebhookEventStatus struct {
	// Processed indicates whether this event has been processed by at least one spawner.
	// Maintained for backward compatibility; prefer checking ProcessedBy for
	// per-spawner tracking.
	// +optional
	Processed bool `json:"processed,omitempty"`

	// ProcessedBy lists the names of TaskSpawners that have processed this event.
	// Each spawner appends its name after processing, allowing multiple spawners
	// to independently react to the same webhook event.
	// +optional
	ProcessedBy []string `json:"processedBy,omitempty"`

	// ProcessedAt is the timestamp when the event was last processed.
	// +optional
	ProcessedAt *metav1.Time `json:"processedAt,omitempty"`

	// Message provides additional information about processing.
	// +optional
	Message string `json:"message,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source`
// +kubebuilder:printcolumn:name="Processed",type=boolean,JSONPath=`.status.processed`
// +kubebuilder:printcolumn:name="ReceivedAt",type=date,JSONPath=`.spec.receivedAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WebhookEvent is the Schema for the webhookevents API.
type WebhookEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebhookEventSpec   `json:"spec,omitempty"`
	Status WebhookEventStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WebhookEventList contains a list of WebhookEvent.
type WebhookEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebhookEvent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WebhookEvent{}, &WebhookEventList{})
}
