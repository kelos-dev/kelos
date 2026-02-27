package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newGetAgentConfigCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool

	cmd := &cobra.Command{
		Use:     "agentconfig [name]",
		Aliases: []string{"agentconfigs", "ac"},
		Short:   "List agent configs or get a specific agent config",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			if *allNamespaces && len(args) == 1 {
				return fmt.Errorf("a resource cannot be retrieved by name across all namespaces")
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()

			if len(args) == 1 {
				ac := &kelosv1alpha1.AgentConfig{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, ac); err != nil {
					return fmt.Errorf("getting agent config: %w", err)
				}

				ac.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("AgentConfig"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, ac)
				case "json":
					return printJSON(os.Stdout, ac)
				default:
					if detail {
						printAgentConfigDetail(os.Stdout, ac)
					} else {
						printAgentConfigTable(os.Stdout, []kelosv1alpha1.AgentConfig{*ac}, false)
					}
					return nil
				}
			}

			acList := &kelosv1alpha1.AgentConfigList{}
			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}
			if err := cl.List(ctx, acList, listOpts...); err != nil {
				return fmt.Errorf("listing agent configs: %w", err)
			}

			acList.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("AgentConfigList"))
			switch output {
			case "yaml":
				return printYAML(os.Stdout, acList)
			case "json":
				return printJSON(os.Stdout, acList)
			default:
				printAgentConfigTable(os.Stdout, acList.Items, *allNamespaces)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific agent config")

	cmd.ValidArgsFunction = completeAgentConfigNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}
