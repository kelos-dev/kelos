package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const configTemplate = `# Axon configuration file
# See: https://github.com/axon-core/axon

# OAuth token (axon auto-creates the Kubernetes secret for you)
oauthToken: ""

# Or use an API key instead:
# apiKey: ""

# Agent type (optional, default: claude-code)
# For codex with oauth: set oauthToken to auth.json content or @filepath
#   oauthToken: "@~/.codex/auth.json"
# type: claude-code

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

# Default AgentConfig resource (optional)
# agentConfig: my-agent-config

# Advanced: provide your own Kubernetes secret directly
# secret: ""
# credentialType: oauth
`

func printNextSteps(configPath string) {
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Next steps:")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "1. Get your credentials:")
	fmt.Fprintln(os.Stdout, "   • For Claude Code (OAuth): https://claude.ai/settings/developer")
	fmt.Fprintln(os.Stdout, "   • For API access (API key): https://console.anthropic.com/settings/keys")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "2. Edit the config file and add your token:")
	fmt.Fprintf(os.Stdout, "   %s\n", configPath)
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "3. Install Axon (if not already installed):")
	fmt.Fprintln(os.Stdout, "   axon install")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "4. Run your first task:")
	fmt.Fprintln(os.Stdout, "   axon run -p \"Create a hello world program in Python\"")
	fmt.Fprintln(os.Stdout, "   axon logs <task-name> -f")
	fmt.Fprintln(os.Stdout, "")
}

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
			printNextSteps(path)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing config file")

	return cmd
}
