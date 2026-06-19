package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentEntrypointsHandleKelosEffort(t *testing.T) {
	tests := []struct {
		path  string
		parts []string
	}{
		{
			path: "../..//codex/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"model_reasoning_effort",
			},
		},
		{
			path: "../..//claude-code/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"--effort",
			},
		},
		{
			path: "../..//gemini/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"thinkingConfig",
				"thinkingBudget",
				"thinkingLevel",
			},
		},
		{
			path: "../..//opencode/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"reasoningEffort",
				"variant",
			},
		},
		{
			path: "../..//cursor/kelos_entrypoint.sh",
			parts: []string{
				"KELOS_EFFORT",
				"Kelos Effort",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Clean(tt.path))
			if err != nil {
				t.Fatalf("reading entrypoint: %v", err)
			}
			content := string(data)
			for _, part := range tt.parts {
				if !strings.Contains(content, part) {
					t.Fatalf("expected %s to contain %q", tt.path, part)
				}
			}
		})
	}
}

func TestAgentEntrypointsPreserveSkillReferenceFiles(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		start      string
		end        string
		targetPath func(home, workdir string) string
	}{
		{
			name:  "codex",
			path:  "../..//codex/kelos_entrypoint.sh",
			start: "# Install each plugin as a Codex skill directory under ~/.codex/skills",
			end:   "# Write MCP server configuration",
			targetPath: func(home, workdir string) string {
				return filepath.Join(home, ".codex", "skills", "team-tools:deploy")
			},
		},
		{
			name:  "opencode",
			path:  "../..//opencode/kelos_entrypoint.sh",
			start: "# Install each plugin's skills and agents into OpenCode's global config",
			end:   "# Run pre-agent setup command",
			targetPath: func(home, workdir string) string {
				return filepath.Join(home, ".config", "opencode", "skills", "team-tools:deploy")
			},
		},
		{
			name:  "cursor",
			path:  "../..//cursor/kelos_entrypoint.sh",
			start: "# Install each plugin's skills and agents into Cursor's config directories.",
			end:   "# Write MCP server configuration",
			targetPath: func(home, workdir string) string {
				return filepath.Join(workdir, ".cursor", "skills", "team-tools:deploy")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			home := filepath.Join(tmp, "home")
			workdir := filepath.Join(tmp, "work")
			pluginDir := filepath.Join(tmp, "plugins")

			writeFile(t, filepath.Join(pluginDir, "team-tools", "skills", "deploy", "SKILL.md"), "# Deploy\n")
			writeFile(t, filepath.Join(pluginDir, "team-tools", "skills", "deploy", "references", "task.yaml"), "apiVersion: kelos.io/v1alpha2\n")
			writeFile(t, filepath.Join(pluginDir, "team-tools", "skills", "deploy", "references", "nested", "workspace.yaml"), "kind: Workspace\n")
			writeFile(t, filepath.Join(pluginDir, "team-tools", "skills", "inline", "SKILL.md"), "# Inline\n")

			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatalf("creating home: %v", err)
			}
			if err := os.MkdirAll(workdir, 0o755); err != nil {
				t.Fatalf("creating workdir: %v", err)
			}
			stalePath := filepath.Join(tt.targetPath(home, workdir), "references", "stale.yaml")
			writeFile(t, stalePath, "stale\n")

			section := extractEntrypointSection(t, tt.path, tt.start, tt.end)
			script := filepath.Join(tmp, "install-skills.sh")
			writeFile(t, script, "#!/usr/bin/env bash\nset -euo pipefail\n"+section)
			if err := os.Chmod(script, 0o755); err != nil {
				t.Fatalf("chmod script: %v", err)
			}

			cmd := exec.Command("bash", script)
			cmd.Dir = workdir
			cmd.Env = append(os.Environ(),
				"HOME="+home,
				"KELOS_PLUGIN_DIR="+pluginDir,
			)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("running entrypoint skill install section: %v\n%s", err, output)
			}

			target := tt.targetPath(home, workdir)
			assertFileContent(t, filepath.Join(target, "SKILL.md"), "# Deploy\n")
			assertFileContent(t, filepath.Join(target, "references", "task.yaml"), "apiVersion: kelos.io/v1alpha2\n")
			assertFileContent(t, filepath.Join(target, "references", "nested", "workspace.yaml"), "kind: Workspace\n")
			assertPathMissing(t, stalePath)

			inlineTarget := filepath.Join(filepath.Dir(target), "team-tools:inline")
			assertFileContent(t, filepath.Join(inlineTarget, "SKILL.md"), "# Inline\n")
		})
	}
}

func TestAgentEntrypointsFailOnSkillTargetConflict(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		start string
		end   string
	}{
		{
			name:  "codex",
			path:  "../..//codex/kelos_entrypoint.sh",
			start: "# Install each plugin as a Codex skill directory under ~/.codex/skills",
			end:   "# Write MCP server configuration",
		},
		{
			name:  "opencode",
			path:  "../..//opencode/kelos_entrypoint.sh",
			start: "# Install each plugin's skills and agents into OpenCode's global config",
			end:   "# Run pre-agent setup command",
		},
		{
			name:  "cursor",
			path:  "../..//cursor/kelos_entrypoint.sh",
			start: "# Install each plugin's skills and agents into Cursor's config directories.",
			end:   "# Write MCP server configuration",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			home := filepath.Join(tmp, "home")
			workdir := filepath.Join(tmp, "work")
			pluginDir := filepath.Join(tmp, "plugins")

			writeFile(t, filepath.Join(pluginDir, "alpha", "skills", "bravo:charlie", "SKILL.md"), "# Conflict A\n")
			writeFile(t, filepath.Join(pluginDir, "alpha:bravo", "skills", "charlie", "SKILL.md"), "# Conflict B\n")

			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatalf("creating home: %v", err)
			}
			if err := os.MkdirAll(workdir, 0o755); err != nil {
				t.Fatalf("creating workdir: %v", err)
			}

			section := extractEntrypointSection(t, tt.path, tt.start, tt.end)
			script := filepath.Join(tmp, "install-skills.sh")
			writeFile(t, script, "#!/usr/bin/env bash\nset -euo pipefail\n"+section)
			if err := os.Chmod(script, 0o755); err != nil {
				t.Fatalf("chmod script: %v", err)
			}

			cmd := exec.Command("bash", script)
			cmd.Dir = workdir
			cmd.Env = append(os.Environ(),
				"HOME="+home,
				"KELOS_PLUGIN_DIR="+pluginDir,
			)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected entrypoint skill install section to fail on conflict")
			}
			outputText := string(output)
			if !strings.Contains(outputText, "Error: Skill target conflict:") ||
				!strings.Contains(outputText, "alpha/bravo:charlie") ||
				!strings.Contains(outputText, "alpha:bravo/charlie") {
				t.Fatalf("expected skill target conflict error, got:\n%s", output)
			}
		})
	}
}

func extractEntrypointSection(t *testing.T, path, start, end string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading entrypoint: %v", err)
	}
	content := string(data)
	startIndex := strings.Index(content, start)
	if startIndex == -1 {
		t.Fatalf("expected %s to contain start marker %q", path, start)
	}
	endIndex := strings.Index(content[startIndex:], end)
	if endIndex == -1 {
		t.Fatalf("expected %s to contain end marker %q after skill install section", path, end)
	}

	return content[startIndex : startIndex+endIndex]
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating parent directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("expected %s content %q, got %q", path, want, got)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s to be removed", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking %s: %v", path, err)
	}
}
