package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type skillsScriptResult struct {
	output string
	calls  string
	root   string
	err    error
}

func runSkillsInstallScript(t *testing.T, skills []kelos.SkillsShSpec) skillsScriptResult {
	t.Helper()

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("apk", "#!/bin/sh\nexit 0\n")
	writeExecutable("chown", "#!/bin/sh\nexit 0\n")
	writeExecutable("npx", `#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  *broken/package*) exit 42 ;;
esac
mkdir -p "$HOME/.agents/skills/working"
printf '# working\n' > "$HOME/.agents/skills/working/SKILL.md"
`)

	script, err := buildSkillsInstallScript(skills, nil)
	if err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, PluginMountPath, root)
	callsPath := filepath.Join(root, "calls")
	command := exec.Command("sh", "-c", script)
	command.Env = append(os.Environ(),
		"HOME="+root,
		"CALLS="+callsPath,
		"PATH="+bin+":"+os.Getenv("PATH"),
	)
	output, runErr := command.CombinedOutput()
	called, _ := os.ReadFile(callsPath)
	return skillsScriptResult{
		output: string(output),
		calls:  string(called),
		root:   root,
		err:    runErr,
	}
}

func TestBuildSkillsInstallScript_OptionalFailureContinues(t *testing.T) {
	result := runSkillsInstallScript(t, []kelos.SkillsShSpec{
		{Source: "broken/package", Optional: true},
		{Source: "working/package", Optional: true},
	})

	if result.err != nil {
		t.Fatalf("script failed: %v\n%s", result.err, result.output)
	}
	if !strings.Contains(result.output, "Optional skills.sh package 'broken/package' failed to install; continuing") {
		t.Fatalf("missing optional-package warning in %q", result.output)
	}
	if !strings.Contains(result.calls, "broken/package") || !strings.Contains(result.calls, "working/package") {
		t.Fatalf("calls = %q, want both packages", result.calls)
	}
	installed := filepath.Join(result.root, SkillsShPluginName, "skills", "working", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed skill %s: %v", installed, err)
	}
}

func TestBuildSkillsInstallScript_RequiredFailureStops(t *testing.T) {
	result := runSkillsInstallScript(t, []kelos.SkillsShSpec{
		{Source: "broken/package"},
		{Source: "working/package", Optional: true},
	})

	if result.err == nil {
		t.Fatal("script succeeded, want required package failure")
	}
	if strings.Contains(result.calls, "working/package") {
		t.Fatalf("calls = %q, later package should not run", result.calls)
	}
}

func TestBuildSkillsInstallScript_AllOptionalFailuresAllowEmptyPluginDir(t *testing.T) {
	result := runSkillsInstallScript(t, []kelos.SkillsShSpec{
		{Source: "broken/package", Optional: true},
	})

	if result.err != nil {
		t.Fatalf("script failed: %v\n%s", result.err, result.output)
	}
	pluginSkillsDir := filepath.Join(result.root, SkillsShPluginName, "skills")
	if info, err := os.Stat(pluginSkillsDir); err != nil || !info.IsDir() {
		t.Fatalf("plugin skills directory %s was not created: %v", pluginSkillsDir, err)
	}
}
