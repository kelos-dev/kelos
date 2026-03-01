package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const configTemplate = `# Kelos configuration file
# See: https://github.com/kelos-dev/kelos

# OAuth token (kelos auto-creates the Kubernetes secret for you)
oauthToken: ""

# Or use an API key instead:
# apiKey: ""

# Agent type (optional, default: claude-code)
# For codex with oauth: set oauthToken to auth.json content or @filepath
#   oauthToken: "@~/.codex/auth.json"
# type: claude-code

# Where to get credentials for each agent type:
#   claude-code:
#     OAuth token: https://claude.ai/settings/developer
#     API key:     https://console.anthropic.com/settings/keys
#   codex:
#     API key:     https://platform.openai.com/api-keys
#     OAuth:       run "codex auth login", then set oauthToken: "@~/.codex/auth.json"
#   gemini:
#     API key:     https://aistudio.google.com/app/apikey
#   opencode:
#     API key:     depends on the model provider (Anthropic, OpenAI, Google, etc.)

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
#   token: ""  # GitHub PAT for git auth and gh CLI (optional)
# Or use GitHub App authentication (recommended for production/org use):
# workspace:
#   repo: https://github.com/org/repo.git
#   ref: main
#   githubApp:
#     appID: ""
#     installationID: ""
#     privateKeyPath: ""  # path to PEM-encoded RSA private key

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
	fmt.Fprintln(os.Stdout, "   • Claude Code (OAuth): https://claude.ai/settings/developer")
	fmt.Fprintln(os.Stdout, "   • Claude Code (API key): https://console.anthropic.com/settings/keys")
	fmt.Fprintln(os.Stdout, "   • Codex (API key): https://platform.openai.com/api-keys")
	fmt.Fprintln(os.Stdout, "   • Gemini (API key): https://aistudio.google.com/app/apikey")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "2. Edit the config file and add your token:")
	fmt.Fprintf(os.Stdout, "   %s\n", configPath)
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "3. Install Kelos (if not already installed):")
	fmt.Fprintln(os.Stdout, "   kelos install")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "4. Run your first task:")
	fmt.Fprintln(os.Stdout, "   kelos run -p \"Create a hello world program in Python\"")
	fmt.Fprintln(os.Stdout, "   kelos logs <task-name> -f")
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
