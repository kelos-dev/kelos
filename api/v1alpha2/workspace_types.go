package v1alpha2

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

// WorkspaceGHProxy configures the workspace-scoped ghproxy.
type WorkspaceGHProxy struct{}

// WorkspaceClone configures how a Workspace repository is cloned.
type WorkspaceClone struct {
	// Paths limits the checkout to these repository paths. Each entry is a
	// directory or file relative to the repository root, such as "docs" or
	// "cmd/main.go", and is matched exactly from the root: "docs" selects
	// the top-level docs directory and everything in it, and nothing else
	// named docs. Only the listed paths are written to disk; files at the
	// repository root are not included unless listed. Blobs outside the
	// listed paths are not downloaded, but the .git directory still records
	// the full tree, so git can fetch the rest on demand unless GitDir is
	// Remove. Entries must be unique and must not start with "/", end with a
	// space (ignoring a trailing slash), or contain empty, "." or ".."
	// segments, control characters, or any of the characters *, ?, [ or \.
	// Up to 64 entries of at most 256 characters each. Not supported for
	// Sessions with a Session.spec.volumeClaimTemplate: such a Session fails.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +kubebuilder:validation:items:Pattern=`^[^*?\[\\\x00-\x1f\x7f]+$`
	// +kubebuilder:validation:XValidation:rule="self.all(p, !p.startsWith('/') && !p.matches(' /?$') && !p.contains('//') && !p.matches('(^|/)[.][.]?(/|$)'))",message="clone.paths entries must be relative to the repository root, must not end with a space, and must not contain empty, '.' or '..' segments"
	Paths []string `json:"paths,omitempty"`

	// GitDir selects what happens to the repository's .git directory after
	// the clone. Keep leaves it in place. Remove deletes it, leaving only the
	// checked-out files, so git commands no longer work in the checkout and
	// git there cannot fetch paths outside Paths. Remove does not keep the
	// GitHub token from the agent: with AgentGitHubToken Token the agent can
	// still clone the full repository elsewhere, so set AgentGitHubToken to
	// None as well to keep the rest of the repository out of reach.
	// Remove cannot be combined with Workspace.spec.remotes, and a Task with
	// Task.spec.branch or a Session with Session.spec.initialBranch fails,
	// because setting up the branch needs .git. Remove is not supported for
	// Sessions with a Session.spec.volumeClaimTemplate: such a Session fails.
	// Omit to use Keep.
	// +optional
	GitDir WorkspaceGitDir `json:"gitDir,omitempty"`
}

// WorkspaceGitDir selects what happens to a Workspace repository's .git
// directory after the clone.
// +kubebuilder:validation:Enum=Keep;Remove
type WorkspaceGitDir string

const (
	// WorkspaceGitDirKeep keeps the .git directory.
	WorkspaceGitDirKeep WorkspaceGitDir = "Keep"
	// WorkspaceGitDirRemove deletes the .git directory after the clone.
	WorkspaceGitDirRemove WorkspaceGitDir = "Remove"
)

// WorkspaceAgentGitHubToken selects whether the agent container receives the
// GitHub token from a Workspace's secretRef.
// +kubebuilder:validation:Enum=Token;None
type WorkspaceAgentGitHubToken string

const (
	// WorkspaceAgentGitHubTokenToken gives the agent container the GitHub
	// token.
	WorkspaceAgentGitHubTokenToken WorkspaceAgentGitHubToken = "Token"
	// WorkspaceAgentGitHubTokenNone keeps the GitHub token out of the agent
	// container.
	WorkspaceAgentGitHubTokenNone WorkspaceAgentGitHubToken = "None"
)

// WorkspaceSpec defines the desired state of Workspace.
// +kubebuilder:validation:XValidation:rule="!has(self.clone) || !has(self.clone.gitDir) || self.clone.gitDir != 'Remove' || !has(self.remotes) || size(self.remotes) == 0",message="remotes cannot be set when clone.gitDir is Remove"
// +kubebuilder:validation:XValidation:rule="!has(self.agentGitHubToken) || self.agentGitHubToken != 'None' || !has(self.ghproxy)",message="ghproxy cannot be set when agentGitHubToken is None: the workspace ghproxy forwards agent requests with the secretRef token"
type WorkspaceSpec struct {
	// Repo is the git repository URL to clone.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^(https?://|git://|git@).*"
	Repo string `json:"repo"`

	// Ref is the git reference to checkout (branch, tag, or commit SHA).
	// Defaults to the repository's default branch if not specified.
	// +optional
	Ref string `json:"ref,omitempty"`

	// Clone configures how the repository is cloned.
	// +optional
	Clone *WorkspaceClone `json:"clone,omitempty"`

	// SecretRef references a Secret containing a GITHUB_TOKEN key for git
	// authentication and GitHub CLI (gh) operations.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`

	// AgentGitHubToken selects whether the agent container receives the
	// GitHub token from SecretRef. Token gives it the token for git and the
	// GitHub CLI (gh). None uses the token only to clone the repository and
	// set up the Task or Session branch; the agent container gets no GitHub
	// token from Kelos. None cannot be combined with GHProxy, which forwards
	// requests from agent pods with the token. None does not otherwise
	// restrict the agent's network access. WorkerPools do not support None.
	// Omit to use Token.
	// +optional
	AgentGitHubToken WorkspaceAgentGitHubToken `json:"agentGitHubToken,omitempty"`

	// GHProxy configures and enables the workspace-scoped ghproxy when set.
	// +optional
	GHProxy *WorkspaceGHProxy `json:"ghproxy,omitempty"`

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
// +kubebuilder:storageversion

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
