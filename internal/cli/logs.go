package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func newLogsCommand(cfg *ClientConfig) *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "View logs from a task's pod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()
			task := &axonv1alpha1.Task{}
			if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, task); err != nil {
				return fmt.Errorf("getting task: %w", err)
			}

			podName := task.Status.PodName
			if podName == "" {
				return fmt.Errorf("task %q has no pod yet", args[0])
			}

			cs, _, err := cfg.NewClientset()
			if err != nil {
				return err
			}

			opts := &corev1.PodLogOptions{Follow: follow}
			stream, err := cs.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(ctx)
			if err != nil {
				return fmt.Errorf("streaming logs: %w", err)
			}
			defer stream.Close()

			if _, err := io.Copy(os.Stdout, stream); err != nil {
				return fmt.Errorf("reading logs: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")

	return cmd
}
