package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// Config holds configuration loaded from the axon config file.
type Config struct {
	OAuthToken     string          `json:"oauthToken,omitempty"`
	APIKey         string          `json:"apiKey,omitempty"`
	Secret         string          `json:"secret,omitempty"`
	CredentialType string          `json:"credentialType,omitempty"`
	Model          string          `json:"model,omitempty"`
	Namespace      string          `json:"namespace,omitempty"`
	Workspace      WorkspaceConfig `json:"workspace,omitempty"`
}

// WorkspaceConfig holds workspace-related configuration.
// If Name is set, it references an existing Workspace CR.
// If Repo is set, the CLI auto-creates a Workspace CR.
type WorkspaceConfig struct {
	Name  string `json:"name,omitempty"`
	Repo  string `json:"repo,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Token string `json:"token,omitempty"`
}

// DefaultConfigPath returns the default config file path (~/.axon/config.yaml).
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".axon", "config.yaml"), nil
}

// LoadConfig reads and parses the config file at the given path.
// If path is empty, the default path (~/.axon/config.yaml) is used.
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
	return cfg, nil
}
