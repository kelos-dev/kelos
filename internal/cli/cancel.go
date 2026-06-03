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

func newCancelCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.AddCommand(newCancelTaskCommand(cfg))

	return cmd
}

func newCancelTaskCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task <name>",
		Short: "Cancel a running task",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("task name is required\nUsage: %s", cmd.Use)
			}
			if len(args) > 1 {
				return fmt.Errorf("too many arguments: expected 1 task name, got %d\nUsage: %s", len(args), cmd.Use)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()
			taskName := args[0]

			var task kelosv1alpha1.Task
			if err := cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: taskName}, &task); err != nil {
				return fmt.Errorf("getting task %s: %w", taskName, err)
			}

			switch task.Status.Phase {
			case kelosv1alpha1.TaskPhaseSucceeded, kelosv1alpha1.TaskPhaseFailed, kelosv1alpha1.TaskPhaseCancelled:
				fmt.Fprintf(os.Stdout, "task/%s is already in terminal phase %s\n", taskName, task.Status.Phase)
				return nil
			}

			now := metav1.Now()
			task.Status.Phase = kelosv1alpha1.TaskPhaseCancelled
			task.Status.Message = "Cancelled by user"
			task.Status.CancelledBy = "user"
			task.Status.CompletionTime = &now
			if err := cl.Status().Update(ctx, &task); err != nil {
				return fmt.Errorf("cancelling task %s: %w", taskName, err)
			}

			fmt.Fprintf(os.Stdout, "task/%s cancelled\n", taskName)
			return nil
		},
	}

	return cmd
}
