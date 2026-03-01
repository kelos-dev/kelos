package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentConfigSpec defines the desired state of AgentConfig.
type AgentConfigSpec struct {
	// AgentsMD is written to the agent's instruction file
	// (e.g., ~/.claude/CLAUDE.md for Claude Code).
	// This is additive and does not overwrite the repo's own instruction files.
	// +optional
	AgentsMD string `json:"agentsMD,omitempty"`

	// Plugins defines Claude Code plugins to inject via --plugin-dir.
	// Each plugin is mounted as a separate plugin directory.
	// Only applicable to claude-code type agents; other agents ignore this.
	// +optional
	Plugins []PluginSpec `json:"plugins,omitempty"`

	// MCPServers defines MCP (Model Context Protocol) servers to make
	// available to the agent. Each entry is written to the agent's native
	// MCP configuration (e.g., ~/.claude.json for Claude Code).
	// +optional
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

// PluginSpec defines a Claude Code plugin bundle.
// A plugin can be defined inline (Skills/Agents) or sourced from a GitHub
// repository (GitHub). These two modes are mutually exclusive.
type PluginSpec struct {
	// Name is the plugin name. Used as the plugin directory name
	// and for namespacing in Claude Code (e.g., <name>:skill-name).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// GitHub sources the plugin from a GitHub repository.
	// When set, Skills and Agents must not be specified.
	// +optional
	GitHub *GitHubPluginSource `json:"github,omitempty"`

	// Skills defines skills for this plugin.
	// Each becomes skills/<name>/SKILL.md in the plugin directory.
	// +optional
	Skills []SkillDefinition `json:"skills,omitempty"`

	// Agents defines sub-agents for this plugin.
	// Each becomes agents/<name>.md in the plugin directory.
	// +optional
	Agents []AgentDefinition `json:"agents,omitempty"`
}

// GitHubPluginSource defines a plugin sourced from a GitHub repository.
type GitHubPluginSource struct {
	// Repo is the GitHub repository in "owner/repo" format.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[^/]+/[^/]+$`
	Repo string `json:"repo"`

	// Ref is an optional branch or tag to check out.
	// If empty, the repository's default branch is used.
	// +optional
	Ref *string `json:"ref,omitempty"`

	// Host is the GitHub hostname. Defaults to "github.com".
	// Set this for GitHub Enterprise Server instances.
	// +optional
	Host *string `json:"host,omitempty"`

	// SecretRef references a Secret containing a GITHUB_TOKEN key
	// for authenticating to private repositories.
	// If not set and the workspace has a token, the workspace token
	// is used as a fallback.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`
}

// SkillDefinition defines a Claude Code skill (slash command).
type SkillDefinition struct {
	// +kubebuilder:validation:MinLength=1
	Name    string `json:"name"`
	Content string `json:"content"`
}

// AgentDefinition defines a Claude Code sub-agent.
type AgentDefinition struct {
	// +kubebuilder:validation:MinLength=1
	Name    string `json:"name"`
	Content string `json:"content"`
}

// MCPServerSpec defines an MCP server configuration.
type MCPServerSpec struct {
	// Name identifies this MCP server. Used as the key in the
	// agent's MCP configuration.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the transport type: "stdio", "http", or "sse".
	// +kubebuilder:validation:Enum=stdio;http;sse
	Type string `json:"type"`

	// Command is the executable to run for stdio transport.
	// Required when type is "stdio".
	// +optional
	Command string `json:"command,omitempty"`

	// Args are command-line arguments for the server process.
	// Only used when type is "stdio".
	// +optional
	Args []string `json:"args,omitempty"`

	// URL is the server endpoint for http or sse transport.
	// Required when type is "http" or "sse".
	// +optional
	URL string `json:"url,omitempty"`

	// Headers are HTTP headers to include in requests.
	// Only used when type is "http" or "sse".
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// Env are environment variables for the server process.
	// Only used when type is "stdio".
	// +optional
	Env map[string]string `json:"env,omitempty"`
}

// AgentConfigReference refers to an AgentConfig resource by name.
type AgentConfigReference struct {
	// Name is the name of the AgentConfig resource.
	Name string `json:"name"`
}

// +genclient
// +genclient:noStatus
// +kubebuilder:object:root=true

// AgentConfig is the Schema for the agentconfigs API.
type AgentConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AgentConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AgentConfigList contains a list of AgentConfig.
type AgentConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentConfig{}, &AgentConfigList{})
}
