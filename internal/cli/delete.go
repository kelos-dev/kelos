package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func newDeleteCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      args[0],
					Namespace: ns,
				},
			}

			if err := cl.Delete(context.Background(), task); err != nil {
				return fmt.Errorf("deleting task: %w", err)
			}
			fmt.Fprintf(os.Stdout, "task/%s deleted\n", args[0])
			return nil
		},
	}

	return cmd
}
