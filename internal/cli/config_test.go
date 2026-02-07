package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_ValidFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
secret: my-secret
credentialType: oauth
model: claude-sonnet-4-5-20250929
namespace: my-namespace
workspace:
  name: my-workspace
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Secret != "my-secret" {
		t.Errorf("Secret = %q, want %q", cfg.Secret, "my-secret")
	}
	if cfg.CredentialType != "oauth" {
		t.Errorf("CredentialType = %q, want %q", cfg.CredentialType, "oauth")
	}
	if cfg.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-5-20250929")
	}
	if cfg.Namespace != "my-namespace" {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, "my-namespace")
	}
	if cfg.Workspace.Name != "my-workspace" {
		t.Errorf("Workspace.Name = %q, want %q", cfg.Workspace.Name, "my-workspace")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Secret != "" {
		t.Errorf("expected empty Secret, got %q", cfg.Secret)
	}
}

func TestLoadConfig_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("secret: [invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoadConfig_Partial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `secret: only-secret
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Secret != "only-secret" {
		t.Errorf("Secret = %q, want %q", cfg.Secret, "only-secret")
	}
	if cfg.CredentialType != "" {
		t.Errorf("CredentialType = %q, want empty", cfg.CredentialType)
	}
	if cfg.Workspace.Name != "" {
		t.Errorf("Workspace.Name = %q, want empty", cfg.Workspace.Name)
	}
	if cfg.Workspace.Repo != "" {
		t.Errorf("Workspace.Repo = %q, want empty", cfg.Workspace.Repo)
	}
}

func TestLoadConfig_DefaultPath(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Skipf("skipping: default config file cannot be parsed: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestLoadConfig_ExplicitPathOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	content := `secret: custom-secret
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Secret != "custom-secret" {
		t.Errorf("Secret = %q, want %q", cfg.Secret, "custom-secret")
	}
}

func TestLoadConfig_OAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `oauthToken: my-oauth-token
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OAuthToken != "my-oauth-token" {
		t.Errorf("OAuthToken = %q, want %q", cfg.OAuthToken, "my-oauth-token")
	}
	if cfg.Secret != "" {
		t.Errorf("Secret = %q, want empty", cfg.Secret)
	}
}

func TestLoadConfig_WorkspaceInline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `workspace:
  repo: https://github.com/org/repo.git
  ref: main
  token: my-token
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Workspace.Repo != "https://github.com/org/repo.git" {
		t.Errorf("Workspace.Repo = %q, want %q", cfg.Workspace.Repo, "https://github.com/org/repo.git")
	}
	if cfg.Workspace.Ref != "main" {
		t.Errorf("Workspace.Ref = %q, want %q", cfg.Workspace.Ref, "main")
	}
	if cfg.Workspace.Token != "my-token" {
		t.Errorf("Workspace.Token = %q, want %q", cfg.Workspace.Token, "my-token")
	}
	if cfg.Workspace.Name != "" {
		t.Errorf("Workspace.Name = %q, want empty", cfg.Workspace.Name)
	}
}

func TestLoadConfig_APIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `apiKey: my-api-key
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey != "my-api-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "my-api-key")
	}
	if cfg.Secret != "" {
		t.Errorf("Secret = %q, want empty", cfg.Secret)
	}
}
