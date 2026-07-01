package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newGetWorkerPoolCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool

	cmd := &cobra.Command{
		Use:     "workerpool [name]",
		Aliases: []string{"workerpools", "wp"},
		Short:   "List worker pools or get a specific worker pool",
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
				wp := &kelos.WorkerPool{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, wp); err != nil {
					return fmt.Errorf("getting worker pool %s: %w", args[0], err)
				}

				wp.SetGroupVersionKind(kelos.GroupVersion.WithKind("WorkerPool"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, wp)
				case "json":
					return printJSON(os.Stdout, wp)
				default:
					if detail {
						printWorkerPoolDetail(os.Stdout, wp)
					} else {
						printWorkerPoolTable(os.Stdout, []kelos.WorkerPool{*wp}, false)
					}
					return nil
				}
			}

			wpList := &kelos.WorkerPoolList{}
			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}
			if err := cl.List(ctx, wpList, listOpts...); err != nil {
				return fmt.Errorf("listing worker pools: %w", err)
			}

			wpList.SetGroupVersionKind(kelos.GroupVersion.WithKind("WorkerPoolList"))
			switch output {
			case "yaml":
				return printYAML(os.Stdout, wpList)
			case "json":
				return printJSON(os.Stdout, wpList)
			default:
				printWorkerPoolTable(os.Stdout, wpList.Items, *allNamespaces)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific worker pool")

	cmd.ValidArgsFunction = completeWorkerPoolNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}
