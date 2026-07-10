/*
Copyright 2026 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workerrunner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	kelosclientset "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

const (
	pollInterval       = 3 * time.Second
	defaultIdleTimeout = 30 * time.Minute
	taskStartMarker    = "---KELOS_TASK_START---"
	taskEndMarker      = "---KELOS_TASK_END---"
)

var errAssignmentChanged = errors.New("task assignment changed")

type cancelSignal struct {
	ch   chan struct{}
	once sync.Once
}

func newCancelSignal() *cancelSignal {
	return &cancelSignal{ch: make(chan struct{})}
}

func (s *cancelSignal) close() {
	s.once.Do(func() {
		close(s.ch)
	})
}

// Config holds the worker runner configuration sourced from environment variables.
type Config struct {
	PodName           string
	PodNamespace      string
	AgentType         string
	IdleTimeout       time.Duration
	MaxTasksPerWorker int32
}

// ConfigFromEnv reads the worker runner configuration from environment variables.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		PodName:      os.Getenv("KELOS_POD_NAME"),
		PodNamespace: os.Getenv("KELOS_POD_NAMESPACE"),
		AgentType:    os.Getenv("KELOS_AGENT_TYPE"),
		IdleTimeout:  defaultIdleTimeout,
	}

	if timeout := os.Getenv("KELOS_IDLE_TIMEOUT"); timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			return Config{}, fmt.Errorf("parsing KELOS_IDLE_TIMEOUT %q: %w", timeout, err)
		}
		cfg.IdleTimeout = d
	}

	if maxTasks := os.Getenv("KELOS_MAX_TASKS_PER_WORKER"); maxTasks != "" {
		n, err := strconv.ParseInt(maxTasks, 10, 32)
		if err != nil {
			return Config{}, fmt.Errorf("parsing KELOS_MAX_TASKS_PER_WORKER %q: %w", maxTasks, err)
		}
		cfg.MaxTasksPerWorker = int32(n)
	}

	return cfg, nil
}

// Runner polls for task assignments and executes them via the agent
// entrypoint. It runs inside the agent container image (injected via init
// container) so it has direct access to /kelos_entrypoint.sh and all tools.
type Runner struct {
	cfg          Config
	kubeClient   kubernetes.Interface
	kelosClient  kelosclientset.Interface
	runAgentFunc func(ctx context.Context, task *kelos.Task) error
}

// NewRunner creates a new worker runner using in-cluster configuration.
func NewRunner(cfg Config) (*Runner, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("building in-cluster config: %w", err)
	}

	return NewRunnerWithConfig(cfg, restCfg)
}

// NewRunnerWithConfig creates a runner with explicit REST config (for testing).
func NewRunnerWithConfig(cfg Config, restCfg *rest.Config) (*Runner, error) {
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	kelosClient, err := kelosclientset.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating kelos client: %w", err)
	}

	return NewRunnerWithClients(cfg, kubeClient, kelosClient), nil
}

// NewRunnerWithClients creates a runner with pre-built clients (for testing).
func NewRunnerWithClients(cfg Config, kubeClient kubernetes.Interface, kelosClient kelosclientset.Interface) *Runner {
	r := &Runner{
		cfg:         cfg,
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
	}
	r.runAgentFunc = r.runAgent
	return r
}

// Run is the main loop that polls for task assignments and executes them.
func (r *Runner) Run(ctx context.Context) error {
	log.Printf("Starting worker runner pod=%s/%s agent=%s idleTimeout=%s",
		r.cfg.PodNamespace, r.cfg.PodName, r.cfg.AgentType, r.cfg.IdleTimeout)

	var tasksCompleted int32
	idleTimer := time.NewTimer(r.cfg.IdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Context cancelled, shutting down")
			return nil
		case <-idleTimer.C:
			log.Printf("Idle timeout reached after %s, exiting", r.cfg.IdleTimeout)
			return nil
		default:
		}

		taskName, err := r.getAssignedTask(ctx)
		if err != nil {
			log.Printf("Error checking for assigned task: %v", err)
			sleepWithContext(ctx, pollInterval)
			continue
		}

		if taskName == "" {
			sleepWithContext(ctx, pollInterval)
			continue
		}

		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}

		log.Printf("Task assigned: %s", taskName)

		if err := r.executeTask(ctx, taskName); err != nil {
			log.Printf("Task %s failed: %v", taskName, err)
		}

		tasksCompleted++
		if err := r.waitForAssignmentRelease(ctx, taskName); err != nil {
			return fmt.Errorf("waiting for assignment release: %w", err)
		}

		if r.cfg.MaxTasksPerWorker > 0 && tasksCompleted >= r.cfg.MaxTasksPerWorker {
			log.Printf("Max tasks per worker reached (%d), exiting", r.cfg.MaxTasksPerWorker)
			return nil
		}

		idleTimer.Reset(r.cfg.IdleTimeout)
	}
}

func (r *Runner) getAssignedTask(ctx context.Context) (string, error) {
	pod, err := r.kubeClient.CoreV1().Pods(r.cfg.PodNamespace).Get(ctx, r.cfg.PodName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting pod %s/%s: %w", r.cfg.PodNamespace, r.cfg.PodName, err)
	}

	return pod.Annotations[kelos.AnnotationWorkerAssignedTask], nil
}

func (r *Runner) executeTask(ctx context.Context, taskName string) error {
	if err := r.setTaskStatus(ctx, taskName, "running", ""); err != nil {
		if errors.Is(err, errAssignmentChanged) {
			log.Printf("Skipping task %s because assignment changed", taskName)
			return nil
		}
		return fmt.Errorf("setting running status: %w", err)
	}

	task, err := r.kelosClient.ApiV1alpha2().Tasks(r.cfg.PodNamespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		reason := fmt.Sprintf("failed to get task: %v", err)
		_ = r.setTaskStatus(ctx, taskName, "failed", reason)
		return fmt.Errorf("getting task %s: %w", taskName, err)
	}

	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cancelRequested := newCancelSignal()
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go r.cancelTaskWhenRequested(watchCtx, taskName, cancel, cancelRequested)

	if requested, err := r.taskCancellationRequested(ctx, taskName); err != nil {
		log.Printf("Error checking cancellation for task %s: %v", taskName, err)
	} else if requested {
		cancel()
		cancelRequested.close()
		reason := "task was cancelled"
		_ = r.setTaskStatus(ctx, taskName, "failed", reason)
		return errors.New(reason)
	}

	fmt.Printf("%s %s\n", taskStartMarker, taskName)
	err = r.runAgentFunc(taskCtx, task)
	fmt.Printf("%s %s\n", taskEndMarker, taskName)
	if err != nil {
		reason := fmt.Sprintf("agent execution failed: %v", err)
		if cancellationSignaled(cancelRequested.ch) {
			reason = "task was cancelled"
		}
		_ = r.setTaskStatus(ctx, taskName, "failed", reason)
		return err
	}

	if err := r.setTaskStatus(ctx, taskName, "succeeded", ""); err != nil {
		if errors.Is(err, errAssignmentChanged) {
			log.Printf("Skipping succeeded status for task %s because assignment changed", taskName)
			return nil
		}
		return fmt.Errorf("setting succeeded status: %w", err)
	}

	log.Printf("Task %s completed successfully", taskName)
	return nil
}

// runAgent invokes the agent entrypoint with the task prompt. The runner
// executes inside the agent container image so /kelos_entrypoint.sh and
// all agent tooling are directly available on the filesystem.
func (r *Runner) runAgent(ctx context.Context, task *kelos.Task) error {
	cmd := exec.CommandContext(ctx, "/kelos_entrypoint.sh", task.Spec.Prompt)
	cmd.Dir = "/workspace/repo"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.Env = taskAgentEnv(os.Environ(), task)

	return cmd.Run()
}

func taskAgentEnv(base []string, task *kelos.Task) []string {
	env := append([]string{}, base...)

	// Refresh the GitHub token env vars from the mounted token file so
	// env-reading tools pick up controller-side token refreshes. The token
	// env vars in os.Environ() are captured at pod start and never update,
	// so for long-lived worker pods they go stale once the installation token
	// is re-minted. The git credential helper already re-reads the file per
	// git call; this covers tools that read GITHUB_TOKEN/GH_TOKEN directly.
	// Only env vars already present are overridden, matching whichever the
	// pod set (GH_TOKEN for github.com, GH_ENTERPRISE_TOKEN for GHE).
	if token := currentGitHubToken(); token != "" {
		env = overrideEnvIfPresent(env, "GITHUB_TOKEN", token)
		env = overrideEnvIfPresent(env, "GH_TOKEN", token)
		env = overrideEnvIfPresent(env, "GH_ENTERPRISE_TOKEN", token)
	}

	env = append(env, "KELOS_PROMPT="+task.Spec.Prompt)
	if task.Spec.Model != "" {
		env = append(env, "KELOS_MODEL="+task.Spec.Model)
	}
	if task.Spec.Effort != "" {
		env = append(env, "KELOS_EFFORT="+task.Spec.Effort)
	}
	if task.Spec.UpstreamRepo != "" {
		env = append(env, "KELOS_UPSTREAM_REPO="+task.Spec.UpstreamRepo)
	}
	return env
}

// currentGitHubToken reads the current GitHub token from the file named by
// KELOS_GITHUB_TOKEN_FILE, returning "" when the env var is unset or the file
// is missing/unreadable/empty. The file is a kubelet-synced secret volume, so
// it reflects controller-side token refreshes within the kubelet sync period.
func currentGitHubToken() string {
	tokenFile := os.Getenv("KELOS_GITHUB_TOKEN_FILE")
	if tokenFile == "" {
		return ""
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// overrideEnvIfPresent replaces the value of key in env when it is already
// present, leaving env unchanged otherwise. It edits in place so the agent
// subprocess sees a single, up-to-date value rather than a duplicate key.
func overrideEnvIfPresent(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return env
}

func (r *Runner) taskCancellationRequested(ctx context.Context, taskName string) (bool, error) {
	pod, err := r.kubeClient.CoreV1().Pods(r.cfg.PodNamespace).Get(ctx, r.cfg.PodName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("getting pod for cancellation check: %w", err)
	}

	if pod.Annotations[kelos.AnnotationWorkerAssignedTask] != taskName {
		return true, nil
	}
	return pod.Annotations[kelos.AnnotationWorkerCancelTask] == taskName, nil
}

func (r *Runner) cancelTaskWhenRequested(ctx context.Context, taskName string, cancel context.CancelFunc, cancelRequested *cancelSignal) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		requested, err := r.taskCancellationRequested(ctx, taskName)
		if err != nil {
			log.Printf("Error checking cancellation for task %s: %v", taskName, err)
			continue
		}
		if requested {
			cancelRequested.close()
			cancel()
			return
		}
	}
}

func cancellationSignaled(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// setTaskStatus updates the pod annotations with retry-on-conflict to handle
// concurrent updates from the controller.
func (r *Runner) setTaskStatus(ctx context.Context, taskName, status, failureReason string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pod, err := r.kubeClient.CoreV1().Pods(r.cfg.PodNamespace).Get(ctx, r.cfg.PodName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("getting pod for status update: %w", err)
		}

		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		if pod.Annotations[kelos.AnnotationWorkerAssignedTask] != taskName {
			return errAssignmentChanged
		}

		pod.Annotations[kelos.AnnotationWorkerTaskStatus] = status
		if failureReason != "" {
			pod.Annotations[kelos.AnnotationWorkerTaskFailReason] = failureReason
		} else {
			delete(pod.Annotations, kelos.AnnotationWorkerTaskFailReason)
		}

		_, err = r.kubeClient.CoreV1().Pods(r.cfg.PodNamespace).Update(ctx, pod, metav1.UpdateOptions{})
		return err
	})
}

func (r *Runner) waitForAssignmentRelease(ctx context.Context, taskName string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pod, err := r.kubeClient.CoreV1().Pods(r.cfg.PodNamespace).Get(ctx, r.cfg.PodName, metav1.GetOptions{})
		if err != nil {
			log.Printf("Error checking annotations for clear: %v", err)
			sleepWithContext(ctx, pollInterval)
			continue
		}

		if pod.Annotations[kelos.AnnotationWorkerAssignedTask] != taskName {
			return nil
		}

		sleepWithContext(ctx, pollInterval)
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
