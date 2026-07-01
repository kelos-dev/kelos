package controller

import (
	"strings"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// MergeAgentConfigs merges multiple AgentConfigSpecs in order.
// agentsMD values are concatenated with "\n\n", plugins and skills are
// appended, and mcpServers are appended with later entries winning on
// name collision. Returns nil if the input slice is empty.
func MergeAgentConfigs(configs []kelos.AgentConfigSpec) *kelos.AgentConfigSpec {
	if len(configs) == 0 {
		return nil
	}
	if len(configs) == 1 {
		result := configs[0]
		return &result
	}

	merged := kelos.AgentConfigSpec{}

	var mdParts []string
	for _, c := range configs {
		if c.AgentsMD != "" {
			mdParts = append(mdParts, c.AgentsMD)
		}
	}
	merged.AgentsMD = strings.Join(mdParts, "\n\n")

	for _, c := range configs {
		merged.Plugins = append(merged.Plugins, c.Plugins...)
	}

	for _, c := range configs {
		merged.Skills = append(merged.Skills, c.Skills...)
	}

	seen := make(map[string]int)
	for _, c := range configs {
		for _, server := range c.MCPServers {
			if idx, exists := seen[server.Name]; exists {
				merged.MCPServers[idx] = server
			} else {
				seen[server.Name] = len(merged.MCPServers)
				merged.MCPServers = append(merged.MCPServers, server)
			}
		}
	}

	return &merged
}

// ResolveAgentConfigRefs returns the effective list of AgentConfigReference
// values from a TaskSpec, preferring spec.worker.agentConfigRefs over the
// legacy spec.agentConfigRefs field.
func ResolveAgentConfigRefs(spec *kelos.TaskSpec) []kelos.AgentConfigReference {
	if spec.Worker != nil && len(spec.Worker.AgentConfigRefs) > 0 {
		return spec.Worker.AgentConfigRefs
	}
	if len(spec.AgentConfigRefs) > 0 {
		return spec.AgentConfigRefs
	}
	return nil
}
