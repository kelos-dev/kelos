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
	// TaskSpawnerPhaseSuspended means the spawner is paused by the user.
	TaskSpawnerPhaseSuspended TaskSpawnerPhase = "Suspended"
)

// When defines the conditions that trigger task spawning.
// Exactly one field must be set.
type When struct {
	// GitHubIssues discovers issues from a GitHub repository.
	// +optional
	GitHubIssues *GitHubIssues `json:"githubIssues,omitempty"`

	// GitHubPullRequests discovers pull requests from a GitHub repository.
	// +optional
	GitHubPullRequests *GitHubPullRequests `json:"githubPullRequests,omitempty"`

	// Cron triggers task spawning on a cron schedule.
	// +optional
	Cron *Cron `json:"cron,omitempty"`

	// Jira discovers issues from a Jira project.
	// +optional
	Jira *Jira `json:"jira,omitempty"`
}

// Cron triggers task spawning on a cron schedule.
type Cron struct {
	// Schedule is a cron expression (e.g., "0 9 * * 1" for every Monday at 9am).
	// +kubebuilder:validation:Required
	Schedule string `json:"schedule"`
}

// GitHubReporting configures status reporting back to GitHub.
type GitHubReporting struct {
	// Enabled posts standard status comments back to the originating GitHub issue or PR.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// GitHubTeamRef identifies a GitHub team in org/team-slug format.
// +kubebuilder:validation:Pattern=`^[^/]+/[^/]+$`
type GitHubTeamRef string

// GitHubCommentPolicy configures comment-based workflow control on GitHub items.
// A matching command is honored if the actor matches any configured user,
// team, or minimum permission rule.
type GitHubCommentPolicy struct {
	// TriggerComment requires a matching command for the item to be included.
	// When set alone, only items with a matching command are discovered.
	// +optional
	TriggerComment string `json:"triggerComment,omitempty"`

	// ExcludeComments blocks items whose most recent matching command is an
	// exclude command. When combined with TriggerComment, the most recent
	// matching command wins.
	// +optional
	ExcludeComments []string `json:"excludeComments,omitempty"`

	// AllowedUsers restricts comment control to specific GitHub usernames.
	// +optional
	AllowedUsers []string `json:"allowedUsers,omitempty"`

	// AllowedTeams restricts comment control to specific GitHub teams in
	// org/team-slug format.
	// +optional
	AllowedTeams []GitHubTeamRef `json:"allowedTeams,omitempty"`

	// MinimumPermission restricts comment control to users with at least the
	// given repository permission.
	// +kubebuilder:validation:Enum=read;triage;write;maintain;admin
	// +optional
	MinimumPermission string `json:"minimumPermission,omitempty"`
}

// GitHubIssues discovers issues from a GitHub repository.
// By default the repository owner and name are derived from the workspace's
// repo URL specified in taskTemplate.workspaceRef. Set the Repo field to
// override this — useful for fork workflows where the workspace points to a
// fork but issues should be discovered from the upstream repository.
// If the workspace has a secretRef, it is used for GitHub API authentication.
// +kubebuilder:validation:XValidation:rule="!(has(self.commentPolicy) && ((has(self.triggerComment) && size(self.triggerComment) > 0) || (has(self.excludeComments) && size(self.excludeComments) > 0)))",message="commentPolicy cannot be used with triggerComment or excludeComments"
type GitHubIssues struct {
	// Repo optionally overrides the repository to poll for issues, in
	// "owner/repo" format or as a full URL. When empty, the repository
	// is derived from the workspace repo URL in taskTemplate.workspaceRef.
	// Use this for fork workflows where the workspace points to a fork
	// but issues should be discovered from the upstream repository.
	// +optional
	Repo string `json:"repo,omitempty"`

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

	// CommentPolicy configures comment-based workflow control and authorization.
	// +optional
	CommentPolicy *GitHubCommentPolicy `json:"commentPolicy,omitempty"`

	// TriggerComment requires a matching comment for the issue to be
	// included. When set alone, only issues with a matching comment are
	// discovered. When set together with ExcludeComments, the most recent
	// matching command wins (scanned in reverse chronological order).
	// Deprecated: use CommentPolicy.TriggerComment instead.
	// +optional
	TriggerComment string `json:"triggerComment,omitempty"`

	// ExcludeComments enables comment-based exclusion. When set, issues
	// whose most recent matching comment is an ExcludeComment are excluded.
	// When combined with TriggerComment, the most recent matching command
	// wins — a TriggerComment after an ExcludeComment re-enables the issue.
	// Deprecated: use CommentPolicy.ExcludeComments instead.
	// +optional
	ExcludeComments []string `json:"excludeComments,omitempty"`

	// Assignee filters issues by assignee username. Use "*" for issues with
	// any assignee, or "none" for issues with no assignee. When empty, no
	// assignee filtering is applied (server-side via GitHub API).
	// +optional
	Assignee string `json:"assignee,omitempty"`

	// Author filters issues by the username of the user who created them
	// (server-side via GitHub API's "creator" parameter). When empty, no
	// author filtering is applied.
	// +optional
	Author string `json:"author,omitempty"`

	// PriorityLabels defines a label-based priority order for discovered items.
	// When maxConcurrency limits how many tasks are created per cycle,
	// items are sorted by the first matching label before task creation.
	// Index 0 is the highest priority. Items without a matching label
	// are scheduled last. When empty, items are processed in discovery order.
	// +optional
	PriorityLabels []string `json:"priorityLabels,omitempty"`

	// Reporting configures status reporting back to the originating GitHub issue.
	// +optional
	Reporting *GitHubReporting `json:"reporting,omitempty"`

	// PollInterval overrides spec.pollInterval for this source (e.g., "30s", "5m").
	// When empty, spec.pollInterval is used.
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`
}

// GitHubPullRequests discovers pull requests from a GitHub repository.
// By default the repository owner and name are derived from the workspace's
// repo URL specified in taskTemplate.workspaceRef. Set the Repo field to
// override this — useful for fork workflows where the workspace points to a
// fork but pull requests should be discovered from the upstream repository.
// If the workspace has a secretRef, it is used for GitHub API authentication.
// +kubebuilder:validation:XValidation:rule="!(has(self.commentPolicy) && ((has(self.triggerComment) && size(self.triggerComment) > 0) || (has(self.excludeComments) && size(self.excludeComments) > 0)))",message="commentPolicy cannot be used with triggerComment or excludeComments"
type GitHubPullRequests struct {
	// Repo optionally overrides the repository to poll for pull requests, in
	// "owner/repo" format or as a full URL. When empty, the repository
	// is derived from the workspace repo URL in taskTemplate.workspaceRef.
	// Use this for fork workflows where the workspace points to a fork
	// but pull requests should be discovered from the upstream repository.
	// +optional
	Repo string `json:"repo,omitempty"`

	// Labels filters pull requests by labels.
	// +optional
	Labels []string `json:"labels,omitempty"`

	// ExcludeLabels filters out pull requests that have any of these labels (client-side).
	// +optional
	ExcludeLabels []string `json:"excludeLabels,omitempty"`

	// State filters pull requests by state (open, closed, all). Defaults to open.
	// +kubebuilder:validation:Enum=open;closed;all
	// +kubebuilder:default=open
	// +optional
	State string `json:"state,omitempty"`

	// ReviewState filters pull requests by aggregated review state. The most
	// recent APPROVED or CHANGES_REQUESTED review from each reviewer on the
	// current head SHA is considered. When set to "any", review state does not
	// gate discovery.
	// +kubebuilder:validation:Enum=approved;changes_requested;any
	// +kubebuilder:default=any
	// +optional
	ReviewState string `json:"reviewState,omitempty"`

	// CommentPolicy configures comment-based workflow control and authorization.
	// +optional
	CommentPolicy *GitHubCommentPolicy `json:"commentPolicy,omitempty"`

	// TriggerComment requires a matching comment for the pull request to be
	// included. When set alone, only PRs with a matching comment are
	// discovered. When set together with ExcludeComments, the most recent
	// matching command wins based on comment timestamps.
	// Deprecated: use CommentPolicy.TriggerComment instead.
	// +optional
	TriggerComment string `json:"triggerComment,omitempty"`

	// ExcludeComments enables comment-based exclusion. When set, PRs
	// whose most recent matching comment is an ExcludeComment are excluded.
	// When combined with TriggerComment, the most recent matching command
	// wins — a TriggerComment after an ExcludeComment re-enables the PR.
	// Deprecated: use CommentPolicy.ExcludeComments instead.
	// +optional
	ExcludeComments []string `json:"excludeComments,omitempty"`

	// Author filters pull requests by the username of the user who opened them.
	// When empty, no author filtering is applied.
	// +optional
	Author string `json:"author,omitempty"`

	// Draft filters pull requests by draft state. When unset, both draft and
	// ready-for-review pull requests are included.
	// +optional
	Draft *bool `json:"draft,omitempty"`

	// PriorityLabels defines a label-based priority order for discovered items.
	// When maxConcurrency limits how many tasks are created per cycle,
	// items are sorted by the first matching label before task creation.
	// Index 0 is the highest priority. Items without a matching label
	// are scheduled last. When empty, items are processed in discovery order.
	// +optional
	PriorityLabels []string `json:"priorityLabels,omitempty"`

	// Reporting configures status reporting back to the originating GitHub pull request.
	// +optional
	Reporting *GitHubReporting `json:"reporting,omitempty"`

	// PollInterval overrides spec.pollInterval for this source (e.g., "30s", "5m").
	// When empty, spec.pollInterval is used.
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`
}

// Jira discovers issues from a Jira project.
// Authentication is provided via a Secret referenced in the TaskSpawner's
// namespace. The secret must contain a "JIRA_TOKEN" key. For Jira Cloud,
// include a "JIRA_USER" key with the email address to use Basic auth
// (email + API token). For Jira Data Center/Server, omit "JIRA_USER" to
// use Bearer token auth with a personal access token (PAT).
type Jira struct {
	// BaseURL is the Jira instance URL (e.g., "https://mycompany.atlassian.net").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^https?://.+"
	BaseURL string `json:"baseUrl"`

	// Project is the Jira project key (e.g., "PROJ").
	// +kubebuilder:validation:Required
	Project string `json:"project"`

	// JQL is an optional JQL filter appended to the default query.
	// When set, the full query is: "project = <project> AND (<jql>)".
	// When empty, all issues in the project are discovered.
	// +optional
	JQL string `json:"jql,omitempty"`

	// SecretRef references a Secret containing a "JIRA_TOKEN" key (required)
	// and an optional "JIRA_USER" key. When "JIRA_USER" is present, Basic
	// auth is used (Jira Cloud). When absent, Bearer token auth is used
	// (Jira Data Center/Server PAT).
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// PollInterval overrides spec.pollInterval for this source (e.g., "30s", "5m").
	// When empty, spec.pollInterval is used.
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`
}

// TaskTemplateMetadata holds optional labels and annotations for spawned Tasks.
type TaskTemplateMetadata struct {
	// Labels are merged into the spawned Task's labels. Values support Go
	// text/template with the same variables as branch and promptTemplate.
	// The kelos.dev/taskspawner label is always set to the TaskSpawner name
	// and overrides any user value for that key.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are merged into the spawned Task's annotations. Values
	// support Go text/template with the same variables as branch and
	// promptTemplate. Values from the GitHub source (e.g. kelos.dev/source-kind)
	// are applied after rendering and override reserved keys on conflict.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// TaskTemplate defines the template for spawned Tasks.
type TaskTemplate struct {
	// Type specifies the agent type (e.g., claude-code).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=claude-code;codex;gemini;opencode;cursor
	Type string `json:"type"`

	// Credentials specifies how to authenticate with the agent.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.type == 'none' || has(self.secretRef)",message="secretRef is required for api-key and oauth credential types"
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
	// Required when using githubIssues or githubPullRequests source; optional
	// for other sources.
	// When set, spawned Tasks inherit this workspace reference.
	// +optional
	WorkspaceRef *WorkspaceReference `json:"workspaceRef,omitempty"`

	// AgentConfigRef references an AgentConfig resource.
	// When set, spawned Tasks inherit this agent config reference.
	// +optional
	AgentConfigRef *AgentConfigReference `json:"agentConfigRef,omitempty"`

	// DependsOn lists Task names that spawned Tasks depend on.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Branch is the git branch spawned Tasks should work on.
	// Supports Go text/template variables from the work item, e.g. "kelos-task-{{.Number}}".
	// Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}
	// GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}
	// GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}
	// Cron sources: {{.Time}}, {{.Schedule}}
	// +optional
	Branch string `json:"branch,omitempty"`

	// PromptTemplate is a Go text/template for rendering the task prompt.
	// Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}
	// GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}
	// GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}
	// Cron sources: {{.Time}}, {{.Schedule}}
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

	// Metadata holds optional labels and annotations for spawned Tasks.
	// +optional
	Metadata *TaskTemplateMetadata `json:"metadata,omitempty"`

	// UpstreamRepo is the upstream repository in "owner/repo" format.
	// When set, spawned Tasks inherit this value and inject
	// KELOS_UPSTREAM_REPO into the agent container. This is typically
	// derived automatically from githubIssues.repo or
	// githubPullRequests.repo by the spawner, but can be set explicitly.
	// +optional
	UpstreamRepo string `json:"upstreamRepo,omitempty"`
}

// TaskSpawnerSpec defines the desired state of TaskSpawner.
// +kubebuilder:validation:XValidation:rule="!(has(self.when.githubIssues) || has(self.when.githubPullRequests)) || has(self.taskTemplate.workspaceRef)",message="taskTemplate.workspaceRef is required when using githubIssues or githubPullRequests source"
type TaskSpawnerSpec struct {
	// When defines the conditions that trigger task spawning.
	// +kubebuilder:validation:Required
	When When `json:"when"`

	// TaskTemplate defines the template for spawned Tasks.
	// +kubebuilder:validation:Required
	TaskTemplate TaskTemplate `json:"taskTemplate"`

	// PollInterval is how often to poll the source for new items (e.g., "5m"). Defaults to "5m".
	// Deprecated: use per-source pollInterval (e.g., spec.when.githubIssues.pollInterval) instead.
	// +kubebuilder:default="5m"
	// +optional
	PollInterval string `json:"pollInterval,omitempty"`

	// MaxConcurrency limits the number of concurrently running (non-terminal) Tasks.
	// When the limit is reached, the spawner skips creating new Tasks until
	// existing ones complete. If unset or zero, there is no concurrency limit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxConcurrency *int32 `json:"maxConcurrency,omitempty"`

	// Suspend tells the spawner to stop polling and creating tasks.
	// Existing running Tasks are not affected (they continue to completion).
	// When set back to false, the spawner resumes from where it left off.
	// Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Suspend *bool `json:"suspend,omitempty"`

	// MaxTotalTasks limits the total number of Tasks this spawner will create
	// over its lifetime. Once reached, the spawner stops creating new Tasks
	// (but continues polling to update status). If unset or zero, there is
	// no limit. This counter persists across spawner restarts via
	// status.totalTasksCreated.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxTotalTasks *int32 `json:"maxTotalTasks,omitempty"`
}

// TaskSpawnerStatus defines the observed state of TaskSpawner.
type TaskSpawnerStatus struct {
	// Phase represents the current phase of the TaskSpawner.
	// +optional
	Phase TaskSpawnerPhase `json:"phase,omitempty"`

	// DeploymentName is the name of the Deployment running the spawner.
	// Set for polling-based sources (GitHub Issues, Jira).
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// CronJobName is the name of the CronJob running the spawner.
	// Set for cron-based sources.
	// +optional
	CronJobName string `json:"cronJobName,omitempty"`

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
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.taskTemplate.workspaceRef.name`
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=`.spec.suspend`
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
