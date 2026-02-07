package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CredentialType defines the type of credentials used for authentication.
type CredentialType string

const (
	// CredentialTypeAPIKey uses an API key for authentication.
	CredentialTypeAPIKey CredentialType = "api-key"
	// CredentialTypeOAuth uses OAuth for authentication.
	CredentialTypeOAuth CredentialType = "oauth"
)

// TaskPhase represents the current phase of a Task.
type TaskPhase string

const (
	// TaskPhasePending means the Task has been accepted but not yet started.
	TaskPhasePending TaskPhase = "Pending"
	// TaskPhaseRunning means the Task is currently running.
	TaskPhaseRunning TaskPhase = "Running"
	// TaskPhaseSucceeded means the Task has completed successfully.
	TaskPhaseSucceeded TaskPhase = "Succeeded"
	// TaskPhaseFailed means the Task has failed.
	TaskPhaseFailed TaskPhase = "Failed"
)

// SecretReference refers to a Secret containing credentials.
type SecretReference struct {
	// Name is the name of the secret.
	Name string `json:"name"`
}

// Credentials defines how to authenticate with the AI agent.
type Credentials struct {
	// Type specifies the credential type (api-key or oauth).
	// +kubebuilder:validation:Enum=api-key;oauth
	Type CredentialType `json:"type"`

	// SecretRef references the Secret containing credentials.
	SecretRef SecretReference `json:"secretRef"`
}

// TaskSpec defines the desired state of Task.
type TaskSpec struct {
	// Type specifies the agent type (e.g., claude-code).
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Prompt is the task prompt to send to the agent.
	// +kubebuilder:validation:Required
	Prompt string `json:"prompt"`

	// Credentials specifies how to authenticate with the agent.
	// +kubebuilder:validation:Required
	Credentials Credentials `json:"credentials"`

	// Model optionally overrides the default model.
	// +optional
	Model string `json:"model,omitempty"`

	// WorkspaceRef optionally references a Workspace resource for the agent to work in.
	// +optional
	WorkspaceRef *WorkspaceReference `json:"workspaceRef,omitempty"`

	// TTLSecondsAfterFinished limits the lifetime of a Task that has finished
	// execution (either Succeeded or Failed). If set, the Task will be
	// automatically deleted after the given number of seconds once it reaches
	// a terminal phase, allowing TaskSpawner to create a new Task.
	// If this field is unset, the Task will not be automatically deleted.
	// If this field is set to zero, the Task will be eligible to be deleted
	// immediately after it finishes.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// TaskStatus defines the observed state of Task.
type TaskStatus struct {
	// Phase represents the current phase of the Task.
	// +optional
	Phase TaskPhase `json:"phase,omitempty"`

	// JobName is the name of the Job created for this Task.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// PodName is the name of the Pod running the Task.
	// +optional
	PodName string `json:"podName,omitempty"`

	// StartTime is when the Task started running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the Task completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Task is the Schema for the tasks API.
type Task struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaskSpec   `json:"spec,omitempty"`
	Status TaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskList contains a list of Task.
type TaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Task `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Task{}, &TaskList{})
}
