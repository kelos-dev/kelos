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

func newDeleteCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.AddCommand(newDeleteTaskCommand(cfg))
	cmd.AddCommand(newDeleteWorkspaceCommand(cfg))
	cmd.AddCommand(newDeleteTaskSpawnerCommand(cfg))
	cmd.AddCommand(newDeleteAgentConfigCommand(cfg))

	return cmd
}

func newDeleteTaskCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "task [name]",
		Aliases: []string{"tasks"},
		Short:   "Delete a task",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify task name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("task name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 task name, got %d\nUsage: %s", len(args), cmd.Use)
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
				taskList := &kelosv1alpha1.TaskList{}
				if err := cl.List(ctx, taskList, client.InNamespace(ns)); err != nil {
					return fmt.Errorf("listing tasks: %w", err)
				}
				if len(taskList.Items) == 0 {
					fmt.Fprintln(os.Stdout, "No tasks found")
					return nil
				}
				for i := range taskList.Items {
					if err := cl.Delete(ctx, &taskList.Items[i]); err != nil {
						return fmt.Errorf("deleting task %s: %w", taskList.Items[i].Name, err)
					}
					fmt.Fprintf(os.Stdout, "task/%s deleted\n", taskList.Items[i].Name)
				}
				return nil
			}

			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      args[0],
					Namespace: ns,
				},
			}

			if err := cl.Delete(ctx, task); err != nil {
				return fmt.Errorf("deleting task: %w", err)
			}
			fmt.Fprintf(os.Stdout, "task/%s deleted\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all tasks in the namespace")
	cmd.ValidArgsFunction = completeTaskNames(cfg)

	return cmd
}

func newDeleteWorkspaceCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "workspace [name]",
		Aliases: []string{"workspaces", "ws"},
		Short:   "Delete a workspace",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify workspace name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("workspace name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 workspace name, got %d\nUsage: %s", len(args), cmd.Use)
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
				wsList := &kelosv1alpha1.WorkspaceList{}
				if err := cl.List(ctx, wsList, client.InNamespace(ns)); err != nil {
					return fmt.Errorf("listing workspaces: %w", err)
				}
				if len(wsList.Items) == 0 {
					fmt.Fprintln(os.Stdout, "No workspaces found")
					return nil
				}
				for i := range wsList.Items {
					if err := cl.Delete(ctx, &wsList.Items[i]); err != nil {
						return fmt.Errorf("deleting workspace %s: %w", wsList.Items[i].Name, err)
					}
					fmt.Fprintf(os.Stdout, "workspace/%s deleted\n", wsList.Items[i].Name)
				}
				return nil
			}

			ws := &kelosv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      args[0],
					Namespace: ns,
				},
			}

			if err := cl.Delete(ctx, ws); err != nil {
				return fmt.Errorf("deleting workspace: %w", err)
			}
			fmt.Fprintf(os.Stdout, "workspace/%s deleted\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all workspaces in the namespace")
	cmd.ValidArgsFunction = completeWorkspaceNames(cfg)

	return cmd
}

func newDeleteTaskSpawnerCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "taskspawner [name]",
		Aliases: []string{"taskspawners", "ts"},
		Short:   "Delete a task spawner",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify task spawner name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("task spawner name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 task spawner name, got %d\nUsage: %s", len(args), cmd.Use)
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
				tsList := &kelosv1alpha1.TaskSpawnerList{}
				if err := cl.List(ctx, tsList, client.InNamespace(ns)); err != nil {
					return fmt.Errorf("listing task spawners: %w", err)
				}
				if len(tsList.Items) == 0 {
					fmt.Fprintln(os.Stdout, "No task spawners found")
					return nil
				}
				for i := range tsList.Items {
					if err := cl.Delete(ctx, &tsList.Items[i]); err != nil {
						return fmt.Errorf("deleting task spawner %s: %w", tsList.Items[i].Name, err)
					}
					fmt.Fprintf(os.Stdout, "taskspawner/%s deleted\n", tsList.Items[i].Name)
				}
				return nil
			}

			ts := &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      args[0],
					Namespace: ns,
				},
			}

			if err := cl.Delete(ctx, ts); err != nil {
				return fmt.Errorf("deleting task spawner: %w", err)
			}
			fmt.Fprintf(os.Stdout, "taskspawner/%s deleted\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all task spawners in the namespace")
	cmd.ValidArgsFunction = completeTaskSpawnerNames(cfg)

	return cmd
}
