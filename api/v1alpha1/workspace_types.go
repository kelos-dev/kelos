package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GitRemote defines an additional git remote to configure in the cloned
// repository after the initial clone.
type GitRemote struct {
	// Name is the git remote name (must not be "origin").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is the git remote URL.
	// +kubebuilder:validation:Pattern="^(https?://|git://|git@).*"
	URL string `json:"url"`
}

// WorkspaceFile defines a file to write into the cloned repository before the
// agent container starts.
type WorkspaceFile struct {
	// Path is the relative file path inside the repository (for example,
	// ".claude/skills/reviewer/SKILL.md" or "CLAUDE.md").
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// Content is the file content to write.
	Content string `json:"content"`
}

// WorkspaceSpec defines the desired state of Workspace.
type WorkspaceSpec struct {
	// Repo is the git repository URL to clone.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^(https?://|git://|git@).*"
	Repo string `json:"repo"`

	// Ref is the git reference to checkout (branch, tag, or commit SHA).
	// Defaults to the repository's default branch if not specified.
	// +optional
	Ref string `json:"ref,omitempty"`

	// SecretRef references a Secret containing a GITHUB_TOKEN key for git
	// authentication and GitHub CLI (gh) operations.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`

	// Remotes are additional git remotes to configure after cloning.
	// The credential from SecretRef applies to all remotes.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(r, r.name != 'origin')",message="remote name 'origin' is reserved for the clone source"
	// +kubebuilder:validation:XValidation:rule="self.map(r, r.name).size() == self.size()",message="remote names must be unique"
	Remotes []GitRemote `json:"remotes,omitempty"`

	// Files are written into the cloned repository before the agent starts.
	// This can be used to inject plugin-like assets such as skills
	// (for example, ".claude/skills/<name>/SKILL.md") and instruction files
	// like "CLAUDE.md" or "AGENTS.md".
	// +optional
	Files []WorkspaceFile `json:"files,omitempty"`

	// SetupCommand is executed in the agent container after the workspace
	// is prepared and before the agent process starts. It runs in the
	// repository root (/workspace/repo) as the agent UID. A non-zero exit
	// fails the Task.
	//
	// The slice is exec-form, matching Kubernetes container.command and
	// lifecycle.postStart.exec.command: it is passed directly to exec
	// with no shell interpretation. Use ["sh", "-c", "<script>"] for
	// shell pipelines, environment expansion, or multi-step scripts.
	//
	// Setup runs after the repo has been cloned, the Ref or Task Branch
	// has been checked out, additional Remotes have been configured,
	// and Files have been written. Secrets reach the setup command via
	// the same env-injection path used by the agent (built-in env vars
	// and PodOverrides.Env).
	// +optional
	// +kubebuilder:validation:MinItems=1
	SetupCommand []string `json:"setupCommand,omitempty"`
}

// +genclient
// +genclient:noStatus
// +kubebuilder:object:root=true

// Workspace is the Schema for the workspaces API.
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace.
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}

// WorkspaceReference refers to a Workspace resource by name.
type WorkspaceReference struct {
	// Name is the name of the Workspace resource.
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&Workspace{}, &WorkspaceList{})
}
