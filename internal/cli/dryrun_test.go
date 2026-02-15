package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if !strings.Contains(output, "axon-credentials") {
		t.Errorf("expected 'axon-credentials' secret reference in output, got:\n%s", output)
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

	if !strings.Contains(output, "axon-workspace") {
		t.Errorf("expected workspace reference 'axon-workspace' in dry-run output, got:\n%s", output)
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

	if !strings.Contains(output, "CustomResourceDefinition") {
		t.Errorf("expected CRD manifest in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "Deployment") {
		t.Errorf("expected Deployment manifest in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "name: axon-controller-role") {
		t.Errorf("expected controller ClusterRole in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "- rolebindings") {
		t.Errorf("expected rolebindings RBAC rule in dry-run output, got:\n%s", output[:min(len(output), 500)])
	}
	// Should not contain installation messages.
	if strings.Contains(output, "Installing axon") {
		t.Errorf("dry-run should not print installation messages, got:\n%s", output[:min(len(output), 500)])
	}
}
