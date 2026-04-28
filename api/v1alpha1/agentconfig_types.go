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

	// Plugins defines plugin bundles containing skills and agents.
	// Each plugin is mounted as a directory and installed using the
	// agent's native mechanism (e.g., --plugin-dir for Claude Code,
	// ~/.codex/skills for Codex, extensions for Gemini).
	// +optional
	Plugins []PluginSpec `json:"plugins,omitempty"`

	// Skills defines skills.sh packages to install into the plugin volume.
	// Each entry references a package in owner/repo format from the skills.sh
	// ecosystem, installed via "npx skills add" in an init container.
	// +optional
	Skills []SkillsShSpec `json:"skills,omitempty"`

	// MCPServers defines MCP (Model Context Protocol) servers to make
	// available to the agent. Each entry is written to the agent's native
	// MCP configuration (e.g., ~/.claude.json for Claude Code).
	// +optional
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

// PluginSpec defines a plugin bundle containing skills and agents.
type PluginSpec struct {
	// Name is the plugin name. Used as the plugin directory name
	// and for namespacing in Claude Code (e.g., <name>:skill-name).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Skills defines skills for this plugin.
	// Each becomes skills/<name>/SKILL.md in the plugin directory.
	// +optional
	Skills []SkillDefinition `json:"skills,omitempty"`

	// Agents defines sub-agents for this plugin.
	// Each becomes agents/<name>.md in the plugin directory.
	// +optional
	Agents []AgentDefinition `json:"agents,omitempty"`
}

// SkillDefinition defines a skill within a plugin.
type SkillDefinition struct {
	// +kubebuilder:validation:MinLength=1
	Name    string `json:"name"`
	Content string `json:"content"`
}

// AgentDefinition defines a sub-agent within a plugin.
type AgentDefinition struct {
	// +kubebuilder:validation:MinLength=1
	Name    string `json:"name"`
	Content string `json:"content"`
}

// SkillsShSpec defines a skills.sh package reference.
type SkillsShSpec struct {
	// Source is the skills.sh package in owner/repo format
	// (e.g., "vercel-labs/agent-skills").
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`

	// Skill selects a specific skill by name from the package.
	// If empty, all skills in the package are installed.
	// +optional
	Skill string `json:"skill,omitempty"`
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

	// HeadersFrom references a Secret whose data keys are header names
	// and values are header values. Only used when type is "http" or "sse".
	// Values from HeadersFrom take precedence over inline Headers for
	// overlapping keys.
	// +optional
	HeadersFrom *SecretValuesSource `json:"headersFrom,omitempty"`

	// Env are environment variables for the server process.
	// Only used when type is "stdio".
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// EnvFrom references a Secret whose data keys are environment variable
	// names and values are environment variable values. Only used when
	// type is "stdio". Values from EnvFrom take precedence over inline Env
	// for overlapping keys.
	// +optional
	EnvFrom *SecretValuesSource `json:"envFrom,omitempty"`

	// WorkingDir is the working directory in which a stdio MCP server
	// command is spawned. Only used when type is "stdio". Useful for
	// servers that must run from a specific project root (for example,
	// "php artisan boost:mcp" requires the Laravel project root as cwd).
	// Relative paths are resolved by the agent against its own current
	// working directory; absolute paths are recommended.
	// +optional
	WorkingDir string `json:"cwd,omitempty"`
}

// SecretValuesSource selects a Secret to populate values from.
type SecretValuesSource struct {
	// SecretRef references the Secret to read data from.
	SecretRef SecretReference `json:"secretRef"`
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
