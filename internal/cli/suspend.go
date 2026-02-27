package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newSuspendCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suspend",
		Short: "Suspend resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.AddCommand(newSuspendTaskSpawnerCommand(cfg))

	return cmd
}

func newSuspendTaskSpawnerCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "taskspawner [name]",
		Aliases: []string{"taskspawners", "ts"},
		Short:   "Suspend a task spawner",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("task spawner name is required\nUsage: %s", cmd.Use)
			}
			if len(args) > 1 {
				return fmt.Errorf("too many arguments: expected 1 task spawner name, got %d\nUsage: %s", len(args), cmd.Use)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()
			key := client.ObjectKey{Name: args[0], Namespace: ns}

			alreadySuspended := false
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				ts := &kelosv1alpha1.TaskSpawner{}
				if err := cl.Get(ctx, key, ts); err != nil {
					return fmt.Errorf("getting task spawner: %w", err)
				}

				if ts.Spec.Suspend != nil && *ts.Spec.Suspend {
					alreadySuspended = true
					return nil
				}

				suspend := true
				ts.Spec.Suspend = &suspend
				return cl.Update(ctx, ts)
			}); err != nil {
				return fmt.Errorf("suspending task spawner: %w", err)
			}

			if alreadySuspended {
				fmt.Fprintf(os.Stdout, "taskspawner/%s is already suspended\n", args[0])
			} else {
				fmt.Fprintf(os.Stdout, "taskspawner/%s suspended\n", args[0])
			}
			return nil
		},
	}

	cmd.ValidArgsFunction = completeTaskSpawnerNames(cfg)

	return cmd
}

func newResumeCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Resume resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.AddCommand(newResumeTaskSpawnerCommand(cfg))

	return cmd
}

func newResumeTaskSpawnerCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "taskspawner [name]",
		Aliases: []string{"taskspawners", "ts"},
		Short:   "Resume a suspended task spawner",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("task spawner name is required\nUsage: %s", cmd.Use)
			}
			if len(args) > 1 {
				return fmt.Errorf("too many arguments: expected 1 task spawner name, got %d\nUsage: %s", len(args), cmd.Use)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()
			key := client.ObjectKey{Name: args[0], Namespace: ns}

			notSuspended := false
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				ts := &kelosv1alpha1.TaskSpawner{}
				if err := cl.Get(ctx, key, ts); err != nil {
					return fmt.Errorf("getting task spawner: %w", err)
				}

				if ts.Spec.Suspend == nil || !*ts.Spec.Suspend {
					notSuspended = true
					return nil
				}

				suspend := false
				ts.Spec.Suspend = &suspend
				return cl.Update(ctx, ts)
			}); err != nil {
				return fmt.Errorf("resuming task spawner: %w", err)
			}

			if notSuspended {
				fmt.Fprintf(os.Stdout, "taskspawner/%s is not suspended\n", args[0])
			} else {
				fmt.Fprintf(os.Stdout, "taskspawner/%s resumed\n", args[0])
			}
			return nil
		},
	}

	cmd.ValidArgsFunction = completeTaskSpawnerNames(cfg)

	return cmd
}
