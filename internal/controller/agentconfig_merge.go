package controller

import (
	"fmt"
	"strings"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type namedAgentConfigSpec struct {
	Name string
	Spec kelos.AgentConfigSpec
}

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
		if c.Kanon != nil {
			kanon := *c.Kanon
			merged.Kanon = &kanon
		}
	}

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

func validateAgentConfigSpecs(configs []namedAgentConfigSpec) error {
	var kanonNames []string
	var inlineNames []string
	for _, c := range configs {
		hasKanon := c.Spec.Kanon != nil
		hasInline := hasInlineAgentConfig(c.Spec)
		if hasKanon {
			kanonNames = append(kanonNames, c.Name)
		}
		if hasInline {
			inlineNames = append(inlineNames, c.Name)
		}
		if hasKanon && hasInline {
			return fmt.Errorf("agentConfig %q: spec.kanon is mutually exclusive with agentsMD, plugins, skills, and mcpServers", c.Name)
		}
	}
	if len(kanonNames) > 1 {
		return fmt.Errorf("multiple Kanon AgentConfigs are not supported: %s", strings.Join(kanonNames, ", "))
	}
	if len(kanonNames) == 1 && len(inlineNames) > 0 {
		return fmt.Errorf("Kanon AgentConfig %q cannot be combined with inline AgentConfigs: %s", kanonNames[0], strings.Join(inlineNames, ", "))
	}
	return nil
}

func hasInlineAgentConfig(spec kelos.AgentConfigSpec) bool {
	return spec.AgentsMD != "" ||
		len(spec.Plugins) > 0 ||
		len(spec.Skills) > 0 ||
		len(spec.MCPServers) > 0
}

// ResolveAgentConfigRefs returns the effective list of AgentConfigReference
// values from a TaskSpec.
func ResolveAgentConfigRefs(spec *kelos.TaskSpec) []kelos.AgentConfigReference {
	if len(spec.AgentConfigRefs) > 0 {
		return spec.AgentConfigRefs
	}
	return nil
}
