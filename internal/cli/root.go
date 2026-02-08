package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	cfg := &ClientConfig{}
	var configPath string

	cmd := &cobra.Command{
		Use:           "axon",
		Short:         "CLI for managing AI agent Tasks on Kubernetes",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			switch cmd.Name() {
			case "init", "install", "uninstall":
				return nil
			}

			config, err := LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			cfg.Config = config

			if !cmd.Flags().Changed("namespace") && config.Namespace != "" {
				cfg.Namespace = config.Namespace
			}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&configPath, "config", "", "path to config file (default ~/.axon/config.yaml)")
	cmd.PersistentFlags().StringVar(&cfg.Kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	cmd.PersistentFlags().StringVarP(&cfg.Namespace, "namespace", "n", "", "Kubernetes namespace")

	cmd.AddCommand(
		newRunCommand(cfg),
		newCreateCommand(cfg),
		newGetCommand(cfg),
		newLogsCommand(cfg),
		newDeleteCommand(cfg),
		newInitCommand(cfg),
		newInstallCommand(cfg),
		newUninstallCommand(cfg),
	)

	return cmd
}
