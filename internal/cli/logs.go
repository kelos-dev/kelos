package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	taskStartMarker        = "---KELOS_TASK_START---"
	taskEndMarker          = "---KELOS_TASK_END---"
	logStreamRetryInterval = 2 * time.Second
)

var errTaskLogSegmentNotFound = errors.New("task log segment not found")

type logStreamRetryMode int

const (
	noLogStreamRetry logStreamRetryMode = iota
	delayedLogStreamRetry
	immediateLogStreamRetry
)

func newLogsCommand(cfg *ClientConfig) *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "View logs from a task's pod",
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

			cs, _, err := cfg.NewClientset()
			if err != nil {
				return err
			}

			ctx := context.Background()
			task := &kelos.Task{}
			if err := cl.Get(ctx, client.ObjectKey{Name: args[0], Namespace: ns}, task); err != nil {
				return fmt.Errorf("getting task: %w", err)
			}

			podName, err := resolveTaskPodName(ctx, cl, ns, task)
			if err != nil {
				return err
			}

			if podName == "" {
				if isTerminalTaskPhase(task.Status.Phase) {
					return fmt.Errorf("task %q has no live pod (task phase: %s)", args[0], task.Status.Phase)
				}
				if !follow {
					return fmt.Errorf("task %q has no pod yet", args[0])
				}

				fmt.Fprintf(os.Stderr, "Waiting for task %q to start...\n", args[0])
				task, err = waitForPod(ctx, cl, args[0], ns)
				if err != nil {
					return err
				}
				podName, err = resolveTaskPodName(ctx, cl, ns, task)
				if err != nil {
					return err
				}
				if podName == "" {
					return fmt.Errorf("task %q pod disappeared before logs could be streamed", args[0])
				}
			}

			containerName := kelos.AgentContainerName

			if follow && task.Spec.WorkspaceRef != nil {
				fmt.Fprintf(os.Stderr, "Streaming init container (git-clone) logs...\n")
				if err := streamLogs(ctx, cl, cs, ns, task.Name, podName, "git-clone", follow); err != nil {
					return err
				}
			}

			if follow {
				fmt.Fprintf(os.Stderr, "Streaming container (%s) logs...\n", containerName)
			}
			agentType := resolveAgentType(ctx, cl, ns, task)
			if task.Spec.WorkerPoolRef != nil {
				return streamWorkerPoolTaskAgentLogs(ctx, cl, cs, ns, podName, containerName, agentType, task.Name, follow)
			}
			return streamAgentLogs(ctx, cl, cs, ns, task.Name, podName, containerName, agentType, follow)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")

	cmd.ValidArgsFunction = completeTaskNames(cfg)

	return cmd
}

func waitForPod(ctx context.Context, cl client.Client, name, namespace string) (*kelos.Task, error) {
	var lastPhase kelos.TaskPhase
	for {
		task := &kelos.Task{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, task); err != nil {
			return nil, fmt.Errorf("getting task: %w", err)
		}

		if task.Status.Phase != lastPhase {
			fmt.Fprintf(os.Stderr, "task/%s %s\n", name, task.Status.Phase)
			lastPhase = task.Status.Phase
		}

		if task.Status.Phase == kelos.TaskPhaseFailed {
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

func resolveTaskPodName(ctx context.Context, cl client.Client, namespace string, task *kelos.Task) (string, error) {
	if task.Spec.WorkerPoolRef != nil && task.Status.PodName != "" {
		return task.Status.PodName, nil
	}

	var pods corev1.PodList
	if err := cl.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
		"kelos.dev/task": task.Name,
	}); err != nil {
		return "", fmt.Errorf("listing task pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return "", nil
	}

	sort.Slice(pods.Items, func(i, j int) bool {
		left := pods.Items[i]
		right := pods.Items[j]
		if left.CreationTimestamp.Time.Equal(right.CreationTimestamp.Time) {
			return left.Name < right.Name
		}
		return left.CreationTimestamp.Time.Before(right.CreationTimestamp.Time)
	})

	return pods.Items[len(pods.Items)-1].Name, nil
}

func isTerminalTaskPhase(phase kelos.TaskPhase) bool {
	return phase == kelos.TaskPhaseSucceeded || phase == kelos.TaskPhaseFailed
}

func streamLogs(ctx context.Context, cl client.Client, cs *kubernetes.Clientset, namespace, taskName, podName, container string, follow bool) error {
	opts := &corev1.PodLogOptions{
		Follow:    follow,
		Container: container,
	}

	stream, err := openTaskLogStream(ctx, cl, namespace, taskName, podName, container, follow, logStreamRetryInterval, func(ctx context.Context) (io.ReadCloser, error) {
		return cs.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	if _, err := io.Copy(os.Stdout, stream); err != nil {
		return fmt.Errorf("reading logs: %w", err)
	}
	return nil
}

func streamAgentLogs(ctx context.Context, cl client.Client, cs *kubernetes.Clientset, namespace, taskName, podName, container, agentType string, follow bool) error {
	opts := &corev1.PodLogOptions{
		Follow:    follow,
		Container: container,
	}

	stream, err := openTaskLogStream(ctx, cl, namespace, taskName, podName, container, follow, logStreamRetryInterval, func(ctx context.Context) (io.ReadCloser, error) {
		return cs.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	return parseAgentLogs(agentType, stream)
}

func streamWorkerPoolTaskAgentLogs(ctx context.Context, cl client.Client, cs *kubernetes.Clientset, namespace, podName, container, agentType, taskName string, follow bool) error {
	opts := &corev1.PodLogOptions{
		Follow:    follow,
		Container: container,
	}

	stream, err := openTaskLogStream(ctx, cl, namespace, taskName, podName, container, follow, logStreamRetryInterval, func(ctx context.Context) (io.ReadCloser, error) {
		return cs.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	filtered, wait := filteredTaskLogReader(stream, taskName)
	parseErr := parseAgentLogs(agentType, filtered)
	filterErr := wait()
	if errors.Is(parseErr, errTaskLogSegmentNotFound) || errors.Is(filterErr, errTaskLogSegmentNotFound) {
		return fmt.Errorf("task %q logs not found in worker pod %s", taskName, podName)
	}
	if parseErr != nil {
		return parseErr
	}
	return filterErr
}

func parseAgentLogs(agentType string, stream io.Reader) error {
	switch agentType {
	case "codex":
		return ParseAndFormatCodexLogs(stream, os.Stdout, os.Stderr)
	case "gemini":
		return ParseAndFormatGeminiLogs(stream, os.Stdout, os.Stderr)
	case "opencode":
		return ParseAndFormatOpenCodeLogs(stream, os.Stdout, os.Stderr)
	default:
		return ParseAndFormatLogs(stream, os.Stdout, os.Stderr)
	}
}

func filteredTaskLogReader(stream io.Reader, taskName string) (io.Reader, func() error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := filterTaskLogSegment(stream, pw, taskName)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		errCh <- err
	}()
	return pr, func() error { return <-errCh }
}

func filterTaskLogSegment(stream io.Reader, out io.Writer, taskName string) error {
	startMarker := fmt.Sprintf("%s %s", taskStartMarker, taskName)
	endMarker := fmt.Sprintf("%s %s", taskEndMarker, taskName)
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	inSegment := false
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case !inSegment && line == startMarker:
			inSegment = true
			found = true
		case inSegment && line == endMarker:
			return nil
		case inSegment:
			if _, err := fmt.Fprintln(out, line); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !found {
		return errTaskLogSegmentNotFound
	}
	return nil
}

func openTaskLogStream(
	ctx context.Context,
	cl client.Client,
	namespace, taskName, podName, container string,
	follow bool,
	retryInterval time.Duration,
	open func(context.Context) (io.ReadCloser, error),
) (io.ReadCloser, error) {
	immediateRetryUsed := false
	for {
		stream, err := open(ctx)
		if err == nil {
			return stream, nil
		}
		if !follow || !apierrors.IsBadRequest(err) {
			return nil, fmt.Errorf("streaming logs: %w", err)
		}

		retryMode, retryErr := taskLogStreamRetryMode(ctx, cl, namespace, taskName, podName, container)
		if retryErr != nil {
			return nil, retryErr
		}
		switch retryMode {
		case noLogStreamRetry:
			return nil, fmt.Errorf("streaming logs: %w", err)
		case immediateLogStreamRetry:
			if immediateRetryUsed {
				return nil, fmt.Errorf("streaming logs: %w", err)
			}
			immediateRetryUsed = true
			continue
		case delayedLogStreamRetry:
			immediateRetryUsed = false
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("streaming logs: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func taskLogStreamRetryMode(ctx context.Context, cl client.Client, namespace, taskName, podName, container string) (logStreamRetryMode, error) {
	task := &kelos.Task{}
	if err := cl.Get(ctx, client.ObjectKey{Name: taskName, Namespace: namespace}, task); err != nil {
		return noLogStreamRetry, fmt.Errorf("getting task %q while waiting for logs: %w", taskName, err)
	}
	if task.Status.Phase == kelos.TaskPhaseFailed {
		msg := "unknown error"
		if task.Status.Message != "" {
			msg = task.Status.Message
		}
		return noLogStreamRetry, fmt.Errorf("task %q failed before logs could be streamed: %s", taskName, msg)
	}
	taskSucceeded := task.Status.Phase == kelos.TaskPhaseSucceeded

	pod := &corev1.Pod{}
	if err := cl.Get(ctx, client.ObjectKey{Name: podName, Namespace: namespace}, pod); err != nil {
		return noLogStreamRetry, fmt.Errorf("getting pod %q while waiting for task %q logs: %w", podName, taskName, err)
	}
	if pod.Spec.NodeName == "" {
		return delayedLogStreamRetry, nil
	}

	statuses := append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...)
	for _, status := range statuses {
		if status.Name != container {
			continue
		}
		if status.State.Running != nil || status.State.Terminated != nil {
			return immediateLogStreamRetry, nil
		}
		if taskSucceeded {
			return noLogStreamRetry, fmt.Errorf("task %q reached phase %s before logs could be streamed", taskName, task.Status.Phase)
		}
		if status.State.Waiting == nil {
			return noLogStreamRetry, nil
		}
		switch status.State.Waiting.Reason {
		case "", "PodInitializing", "ContainerCreating":
			return delayedLogStreamRetry, nil
		default:
			return noLogStreamRetry, nil
		}
	}

	if taskSucceeded {
		return noLogStreamRetry, fmt.Errorf("task %q reached phase %s before logs could be streamed", taskName, task.Status.Phase)
	}
	if pod.Status.Phase == corev1.PodPending {
		return delayedLogStreamRetry, nil
	}
	return noLogStreamRetry, nil
}

// resolveAgentType determines the effective agent type for log parsing,
// checking spec.worker.type, legacy spec.type, and the referenced WorkerPool.
func resolveAgentType(ctx context.Context, cl client.Client, namespace string, task *kelos.Task) string {
	if task.Spec.Worker != nil && task.Spec.Worker.Type != "" {
		return task.Spec.Worker.Type
	}
	if task.Spec.Type != "" {
		return task.Spec.Type
	}
	if task.Spec.WorkerPoolRef != nil {
		var pool kelos.WorkerPool
		if err := cl.Get(ctx, client.ObjectKey{Name: task.Spec.WorkerPoolRef.Name, Namespace: namespace}, &pool); err == nil {
			return pool.Spec.Worker.Type
		}
	}
	return ""
}
