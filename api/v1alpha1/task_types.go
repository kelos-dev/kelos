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

// MCPTransportType defines the transport type for an MCP server.
type MCPTransportType string

const (
	// MCPTransportStdio connects to an MCP server via stdio.
	MCPTransportStdio MCPTransportType = "stdio"
	// MCPTransportHTTP connects to an MCP server via HTTP.
	MCPTransportHTTP MCPTransportType = "http"
	// MCPTransportSSE connects to an MCP server via Server-Sent Events.
	MCPTransportSSE MCPTransportType = "sse"
)

// MCPServer defines an MCP server that the coding agent can use as a plugin.
type MCPServer struct {
	// Name is the unique name of the MCP server.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Transport specifies the transport type (stdio, http, or sse).
	// +kubebuilder:validation:Enum=stdio;http;sse
	// +kubebuilder:validation:Required
	Transport MCPTransportType `json:"transport"`

	// Target is the command (for stdio) or URL (for http/sse).
	// For stdio, this is the command and arguments to run (e.g., "npx -y @example/server").
	// For http/sse, this is the URL to connect to (e.g., "https://api.example.com/mcp").
	// +kubebuilder:validation:Required
	Target string `json:"target"`

	// Args is an optional list of arguments for stdio transport.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env is an optional map of environment variables for the MCP server.
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// Headers is an optional map of HTTP headers for http/sse transport.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`
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

	// MCPServers is an optional list of MCP servers to use as plugins.
	// +optional
	MCPServers []MCPServer `json:"mcpServers,omitempty"`

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
