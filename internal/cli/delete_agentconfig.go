package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newDeleteAgentConfigCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "agentconfig [name]",
		Aliases: []string{"agentconfigs", "ac"},
		Short:   "Delete an agent config",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify agent config name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("agent config name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 agent config name, got %d\nUsage: %s", len(args), cmd.Use)
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()

			if all {
				acList := &kelosv1alpha1.AgentConfigList{}
				if err := cl.List(ctx, acList, client.InNamespace(ns)); err != nil {
					return fmt.Errorf("listing agent configs: %w", err)
				}
				if len(acList.Items) == 0 {
					fmt.Fprintln(os.Stdout, "No agent configs found")
					return nil
				}
				for i := range acList.Items {
					if err := cl.Delete(ctx, &acList.Items[i]); err != nil {
						return fmt.Errorf("deleting agent config %s: %w", acList.Items[i].Name, err)
					}
					fmt.Fprintf(os.Stdout, "agentconfig/%s deleted\n", acList.Items[i].Name)
				}
				return nil
			}

			ac := &kelosv1alpha1.AgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      args[0],
					Namespace: ns,
				},
			}

			if err := cl.Delete(ctx, ac); err != nil {
				return fmt.Errorf("deleting agent config: %w", err)
			}
			fmt.Fprintf(os.Stdout, "agentconfig/%s deleted\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all agent configs in the namespace")
	cmd.ValidArgsFunction = completeAgentConfigNames(cfg)

	return cmd
}
