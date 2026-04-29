package controller

import (
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestMergeAgentConfigs_Empty(t *testing.T) {
	if got := MergeAgentConfigs(nil); got != nil {
		t.Errorf("Expected nil, got %+v", got)
	}
	if got := MergeAgentConfigs([]kelosv1alpha1.AgentConfigSpec{}); got != nil {
		t.Errorf("Expected nil, got %+v", got)
	}
}

func TestMergeAgentConfigs_Single(t *testing.T) {
	input := kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "# Instructions",
		Plugins:  []kelosv1alpha1.PluginSpec{{Name: "p1"}},
		Skills:   []kelosv1alpha1.SkillsShSpec{{Source: "owner/repo"}},
		MCPServers: []kelosv1alpha1.MCPServerSpec{
			{Name: "server1", Type: "stdio", Command: "cmd"},
		},
	}
	got := MergeAgentConfigs([]kelosv1alpha1.AgentConfigSpec{input})
	if got == nil {
		t.Fatal("Expected non-nil result")
	}
	if got.AgentsMD != "# Instructions" {
		t.Errorf("AgentsMD = %q, want %q", got.AgentsMD, "# Instructions")
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "p1" {
		t.Errorf("Plugins = %+v, want [{Name: p1}]", got.Plugins)
	}
	if len(got.Skills) != 1 || got.Skills[0].Source != "owner/repo" {
		t.Errorf("Skills = %+v, want [{Source: owner/repo}]", got.Skills)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].Name != "server1" {
		t.Errorf("MCPServers = %+v, want [{Name: server1}]", got.MCPServers)
	}
}

func TestMergeAgentConfigs_AgentsMDConcatenation(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{AgentsMD: "# Config A"},
		{AgentsMD: "# Config B"},
	}
	got := MergeAgentConfigs(configs)
	want := "# Config A\n\n# Config B"
	if got.AgentsMD != want {
		t.Errorf("AgentsMD = %q, want %q", got.AgentsMD, want)
	}
}

func TestMergeAgentConfigs_AgentsMDSkipsEmpty(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{AgentsMD: ""},
		{AgentsMD: "# Config B"},
	}
	got := MergeAgentConfigs(configs)
	if got.AgentsMD != "# Config B" {
		t.Errorf("AgentsMD = %q, want %q", got.AgentsMD, "# Config B")
	}
}

func TestMergeAgentConfigs_PluginsAppended(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{Plugins: []kelosv1alpha1.PluginSpec{{Name: "p1"}}},
		{Plugins: []kelosv1alpha1.PluginSpec{{Name: "p2"}, {Name: "p3"}}},
	}
	got := MergeAgentConfigs(configs)
	if len(got.Plugins) != 3 {
		t.Fatalf("len(Plugins) = %d, want 3", len(got.Plugins))
	}
	names := []string{got.Plugins[0].Name, got.Plugins[1].Name, got.Plugins[2].Name}
	want := []string{"p1", "p2", "p3"}
	for i := range names {
		if names[i] != want[i] {
			t.Errorf("Plugins[%d].Name = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestMergeAgentConfigs_SkillsAppended(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{Skills: []kelosv1alpha1.SkillsShSpec{{Source: "a/b"}}},
		{Skills: []kelosv1alpha1.SkillsShSpec{{Source: "c/d"}}},
	}
	got := MergeAgentConfigs(configs)
	if len(got.Skills) != 2 {
		t.Fatalf("len(Skills) = %d, want 2", len(got.Skills))
	}
	if got.Skills[0].Source != "a/b" || got.Skills[1].Source != "c/d" {
		t.Errorf("Skills = %+v", got.Skills)
	}
}

func TestMergeAgentConfigs_MCPServersAppended(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{MCPServers: []kelosv1alpha1.MCPServerSpec{{Name: "s1", Type: "stdio"}}},
		{MCPServers: []kelosv1alpha1.MCPServerSpec{{Name: "s2", Type: "http"}}},
	}
	got := MergeAgentConfigs(configs)
	if len(got.MCPServers) != 2 {
		t.Fatalf("len(MCPServers) = %d, want 2", len(got.MCPServers))
	}
	if got.MCPServers[0].Name != "s1" || got.MCPServers[1].Name != "s2" {
		t.Errorf("MCPServers = %+v", got.MCPServers)
	}
}

func TestMergeAgentConfigs_MCPServersLaterWins(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{MCPServers: []kelosv1alpha1.MCPServerSpec{{Name: "shared", Type: "stdio", Command: "old"}}},
		{MCPServers: []kelosv1alpha1.MCPServerSpec{{Name: "shared", Type: "http", URL: "http://new"}}},
	}
	got := MergeAgentConfigs(configs)
	if len(got.MCPServers) != 1 {
		t.Fatalf("len(MCPServers) = %d, want 1", len(got.MCPServers))
	}
	if got.MCPServers[0].Type != "http" || got.MCPServers[0].URL != "http://new" {
		t.Errorf("MCPServers[0] = %+v, want http type with new URL", got.MCPServers[0])
	}
}

func TestMergeAgentConfigs_MCPServersOrderPreserved(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{MCPServers: []kelosv1alpha1.MCPServerSpec{
			{Name: "a", Type: "stdio", Command: "a1"},
			{Name: "b", Type: "stdio", Command: "b1"},
		}},
		{MCPServers: []kelosv1alpha1.MCPServerSpec{
			{Name: "c", Type: "http", URL: "http://c"},
			{Name: "a", Type: "http", URL: "http://a2"},
		}},
	}
	got := MergeAgentConfigs(configs)
	if len(got.MCPServers) != 3 {
		t.Fatalf("len(MCPServers) = %d, want 3", len(got.MCPServers))
	}
	// Order: a (first seen, overwritten), b, c
	if got.MCPServers[0].Name != "a" || got.MCPServers[0].Type != "http" {
		t.Errorf("MCPServers[0] = %+v, want a/http (later wins)", got.MCPServers[0])
	}
	if got.MCPServers[1].Name != "b" {
		t.Errorf("MCPServers[1].Name = %q, want %q", got.MCPServers[1].Name, "b")
	}
	if got.MCPServers[2].Name != "c" {
		t.Errorf("MCPServers[2].Name = %q, want %q", got.MCPServers[2].Name, "c")
	}
}

func TestMergeAgentConfigs_ThreeConfigs(t *testing.T) {
	configs := []kelosv1alpha1.AgentConfigSpec{
		{
			AgentsMD: "## Environment",
			Plugins:  []kelosv1alpha1.PluginSpec{{Name: "base"}},
			MCPServers: []kelosv1alpha1.MCPServerSpec{
				{Name: "shared", Type: "stdio", Command: "v1"},
			},
		},
		{
			AgentsMD: "## Standards",
			Skills:   []kelosv1alpha1.SkillsShSpec{{Source: "org/skills"}},
		},
		{
			AgentsMD: "## Identity",
			Plugins:  []kelosv1alpha1.PluginSpec{{Name: "role"}},
			MCPServers: []kelosv1alpha1.MCPServerSpec{
				{Name: "shared", Type: "http", URL: "http://v2"},
				{Name: "extra", Type: "sse", URL: "http://extra"},
			},
		},
	}
	got := MergeAgentConfigs(configs)

	wantMD := "## Environment\n\n## Standards\n\n## Identity"
	if got.AgentsMD != wantMD {
		t.Errorf("AgentsMD = %q, want %q", got.AgentsMD, wantMD)
	}
	if len(got.Plugins) != 2 || got.Plugins[0].Name != "base" || got.Plugins[1].Name != "role" {
		t.Errorf("Plugins = %+v", got.Plugins)
	}
	if len(got.Skills) != 1 || got.Skills[0].Source != "org/skills" {
		t.Errorf("Skills = %+v", got.Skills)
	}
	if len(got.MCPServers) != 2 {
		t.Fatalf("len(MCPServers) = %d, want 2", len(got.MCPServers))
	}
	if got.MCPServers[0].Name != "shared" || got.MCPServers[0].Type != "http" {
		t.Errorf("MCPServers[0] = %+v, want shared/http", got.MCPServers[0])
	}
	if got.MCPServers[1].Name != "extra" {
		t.Errorf("MCPServers[1].Name = %q, want %q", got.MCPServers[1].Name, "extra")
	}
}

func TestResolveAgentConfigRefs_NeitherSet(t *testing.T) {
	spec := &kelosv1alpha1.TaskSpec{}
	if got := ResolveAgentConfigRefs(spec); got != nil {
		t.Errorf("Expected nil, got %+v", got)
	}
}

func TestResolveAgentConfigRefs_SingularSet(t *testing.T) {
	spec := &kelosv1alpha1.TaskSpec{
		AgentConfigRef: &kelosv1alpha1.AgentConfigReference{Name: "single"},
	}
	got := ResolveAgentConfigRefs(spec)
	if len(got) != 1 || got[0].Name != "single" {
		t.Errorf("Expected [{Name: single}], got %+v", got)
	}
}

func TestResolveAgentConfigRefs_PluralSet(t *testing.T) {
	spec := &kelosv1alpha1.TaskSpec{
		AgentConfigRefs: []kelosv1alpha1.AgentConfigReference{
			{Name: "first"},
			{Name: "second"},
		},
	}
	got := ResolveAgentConfigRefs(spec)
	if len(got) != 2 || got[0].Name != "first" || got[1].Name != "second" {
		t.Errorf("Expected [first, second], got %+v", got)
	}
}

func TestResolveAgentConfigRefs_PluralTakesPrecedence(t *testing.T) {
	spec := &kelosv1alpha1.TaskSpec{
		AgentConfigRef: &kelosv1alpha1.AgentConfigReference{Name: "singular"},
		AgentConfigRefs: []kelosv1alpha1.AgentConfigReference{
			{Name: "plural1"},
			{Name: "plural2"},
		},
	}
	got := ResolveAgentConfigRefs(spec)
	if len(got) != 2 || got[0].Name != "plural1" {
		t.Errorf("Expected plural to take precedence, got %+v", got)
	}
}
