package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func newGetCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.AddCommand(newGetTaskCommand(cfg))

	return cmd
}

func newGetTaskCommand(cfg *ClientConfig) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:     "task [name]",
		Aliases: []string{"tasks"},
		Short:   "List tasks or get details of a specific task",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()

			if len(args) == 1 {
				task := &axonv1alpha1.Task{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, task); err != nil {
					return fmt.Errorf("getting task: %w", err)
				}

				task.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("Task"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, task)
				case "json":
					return printJSON(os.Stdout, task)
				default:
					printTaskDetail(os.Stdout, task)
					return nil
				}
			}

			taskList := &axonv1alpha1.TaskList{}
			if err := cl.List(ctx, taskList, client.InNamespace(ns)); err != nil {
				return fmt.Errorf("listing tasks: %w", err)
			}

			taskList.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("TaskList"))
			switch output {
			case "yaml":
				return printYAML(os.Stdout, taskList)
			case "json":
				return printJSON(os.Stdout, taskList)
			default:
				printTaskTable(os.Stdout, taskList.Items)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")

	return cmd
}
