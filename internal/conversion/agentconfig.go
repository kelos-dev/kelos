package conversion

import (
	"context"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	preservedMCPValueFromEnvAnnotation = "kelos.dev/v1alpha2-mcp-value-from-env"
	preservedSkillsSecretRefAnnotation = "kelos.dev/v1alpha2-skills-secret-ref"
	preservedSkillsOptionalAnnotation  = "kelos.dev/v1alpha2-skills-optional"
)

type preservedMCPValueFromEnv struct {
	Index int             `json:"index"`
	Name  string          `json:"name"`
	Env   []corev1.EnvVar `json:"env"`
}

type preservedSkillsSecretRef struct {
	Index     int                      `json:"index"`
	Source    string                   `json:"source"`
	Skill     string                   `json:"skill,omitempty"`
	SecretRef v1alpha2.SecretReference `json:"secretRef"`
}

type preservedSkillsOptional struct {
	Index  int    `json:"index"`
	Source string `json:"source"`
	Skill  string `json:"skill,omitempty"`
}

func AgentConfigToV1alpha2(ctx context.Context, src *v1alpha1.AgentConfig, dst *v1alpha2.AgentConfig) error {
	return agentConfigToHub(ctx, src, dst)
}

func AgentConfigFromV1alpha2(ctx context.Context, src *v1alpha2.AgentConfig, dst *v1alpha1.AgentConfig) error {
	return agentConfigFromHub(ctx, src, dst)
}

func agentConfigToHub(_ context.Context, src *v1alpha1.AgentConfig, dst *v1alpha2.AgentConfig) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	dst.Spec.AgentsMD = src.Spec.AgentsMD
	dst.Spec.Plugins = pluginsToV1alpha2(src.Spec.Plugins)
	dst.Spec.Skills = skillsToV1alpha2(src.Spec.Skills)
	dst.Spec.MCPServers = mcpServersToV1alpha2(src.Spec.MCPServers)
	if err := restorePreservedMCPValueFromEnv(src.Annotations, dst.Spec.MCPServers); err != nil {
		return err
	}
	if err := restorePreservedSkillsSecretRefs(src.Annotations, dst.Spec.Skills); err != nil {
		return err
	}
	if err := restorePreservedSkillsOptional(src.Annotations, dst.Spec.Skills); err != nil {
		return err
	}
	deleteAnnotation(dst.Annotations, preservedMCPValueFromEnvAnnotation)
	deleteAnnotation(dst.Annotations, preservedSkillsSecretRefAnnotation)
	deleteAnnotation(dst.Annotations, preservedSkillsOptionalAnnotation)
	return nil
}

func agentConfigFromHub(_ context.Context, src *v1alpha2.AgentConfig, dst *v1alpha1.AgentConfig) error {
	src.ObjectMeta.DeepCopyInto(&dst.ObjectMeta)
	dst.Spec.AgentsMD = src.Spec.AgentsMD
	dst.Spec.Plugins = pluginsFromV1alpha2(src.Spec.Plugins)
	dst.Spec.Skills = skillsFromV1alpha2(src.Spec.Skills)
	dst.Spec.MCPServers = mcpServersFromV1alpha2(src.Spec.MCPServers)
	if err := setPreservedMCPValueFromEnvAnnotation(dst, src.Spec.MCPServers); err != nil {
		return err
	}
	if err := setPreservedSkillsSecretRefAnnotation(dst, src.Spec.Skills); err != nil {
		return err
	}
	return setPreservedSkillsOptionalAnnotation(dst, src.Spec.Skills)
}

func mcpServersToV1alpha2(in []v1alpha1.MCPServerSpec) []v1alpha2.MCPServerSpec {
	if in == nil {
		return nil
	}
	out := make([]v1alpha2.MCPServerSpec, len(in))
	for i, s := range in {
		out[i] = v1alpha2.MCPServerSpec{
			Name:        s.Name,
			Type:        s.Type,
			Command:     s.Command,
			Args:        copyStrings(s.Args),
			URL:         s.URL,
			Headers:     copyStringMap(s.Headers),
			HeadersFrom: secretValuesSourceToV1alpha2(s.HeadersFrom),
			Env:         envMapToList(s.Env),
			EnvFrom:     secretValuesSourceToV1alpha2(s.EnvFrom),
		}
	}
	return out
}

func mcpServersFromV1alpha2(in []v1alpha2.MCPServerSpec) []v1alpha1.MCPServerSpec {
	if in == nil {
		return nil
	}
	out := make([]v1alpha1.MCPServerSpec, len(in))
	for i, s := range in {
		out[i] = v1alpha1.MCPServerSpec{
			Name:        s.Name,
			Type:        s.Type,
			Command:     s.Command,
			Args:        copyStrings(s.Args),
			URL:         s.URL,
			Headers:     copyStringMap(s.Headers),
			HeadersFrom: secretValuesSourceFromV1alpha2(s.HeadersFrom),
			Env:         envListToMap(s.Env),
			EnvFrom:     secretValuesSourceFromV1alpha2(s.EnvFrom),
		}
	}
	return out
}

func envMapToList(m map[string]string) []corev1.EnvVar {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: m[k]})
	}
	return out
}

func envListToMap(list []corev1.EnvVar) map[string]string {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]string, len(list))
	for _, e := range list {
		if e.ValueFrom != nil {
			continue
		}
		out[e.Name] = e.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func setPreservedMCPValueFromEnvAnnotation(dst *v1alpha1.AgentConfig, servers []v1alpha2.MCPServerSpec) error {
	preserved := collectMCPValueFromEnv(servers)
	if len(preserved) == 0 {
		deleteAnnotation(dst.Annotations, preservedMCPValueFromEnvAnnotation)
		return nil
	}
	data, err := json.Marshal(preserved)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedMCPValueFromEnvAnnotation] = string(data)
	return nil
}

func collectMCPValueFromEnv(servers []v1alpha2.MCPServerSpec) []preservedMCPValueFromEnv {
	var preserved []preservedMCPValueFromEnv
	for i, server := range servers {
		var env []corev1.EnvVar
		for _, item := range server.Env {
			if item.ValueFrom == nil {
				continue
			}
			env = append(env, *item.DeepCopy())
		}
		if len(env) > 0 {
			preserved = append(preserved, preservedMCPValueFromEnv{
				Index: i,
				Name:  server.Name,
				Env:   env,
			})
		}
	}
	return preserved
}

func restorePreservedMCPValueFromEnv(annotations map[string]string, servers []v1alpha2.MCPServerSpec) error {
	raw := annotations[preservedMCPValueFromEnvAnnotation]
	if raw == "" {
		return nil
	}
	var preserved []preservedMCPValueFromEnv
	if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
		// The annotation is best-effort preservation data and can be set by
		// users; malformed data must not block API version conversion.
		return nil
	}
	for _, item := range preserved {
		index, ok := preservedServerIndex(servers, item)
		if !ok {
			continue
		}
		restoreServerValueFromEnv(&servers[index], item.Env)
	}
	return nil
}

func preservedServerIndex(servers []v1alpha2.MCPServerSpec, item preservedMCPValueFromEnv) (int, bool) {
	if item.Index >= 0 && item.Index < len(servers) && servers[item.Index].Name == item.Name {
		return item.Index, true
	}
	if item.Name == "" {
		return 0, false
	}
	var found int
	count := 0
	for i, server := range servers {
		if server.Name != item.Name {
			continue
		}
		found = i
		count++
	}
	if count != 1 {
		return 0, false
	}
	return found, true
}

func restoreServerValueFromEnv(server *v1alpha2.MCPServerSpec, preserved []corev1.EnvVar) {
	existing := map[string]struct{}{}
	for _, item := range server.Env {
		existing[item.Name] = struct{}{}
	}
	for _, item := range preserved {
		if item.ValueFrom == nil {
			continue
		}
		if _, ok := existing[item.Name]; ok {
			continue
		}
		server.Env = append(server.Env, *item.DeepCopy())
		existing[item.Name] = struct{}{}
	}
}

func setPreservedSkillsSecretRefAnnotation(dst *v1alpha1.AgentConfig, skills []v1alpha2.SkillsShSpec) error {
	preserved := collectSkillsSecretRefs(skills)
	if len(preserved) == 0 {
		deleteAnnotation(dst.Annotations, preservedSkillsSecretRefAnnotation)
		return nil
	}
	data, err := json.Marshal(preserved)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedSkillsSecretRefAnnotation] = string(data)
	return nil
}

func collectSkillsSecretRefs(skills []v1alpha2.SkillsShSpec) []preservedSkillsSecretRef {
	var preserved []preservedSkillsSecretRef
	for i, skill := range skills {
		if skill.SecretRef == nil {
			continue
		}
		preserved = append(preserved, preservedSkillsSecretRef{
			Index:     i,
			Source:    skill.Source,
			Skill:     skill.Skill,
			SecretRef: *skill.SecretRef,
		})
	}
	return preserved
}

func restorePreservedSkillsSecretRefs(annotations map[string]string, skills []v1alpha2.SkillsShSpec) error {
	raw := annotations[preservedSkillsSecretRefAnnotation]
	if raw == "" {
		return nil
	}
	var preserved []preservedSkillsSecretRef
	if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
		return nil
	}
	for _, item := range preserved {
		index, ok := preservedSkillIndex(skills, item.Index, item.Source, item.Skill)
		if !ok {
			continue
		}
		if skills[index].SecretRef == nil {
			ref := item.SecretRef
			skills[index].SecretRef = &ref
		}
	}
	return nil
}

func setPreservedSkillsOptionalAnnotation(dst *v1alpha1.AgentConfig, skills []v1alpha2.SkillsShSpec) error {
	var preserved []preservedSkillsOptional
	for i, skill := range skills {
		if !skill.Optional {
			continue
		}
		preserved = append(preserved, preservedSkillsOptional{
			Index:  i,
			Source: skill.Source,
			Skill:  skill.Skill,
		})
	}
	if len(preserved) == 0 {
		deleteAnnotation(dst.Annotations, preservedSkillsOptionalAnnotation)
		return nil
	}
	data, err := json.Marshal(preserved)
	if err != nil {
		return err
	}
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}
	dst.Annotations[preservedSkillsOptionalAnnotation] = string(data)
	return nil
}

func restorePreservedSkillsOptional(annotations map[string]string, skills []v1alpha2.SkillsShSpec) error {
	raw := annotations[preservedSkillsOptionalAnnotation]
	if raw == "" {
		return nil
	}
	var preserved []preservedSkillsOptional
	if err := json.Unmarshal([]byte(raw), &preserved); err != nil {
		// Conversion must not be blocked by malformed preservation metadata.
		return nil
	}
	for _, item := range preserved {
		index, ok := preservedSkillIndex(skills, item.Index, item.Source, item.Skill)
		if ok {
			skills[index].Optional = true
		}
	}
	return nil
}

func preservedSkillIndex(skills []v1alpha2.SkillsShSpec, itemIndex int, itemSource, itemSkill string) (int, bool) {
	matches := func(skill v1alpha2.SkillsShSpec) bool {
		return skill.Source == itemSource && skill.Skill == itemSkill
	}
	if itemIndex >= 0 && itemIndex < len(skills) && matches(skills[itemIndex]) {
		return itemIndex, true
	}
	if itemSource == "" {
		return 0, false
	}
	var found int
	count := 0
	for i, skill := range skills {
		if !matches(skill) {
			continue
		}
		found = i
		count++
	}
	if count != 1 {
		return 0, false
	}
	return found, true
}

func deleteAnnotation(annotations map[string]string, key string) {
	if annotations == nil {
		return
	}
	delete(annotations, key)
}

func secretValuesSourceToV1alpha2(s *v1alpha1.SecretValuesSource) *v1alpha2.SecretValuesSource {
	if s == nil {
		return nil
	}
	return &v1alpha2.SecretValuesSource{
		SecretRef: v1alpha2.SecretReference{Name: s.SecretRef.Name},
	}
}

func secretValuesSourceFromV1alpha2(s *v1alpha2.SecretValuesSource) *v1alpha1.SecretValuesSource {
	if s == nil {
		return nil
	}
	return &v1alpha1.SecretValuesSource{
		SecretRef: v1alpha1.SecretReference{Name: s.SecretRef.Name},
	}
}

func pluginsToV1alpha2(in []v1alpha1.PluginSpec) []v1alpha2.PluginSpec {
	if in == nil {
		return nil
	}
	out := make([]v1alpha2.PluginSpec, len(in))
	for i, p := range in {
		out[i] = v1alpha2.PluginSpec{
			Name:   p.Name,
			Skills: skillDefsToV1alpha2(p.Skills),
			Agents: agentDefsToV1alpha2(p.Agents),
		}
	}
	return out
}

func pluginsFromV1alpha2(in []v1alpha2.PluginSpec) []v1alpha1.PluginSpec {
	if in == nil {
		return nil
	}
	out := make([]v1alpha1.PluginSpec, len(in))
	for i, p := range in {
		out[i] = v1alpha1.PluginSpec{
			Name:   p.Name,
			Skills: skillDefsFromV1alpha2(p.Skills),
			Agents: agentDefsFromV1alpha2(p.Agents),
		}
	}
	return out
}

func skillDefsToV1alpha2(in []v1alpha1.SkillDefinition) []v1alpha2.SkillDefinition {
	if in == nil {
		return nil
	}
	out := make([]v1alpha2.SkillDefinition, len(in))
	for i, s := range in {
		out[i] = v1alpha2.SkillDefinition{Name: s.Name, Content: s.Content}
	}
	return out
}

func skillDefsFromV1alpha2(in []v1alpha2.SkillDefinition) []v1alpha1.SkillDefinition {
	if in == nil {
		return nil
	}
	out := make([]v1alpha1.SkillDefinition, len(in))
	for i, s := range in {
		out[i] = v1alpha1.SkillDefinition{Name: s.Name, Content: s.Content}
	}
	return out
}

func agentDefsToV1alpha2(in []v1alpha1.AgentDefinition) []v1alpha2.AgentDefinition {
	if in == nil {
		return nil
	}
	out := make([]v1alpha2.AgentDefinition, len(in))
	for i, a := range in {
		out[i] = v1alpha2.AgentDefinition{Name: a.Name, Content: a.Content}
	}
	return out
}

func agentDefsFromV1alpha2(in []v1alpha2.AgentDefinition) []v1alpha1.AgentDefinition {
	if in == nil {
		return nil
	}
	out := make([]v1alpha1.AgentDefinition, len(in))
	for i, a := range in {
		out[i] = v1alpha1.AgentDefinition{Name: a.Name, Content: a.Content}
	}
	return out
}

func skillsToV1alpha2(in []v1alpha1.SkillsShSpec) []v1alpha2.SkillsShSpec {
	if in == nil {
		return nil
	}
	out := make([]v1alpha2.SkillsShSpec, len(in))
	for i, s := range in {
		out[i] = v1alpha2.SkillsShSpec{Source: s.Source, Skill: s.Skill}
	}
	return out
}

func skillsFromV1alpha2(in []v1alpha2.SkillsShSpec) []v1alpha1.SkillsShSpec {
	if in == nil {
		return nil
	}
	out := make([]v1alpha1.SkillsShSpec, len(in))
	for i, s := range in {
		out[i] = v1alpha1.SkillsShSpec{Source: s.Source, Skill: s.Skill}
	}
	return out
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
