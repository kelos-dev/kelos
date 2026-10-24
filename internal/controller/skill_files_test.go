package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestPluginSkillFiles(t *testing.T) {
	plugins := []kelos.PluginSpec{{Name: "tools", Skills: []kelos.SkillDefinition{{
		Name: "review", Content: "# Review\n",
		Files: []kelos.SkillFile{
			{Path: "references/nested/checklist.md", Content: "Check compatibility\n"},
			{Path: "scripts/check.sh", Content: "#!/bin/sh\nprintf 'checked\\n'\n", Executable: true},
		},
	}}}}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "review", Namespace: "default"},
		Spec:       kelos.TaskSpec{Type: AgentTypeClaudeCode, Prompt: "Review", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}},
	}
	cm, err := buildPluginConfigMap(task, plugins)
	if err != nil {
		t.Fatal(err)
	}
	job, err := NewJobBuilder().Build(task, nil, &kelos.AgentConfigSpec{Plugins: plugins}, task.Spec.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	var items []corev1.KeyToPath
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == PluginStagingVolumeName {
			items = volume.ConfigMap.Items
		}
	}
	if len(items) != 3 {
		t.Fatalf("projection has %d items, want 3", len(items))
	}
	tmp := t.TempDir()
	staging, target := filepath.Join(tmp, "staging"), filepath.Join(tmp, "plugins")
	dataDir := filepath.Join(staging, "..timestamp")
	for _, item := range items {
		file := filepath.Join(dataDir, item.Path)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if item.Mode != nil {
			mode = os.FileMode(*item.Mode)
		}
		if err := os.WriteFile(file, []byte(cm.Data[item.Key]), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..timestamp", "tools"), filepath.Join(staging, "tools")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer(PluginStagingMountPath, staging, PluginMountPath, target).Replace(pluginSetupScript)
	if output, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("plugin setup: %v\n%s", err, output)
	}
	wantFiles := map[string]struct {
		content string
		mode    os.FileMode
	}{
		"tools/skills/review/SKILL.md":                       {"# Review\n", 0o644},
		"tools/skills/review/references/nested/checklist.md": {"Check compatibility\n", 0o644},
		"tools/skills/review/scripts/check.sh":               {"#!/bin/sh\nprintf 'checked\\n'\n", 0o755},
	}
	for _, item := range items {
		want, ok := wantFiles[item.Path]
		if !ok {
			t.Fatalf("unexpected projected path %q", item.Path)
		}
		file := filepath.Join(target, item.Path)
		assertFileContent(t, file, want.content)
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want.mode {
			t.Errorf("%s mode = %o, want %o", item.Path, got, want.mode)
		}
	}
	output, err := exec.Command(filepath.Join(target, "tools/skills/review/scripts/check.sh")).CombinedOutput()
	if err != nil || string(output) != "checked\n" {
		t.Fatalf("script output = %q, error = %v", output, err)
	}
}

func TestPluginSkillFileValidation(t *testing.T) {
	for _, filePath := range []string{"", ".", "..", "../outside", "references/../../outside", "/absolute", "references\\file", "references/../file", "references//file", "references/", "file\x00"} {
		t.Run(filePath, func(t *testing.T) {
			_, _, err := buildPluginConfigMapData([]kelos.PluginSpec{{Name: "tools", Skills: []kelos.SkillDefinition{{
				Name: "review", Files: []kelos.SkillFile{{Path: filePath}},
			}}}})
			if err == nil {
				t.Fatal("expected invalid path error")
			}
		})
	}
	for _, files := range [][]kelos.SkillFile{
		{{Path: "SKILL.md"}},
		{{Path: "SKILL.md/nested"}},
		{{Path: "references/check.md"}, {Path: "references/check.md"}},
		{{Path: "references"}, {Path: "references/check.md"}},
		{{Path: "references/check.md"}, {Path: "references"}},
		{{Path: "asset", Content: string([]byte{0xff})}},
	} {
		_, _, err := buildPluginConfigMapData([]kelos.PluginSpec{{Name: "tools", Skills: []kelos.SkillDefinition{{Name: "review", Files: files}}}})
		if err == nil {
			t.Errorf("expected error for files %#v", files)
		}
	}
}

func TestPluginSkillFilesSizeBudget(t *testing.T) {
	plugins := []kelos.PluginSpec{{Name: "tools", Skills: []kelos.SkillDefinition{{
		Name: "review", Content: "x", Files: []kelos.SkillFile{{Path: "reference.md", Content: strings.Repeat("x", pluginConfigMapMaxBytes)}},
	}}}}
	if _, _, err := buildPluginConfigMapData(plugins); err == nil || !strings.Contains(err.Error(), "ConfigMap budget") {
		t.Fatalf("error = %v, want size budget error", err)
	}
	plugins[0].Skills[0].Files[0].Content = strings.Repeat("x", pluginConfigMapMaxBytes-1)
	if _, _, err := buildPluginConfigMapData(plugins); err != nil {
		t.Fatalf("content at budget: %v", err)
	}
}

func TestPluginSkillFilesWithOverlappingSkillDefinitions(t *testing.T) {
	plugins := []kelos.PluginSpec{
		{Name: "tools", Skills: []kelos.SkillDefinition{{Name: "review", Content: "First instructions"}}},
		{Name: "tools", Skills: []kelos.SkillDefinition{{Name: "review", Content: "Second instructions"}}},
	}
	if _, _, err := buildPluginConfigMapData(plugins); err != nil {
		t.Fatalf("overlapping skill definitions: %v", err)
	}
	plugins[0].Skills[0].Files = []kelos.SkillFile{{Path: "references/check.md", Content: "Checklist"}}
	data, items, err := buildPluginConfigMapData(plugins)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || data["p0-s0-f0"] != "Checklist" || data["p1-s0"] != "Second instructions" {
		t.Fatalf("unexpected bundle: data = %#v, items = %#v", data, items)
	}
	plugins[1].Skills[0].Files = []kelos.SkillFile{{Path: "references/check.md", Content: "Another checklist"}}
	if _, _, err := buildPluginConfigMapData(plugins); err == nil {
		t.Fatal("expected a conflict between supporting files in overlapping skills")
	}
}
