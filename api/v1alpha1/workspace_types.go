package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

	// Files are written into the cloned repository before the agent starts.
	// This can be used to inject plugin-like assets such as skills
	// (for example, ".claude/skills/<name>/SKILL.md") and instruction files
	// like "CLAUDE.md" or "AGENTS.md".
	// +optional
	Files []WorkspaceFile `json:"files,omitempty"`
}

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
