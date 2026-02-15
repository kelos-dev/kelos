package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func newGetCommand(cfg *ClientConfig) *cobra.Command {
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.PersistentFlags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List resources across all namespaces")

	cmd.AddCommand(newGetTaskCommand(cfg, &allNamespaces))
	cmd.AddCommand(newGetTaskSpawnerCommand(cfg, &allNamespaces))
	cmd.AddCommand(newGetWorkspaceCommand(cfg, &allNamespaces))

	return cmd
}

func newGetTaskSpawnerCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var watch bool

	cmd := &cobra.Command{
		Use:     "taskspawner [name]",
		Aliases: []string{"taskspawners", "ts"},
		Short:   "List task spawners or get details of a specific task spawner",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			if watch && output != "" {
				return fmt.Errorf("--watch is not supported with --output")
			}

			if watch && len(args) == 1 {
				return fmt.Errorf("--watch is only supported when listing resources")
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
				ts := &axonv1alpha1.TaskSpawner{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, ts); err != nil {
					return fmt.Errorf("getting task spawner: %w", err)
				}

				ts.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("TaskSpawner"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, ts)
				case "json":
					return printJSON(os.Stdout, ts)
				default:
					printTaskSpawnerDetail(os.Stdout, ts)
					return nil
				}
			}

			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}

			if watch {
				return watchTaskSpawners(ctx, cl, listOpts, *allNamespaces)
			}

			tsList := &axonv1alpha1.TaskSpawnerList{}
			if err := cl.List(ctx, tsList, listOpts...); err != nil {
				return fmt.Errorf("listing task spawners: %w", err)
			}

			tsList.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("TaskSpawnerList"))
			switch output {
			case "yaml":
				return printYAML(os.Stdout, tsList)
			case "json":
				return printJSON(os.Stdout, tsList)
			default:
				printTaskSpawnerTable(os.Stdout, tsList.Items, *allNamespaces)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for changes")

	cmd.ValidArgsFunction = completeTaskSpawnerNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func newGetTaskCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var watch bool

	cmd := &cobra.Command{
		Use:     "task [name]",
		Aliases: []string{"tasks"},
		Short:   "List tasks or get details of a specific task",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			if watch && output != "" {
				return fmt.Errorf("--watch is not supported with --output")
			}

			if watch && len(args) == 1 {
				return fmt.Errorf("--watch is only supported when listing resources")
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

			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}

			if watch {
				return watchTasks(ctx, cl, listOpts, *allNamespaces)
			}

			taskList := &axonv1alpha1.TaskList{}
			if err := cl.List(ctx, taskList, listOpts...); err != nil {
				return fmt.Errorf("listing tasks: %w", err)
			}

			taskList.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("TaskList"))
			switch output {
			case "yaml":
				return printYAML(os.Stdout, taskList)
			case "json":
				return printJSON(os.Stdout, taskList)
			default:
				printTaskTable(os.Stdout, taskList.Items, *allNamespaces)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for changes")

	cmd.ValidArgsFunction = completeTaskNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func newGetWorkspaceCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var watch bool

	cmd := &cobra.Command{
		Use:     "workspace [name]",
		Aliases: []string{"workspaces", "ws"},
		Short:   "List workspaces or get details of a specific workspace",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			if watch && output != "" {
				return fmt.Errorf("--watch is not supported with --output")
			}

			if watch && len(args) == 1 {
				return fmt.Errorf("--watch is only supported when listing resources")
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
				ws := &axonv1alpha1.Workspace{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, ws); err != nil {
					return fmt.Errorf("getting workspace: %w", err)
				}

				ws.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("Workspace"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, ws)
				case "json":
					return printJSON(os.Stdout, ws)
				default:
					printWorkspaceDetail(os.Stdout, ws)
					return nil
				}
			}

			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}

			if watch {
				return watchWorkspaces(ctx, cl, listOpts, *allNamespaces)
			}

			wsList := &axonv1alpha1.WorkspaceList{}
			if err := cl.List(ctx, wsList, listOpts...); err != nil {
				return fmt.Errorf("listing workspaces: %w", err)
			}

			wsList.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("WorkspaceList"))
			switch output {
			case "yaml":
				return printYAML(os.Stdout, wsList)
			case "json":
				return printJSON(os.Stdout, wsList)
			default:
				printWorkspaceTable(os.Stdout, wsList.Items, *allNamespaces)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for changes")

	cmd.ValidArgsFunction = completeWorkspaceNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}
