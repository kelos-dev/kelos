package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
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

			cs, _, err := cfg.NewClientset()
			if err != nil {
				return err
			}

			ctx := context.Background()
			task := &axonv1alpha1.Task{}
			if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, task); err != nil {
				return fmt.Errorf("getting task: %w", err)
			}

			if task.Status.PodName == "" {
				if !follow {
					return fmt.Errorf("task %q has no pod yet", args[0])
				}

				fmt.Fprintf(os.Stderr, "Waiting for task %q to start...\n", args[0])
				task, err = waitForPod(ctx, cl, args[0], ns)
				if err != nil {
					return err
				}
			}

			if follow && task.Spec.WorkspaceRef != nil {
				fmt.Fprintf(os.Stderr, "Streaming init container (git-clone) logs...\n")
				if err := streamLogs(ctx, cs, ns, task.Status.PodName, "git-clone", follow); err != nil {
					return err
				}
			}

			if follow {
				fmt.Fprintf(os.Stderr, "Streaming container (claude-code) logs...\n")
			}
			return streamClaudeCodeLogs(ctx, cs, ns, task.Status.PodName, follow)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")

	cmd.ValidArgsFunction = completeTaskNames(cfg)

	return cmd
}

func waitForPod(ctx context.Context, cl client.Client, name, namespace string) (*axonv1alpha1.Task, error) {
	var lastPhase axonv1alpha1.TaskPhase
	for {
		task := &axonv1alpha1.Task{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, task); err != nil {
			return nil, fmt.Errorf("getting task: %w", err)
		}

		if task.Status.Phase != lastPhase {
			fmt.Fprintf(os.Stderr, "task/%s %s\n", name, task.Status.Phase)
			lastPhase = task.Status.Phase
		}

		if task.Status.Phase == axonv1alpha1.TaskPhaseFailed {
			msg := "unknown error"
			if task.Status.Message != "" {
				msg = task.Status.Message
			}
			return nil, fmt.Errorf("task %q failed before starting: %s", name, msg)
		}

		if task.Status.PodName != "" {
			return task, nil
		}

		time.Sleep(2 * time.Second)
	}
}

func streamLogs(ctx context.Context, cs *kubernetes.Clientset, namespace, podName, container string, follow bool) error {
	opts := &corev1.PodLogOptions{
		Follow:    follow,
		Container: container,
	}

	for {
		stream, err := cs.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
		if err != nil {
			if follow && isContainerNotReady(err) {
				time.Sleep(2 * time.Second)
				continue
			}
			return fmt.Errorf("streaming logs: %w", err)
		}
		defer stream.Close()

		if _, err := io.Copy(os.Stdout, stream); err != nil {
			return fmt.Errorf("reading logs: %w", err)
		}
		return nil
	}
}

func streamClaudeCodeLogs(ctx context.Context, cs *kubernetes.Clientset, namespace, podName string, follow bool) error {
	opts := &corev1.PodLogOptions{
		Follow:    follow,
		Container: "claude-code",
	}

	for {
		stream, err := cs.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
		if err != nil {
			if follow && isContainerNotReady(err) {
				time.Sleep(2 * time.Second)
				continue
			}
			return fmt.Errorf("streaming logs: %w", err)
		}
		defer stream.Close()

		return ParseAndFormatLogs(stream, os.Stdout, os.Stderr)
	}
}

func isContainerNotReady(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "is waiting to start") || strings.Contains(msg, "PodInitializing")
}
