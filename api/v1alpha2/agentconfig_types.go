package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentConfigSpec defines the desired state of AgentConfig.
//
// +kubebuilder:validation:XValidation:rule="!has(self.kanon) || ((!has(self.agentsMD) || size(self.agentsMD) == 0) && (!has(self.plugins) || size(self.plugins) == 0) && (!has(self.skills) || size(self.skills) == 0) && (!has(self.mcpServers) || size(self.mcpServers) == 0))",message="kanon is mutually exclusive with agentsMD, plugins, skills, and mcpServers"
type AgentConfigSpec struct {
	// AgentsMD is written to the agent's instruction file
	// (e.g., ~/.claude/CLAUDE.md for Claude Code).
	// This is additive and does not overwrite the repo's own instruction files.
	// +optional
	AgentsMD string `json:"agentsMD,omitempty"`

	// Kanon references a Kanon source repository. When set, the Task pod
	// clones the repository and applies its root kanon.yaml to the agent's
	// user-level configuration before the agent starts.
	// Mutually exclusive with AgentsMD, Plugins, Skills, and MCPServers.
	// +optional
	Kanon *KanonSourceSpec `json:"kanon,omitempty"`

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

// KanonSourceSpec references a Kanon source repository.
type KanonSourceSpec struct {
	// Repo is the Git repository URL containing kanon.yaml at its root.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Ref is an optional branch, tag, or commit to check out.
	// +optional
	Ref string `json:"ref,omitempty"`

	// SecretRef references a Secret containing GITHUB_TOKEN for cloning
	// private Kanon source repositories.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`
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
//
// +kubebuilder:validation:XValidation:rule="self.type != 'stdio' || (has(self.command) && size(self.command) > 0)",message="command is required when type is stdio"
// +kubebuilder:validation:XValidation:rule="!(self.type == 'http' || self.type == 'sse') || (has(self.url) && size(self.url) > 0)",message="url is required when type is http or sse"
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
	//
	// Each entry must set Name and either Value (a literal string) or
	// ValueFrom (a reference to a key in a Secret or ConfigMap). Only the
	// SecretKeyRef and ConfigMapKeyRef variants of ValueFrom are honored;
	// all other variants (FieldRef, ResourceFieldRef, FileKeyRef, and any
	// future EnvVarSource additions) are pod-scoped or otherwise meaningless
	// for an MCP server process. They cannot be enforced in the CRD schema
	// (a CEL rule over a []EnvVar exceeds the API server cost budget), so an
	// unsupported variant is rejected when a Task consumes the server, which
	// fails the Task rather than the AgentConfig apply.
	//
	// When ValueFrom is marked optional and the referenced Secret/ConfigMap
	// or key is missing, the variable is omitted (matching kubelet
	// semantics for pod env), not set to an empty string.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// EnvFrom references a Secret whose data keys are environment variable
	// names and values are environment variable values. Only used when
	// type is "stdio". Values from EnvFrom take precedence over inline Env
	// for overlapping keys (note this is the opposite of pod-spec ordering,
	// where env overrides envFrom).
	// +optional
	EnvFrom *SecretValuesSource `json:"envFrom,omitempty"`
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
// +kubebuilder:storageversion

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
