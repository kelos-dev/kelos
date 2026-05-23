package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCommand_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"init", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty config file")
	}
}

func TestInitCommand_CreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.yaml")

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"init", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}

func TestInitCommand_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"init", "--config", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when file exists without --force")
	}
}

func TestInitCommand_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"init", "--config", path, "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(data) == "existing" {
		t.Fatal("expected file to be overwritten")
	}
}

func TestPrintNextSteps_ClaudeCodeOAuthUsesSetupToken(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	printNextSteps("/tmp/config.yaml")

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading captured output: %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "Claude Code (OAuth): run 'claude setup-token'") {
		t.Errorf("expected next-steps output to instruct running 'claude setup-token' for Claude Code OAuth, got:\n%s", output)
	}
	if strings.Contains(output, "https://claude.ai/settings/developer") {
		t.Errorf("next-steps output must not link to https://claude.ai/settings/developer (API key page, not OAuth), got:\n%s", output)
	}
}

func TestInitCommand_ConfigContainsCredentialURLs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"init", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}

	content := string(data)
	expectedStrings := []string{
		"claude setup-token",
		"https://console.anthropic.com/settings/keys",
		"https://platform.openai.com/api-keys",
		"https://aistudio.google.com/app/apikey",
		"https://cursor.com/dashboard",
	}
	for _, s := range expectedStrings {
		if !strings.Contains(content, s) {
			t.Errorf("config file missing credential info: %s", s)
		}
	}
}
