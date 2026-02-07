package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const configTemplate = `# Axon configuration file
# See: https://github.com/gjkim42/axon

# OAuth token (axon auto-creates the Kubernetes secret for you)
oauthToken: ""

# Or use an API key instead:
# apiKey: ""

# Model override (optional)
# model: ""

# Default namespace (optional)
# namespace: default

# Default workspace (optional)
# Reference an existing Workspace resource by name:
# workspace:
#   name: my-workspace
# Or specify inline (CLI auto-creates the Workspace resource):
# workspace:
#   repo: https://github.com/org/repo.git
#   ref: main
#   token: ""  # GitHub token for git auth and gh CLI (optional)

# Advanced: provide your own Kubernetes secret directly
# secret: ""
# credentialType: oauth
`

func newInitCommand(_ *ClientConfig) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("config")
			if path == "" {
				var err error
				path, err = DefaultConfigPath()
				if err != nil {
					return err
				}
			}

			if !force {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("config file already exists: %s (use --force to overwrite)", path)
				}
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return fmt.Errorf("creating directory: %w", err)
			}

			if err := os.WriteFile(path, []byte(configTemplate), 0o600); err != nil {
				return fmt.Errorf("writing config file: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Config file created: %s\n", path)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing config file")

	return cmd
}
