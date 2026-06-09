package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_ValidFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
secret: my-secret
credentialType: oauth
type: codex
model: claude-sonnet-4-5-20250929
effort: high
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
	if cfg.Type != "codex" {
		t.Errorf("Type = %q, want %q", cfg.Type, "codex")
	}
	if cfg.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-5-20250929")
	}
	if cfg.Effort != "high" {
		t.Errorf("Effort = %q, want %q", cfg.Effort, "high")
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

func TestLoadConfig_Type(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `type: codex
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Type != "codex" {
		t.Errorf("Type = %q, want %q", cfg.Type, "codex")
	}
}

func TestLoadConfig_TypeEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `secret: my-secret
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Type != "" {
		t.Errorf("Type = %q, want empty", cfg.Type)
	}
}

func TestLoadConfig_AgentConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `agentConfig: my-agent-config
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AgentConfig != "my-agent-config" {
		t.Errorf("AgentConfig = %q, want %q", cfg.AgentConfig, "my-agent-config")
	}
}

func TestLoadConfig_Env(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `env:
  - name: CLAUDE_CODE_USE_BEDROCK
    value: "1"
  - name: AWS_REGION
    value: us-west-2
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Env) != 2 {
		t.Fatalf("Env length = %d, want 2", len(cfg.Env))
	}
	if cfg.Env[0].Name != "CLAUDE_CODE_USE_BEDROCK" || cfg.Env[0].Value == nil || *cfg.Env[0].Value != "1" {
		t.Errorf("Env[0] = %+v, want CLAUDE_CODE_USE_BEDROCK=1", cfg.Env[0])
	}
	if cfg.Env[1].Name != "AWS_REGION" || cfg.Env[1].Value == nil || *cfg.Env[1].Value != "us-west-2" {
		t.Errorf("Env[1] = %+v, want AWS_REGION=us-west-2", cfg.Env[1])
	}
}

func TestLoadConfig_EnvEmptyValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `env:
  - name: FOO
    value: ""
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Env) != 1 {
		t.Fatalf("Env length = %d, want 1", len(cfg.Env))
	}
	if cfg.Env[0].Name != "FOO" {
		t.Errorf("Env[0].Name = %q, want FOO", cfg.Env[0].Name)
	}
	if cfg.Env[0].Value == nil {
		t.Fatal("Env[0].Value is nil, want pointer to empty string")
	}
	if *cfg.Env[0].Value != "" {
		t.Errorf("Env[0].Value = %q, want empty string", *cfg.Env[0].Value)
	}
}

func TestLoadConfig_EnvValueFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `env:
  - name: MY_SECRET
    valueFrom:
      secretKeyRef:
        name: my-secret
        key: token
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Env) != 1 {
		t.Fatalf("Env length = %d, want 1", len(cfg.Env))
	}
	if cfg.Env[0].Name != "MY_SECRET" {
		t.Errorf("Env[0].Name = %q, want MY_SECRET", cfg.Env[0].Name)
	}
	if cfg.Env[0].ValueFrom == nil {
		t.Fatal("Env[0].ValueFrom is nil")
	}
	if cfg.Env[0].ValueFrom.SecretKeyRef == nil {
		t.Fatal("Env[0].ValueFrom.SecretKeyRef is nil")
	}
	if cfg.Env[0].ValueFrom.SecretKeyRef.LocalObjectReference.Name != "my-secret" {
		t.Errorf("SecretKeyRef.Name = %q, want my-secret", cfg.Env[0].ValueFrom.SecretKeyRef.LocalObjectReference.Name)
	}
	if cfg.Env[0].ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("SecretKeyRef.Key = %q, want token", cfg.Env[0].ValueFrom.SecretKeyRef.Key)
	}
}

func TestLoadConfig_EnvValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "empty name",
			yaml: `env:
  - name: ""
    value: x
`,
			wantErr: "env[0]: name is required",
		},
		{
			name: "invalid name",
			yaml: `env:
  - name: "1BAD"
    value: x
`,
			wantErr: `env[0]: invalid name "1BAD"`,
		},
		{
			name: "both value and valueFrom",
			yaml: `env:
  - name: FOO
    value: bar
    valueFrom:
      secretKeyRef:
        name: s
        key: k
`,
			wantErr: `env[0] "FOO": value and valueFrom are mutually exclusive`,
		},
		{
			name: "neither value nor valueFrom",
			yaml: `env:
  - name: FOO
`,
			wantErr: `env[0] "FOO": one of value or valueFrom is required`,
		},
		{
			name: "valueFrom without ref",
			yaml: `env:
  - name: FOO
    valueFrom: {}
`,
			wantErr: `env[0] "FOO": valueFrom must specify secretKeyRef or configMapKeyRef`,
		},
		{
			name: "secretKeyRef missing key",
			yaml: `env:
  - name: FOO
    valueFrom:
      secretKeyRef:
        name: my-secret
`,
			wantErr: `env[0] "FOO": secretKeyRef requires both name and key`,
		},
		{
			name: "configMapKeyRef missing name",
			yaml: `env:
  - name: FOO
    valueFrom:
      configMapKeyRef:
        key: k
`,
			wantErr: `env[0] "FOO": configMapKeyRef requires both name and key`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadConfig_EnvConfigMapKeyRef(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `env:
  - name: APP_CONF
    valueFrom:
      configMapKeyRef:
        name: my-cm
        key: app.conf
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Env) != 1 {
		t.Fatalf("Env length = %d, want 1", len(cfg.Env))
	}
	if cfg.Env[0].ValueFrom == nil || cfg.Env[0].ValueFrom.ConfigMapKeyRef == nil {
		t.Fatal("expected ConfigMapKeyRef to be set")
	}
	if cfg.Env[0].ValueFrom.ConfigMapKeyRef.Name != "my-cm" {
		t.Errorf("ConfigMapKeyRef.Name = %q, want my-cm", cfg.Env[0].ValueFrom.ConfigMapKeyRef.Name)
	}
	if cfg.Env[0].ValueFrom.ConfigMapKeyRef.Key != "app.conf" {
		t.Errorf("ConfigMapKeyRef.Key = %q, want app.conf", cfg.Env[0].ValueFrom.ConfigMapKeyRef.Key)
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
