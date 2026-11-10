package sessionruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderSetupUsesConventionalPaths(t *testing.T) {
	tests := []struct {
		agentType    string
		command      string
		directory    string
		configEnv    string
		stateDir     string
		instructions string
		configFile   string
		config       string
	}{
		{
			agentType: "claude-code", command: "claude", directory: ".claude",
			configEnv: "CLAUDE_CONFIG_DIR", stateDir: "claude-config", instructions: "CLAUDE.md",
			configFile: "settings.json", config: `{"language":"English"}`,
		},
		{
			agentType: "codex", command: "codex", directory: ".codex",
			configEnv: "CODEX_HOME", stateDir: "codex-home", instructions: "AGENTS.md",
			configFile: "config.toml", config: "model_verbosity = \"low\"\n",
		},
		{
			agentType: "opencode", command: "opencode", directory: ".config/opencode",
			configEnv: "OPENCODE_CONFIG_DIR", stateDir: "opencode-config", instructions: "AGENTS.md",
			configFile: "opencode.json", config: `{"theme":"system"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			for _, mode := range []string{"task", "session"} {
				t.Run(mode, func(t *testing.T) {
					tmp := t.TempDir()
					workdir := filepath.Join(tmp, "workspace", "repo")
					stateDir := filepath.Join(tmp, "workspace", ".kelos", "session")
					configDir := filepath.Join(stateDir, tt.stateDir)
					home := filepath.Join(tmp, "home")
					binDir := filepath.Join(tmp, "bin")
					writeProviderTestFile(t, filepath.Join(binDir, tt.command), "#!/bin/sh\nprintf '{}\\n'\n")
					capture := filepath.Join(binDir, "capture")
					writeProviderTestFile(t, capture, "#!/bin/sh\ncat\n")
					for _, path := range []string{filepath.Join(binDir, tt.command), capture} {
						if err := os.Chmod(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
					entrypoint, err := os.ReadFile(filepath.Join("..", "..", tt.agentType, "kelos_entrypoint.sh"))
					if err != nil {
						t.Fatal(err)
					}
					script := filepath.Join(tmp, "entrypoint.sh")
					writeProviderTestFile(t, script, strings.ReplaceAll(string(entrypoint), "/kelos/kelos-capture", capture))
					if err := os.Chmod(script, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(workdir, 0700); err != nil {
						t.Fatal(err)
					}
					setup := fmt.Sprintf(`set -eu
test "$(cat "$HOME/%s/%s")" = "Team instructions"
printf '%%s' '%s' > "$HOME/%s/%s.tmp"
mv "$HOME/%s/%s.tmp" "$HOME/%s/%s"
`, tt.directory, tt.instructions, tt.config, tt.directory, tt.configFile,
						tt.directory, tt.configFile, tt.directory, tt.configFile)
					if tt.agentType == "claude-code" {
						setup += `test -f "$HOME/.claude.json"
printf '%s' '{"setup":true}' > "$HOME/.claude.json.tmp"
mv "$HOME/.claude.json.tmp" "$HOME/.claude.json"
`
					}
					setupJSON, err := json.Marshal([]string{"sh", "-c", setup})
					if err != nil {
						t.Fatal(err)
					}
					starts := 1
					if mode == "session" {
						starts = 3
						writeProviderTestFile(t, filepath.Join(configDir, "sessions", "retained.jsonl"), "retained conversation")
						if tt.agentType == "opencode" {
							writeProviderTestFile(t, filepath.Join(stateDir, "opencode-data", "opencode", "storage", "retained.json"), "retained data")
						}
					}
					for start := range starts {
						if start == 2 {
							home = filepath.Join(tmp, "replacement-home")
						}
						if err := os.MkdirAll(filepath.Join(home, tt.directory), 0700); err != nil {
							t.Fatal(err)
						}
						environment := []string{
							"HOME=" + home, "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
							"KELOS_AGENTS_MD=Team instructions", "KELOS_SETUP_COMMAND=" + string(setupJSON),
							`KELOS_MCP_SERVERS={"mcpServers":{"tools":{"command":"tools-server"}}}`,
						}
						if mode == "session" {
							environment = append(environment, tt.configEnv+"="+configDir, "XDG_DATA_HOME="+filepath.Join(stateDir, "opencode-data"))
							if start > 0 {
								assertProviderTestFile(t, filepath.Join(configDir, tt.configFile), tt.config)
							}
							config := Config{AgentType: tt.agentType, WorkingDir: workdir, Environment: environment}
							if err := runAgentSetup(t.Context(), config, script); err != nil {
								t.Fatal(err)
							}
							assertProviderTestFile(t, filepath.Join(home, tt.directory, "sessions", "retained.jsonl"), "retained conversation")
							if tt.agentType == "opencode" {
								assertProviderTestFile(t, filepath.Join(home, ".local", "share", "opencode", "storage", "retained.json"), "retained data")
							}
						} else {
							command := exec.Command("bash", script, "test prompt")
							command.Dir = workdir
							command.Env = environment
							if output, err := command.CombinedOutput(); err != nil {
								t.Fatalf("setup on start %d: %v\n%s", start, err, output)
							}
						}
						targetDir := filepath.Join(home, tt.directory)
						if mode == "session" {
							targetDir = configDir
						}
						assertProviderTestFile(t, filepath.Join(targetDir, tt.configFile), tt.config)
						if tt.agentType == "claude-code" {
							metadata := filepath.Join(home, ".claude.json")
							if mode == "session" {
								metadata = filepath.Join(configDir, ".claude.json")
							}
							assertProviderTestFile(t, metadata, `{"setup":true}`)
						}
					}
				})
			}
		})
	}
}

func TestLinkProviderPathSeedsImageDefaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "home", ".claude")
	target := filepath.Join(tmp, "state", "claude-config")
	writeProviderTestFile(t, filepath.Join(path, "settings.json"), "image settings")
	writeProviderTestFile(t, filepath.Join(path, "skills", "team", "SKILL.md"), "image skill")
	writeProviderTestFile(t, filepath.Join(path, "skills", "team", "reference.md"), "image reference")
	writeProviderTestFile(t, filepath.Join(target, "settings.json"), "retained settings")
	writeProviderTestFile(t, filepath.Join(target, "skills", "team", "SKILL.md"), "retained skill")
	if err := linkProviderPath(path, target, true); err != nil {
		t.Fatal(err)
	}
	assertProviderTestFile(t, filepath.Join(path, "settings.json"), "retained settings")
	assertProviderTestFile(t, filepath.Join(path, "skills", "team", "SKILL.md"), "retained skill")
	assertProviderTestFile(t, filepath.Join(target, "skills", "team", "reference.md"), "image reference")
	info, err := os.Stat(filepath.Join(target, "skills", "team", "reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("seeded file permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestLinkProviderPathSeedsClaudeMetadata(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "home", ".claude.json")
	target := filepath.Join(tmp, "state", "claude-config", ".claude.json")
	writeProviderTestFile(t, path, `{"mcpServers":{}}`)
	if err := linkProviderPath(path, target, false); err != nil {
		t.Fatal(err)
	}
	assertProviderTestFile(t, target, `{"mcpServers":{}}`)
}

func TestLinkProviderPathRejectsConflictingLink(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".codex")
	other := filepath.Join(tmp, "other")
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if err := linkProviderPath(path, filepath.Join(tmp, "state"), true); err == nil {
		t.Fatal("expected a conflicting link to fail setup")
	}
	link, err := os.Readlink(path)
	if err != nil || link != other {
		t.Fatalf("conflicting link = %q, %v", link, err)
	}
}

func TestPrepareProviderPathsRejectsMissingConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		agentType   string
		environment []string
		wantError   string
	}{
		{name: "claude config", agentType: "claude-code", wantError: "CLAUDE_CONFIG_DIR"},
		{name: "codex home", agentType: "codex", wantError: ".codex"},
		{name: "opencode data", agentType: "opencode", wantError: "XDG_DATA_HOME"},
		{name: "opencode config", agentType: "opencode", environment: []string{"XDG_DATA_HOME=" + t.TempDir()}, wantError: ".config/opencode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			environment := append([]string{"HOME=" + t.TempDir()}, tt.environment...)
			if err := prepareProviderPaths(tt.agentType, environment, nil); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("missing configuration error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestFinishProviderSetupPreservesSharedHomeMetadata(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	writeProviderTestFile(t, path, `{"setup":true}`)
	if err := finishProviderSetup("claude-code", []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + home}); err != nil {
		t.Fatal(err)
	}
	assertProviderTestFile(t, path, `{"setup":true}`)
}

func TestFinishProviderSetupReplacesMetadataAtomically(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configDir := filepath.Join(tmp, "config")
	target := filepath.Join(configDir, ".claude.json")
	writeProviderTestFile(t, target, `{"retained":true}`)
	snapshot := filepath.Join(configDir, "snapshot.json")
	if err := os.Link(target, snapshot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude.json")
	writeProviderTestFile(t, path, `{"setup":true}`)
	if err := finishProviderSetup("claude-code", []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + configDir}); err != nil {
		t.Fatal(err)
	}
	assertProviderTestFile(t, target, `{"setup":true}`)
	assertProviderTestFile(t, snapshot, `{"retained":true}`)
	if link, err := os.Readlink(path); err != nil || link != target {
		t.Fatalf("metadata link = %q, %v, want %q", link, err, target)
	}
}

func TestProviderSetupPreservesMetadataThroughSharedHomeAlias(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	alias := filepath.Join(tmp, "alias")
	path := filepath.Join(home, ".claude.json")
	writeProviderTestFile(t, path, `{"setup":true}`)
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	environment := []string{"HOME=" + alias, "CLAUDE_CONFIG_DIR=" + home}
	if err := prepareProviderPaths("claude-code", environment, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishProviderSetup("claude-code", environment); err != nil {
		t.Fatal(err)
	}
	assertProviderTestFile(t, path, `{"setup":true}`)
}

func TestLinkProviderPathRejectsRetainedFileTypeMismatch(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "home", ".claude.json")
	target := filepath.Join(tmp, "config", ".claude.json")
	writeProviderTestFile(t, path, "image config")
	writeProviderTestFile(t, filepath.Join(target, "retained.json"), "retained state")
	if err := linkProviderPath(path, target, false); err == nil || !strings.Contains(err.Error(), "different file type") {
		t.Fatalf("file type mismatch error = %v", err)
	}
	assertProviderTestFile(t, path, "image config")
	assertProviderTestFile(t, filepath.Join(target, "retained.json"), "retained state")
}

func TestLinkProviderPathRejectsReverseAlias(t *testing.T) {
	for _, directory := range []bool{true, false} {
		t.Run(fmt.Sprintf("directory=%t", directory), func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, "home", "config")
			target := filepath.Join(tmp, "state", "config")
			file := path
			if directory {
				file = filepath.Join(path, "settings.json")
			}
			writeProviderTestFile(t, file, "image settings")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, target); err != nil {
				t.Fatal(err)
			}
			if err := linkProviderPath(path, target, directory); err == nil || !strings.Contains(err.Error(), "different file type") {
				t.Fatalf("reverse alias error = %v", err)
			}
			assertProviderTestFile(t, file, "image settings")
			if link, err := os.Readlink(target); err != nil || link != path {
				t.Fatalf("retained link = %q, %v, want %q", link, err, path)
			}
		})
	}
}

func TestRunAgentSetupReturnsPersistenceFailure(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	script := filepath.Join(tmp, "entrypoint.sh")
	writeProviderTestFile(t, script, `#!/bin/sh
set -eu
test "$KELOS_SESSION_SETUP_ONLY" = 1
test -L "$HOME/.claude"
rm "$HOME/.claude.json"
mkdir "$HOME/.claude.json"
`)
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	config := Config{AgentType: "claude-code", WorkingDir: tmp, Environment: []string{
		"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(tmp, "config"), "PATH=" + os.Getenv("PATH"),
	}}
	if err := runAgentSetup(t.Context(), config, script); err == nil || !strings.Contains(err.Error(), "persisting Session agent setup") {
		t.Fatalf("setup persistence error = %v", err)
	}
}

func writeProviderTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertProviderTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
