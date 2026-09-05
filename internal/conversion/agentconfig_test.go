package conversion

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestAgentConfigToHub_EnvMapToSortedList(t *testing.T) {
	src := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha1.AgentConfigSpec{
			MCPServers: []v1alpha1.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env:  map[string]string{"B": "2", "A": "1"},
				},
			},
		},
	}

	dst := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	want := []corev1.EnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	if !reflect.DeepEqual(dst.Spec.MCPServers[0].Env, want) {
		t.Errorf("Env = %#v, want %#v (sorted)", dst.Spec.MCPServers[0].Env, want)
	}
	if dst.Name != "cfg" || dst.Namespace != "default" {
		t.Errorf("ObjectMeta not copied: %q/%q", dst.Namespace, dst.Name)
	}
}

func TestAgentConfigFromHub_EnvListToMap_DropsValueFrom(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha2.AgentConfigSpec{
			MCPServers: []v1alpha2.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env: []corev1.EnvVar{
						{Name: "LITERAL", Value: "x"},
						{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
								Key:                  "k",
							},
						}},
					},
				},
			},
		},
	}

	dst := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}

	got := dst.Spec.MCPServers[0].Env
	want := map[string]string{"LITERAL": "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Env = %#v, want %#v (valueFrom entry dropped)", got, want)
	}
	if dst.Annotations[preservedMCPValueFromEnvAnnotation] == "" {
		t.Fatal("expected valueFrom env to be preserved in round-trip annotation")
	}
}

func TestAgentConfigToHub_SkillsSecretRefNilForV1alpha1(t *testing.T) {
	src := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha1.AgentConfigSpec{
			Skills: []v1alpha1.SkillsShSpec{{Source: "owner/repo", Skill: "deploy"}},
		},
	}

	dst := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	if len(dst.Spec.Skills) != 1 {
		t.Fatalf("len(Skills) = %d, want 1", len(dst.Spec.Skills))
	}
	if dst.Spec.Skills[0].SecretRef != nil {
		t.Errorf("Skills[0].SecretRef = %#v, want nil", dst.Spec.Skills[0].SecretRef)
	}
}

func TestAgentConfigFromHub_PreservesSkillsSecretRefAnnotation(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha2.AgentConfigSpec{
			Skills: []v1alpha2.SkillsShSpec{{
				Source:    "owner/private-skills",
				Skill:     "deploy",
				SecretRef: &v1alpha2.SecretReference{Name: "skills-token"},
			}},
		},
	}

	dst := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}

	if got := dst.Spec.Skills; len(got) != 1 || got[0].Source != "owner/private-skills" || got[0].Skill != "deploy" {
		t.Errorf("Skills = %#v, want source and skill preserved", got)
	}
	if dst.Annotations[preservedSkillsSecretRefAnnotation] == "" {
		t.Fatal("expected skills secretRef to be preserved in round-trip annotation")
	}
}

func TestAgentConfigRoundTrip_PreservesSkillsSecretRef(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha2.AgentConfigSpec{
			Skills: []v1alpha2.SkillsShSpec{{
				Source:    "owner/private-skills",
				Skill:     "deploy",
				SecretRef: &v1alpha2.SecretReference{Name: "skills-token"},
			}},
		},
	}

	spoke := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, spoke); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}
	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	if len(hub.Spec.Skills) != 1 {
		t.Fatalf("len(Skills) = %d, want 1", len(hub.Spec.Skills))
	}
	if hub.Spec.Skills[0].SecretRef == nil || hub.Spec.Skills[0].SecretRef.Name != "skills-token" {
		t.Fatalf("Skills[0].SecretRef = %#v, want skills-token", hub.Spec.Skills[0].SecretRef)
	}
	if _, ok := hub.Annotations[preservedSkillsSecretRefAnnotation]; ok {
		t.Errorf("hub annotation %q should be removed after restore", preservedSkillsSecretRefAnnotation)
	}
	if spoke.Annotations[preservedSkillsSecretRefAnnotation] == "" {
		t.Errorf("source spoke annotation %q should not be mutated during restore", preservedSkillsSecretRefAnnotation)
	}
}

func TestAgentConfigRoundTrip_PreservesOptionalSkill(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha2.AgentConfigSpec{
			Skills: []v1alpha2.SkillsShSpec{{Source: "owner/optional", Optional: true}},
		},
	}

	spoke := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, spoke); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}
	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	if len(hub.Spec.Skills) != 1 || !hub.Spec.Skills[0].Optional {
		t.Fatalf("Skills = %#v, want one optional skill", hub.Spec.Skills)
	}
}

func TestAgentConfigRoundTrip_DoesNotRestoreReorderedDuplicateOptionalSkill(t *testing.T) {
	spoke := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedSkillsOptionalAnnotation: `[{"index":2,"source":"owner/optional","skill":"deploy"}]`,
			},
		},
		Spec: v1alpha1.AgentConfigSpec{
			Skills: []v1alpha1.SkillsShSpec{
				{Source: "owner/optional", Skill: "deploy"},
				{Source: "owner/optional", Skill: "deploy"},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	for i, skill := range hub.Spec.Skills {
		if skill.Optional {
			t.Errorf("skill %d Optional = true, want no ambiguous restore", i)
		}
	}
}

func TestAgentConfigRoundTrip_DoesNotRestoreReorderedDuplicateSkill(t *testing.T) {
	spoke := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedSkillsSecretRefAnnotation: `[{"index":2,"source":"owner/private-skills","skill":"deploy","secretRef":{"name":"skills-token"}}]`,
			},
		},
		Spec: v1alpha1.AgentConfigSpec{
			Skills: []v1alpha1.SkillsShSpec{
				{Source: "owner/private-skills", Skill: "deploy"},
				{Source: "owner/private-skills", Skill: "deploy"},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	for i, skill := range hub.Spec.Skills {
		if skill.SecretRef != nil {
			t.Errorf("skill %d secretRef = %#v, want no ambiguous restore", i, skill.SecretRef)
		}
	}
}

func TestAgentConfigRoundTrip_PreservesValueFromEnv(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha2.AgentConfigSpec{
			MCPServers: []v1alpha2.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env: []corev1.EnvVar{
						{Name: "LITERAL", Value: "x"},
						{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
								Key:                  "k",
							},
						}},
					},
				},
			},
		},
	}

	spoke := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, spoke); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}
	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	if !reflect.DeepEqual(src.Spec.MCPServers[0].Env, hub.Spec.MCPServers[0].Env) {
		t.Errorf("Env round-trip mismatch:\n got  = %#v\n want = %#v", hub.Spec.MCPServers[0].Env, src.Spec.MCPServers[0].Env)
	}
	if _, ok := hub.Annotations[preservedMCPValueFromEnvAnnotation]; ok {
		t.Errorf("hub annotation %q should be removed after restore", preservedMCPValueFromEnvAnnotation)
	}
	if spoke.Annotations[preservedMCPValueFromEnvAnnotation] == "" {
		t.Errorf("source spoke annotation %q should not be mutated during restore", preservedMCPValueFromEnvAnnotation)
	}
}

func TestAgentConfigFromHub_DoesNotMutateSourceAnnotations(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cfg",
			Namespace:   "default",
			Annotations: map[string]string{"existing": "keep"},
		},
		Spec: v1alpha2.AgentConfigSpec{
			MCPServers: []v1alpha2.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env: []corev1.EnvVar{
						{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
								Key:                  "k",
							},
						}},
					},
				},
			},
		},
	}

	dst := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}

	if src.Annotations[preservedMCPValueFromEnvAnnotation] != "" {
		t.Errorf("source hub annotation %q should not be added during down-conversion", preservedMCPValueFromEnvAnnotation)
	}
	if dst.Annotations[preservedMCPValueFromEnvAnnotation] == "" {
		t.Errorf("destination spoke annotation %q should be set", preservedMCPValueFromEnvAnnotation)
	}
}

func TestAgentConfigRoundTrip_LiteralEnvOverridesPreservedValueFrom(t *testing.T) {
	spoke := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedMCPValueFromEnvAnnotation: `[{"index":0,"name":"local","env":[{"name":"TOKEN","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]}]`,
			},
		},
		Spec: v1alpha1.AgentConfigSpec{
			MCPServers: []v1alpha1.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env:  map[string]string{"TOKEN": "literal"},
				},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	want := []corev1.EnvVar{{Name: "TOKEN", Value: "literal"}}
	if !reflect.DeepEqual(hub.Spec.MCPServers[0].Env, want) {
		t.Errorf("Env = %#v, want literal override %#v", hub.Spec.MCPServers[0].Env, want)
	}
}

func TestAgentConfigToHub_IgnoresMalformedPreservedValueFromAnnotation(t *testing.T) {
	spoke := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedMCPValueFromEnvAnnotation: "not json",
			},
		},
		Spec: v1alpha1.AgentConfigSpec{
			MCPServers: []v1alpha1.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env:  map[string]string{"TOKEN": "literal"},
				},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	want := []corev1.EnvVar{{Name: "TOKEN", Value: "literal"}}
	if !reflect.DeepEqual(hub.Spec.MCPServers[0].Env, want) {
		t.Errorf("Env = %#v, want literal env %#v", hub.Spec.MCPServers[0].Env, want)
	}
	if _, ok := hub.Annotations[preservedMCPValueFromEnvAnnotation]; ok {
		t.Errorf("hub annotation %q should be removed after ignoring malformed data", preservedMCPValueFromEnvAnnotation)
	}
}

func TestAgentConfigRoundTrip_RestoreValueFromAfterServerReorder(t *testing.T) {
	spoke := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedMCPValueFromEnvAnnotation: `[{"index":1,"name":"local","env":[{"name":"TOKEN","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]}]`,
			},
		},
		Spec: v1alpha1.AgentConfigSpec{
			MCPServers: []v1alpha1.MCPServerSpec{
				{Name: "local", Type: "stdio"},
				{Name: "other", Type: "stdio"},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	want := []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
			Key:                  "k",
		},
	}}}
	if !reflect.DeepEqual(hub.Spec.MCPServers[0].Env, want) {
		t.Errorf("Env = %#v, want restored valueFrom %#v", hub.Spec.MCPServers[0].Env, want)
	}
	if len(hub.Spec.MCPServers[1].Env) != 0 {
		t.Errorf("unexpected env on other server: %#v", hub.Spec.MCPServers[1].Env)
	}
}

func TestAgentConfigRoundTrip_DoesNotRestoreReorderedDuplicateServerName(t *testing.T) {
	spoke := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				preservedMCPValueFromEnvAnnotation: `[{"index":2,"name":"local","env":[{"name":"TOKEN","valueFrom":{"secretKeyRef":{"name":"s","key":"k"}}}]}]`,
			},
		},
		Spec: v1alpha1.AgentConfigSpec{
			MCPServers: []v1alpha1.MCPServerSpec{
				{Name: "local", Type: "stdio"},
				{Name: "local", Type: "stdio"},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), spoke, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}

	for i, server := range hub.Spec.MCPServers {
		if len(server.Env) != 0 {
			t.Errorf("server %d env = %#v, want no ambiguous restore", i, server.Env)
		}
	}
}

func TestAgentConfigFromHub_EnvAllValueFrom_NilMap(t *testing.T) {
	src := &v1alpha2.AgentConfig{
		Spec: v1alpha2.AgentConfigSpec{
			MCPServers: []v1alpha2.MCPServerSpec{
				{
					Name: "local",
					Type: "stdio",
					Env: []corev1.EnvVar{
						{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
								Key:                  "k",
							},
						}},
					},
				},
			},
		},
	}

	dst := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}
	if dst.Spec.MCPServers[0].Env != nil {
		t.Errorf("Env = %#v, want nil", dst.Spec.MCPServers[0].Env)
	}
}

func TestAgentConfigRoundTrip_PreservesV1alpha1(t *testing.T) {
	orig := &v1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "ns"},
		Spec: v1alpha1.AgentConfigSpec{
			AgentsMD: "hello",
			Plugins: []v1alpha1.PluginSpec{
				{
					Name:   "p1",
					Skills: []v1alpha1.SkillDefinition{{Name: "s1", Content: "c1"}},
					Agents: []v1alpha1.AgentDefinition{{Name: "a1", Content: "ac1"}},
				},
			},
			Skills: []v1alpha1.SkillsShSpec{{Source: "owner/repo", Skill: "thing"}},
			MCPServers: []v1alpha1.MCPServerSpec{
				{
					Name:        "local",
					Type:        "stdio",
					Command:     "npx",
					Args:        []string{"-y", "pkg"},
					Env:         map[string]string{"A": "1", "B": "2"},
					EnvFrom:     &v1alpha1.SecretValuesSource{SecretRef: v1alpha1.SecretReference{Name: "bulk"}},
					Headers:     map[string]string{"X": "y"},
					HeadersFrom: &v1alpha1.SecretValuesSource{SecretRef: v1alpha1.SecretReference{Name: "hdr"}},
					URL:         "https://example.com",
				},
			},
		},
	}

	hub := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), orig, hub); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}
	got := &v1alpha1.AgentConfig{}
	if err := agentConfigFromHub(context.Background(), hub, got); err != nil {
		t.Fatalf("agentConfigFromHub() error = %v", err)
	}

	if !reflect.DeepEqual(orig.Spec, got.Spec) {
		t.Errorf("round-trip mismatch:\n orig = %#v\n got  = %#v", orig.Spec, got.Spec)
	}
}

func TestAgentConfigToHub_PreservesNonEnvFields(t *testing.T) {
	src := &v1alpha1.AgentConfig{
		Spec: v1alpha1.AgentConfigSpec{
			MCPServers: []v1alpha1.MCPServerSpec{
				{
					Name:        "local",
					Type:        "sse",
					URL:         "https://example.com",
					Headers:     map[string]string{"A": "b"},
					HeadersFrom: &v1alpha1.SecretValuesSource{SecretRef: v1alpha1.SecretReference{Name: "hdr"}},
					EnvFrom:     &v1alpha1.SecretValuesSource{SecretRef: v1alpha1.SecretReference{Name: "bulk"}},
				},
			},
		},
	}
	dst := &v1alpha2.AgentConfig{}
	if err := agentConfigToHub(context.Background(), src, dst); err != nil {
		t.Fatalf("agentConfigToHub() error = %v", err)
	}
	s := dst.Spec.MCPServers[0]
	if s.URL != "https://example.com" || s.Type != "sse" {
		t.Errorf("scalar fields not copied: %#v", s)
	}
	if s.HeadersFrom == nil || s.HeadersFrom.SecretRef.Name != "hdr" {
		t.Errorf("HeadersFrom not copied: %#v", s.HeadersFrom)
	}
	if s.EnvFrom == nil || s.EnvFrom.SecretRef.Name != "bulk" {
		t.Errorf("EnvFrom not copied: %#v", s.EnvFrom)
	}
}
