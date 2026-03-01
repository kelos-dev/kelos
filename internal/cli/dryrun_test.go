package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout to a pipe, executes fn, and returns
// everything written to stdout. Reading happens in a goroutine to avoid
// pipe buffer deadlocks when the output is large.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	os.Stdout = w

	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		out.ReadFrom(r)
		close(done)
	}()

	fn()

	w.Close()
	os.Stdout = old
	<-done
	return out.String()
}

func TestRunCommand_DryRun(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("secret: my-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"run",
		"--config", cfgPath,
		"--dry-run",
		"--prompt", "hello world",
		"--name", "test-task",
		"--namespace", "test-ns",
	})

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kind: Task") {
		t.Errorf("expected YAML output to contain 'kind: Task', got:\n%s", output)
	}
	if !strings.Contains(output, "name: test-task") {
		t.Errorf("expected YAML output to contain 'name: test-task', got:\n%s", output)
	}
	if !strings.Contains(output, "namespace: test-ns") {
		t.Errorf("expected YAML output to contain 'namespace: test-ns', got:\n%s", output)
	}
	if !strings.Contains(output, "prompt: hello world") {
		t.Errorf("expected YAML output to contain 'prompt: hello world', got:\n%s", output)
	}
	if !strings.Contains(output, "my-secret") {
		t.Errorf("expected YAML output to contain secret name 'my-secret', got:\n%s", output)
	}
	// Ensure no "created" message is printed.
	if strings.Contains(output, "created") {
		t.Errorf("dry-run should not print 'created' message, got:\n%s", output)
	}
}

func TestResolveCredentialValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"none", ""},
		{"", ""},
		{"my-api-key", "my-api-key"},
	}
	for _, tt := range tests {
		got := resolveCredentialValue(tt.input)
		if got != tt.want {
			t.Errorf("resolveCredentialValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRunCommand_DryRun_OpenCodeNoneAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "apiKey: none\ntype: opencode\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"run",
		"--config", cfgPath,
		"--dry-run",
		"--prompt", "hello",
		"--name", "none-task",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "type: opencode") {
		t.Errorf("expected 'type: opencode' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "kelos-credentials") {
		t.Errorf("expected 'kelos-credentials' secret reference in output, got:\n%s", output)
	}
}

func TestRunCommand_DryRun_AgentType(t *testing.T) {
	for _, agentType := range []string{"claude-code", "codex", "gemini", "opencode"} {
		t.Run(agentType, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte("secret: my-secret\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := NewRootCommand()
			cmd.SetArgs([]string{
				"run",
				"--config", cfgPath,
				"--dry-run",
				"--prompt", "hello",
				"--name", "type-task",
				"--namespace", "test-ns",
				"--type", agentType,
			})

			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			if err := cmd.Execute(); err != nil {
				w.Close()
				os.Stdout = old
				t.Fatalf("unexpected error: %v", err)
			}

			w.Close()
			os.Stdout = old
			var out bytes.Buffer
			out.ReadFrom(r)
			output := out.String()

			if !strings.Contains(output, "type: "+agentType) {
				t.Errorf("expected YAML output to contain 'type: %s', got:\n%s", agentType, output)
			}
		})
	}
}

func TestRunCommand_DryRun_WithWorkspaceConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := `secret: my-secret
workspace:
  repo: https://github.com/org/repo.git
  ref: main
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"run",
		"--config", cfgPath,
		"--dry-run",
		"--prompt", "hello",
		"--name", "ws-task",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kelos-workspace") {
		t.Errorf("expected workspace reference 'kelos-workspace' in dry-run output, got:\n%s", output)
	}
}

func TestCreateWorkspaceCommand_DryRun(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "workspace", "my-ws",
		"--config", cfgPath,
		"--dry-run",
		"--repo", "https://github.com/org/repo.git",
		"--ref", "main",
		"--secret", "gh-token",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kind: Workspace") {
		t.Errorf("expected 'kind: Workspace' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: my-ws") {
		t.Errorf("expected 'name: my-ws' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "namespace: test-ns") {
		t.Errorf("expected 'namespace: test-ns' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got:\n%s", output)
	}
	if !strings.Contains(output, "gh-token") {
		t.Errorf("expected secret name in output, got:\n%s", output)
	}
	if strings.Contains(output, "created") {
		t.Errorf("dry-run should not print 'created' message, got:\n%s", output)
	}
}

func TestCreateWorkspaceCommand_DryRun_WithToken(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "workspace", "my-ws",
		"--config", cfgPath,
		"--dry-run",
		"--repo", "https://github.com/org/repo.git",
		"--token", "ghp_test123",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	// Token should produce a secret reference in the output.
	if !strings.Contains(output, "my-ws-credentials") {
		t.Errorf("expected auto-generated secret name 'my-ws-credentials' in output, got:\n%s", output)
	}
}

func TestCreateCommand_NoTaskSpawnerSubcommand(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"create", "taskspawner", "--name", "test"})

	// Silence usage output from cobra.
	root.SilenceUsage = true

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when running 'create taskspawner', but got none")
	}
}

func TestCreateWorkspaceCommand_MissingName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "workspace",
		"--config", cfgPath,
		"--repo", "https://github.com/org/repo.git",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when name is missing")
	}
	if !strings.Contains(err.Error(), "workspace name is required") {
		t.Errorf("expected 'workspace name is required' error, got: %v", err)
	}
}

func TestCreateAgentConfigCommand_DryRun(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "my-ac",
		"--config", cfgPath,
		"--dry-run",
		"--agents-md", "Follow TDD",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kind: AgentConfig") {
		t.Errorf("expected 'kind: AgentConfig' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: my-ac") {
		t.Errorf("expected 'name: my-ac' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "namespace: test-ns") {
		t.Errorf("expected 'namespace: test-ns' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Follow TDD") {
		t.Errorf("expected agentsMD content in output, got:\n%s", output)
	}
	if strings.Contains(output, "created") {
		t.Errorf("dry-run should not print 'created' message, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_WithSkillAndAgent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "plugin-ac",
		"--config", cfgPath,
		"--dry-run",
		"--agents-md", "instructions",
		"--skill", "deploy=deploy content",
		"--agent", "reviewer=reviewer content",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kind: AgentConfig") {
		t.Errorf("expected 'kind: AgentConfig' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "deploy content") {
		t.Errorf("expected skill content in output, got:\n%s", output)
	}
	if !strings.Contains(output, "reviewer content") {
		t.Errorf("expected agent content in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: deploy") {
		t.Errorf("expected skill name 'deploy' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: reviewer") {
		t.Errorf("expected agent name 'reviewer' in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_FileReference(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	mdFile := filepath.Join(dir, "agents.md")
	if err := os.WriteFile(mdFile, []byte("# Project Rules\nFollow TDD."), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "file-ac",
		"--config", cfgPath,
		"--dry-run",
		"--agents-md", "@" + mdFile,
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "Follow TDD") {
		t.Errorf("expected file content in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_WithGitHubPlugin(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "gh-ac",
		"--config", cfgPath,
		"--dry-run",
		"--github-plugin", "tools=acme/tools@v1.0",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kind: AgentConfig") {
		t.Errorf("expected 'kind: AgentConfig' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: tools") {
		t.Errorf("expected plugin name 'tools' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "repo: acme/tools") {
		t.Errorf("expected repo 'acme/tools' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "ref: v1.0") {
		t.Errorf("expected ref 'v1.0' in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_GitHubPluginWithSecret(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "private-ac",
		"--config", cfgPath,
		"--dry-run",
		"--github-plugin", "priv=corp/private-tools,host=ghe.corp.com,secret=my-token",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "repo: corp/private-tools") {
		t.Errorf("expected repo in output, got:\n%s", output)
	}
	if !strings.Contains(output, "host: ghe.corp.com") {
		t.Errorf("expected host in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: my-token") {
		t.Errorf("expected secret name in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_GitHubPluginCombinedWithInline(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "combined-ac",
		"--config", cfgPath,
		"--dry-run",
		"--skill", "review=Review the code",
		"--github-plugin", "tools=acme/tools",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	// Should have both inline "kelos" plugin and GitHub "tools" plugin.
	if !strings.Contains(output, "name: kelos") {
		t.Errorf("expected inline plugin name 'kelos' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "name: tools") {
		t.Errorf("expected github plugin name 'tools' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "repo: acme/tools") {
		t.Errorf("expected repo in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Review the code") {
		t.Errorf("expected skill content in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_WithMarketplacePlugin(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "mp-ac",
		"--config", cfgPath,
		"--dry-run",
		"--marketplace-plugin", "github@anthropics-claude-code",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "kind: AgentConfig") {
		t.Errorf("expected 'kind: AgentConfig' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "github@anthropics-claude-code") {
		t.Errorf("expected marketplace plugin in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_DryRun_MarketplacePluginCombinedWithGitHub(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig", "combined-mp-ac",
		"--config", cfgPath,
		"--dry-run",
		"--marketplace-plugin", "github@anthropics-claude-code",
		"--github-plugin", "tools=acme/tools@v1.0",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "github@anthropics-claude-code") {
		t.Errorf("expected marketplace plugin in output, got:\n%s", output)
	}
	if !strings.Contains(output, "repo: acme/tools") {
		t.Errorf("expected github plugin repo in output, got:\n%s", output)
	}
}

func TestCreateAgentConfigCommand_MissingName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"create", "agentconfig",
		"--config", cfgPath,
		"--agents-md", "test",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when name is missing")
	}
	if !strings.Contains(err.Error(), "agentconfig name is required") {
		t.Errorf("expected 'agentconfig name is required' error, got: %v", err)
	}
}

func TestRunCommand_DryRun_AgentConfigFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "secret: my-secret\nagentConfig: default-ac\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"run",
		"--config", cfgPath,
		"--dry-run",
		"--prompt", "hello",
		"--name", "ac-task",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "default-ac") {
		t.Errorf("expected agentConfigRef 'default-ac' in output, got:\n%s", output)
	}
}

func TestInstallCommand_DryRun(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "CustomResourceDefinition") {
		t.Errorf("expected CRD manifest in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "Deployment") {
		t.Errorf("expected Deployment manifest in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "name: kelos-controller-role") {
		t.Errorf("expected controller ClusterRole in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "- rolebindings") {
		t.Errorf("expected rolebindings RBAC rule in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	// Should not contain installation messages.
	if strings.Contains(output, "Installing kelos") {
		t.Errorf("dry-run should not print installation messages, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_DryRun_Version(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--version", "v0.5.0"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(output, ":latest") {
		t.Errorf("expected all :latest tags to be replaced, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, ":v0.5.0") {
		t.Errorf("expected :v0.5.0 tags in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_DryRun_ImagePullPolicy(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--image-pull-policy", "Always"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "imagePullPolicy: Always") {
		t.Errorf("expected imagePullPolicy: Always in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "--spawner-image-pull-policy=Always") {
		t.Errorf("expected --spawner-image-pull-policy=Always in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestRunCommand_DryRun_CodexOAuthToken(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "oauthToken: '{\"token\":\"test\"}'\ntype: codex\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"run",
		"--config", cfgPath,
		"--dry-run",
		"--prompt", "hello",
		"--name", "codex-oauth-task",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "type: codex") {
		t.Errorf("expected 'type: codex' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "type: oauth") {
		t.Errorf("expected credential 'type: oauth' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "kelos-credentials") {
		t.Errorf("expected 'kelos-credentials' secret reference in output, got:\n%s", output)
	}
}

func TestRunCommand_DryRun_CodexOAuthToken_FileRef(t *testing.T) {
	dir := t.TempDir()

	authFile := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authFile, []byte(`{"token":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf("oauthToken: \"@%s\"\ntype: codex\n", authFile)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"run",
		"--config", cfgPath,
		"--dry-run",
		"--prompt", "hello",
		"--name", "codex-file-task",
		"--namespace", "test-ns",
	})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := cmd.Execute(); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var out bytes.Buffer
	out.ReadFrom(r)
	output := out.String()

	if !strings.Contains(output, "type: oauth") {
		t.Errorf("expected credential 'type: oauth' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "kelos-credentials") {
		t.Errorf("expected 'kelos-credentials' secret reference in output, got:\n%s", output)
	}
}

func TestResolveContent(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got, err := resolveContent("")
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("plain string", func(t *testing.T) {
		got, err := resolveContent("my-token")
		if err != nil {
			t.Fatal(err)
		}
		if got != "my-token" {
			t.Errorf("expected %q, got %q", "my-token", got)
		}
	})

	t.Run("file reference", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "token.txt")
		if err := os.WriteFile(f, []byte("file-content"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveContent("@" + f)
		if err != nil {
			t.Fatal(err)
		}
		if got != "file-content" {
			t.Errorf("expected %q, got %q", "file-content", got)
		}
	})

	t.Run("file reference trims trailing newline", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "token.txt")
		if err := os.WriteFile(f, []byte("file-content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveContent("@" + f)
		if err != nil {
			t.Fatal(err)
		}
		if got != "file-content" {
			t.Errorf("expected %q, got %q", "file-content", got)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := resolveContent("@/nonexistent/path")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}
