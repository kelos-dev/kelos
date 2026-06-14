package v1alpha2

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CredentialType defines the type of credentials used for authentication.
type CredentialType string

const (
	// CredentialTypeAPIKey uses an API key for authentication.
	CredentialTypeAPIKey CredentialType = "api-key"
	// CredentialTypeOAuth uses OAuth for authentication.
	CredentialTypeOAuth CredentialType = "oauth"
	// CredentialTypeNone disables built-in credential injection.
	// Users supply their own credentials via PodOverrides.Env.
	CredentialTypeNone CredentialType = "none"
)

// AgentContainerName is the stable name of the agent (main) container in
// task pods. It carries the reserved kelos- prefix so the name no longer
// varies per agent type and so user-supplied containers
// (PodOverrides.ExtraContainers / ExtraInitContainers) cannot collide with
// it. Consumers that read the agent container's logs (controller, CLI,
// reporting) reference this constant rather than task.Spec.Type.
const AgentContainerName = "kelos-agent"

// ReservedContainerNamePrefix is reserved for Kelos-internal container names
// (e.g. AgentContainerName). User-supplied containers
// (PodOverrides.ExtraContainers / ExtraInitContainers) must not use it, so
// future Kelos-internal containers can adopt the prefix without colliding.
const ReservedContainerNamePrefix = "kelos-"

// ReservedVolumeNamePrefix is reserved for Kelos-internal volume names
// (e.g. kelos-plugin). User-supplied PodOverrides.Volumes entries must
// not use it, so future Kelos-internal volumes can adopt the prefix
// without colliding.
const ReservedVolumeNamePrefix = ReservedContainerNamePrefix

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
	// TaskPhaseWaiting means the Task is waiting for dependencies or branch lock.
	TaskPhaseWaiting TaskPhase = "Waiting"
)

// SecretReference refers to a Secret containing credentials.
type SecretReference struct {
	// Name is the name of the secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// Credentials defines how to authenticate with the AI agent.
type Credentials struct {
	// Type specifies the credential type.
	// +kubebuilder:validation:Enum=api-key;oauth;none
	Type CredentialType `json:"type"`

	// SecretRef references the Secret containing credentials.
	// Required for api-key and oauth types. Not used with none.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`
}

// PodOverrides defines optional overrides for the agent pod.
type PodOverrides struct {
	// Labels specifies additional labels to apply to the Job and its Pod.
	// These are merged with the built-in labels. If a user-specified label
	// conflicts with a built-in one, the built-in takes precedence.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Resources defines resource limits and requests for the agent container.
	// Applies only to the agent container; configure additional containers
	// directly via ExtraContainers or ExtraInitContainers.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ActiveDeadlineSeconds specifies the maximum duration in seconds
	// that the agent pod can run before being terminated.
	// This is set on the Job's activeDeadlineSeconds field.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// Env specifies additional environment variables for the agent container.
	// These are appended after the built-in env vars (credentials, model, GitHub token).
	// If a user-specified env var conflicts with a built-in one, the built-in takes precedence.
	// Applies only to the agent container; configure additional containers
	// directly via ExtraContainers or ExtraInitContainers.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// NodeSelector constrains agent pods to nodes matching the given labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations allows agent pods to be scheduled on nodes with matching
	// taints. Use with NodeSelector or Affinity to target dedicated node
	// pools (e.g., GPU nodes, AI-agent pools).
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity specifies node, pod, and pod-anti affinity rules for the
	// agent pod. Useful for spreading agents across nodes (anti-affinity)
	// or expressing scheduling preferences beyond simple NodeSelector.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// ImagePullSecrets specifies secrets used to pull container images
	// from private registries. Required when the agent image (or any
	// init container image) lives in a private registry.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// ServiceAccountName sets the pod's service account.
	// Use with workload identity systems such as IRSA on EKS, GKE
	// Workload Identity, or Azure Workload Identity.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Volumes is a list of additional volumes to attach to the agent pod.
	// User-supplied volume names must not collide with Kelos-reserved
	// names ("workspace", "kelos-plugin").
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts is a list of additional volume mounts to add to the
	// agent container. Names must reference either a user-supplied volume
	// from Volumes or a Kelos-managed volume ("workspace", "kelos-plugin").
	// Applies only to the agent container; configure additional containers
	// directly via ExtraContainers or ExtraInitContainers.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// PodSecurityContext is applied to the agent pod. Fields set here
	// override Kelos defaults; fields left unset retain Kelos defaults
	// (in particular, FSGroup is retained when a workspace is mounted so
	// the agent user keeps read/write access to the workspace volume).
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// ContainerSecurityContext is applied to the agent container. Use
	// this to declare allowPrivilegeEscalation=false, capabilities.drop=[ALL],
	// readOnlyRootFilesystem=true, etc., so the spawned pod can land in a
	// PSS restricted namespace. Applies only to the agent container;
	// configure additional containers directly via ExtraContainers or
	// ExtraInitContainers.
	// +optional
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`

	// ExtraContainers is a list of additional containers to run alongside
	// the agent container in the same pod. These share the pod's network
	// namespace (accessible via localhost) and can mount user-supplied
	// volumes from the Volumes field.
	// Applies only to extra containers; to configure the agent container
	// itself, use the Resources, Env, VolumeMounts, and
	// ContainerSecurityContext fields.
	// Container names must not use the Kelos-reserved "kelos-" prefix or
	// collide with a built-in init container name: git-clone, remote-setup,
	// branch-setup, workspace-files, plugin-setup, skills-install.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.name != '')",message="extraContainers[].name must not be empty"
	// +kubebuilder:validation:XValidation:rule="self.all(c, !c.name.startsWith('kelos-') && !(c.name in ['git-clone', 'remote-setup', 'branch-setup', 'workspace-files', 'plugin-setup', 'skills-install']))",message="extraContainers[].name must not use the reserved 'kelos-' prefix or a built-in init container name"
	ExtraContainers []corev1.Container `json:"extraContainers,omitempty"`

	// ExtraInitContainers is a list of additional init containers to run
	// in the agent pod. They are placed after all Kelos built-in init
	// containers (git-clone, remote-setup, branch-setup, workspace-files,
	// plugin-setup, skills-install), ensuring the workspace is ready
	// before they start. Set restartPolicy: Always for sidecar semantics
	// (long-running services like databases) or leave it unset for
	// one-shot init tasks.
	// Containers can mount user-supplied volumes from the Volumes field
	// as well as Kelos-managed volumes (workspace, kelos-plugin). Note
	// that the workspace volume uses FSGroup-based permissions; containers
	// running as a UID outside the pod's FSGroup will not have write
	// access to the workspace.
	// Container names must not use the Kelos-reserved "kelos-" prefix or
	// collide with a built-in init container name: git-clone, remote-setup,
	// branch-setup, workspace-files, plugin-setup, skills-install.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.name != '')",message="extraInitContainers[].name must not be empty"
	// +kubebuilder:validation:XValidation:rule="self.all(c, !c.name.startsWith('kelos-') && !(c.name in ['git-clone', 'remote-setup', 'branch-setup', 'workspace-files', 'plugin-setup', 'skills-install']))",message="extraInitContainers[].name must not use the reserved 'kelos-' prefix or a built-in init container name"
	ExtraInitContainers []corev1.Container `json:"extraInitContainers,omitempty"`
}

// TaskSpec defines the desired state of Task.
type TaskSpec struct {
	// Type specifies the agent type (e.g., claude-code).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=claude-code;codex;gemini;opencode;cursor
	Type string `json:"type"`

	// Prompt is the task prompt to send to the agent.
	// +kubebuilder:validation:Required
	Prompt string `json:"prompt"`

	// Credentials specifies how to authenticate with the agent.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.type == 'none' || has(self.secretRef)",message="secretRef is required for api-key and oauth credential types"
	Credentials Credentials `json:"credentials"`

	// Model optionally overrides the default model.
	// +optional
	Model string `json:"model,omitempty"`

	// Effort optionally controls how much reasoning effort the agent should use.
	// Values are agent-specific and passed through without validation.
	// +optional
	Effort string `json:"effort,omitempty"`

	// Image optionally overrides the default agent container image.
	// Custom images must implement the agent image interface
	// (see docs/agent-image-interface.md).
	// +optional
	Image string `json:"image,omitempty"`

	// WorkspaceRef optionally references a Workspace resource for the agent to work in.
	// +optional
	WorkspaceRef *WorkspaceReference `json:"workspaceRef,omitempty"`

	// AgentConfigRefs references an ordered list of AgentConfig resources.
	// Configs are merged in order: agentsMD is concatenated, plugins/skills
	// are appended, mcpServers are appended with later entries winning on
	// name collision.
	// +optional
	// +kubebuilder:validation:MinItems=1
	AgentConfigRefs []AgentConfigReference `json:"agentConfigRefs,omitempty"`

	// DependsOn lists Task names that must succeed before this Task starts.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Branch is the git branch this Task works on. When set, an init
	// container checks out this branch before the agent starts. The
	// controller ensures only one Task with the same Branch value
	// runs at a time for the same workspace.
	// +optional
	Branch string `json:"branch,omitempty"`

	// UpstreamRepo is the upstream repository in "owner/repo" format.
	// When set, the KELOS_UPSTREAM_REPO environment variable is injected
	// into the agent container so that post-run PR capture and gh CLI
	// operations target the correct repository in fork workflows.
	// +optional
	UpstreamRepo string `json:"upstreamRepo,omitempty"`

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

	// PodFailurePolicy specifies how failed pods affect the backing Job's
	// retry accounting. If unset, Kelos leaves Job.spec.podFailurePolicy unset
	// and Kubernetes default Job handling applies.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.rules.all(r, r.action != 'FailIndex')",message="podFailurePolicy.rules[].action FailIndex is not supported for Task Jobs"
	PodFailurePolicy *batchv1.PodFailurePolicy `json:"podFailurePolicy,omitempty"`

	// PodOverrides allows customizing the agent pod configuration.
	// +optional
	PodOverrides *PodOverrides `json:"podOverrides,omitempty"`
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

	// Outputs contains URLs and references produced by the agent
	// (e.g. branch names, PR URLs).
	// +optional
	Outputs []string `json:"outputs,omitempty"`

	// Results contains structured key-value outputs produced by the agent.
	// +optional
	Results map[string]string `json:"results,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Branch",type=string,JSONPath=`.spec.branch`,priority=1
// +kubebuilder:printcolumn:name="Depends On",type=string,JSONPath=`.spec.dependsOn`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Task is the Schema for the tasks API.
type Task struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Task spec is immutable after creation"
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
