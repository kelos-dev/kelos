package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

var envVarNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config holds configuration loaded from the kelos config file.
type Config struct {
	OAuthToken     string          `json:"oauthToken,omitempty"`
	APIKey         string          `json:"apiKey,omitempty"`
	Secret         string          `json:"secret,omitempty"`
	CredentialType string          `json:"credentialType,omitempty"`
	Type           string          `json:"type,omitempty"`
	Model          string          `json:"model,omitempty"`
	Effort         string          `json:"effort,omitempty"`
	Namespace      string          `json:"namespace,omitempty"`
	Workspace      WorkspaceConfig `json:"workspace,omitempty"`
	AgentConfig    string          `json:"agentConfig,omitempty"`
	Env            []EnvVar        `json:"env,omitempty"`
}

// EnvVar represents an environment variable in the config file.
type EnvVar struct {
	Name      string        `json:"name"`
	Value     *string       `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

// EnvVarSource represents a source for an environment variable's value.
// Only secretKeyRef and configMapKeyRef are supported.
type EnvVarSource struct {
	SecretKeyRef    *corev1.SecretKeySelector    `json:"secretKeyRef,omitempty"`
	ConfigMapKeyRef *corev1.ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
}

// ToCorev1EnvVarSource converts to a Kubernetes EnvVarSource.
func (s *EnvVarSource) ToCorev1EnvVarSource() *corev1.EnvVarSource {
	if s == nil {
		return nil
	}
	return &corev1.EnvVarSource{
		SecretKeyRef:    s.SecretKeyRef,
		ConfigMapKeyRef: s.ConfigMapKeyRef,
	}
}

// WorkspaceConfig holds workspace-related configuration.
// If Name is set, it references an existing Workspace CR.
// If Repo is set, the CLI auto-creates a Workspace CR.
type WorkspaceConfig struct {
	Name      string           `json:"name,omitempty"`
	Repo      string           `json:"repo,omitempty"`
	Ref       string           `json:"ref,omitempty"`
	Token     string           `json:"token,omitempty"`
	GitHubApp *GitHubAppConfig `json:"githubApp,omitempty"`
}

// GitHubAppConfig holds GitHub App credentials for workspace authentication.
type GitHubAppConfig struct {
	AppID          string `json:"appID"`
	InstallationID string `json:"installationID"`
	PrivateKeyPath string `json:"privateKeyPath"`
}

// DefaultConfigPath returns the default config file path (~/.kelos/config.yaml).
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".kelos", "config.yaml"), nil
}

// LoadConfig reads and parses the config file at the given path.
// If path is empty, the default path (~/.kelos/config.yaml) is used.
// If the file does not exist, an empty Config is returned without error.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return &Config{}, nil
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	if err := validateEnvVars(cfg.Env); err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	return cfg, nil
}

func validateEnvVars(envVars []EnvVar) error {
	for i, ev := range envVars {
		if ev.Name == "" {
			return fmt.Errorf("env[%d]: name is required", i)
		}
		if !envVarNameRegexp.MatchString(ev.Name) {
			return fmt.Errorf("env[%d]: invalid name %q: must match [A-Za-z_][A-Za-z0-9_]*", i, ev.Name)
		}
		if ev.Value != nil && ev.ValueFrom != nil {
			return fmt.Errorf("env[%d] %q: value and valueFrom are mutually exclusive", i, ev.Name)
		}
		if ev.Value == nil && ev.ValueFrom == nil {
			return fmt.Errorf("env[%d] %q: one of value or valueFrom is required", i, ev.Name)
		}
		if ev.ValueFrom != nil {
			if ev.ValueFrom.SecretKeyRef == nil && ev.ValueFrom.ConfigMapKeyRef == nil {
				return fmt.Errorf("env[%d] %q: valueFrom must specify secretKeyRef or configMapKeyRef", i, ev.Name)
			}
			if ev.ValueFrom.SecretKeyRef != nil && ev.ValueFrom.ConfigMapKeyRef != nil {
				return fmt.Errorf("env[%d] %q: valueFrom must specify only one of secretKeyRef or configMapKeyRef", i, ev.Name)
			}
			if ref := ev.ValueFrom.SecretKeyRef; ref != nil {
				if ref.Name == "" || ref.Key == "" {
					return fmt.Errorf("env[%d] %q: secretKeyRef requires both name and key", i, ev.Name)
				}
			}
			if ref := ev.ValueFrom.ConfigMapKeyRef; ref != nil {
				if ref.Name == "" || ref.Key == "" {
					return fmt.Errorf("env[%d] %q: configMapKeyRef requires both name and key", i, ev.Name)
				}
			}
		}
	}
	return nil
}
