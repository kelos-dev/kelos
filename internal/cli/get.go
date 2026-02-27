package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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
	cmd.AddCommand(newGetAgentConfigCommand(cfg, &allNamespaces))

	return cmd
}

func newGetTaskSpawnerCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool

	cmd := &cobra.Command{
		Use:     "taskspawner [name]",
		Aliases: []string{"taskspawners", "ts"},
		Short:   "List task spawners or get a specific task spawner",
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
				ts := &kelosv1alpha1.TaskSpawner{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, ts); err != nil {
					return fmt.Errorf("getting task spawner: %w", err)
				}

				ts.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("TaskSpawner"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, ts)
				case "json":
					return printJSON(os.Stdout, ts)
				default:
					if detail {
						printTaskSpawnerDetail(os.Stdout, ts)
					} else {
						printTaskSpawnerTable(os.Stdout, []kelosv1alpha1.TaskSpawner{*ts}, false)
					}
					return nil
				}
			}

			tsList := &kelosv1alpha1.TaskSpawnerList{}
			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}
			if err := cl.List(ctx, tsList, listOpts...); err != nil {
				return fmt.Errorf("listing task spawners: %w", err)
			}

			tsList.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("TaskSpawnerList"))
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
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific task spawner")

	cmd.ValidArgsFunction = completeTaskSpawnerNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func newGetTaskCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool
	var phases []string

	cmd := &cobra.Command{
		Use:     "task [name]",
		Aliases: []string{"tasks"},
		Short:   "List tasks or get a specific task",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			if *allNamespaces && len(args) == 1 {
				return fmt.Errorf("a resource cannot be retrieved by name across all namespaces")
			}

			if err := validatePhases(phases); err != nil {
				return err
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()

			if len(args) == 1 {
				task := &kelosv1alpha1.Task{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, task); err != nil {
					return fmt.Errorf("getting task: %w", err)
				}

				task.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("Task"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, task)
				case "json":
					return printJSON(os.Stdout, task)
				default:
					if detail {
						printTaskDetail(os.Stdout, task)
					} else {
						printTaskTable(os.Stdout, []kelosv1alpha1.Task{*task}, false)
					}
					return nil
				}
			}

			taskList := &kelosv1alpha1.TaskList{}
			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}
			if err := cl.List(ctx, taskList, listOpts...); err != nil {
				return fmt.Errorf("listing tasks: %w", err)
			}

			taskList.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("TaskList"))

			if len(phases) > 0 {
				taskList.Items = filterTasksByPhase(taskList.Items, phases)
			}

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
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific task")
	cmd.Flags().StringSliceVar(&phases, "phase", nil, "Filter tasks by phase (Pending, Running, Waiting, Succeeded, Failed)")

	cmd.ValidArgsFunction = completeTaskNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("phase", cobra.FixedCompletions(
		[]string{"Pending", "Running", "Waiting", "Succeeded", "Failed"},
		cobra.ShellCompDirectiveNoFileComp,
	))

	return cmd
}

func newGetWorkspaceCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool

	cmd := &cobra.Command{
		Use:     "workspace [name]",
		Aliases: []string{"workspaces", "ws"},
		Short:   "List workspaces or get a specific workspace",
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
				ws := &kelosv1alpha1.Workspace{}
				if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, ws); err != nil {
					return fmt.Errorf("getting workspace: %w", err)
				}

				ws.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("Workspace"))
				switch output {
				case "yaml":
					return printYAML(os.Stdout, ws)
				case "json":
					return printJSON(os.Stdout, ws)
				default:
					if detail {
						printWorkspaceDetail(os.Stdout, ws)
					} else {
						printWorkspaceTable(os.Stdout, []kelosv1alpha1.Workspace{*ws}, false)
					}
					return nil
				}
			}

			wsList := &kelosv1alpha1.WorkspaceList{}
			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}
			if err := cl.List(ctx, wsList, listOpts...); err != nil {
				return fmt.Errorf("listing workspaces: %w", err)
			}

			wsList.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("WorkspaceList"))
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
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific workspace")

	cmd.ValidArgsFunction = completeWorkspaceNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

var validTaskPhases = map[kelosv1alpha1.TaskPhase]bool{
	kelosv1alpha1.TaskPhasePending:   true,
	kelosv1alpha1.TaskPhaseRunning:   true,
	kelosv1alpha1.TaskPhaseWaiting:   true,
	kelosv1alpha1.TaskPhaseSucceeded: true,
	kelosv1alpha1.TaskPhaseFailed:    true,
}

func validatePhases(phases []string) error {
	for _, p := range phases {
		if !validTaskPhases[kelosv1alpha1.TaskPhase(p)] {
			return fmt.Errorf("unknown phase %q: must be one of Pending, Running, Waiting, Succeeded, Failed", p)
		}
	}
	return nil
}

func filterTasksByPhase(tasks []kelosv1alpha1.Task, phases []string) []kelosv1alpha1.Task {
	phaseSet := make(map[kelosv1alpha1.TaskPhase]bool, len(phases))
	for _, p := range phases {
		phaseSet[kelosv1alpha1.TaskPhase(p)] = true
	}
	filtered := make([]kelosv1alpha1.Task, 0, len(tasks))
	for _, t := range tasks {
		if phaseSet[t.Status.Phase] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
