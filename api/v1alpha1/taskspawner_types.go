package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskSpawnerPhase represents the current phase of a TaskSpawner.
type TaskSpawnerPhase string

const (
	// TaskSpawnerPhasePending means the TaskSpawner has been accepted but the spawner is not yet running.
	TaskSpawnerPhasePending TaskSpawnerPhase = "Pending"
	// TaskSpawnerPhaseRunning means the spawner is actively polling and creating tasks.
	TaskSpawnerPhaseRunning TaskSpawnerPhase = "Running"
	// TaskSpawnerPhaseFailed means the spawner has failed.
	TaskSpawnerPhaseFailed TaskSpawnerPhase = "Failed"
)

// When defines the conditions that trigger task spawning.
// Exactly one field must be set.
type When struct {
	// GitHubIssues discovers issues from a GitHub repository.
	// +optional
	GitHubIssues *GitHubIssues `json:"githubIssues,omitempty"`

	// Cron triggers task spawning on a cron schedule.
	// +optional
	Cron *Cron `json:"cron,omitempty"`
}

// Cron triggers task spawning on a cron schedule.
type Cron struct {
	// Schedule is a cron expression (e.g., "0 9 * * 1" for every Monday at 9am).
	// +kubebuilder:validation:Required
	Schedule string `json:"schedule"`
}

// GitHubIssues discovers issues from a GitHub repository.
// The repository owner and name are derived from the workspace's repo URL
// specified in taskTemplate.workspaceRef.
// If the workspace has a secretRef, it is used for GitHub API authentication.
type GitHubIssues struct {
	// Types specifies which item types to discover: "issues", "pulls", or both.
	// +kubebuilder:validation:Items:Enum=issues;pulls
	// +kubebuilder:default={"issues"}
	// +optional
	Types []string `json:"types,omitempty"`

	// Labels filters issues by labels.
	// +optional
	Labels []string `json:"labels,omitempty"`

	// ExcludeLabels filters out issues that have any of these labels (client-side).
	// +optional
	ExcludeLabels []string `json:"excludeLabels,omitempty"`

	// State filters issues by state (open, closed, all). Defaults to open.
	// +kubebuilder:validation:Enum=open;closed;all
	// +kubebuilder:default=open
	// +optional
	State string `json:"state,omitempty"`
}

// TaskTemplate defines the template for spawned Tasks.
type TaskTemplate struct {
	// Type specifies the agent type (e.g., claude-code).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=claude-code;codex;gemini
	Type string `json:"type"`

	// Credentials specifies how to authenticate with the agent.
	// +kubebuilder:validation:Required
	Credentials Credentials `json:"credentials"`

	// Model optionally overrides the default model.
	// +optional
	Model string `json:"model,omitempty"`

	// Image optionally overrides the default agent container image.
	// Custom images must implement the agent image interface
	// (see docs/agent-image-interface.md).
	// +optional
	Image string `json:"image,omitempty"`

	// WorkspaceRef references the Workspace that defines the repository.
	// Required when using githubIssues source; optional for other sources.
	// When set, spawned Tasks inherit this workspace reference.
	// +optional
	WorkspaceRef *WorkspaceReference `json:"workspaceRef,omitempty"`

	// AgentConfigRef references an AgentConfig resource.
	// When set, spawned Tasks inherit this agent config reference.
	// +optional
	AgentConfigRef *AgentConfigReference `json:"agentConfigRef,omitempty"`

	// PromptTemplate is a Go text/template for rendering the task prompt.
	// Available variables: {{.ID}}, {{.Number}}, {{.Title}}, {{.Body}}, {{.URL}}, {{.Comments}}, {{.Labels}}, {{.Kind}}, {{.Time}}, {{.Schedule}}.
	// +optional
	PromptTemplate string `json:"promptTemplate,omitempty"`

	// TTLSecondsAfterFinished limits the lifetime of a Task that has finished
	// execution (either Succeeded or Failed). If set, spawned Tasks will be
	// automatically deleted after the given number of seconds once they reach
	// a terminal phase, allowing TaskSpawner to create a new Task.
	// If this field is unset, spawned Tasks will not be automatically deleted.
	// If this field is set to zero, spawned Tasks will be eligible to be deleted
	// immediately after they finish.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// PodOverrides allows customizing the agent pod configuration for spawned Tasks.
	// +optional
	PodOverrides *PodOverrides `json:"podOverrides,omitempty"`
}

// TaskSpawnerSpec defines the desired state of TaskSpawner.
// +kubebuilder:validation:XValidation:rule="!has(self.when.githubIssues) || has(self.taskTemplate.workspaceRef)",message="taskTemplate.workspaceRef is required when using githubIssues source"
type TaskSpawnerSpec struct {
	// When defines the conditions that trigger task spawning.
	// +kubebuilder:validation:Required
	When When `json:"when"`

	// TaskTemplate defines the template for spawned Tasks.
	// +kubebuilder:validation:Required
	TaskTemplate TaskTemplate `json:"taskTemplate"`

	// PollInterval is how often to poll the source for new items (e.g., "5m"). Defaults to "5m".
	// +kubebuilder:default="5m"
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`

	// MaxConcurrency limits the number of concurrently running (non-terminal) Tasks.
	// When the limit is reached, the spawner skips creating new Tasks until
	// existing ones complete. If unset or zero, there is no concurrency limit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxConcurrency *int32 `json:"maxConcurrency,omitempty"`
}

// TaskSpawnerStatus defines the observed state of TaskSpawner.
type TaskSpawnerStatus struct {
	// Phase represents the current phase of the TaskSpawner.
	// +optional
	Phase TaskSpawnerPhase `json:"phase,omitempty"`

	// DeploymentName is the name of the Deployment running the spawner.
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// TotalDiscovered is the total number of work items discovered.
	// +optional
	TotalDiscovered int `json:"totalDiscovered,omitempty"`

	// TotalTasksCreated is the total number of Tasks created.
	// +optional
	TotalTasksCreated int `json:"totalTasksCreated,omitempty"`

	// ActiveTasks is the number of currently active (non-terminal) Tasks.
	// +optional
	ActiveTasks int `json:"activeTasks,omitempty"`

	// LastDiscoveryTime is the last time the source was polled.
	// +optional
	LastDiscoveryTime *metav1.Time `json:"lastDiscoveryTime,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.taskTemplate.workspaceRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeTasks`
// +kubebuilder:printcolumn:name="Discovered",type=integer,JSONPath=`.status.totalDiscovered`
// +kubebuilder:printcolumn:name="Tasks",type=integer,JSONPath=`.status.totalTasksCreated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskSpawner is the Schema for the taskspawners API.
type TaskSpawner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaskSpawnerSpec   `json:"spec,omitempty"`
	Status TaskSpawnerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskSpawnerList contains a list of TaskSpawner.
type TaskSpawnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskSpawner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskSpawner{}, &TaskSpawnerList{})
}
