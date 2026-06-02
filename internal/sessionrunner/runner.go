/*
Copyright 2025 Kelos contributors.

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

package sessionrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/capture"
	kelosversioned "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned"
)

// errAgentReportedFailure is returned when the agent process exits cleanly
// but reports is_error=true in its result JSON.
var errAgentReportedFailure = errors.New("agent reported failure in result output")

// errTokenExpired is returned when the agent session ended due to GitHub
// token expiration detected in the agent output.
var errTokenExpired = errors.New("agent session failed due to GitHub token expiration")

// errCredentialUnavailable indicates a hard credential failure: the token
// secret is missing, empty, or expired. Distinguished from transient API
// errors so processTask can fail fast instead of running unauthenticated.
var errCredentialUnavailable = errors.New("credential unavailable")

const (
	annotationAssignedTask      = "kelos.dev/assigned-task"
	annotationTaskStatus        = "kelos.dev/task-status"
	annotationTasksCompleted    = "kelos.dev/tasks-completed"
	annotationSessionStart      = "kelos.dev/session-start-time"
	annotationTokenExpiresAt    = "kelos.dev/token-expires-at"
	annotationTaskFailureReason = "kelos.dev/task-failure-reason"

	defaultIdleTimeout          = 30 * time.Minute
	defaultMaxSessionDuration   = 8 * time.Hour
	defaultTokenRefreshInterval = 10 * time.Minute
	tokenExpiryMargin           = 5 * time.Minute
	tokenRefreshRetryInterval   = 30 * time.Second
	pollInterval                = 3 * time.Second
)

// tokenFilePath is the path where the refreshed token is written so that
// subprocesses can read it. It is a var to allow overriding in tests.
var tokenFilePath = "/workspace/.kelos-token"

// Config holds the session runner configuration, typically from environment variables.
type Config struct {
	PodName              string
	PodNamespace         string
	AgentType            string
	TaskSpawner          string
	TokenSecret          string
	IdleTimeout          time.Duration
	MaxTasksPerSession   int32
	MaxSessionDuration   time.Duration
	TokenRefreshInterval time.Duration
	AuthFailurePatterns  []string
}

// ConfigFromEnv reads session runner configuration from environment variables.
func ConfigFromEnv() Config {
	cfg := Config{
		PodName:              os.Getenv("KELOS_POD_NAME"),
		PodNamespace:         os.Getenv("KELOS_POD_NAMESPACE"),
		AgentType:            os.Getenv("KELOS_AGENT_TYPE"),
		TaskSpawner:          os.Getenv("KELOS_TASKSPAWNER"),
		TokenSecret:          os.Getenv("KELOS_TOKEN_SECRET"),
		IdleTimeout:          defaultIdleTimeout,
		MaxSessionDuration:   defaultMaxSessionDuration,
		TokenRefreshInterval: defaultTokenRefreshInterval,
	}

	if v := os.Getenv("KELOS_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.IdleTimeout = d
		}
	}
	if v := os.Getenv("KELOS_MAX_TASKS_PER_SESSION"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			cfg.MaxTasksPerSession = int32(n)
		}
	}
	if v := os.Getenv("KELOS_MAX_SESSION_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionDuration = d
		}
	}
	if v := os.Getenv("KELOS_TOKEN_REFRESH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.TokenRefreshInterval = d
		}
	}
	if v := os.Getenv("KELOS_AUTH_FAILURE_PATTERNS"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				cfg.AuthFailurePatterns = append(cfg.AuthFailurePatterns, trimmed)
			}
		}
	}

	return cfg
}

// Runner implements the session runner main loop.
type Runner struct {
	config      Config
	kubeClient  kubernetes.Interface
	kelosClient kelosversioned.Interface
	workspace   *WorkspaceManager

	// runAgentFn executes the agent entrypoint. Defaults to runAgent; override in tests.
	runAgentFn func(ctx context.Context, task *kelosv1alpha1.Task) (string, error)
}

// NewRunner creates a new session runner.
func NewRunner(cfg Config) (*Runner, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	kelosClient, err := kelosversioned.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kelos client: %w", err)
	}

	r := &Runner{
		config:      cfg,
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   NewWorkspaceManager(),
	}
	r.runAgentFn = r.runAgent
	return r, nil
}

// Run executes the session runner main loop. It blocks until the session
// ends (idle timeout, max tasks, max duration, or context cancellation).
func (r *Runner) Run(ctx context.Context) error {
	fmt.Printf("Session runner starting: pod=%s namespace=%s spawner=%s\n",
		r.config.PodName, r.config.PodNamespace, r.config.TaskSpawner)

	sessionStart := time.Now()
	tasksCompleted := int32(0)
	lastTaskTime := time.Now()
	lastProcessedAssignment := taskAssignment{}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Session runner shutting down: context cancelled")
			return nil
		default:
		}

		// Check session limits.
		if r.config.MaxSessionDuration > 0 && time.Since(sessionStart) > r.config.MaxSessionDuration {
			fmt.Println("Session runner exiting: max session duration reached")
			return nil
		}
		if r.config.MaxTasksPerSession > 0 && tasksCompleted >= r.config.MaxTasksPerSession {
			fmt.Println("Session runner exiting: max tasks per session reached")
			return nil
		}
		if time.Since(lastTaskTime) > r.config.IdleTimeout {
			fmt.Println("Session runner exiting: idle timeout reached")
			return nil
		}

		// Check for task assignment.
		assignment, err := r.getAssignedTask(ctx)
		if err != nil {
			fmt.Printf("Error checking task assignment: %v\n", err)
			time.Sleep(pollInterval)
			continue
		}

		// Skip if no task is assigned, or if the assignment has not changed.
		// Comparing both name and resourceVersion ensures that a retried task
		// (same name re-assigned after SessionRetryCount increment) is detected
		// as new work because the pod update changes its ResourceVersion.
		if assignment.name == "" || assignment == lastProcessedAssignment {
			time.Sleep(pollInterval)
			continue
		}

		// Process the assigned task.
		fmt.Printf("Task assigned: %s\n", assignment.name)

		if err := r.processTask(ctx, assignment.name); err != nil {
			fmt.Printf("Task %s failed: %v\n", assignment.name, err)
			annotations := map[string]string{annotationTaskStatus: "failed"}
			if errors.Is(err, errTokenExpired) {
				annotations[annotationTaskFailureReason] = "token-expired"
			}
			if setErr := r.setAnnotations(ctx, annotations); setErr != nil {
				fmt.Printf("Error setting task status annotations: %v\n", setErr)
			}
		} else {
			fmt.Printf("Task %s completed successfully\n", assignment.name)
			if setErr := r.setTaskStatus(ctx, "succeeded"); setErr != nil {
				fmt.Printf("Error setting task status to succeeded: %v\n", setErr)
			}
		}

		lastProcessedAssignment = assignment
		lastTaskTime = time.Now()
		tasksCompleted++
		if setErr := r.setAnnotation(ctx, annotationTasksCompleted, strconv.Itoa(int(tasksCompleted))); setErr != nil {
			fmt.Printf("Error updating tasks completed count: %v\n", setErr)
		}
	}
}

// taskAssignment holds the task name and the pod ResourceVersion at the time
// of reading, so that re-assignments of the same task name (after retry) are
// correctly detected as new work.
type taskAssignment struct {
	name            string
	resourceVersion string
}

// getAssignedTask checks the pod's annotations for a task assignment.
// Returns empty if no task is assigned or if the task has already been
// processed (status annotation set) — this prevents the runner from
// re-executing a task after its own annotation writes bump resourceVersion.
func (r *Runner) getAssignedTask(ctx context.Context) (taskAssignment, error) {
	pod, err := r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Get(ctx, r.config.PodName, metav1.GetOptions{})
	if err != nil {
		return taskAssignment{}, err
	}
	// If the status annotation is already set, we've already processed this
	// assignment. Wait for the SessionReconciler to clear annotations before
	// treating a new assignment as actionable.
	if pod.Annotations[annotationTaskStatus] != "" {
		return taskAssignment{}, nil
	}
	return taskAssignment{
		name:            pod.Annotations[annotationAssignedTask],
		resourceVersion: pod.ResourceVersion,
	}, nil
}

// processTask handles a single task: workspace reset, agent invocation.
func (r *Runner) processTask(ctx context.Context, taskName string) (retErr error) {
	// Read Task object for prompt, branch, etc.
	task, err := r.kelosClient.ApiV1alpha1().Tasks(r.config.PodNamespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get task %s: %w", taskName, err)
	}

	// Set status to running.
	if err := r.setTaskStatus(ctx, "running"); err != nil {
		return fmt.Errorf("failed to set task status to running: %w", err)
	}

	startTime := metav1.Now()
	var outputs []string
	var results map[string]string

	// Only write CompletionTime on success. Writing it unconditionally breaks
	// the SessionReconciler's "infer success from CompletionTime" fallback
	// when a failed task also fails to write its "failed" pod annotation.
	defer func() {
		if retErr != nil {
			return
		}
		statusCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.updateTaskStatus(statusCtx, taskName, &startTime, outputs, results); err != nil {
			fmt.Printf("Error updating task status: %v\n", err)
		}
	}()

	// Refresh token from Secret before running the agent.
	// Hard credential failures (secret missing/expired) fail the task
	// immediately rather than running the agent unauthenticated.
	// Transient errors also fail the task so it gets requeued — running
	// without credentials would strip git/gh auth and leave the agent
	// unable to interact with private repositories.
	if err := r.refreshToken(ctx); err != nil {
		if errors.Is(err, errCredentialUnavailable) {
			return errTokenExpired
		}
		return fmt.Errorf("failed to refresh token before agent start: %w", err)
	}

	// Start periodic token refresh for long-running tasks.
	refreshCtx, refreshCancel := context.WithCancel(ctx)
	refreshWg := r.startTokenRefreshLoop(refreshCtx)

	// On return: cancel the refresh loop, wait for it to finish, then
	// remove the token file so no stale credential lingers on disk.
	defer func() {
		refreshCancel()
		refreshWg.Wait()
		os.Remove(tokenFilePath)
	}()

	// Reset workspace first while the pre-existing credential helper in
	// .git/config is still intact for fetch/checkout operations.
	if err := r.workspace.Reset(ctx, task.Spec.Branch); err != nil {
		return fmt.Errorf("workspace reset failed: %w", err)
	}

	// Configure git to read credentials from the token file so that
	// refreshed tokens are visible to the running agent subprocess.
	if r.config.TokenSecret != "" {
		if err := configureFileCredentialHelper(ctx); err != nil {
			fmt.Printf("Warning: failed to configure file-based credential helper: %v\n", err)
		}
	}

	// Invoke the agent entrypoint and capture outputs.
	agentOutput, agentErr := r.runAgentFn(ctx, task)

	// Parse outputs.
	outputs = capture.ParseOutputs(agentOutput)
	results = capture.ResultsFromOutputs(outputs)
	outputLines := bytes.Split([]byte(agentOutput), []byte("\n"))

	// Check auth failure before exit code — the agent may crash (non-zero)
	// or exit cleanly when hitting a 401; either way we want to retry.
	if capture.IsAuthFailure(r.config.AgentType, outputLines, r.config.AuthFailurePatterns) {
		return errTokenExpired
	}

	if agentErr != nil {
		return agentErr
	}

	// Even if the process exited 0, check if the agent reported failure.
	if capture.IsAgentError(r.config.AgentType, outputLines) {
		return errAgentReportedFailure
	}

	return nil
}

// tailBufferSize is the maximum bytes retained from agent stdout for output
// marker parsing. The markers are always emitted at the end of the run by
// kelos-capture, so only the tail is needed.
const tailBufferSize = 256 * 1024

// runAgent invokes the agent entrypoint with the task prompt.
// It returns the tail of captured stdout (for output parsing) and any execution error.
func (r *Runner) runAgent(ctx context.Context, task *kelosv1alpha1.Task) (string, error) {
	entrypoint := "/kelos_entrypoint.sh"

	// Build env for the subprocess. Strip token env vars only when
	// GH_CONFIG_DIR is set, so `gh` falls back to hosts.yml (which is
	// periodically refreshed). Without GH_CONFIG_DIR there is no hosts.yml
	// to fall back to, so keep env vars to avoid a regression.
	env := os.Environ()
	if os.Getenv("GH_CONFIG_DIR") != "" {
		env = filterTokenEnvVars(env)
	}
	if task.Spec.Branch != "" {
		env = append(env, fmt.Sprintf("KELOS_BRANCH=%s", task.Spec.Branch))
	}

	tail := newTailWriter(tailBufferSize)
	cmd := exec.CommandContext(ctx, entrypoint, task.Spec.Prompt)
	cmd.Dir = workspaceRepoPath
	cmd.Stdout = io.MultiWriter(os.Stdout, tail)
	cmd.Stderr = os.Stderr
	cmd.Env = env

	err := cmd.Run()
	return tail.String(), err
}

// tokenEnvVarPrefixes are the env vars that carry GitHub tokens. We strip
// these from the agent subprocess so that `gh` falls back to hosts.yml.
var tokenEnvVarPrefixes = []string{"GITHUB_TOKEN=", "GH_TOKEN=", "GH_ENTERPRISE_TOKEN="}

// filterTokenEnvVars returns a copy of env with token-related variables removed.
func filterTokenEnvVars(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, prefix := range tokenEnvVarPrefixes {
			if strings.HasPrefix(e, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// refreshToken reads the current GITHUB_TOKEN from the configured Secret and
// writes it to the token file on disk so that running subprocesses (git
// credential helper, gh CLI) pick up the refreshed value. It also updates
// the process environment for any direct use by the session runner itself.
func (r *Runner) refreshToken(ctx context.Context) error {
	if r.config.TokenSecret == "" {
		return nil
	}

	secret, err := r.kubeClient.CoreV1().Secrets(r.config.PodNamespace).Get(ctx, r.config.TokenSecret, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("token secret %s not found: %w", r.config.TokenSecret, errCredentialUnavailable)
		}
		return fmt.Errorf("reading token secret %s: %w", r.config.TokenSecret, err)
	}

	token, ok := secret.Data["GITHUB_TOKEN"]
	if !ok || len(token) == 0 {
		return fmt.Errorf("secret %s missing GITHUB_TOKEN key: %w", r.config.TokenSecret, errCredentialUnavailable)
	}

	// If the Secret carries an expiry annotation, reject tokens that are
	// already expired or about to expire so the caller can retry sooner.
	if expiresAtStr, hasExpiry := secret.Annotations[annotationTokenExpiresAt]; hasExpiry {
		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			return fmt.Errorf("parsing %s annotation: %w", annotationTokenExpiresAt, err)
		}
		if time.Until(expiresAt) < tokenExpiryMargin {
			return fmt.Errorf("token in secret %s is expired or expiring within %v: %w", r.config.TokenSecret, tokenExpiryMargin, errCredentialUnavailable)
		}
	}

	tokenStr := strings.TrimSpace(string(token))

	// Write token atomically (temp file + rename) so readers never see
	// a partially-written or empty file.
	if err := atomicWriteFile(tokenFilePath, []byte(tokenStr), 0600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}

	// Update gh CLI hosts.yml so `gh` picks up the refreshed token.
	if err := r.writeGHHostsFile(tokenStr); err != nil {
		fmt.Printf("Warning: failed to update gh hosts.yml: %v\n", err)
	}

	os.Setenv("GITHUB_TOKEN", tokenStr)
	if os.Getenv("GH_ENTERPRISE_TOKEN") != "" {
		os.Setenv("GH_ENTERPRISE_TOKEN", tokenStr)
	} else if os.Getenv("GH_TOKEN") != "" {
		os.Setenv("GH_TOKEN", tokenStr)
	}
	fmt.Println("Refreshed GitHub token from secret")
	return nil
}

// writeGHHostsFile merges the refreshed token into the gh CLI hosts.yml,
// preserving all other host entries and fields (e.g. git_protocol).
func (r *Runner) writeGHHostsFile(token string) error {
	ghConfigDir := os.Getenv("GH_CONFIG_DIR")
	if ghConfigDir == "" {
		return nil
	}

	host := os.Getenv("GH_HOST")
	if host == "" {
		host = "github.com"
	}

	hostsPath := filepath.Join(ghConfigDir, "hosts.yml")

	if err := os.MkdirAll(ghConfigDir, 0700); err != nil {
		return err
	}

	// Use untyped maps to preserve unknown fields during round-trip.
	hosts := make(map[string]map[string]interface{})
	if data, err := os.ReadFile(hostsPath); err == nil {
		if err := yaml.Unmarshal(data, &hosts); err != nil {
			return fmt.Errorf("parsing existing hosts.yml: %w", err)
		}
	}

	entry := hosts[host]
	if entry == nil {
		entry = make(map[string]interface{})
	}
	entry["oauth_token"] = token
	entry["user"] = "x-access-token"
	hosts[host] = entry

	out, err := yaml.Marshal(hosts)
	if err != nil {
		return fmt.Errorf("marshaling hosts.yml: %w", err)
	}
	return atomicWriteFile(hostsPath, out, 0600)
}

// configureFileCredentialHelper replaces the git credential helper in the
// workspace repo config to read the token from the on-disk file rather than
// from the $GITHUB_TOKEN env var (which is stale in subprocesses).
func configureFileCredentialHelper(ctx context.Context) error {
	helper := fmt.Sprintf(`!f() { echo "username=x-access-token"; echo "password=$(cat %s)"; }; f`, tokenFilePath)

	// Unset all existing credential helpers to avoid accumulation across tasks.
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "--unset-all", "credential.helper")
	cmd.Dir = workspaceRepoPath
	// --unset-all returns exit 5 if the key doesn't exist; ignore that.
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 5 {
			return err
		}
	}

	// Set the empty-string entry (disables system/global helpers) followed by our helper.
	cmd = exec.CommandContext(ctx, "git", "config", "--local", "credential.helper", "")
	cmd.Dir = workspaceRepoPath
	if err := cmd.Run(); err != nil {
		return err
	}

	cmd = exec.CommandContext(ctx, "git", "config", "--local", "--add", "credential.helper", helper)
	cmd.Dir = workspaceRepoPath
	return cmd.Run()
}

// atomicWriteFile writes data to a temp file in the same directory and renames
// it to path, ensuring readers never see a partially-written file.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".kelos-token-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// startTokenRefreshLoop runs a background goroutine that periodically refreshes
// the GitHub token from the configured Secret. It stops when ctx is cancelled.
// The returned WaitGroup is done when the goroutine exits.
func (r *Runner) startTokenRefreshLoop(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	if r.config.TokenSecret == "" || r.config.TokenRefreshInterval <= 0 {
		return &wg
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(r.config.TokenRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.refreshToken(ctx); err != nil {
					fmt.Printf("Warning: periodic token refresh failed: %v\n", err)
					// Retry sooner while the controller regenerates the token.
					ticker.Reset(tokenRefreshRetryInterval)
				} else {
					// Restore normal interval after a successful refresh.
					ticker.Reset(r.config.TokenRefreshInterval)
				}
			}
		}
	}()
	return &wg
}

// updateTaskStatus writes completion timestamps and any captured outputs to the
// Task status. It retries on conflict since the SessionReconciler may write
// concurrently.
func (r *Runner) updateTaskStatus(ctx context.Context, taskName string, startTime *metav1.Time, outputs []string, results map[string]string) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		task, err := r.kelosClient.ApiV1alpha1().Tasks(r.config.PodNamespace).Get(ctx, taskName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if task.Status.StartTime == nil {
			task.Status.StartTime = startTime
		}
		now := metav1.Now()
		task.Status.CompletionTime = &now
		if len(outputs) > 0 {
			task.Status.Outputs = outputs
			task.Status.Results = results
		}
		_, err = r.kelosClient.ApiV1alpha1().Tasks(r.config.PodNamespace).UpdateStatus(ctx, task, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return fmt.Errorf("failed to update task status after %d retries", maxRetries)
}

// setTaskStatus sets the kelos.dev/task-status annotation on the pod.
func (r *Runner) setTaskStatus(ctx context.Context, status string) error {
	return r.setAnnotation(ctx, annotationTaskStatus, status)
}

// setAnnotations sets multiple annotations on the pod atomically with retry-on-conflict.
func (r *Runner) setAnnotations(ctx context.Context, annotations map[string]string) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		pod, err := r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Get(ctx, r.config.PodName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			pod.Annotations[k] = v
		}
		_, err = r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Update(ctx, pod, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return fmt.Errorf("failed to set annotations after %d retries", maxRetries)
}

// setAnnotation sets a single annotation on the pod with retry-on-conflict.
func (r *Runner) setAnnotation(ctx context.Context, key, value string) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		pod, err := r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Get(ctx, r.config.PodName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[key] = value
		_, err = r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Update(ctx, pod, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		// Retry on conflict.
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return fmt.Errorf("failed to set annotation %s after %d retries", key, maxRetries)
}

// tailWriter is a fixed-size ring buffer that retains only the last N bytes
// written to it. This bounds memory usage when capturing verbose agent output.
type tailWriter struct {
	buf  []byte
	size int
	pos  int
	full bool
}

func newTailWriter(size int) *tailWriter {
	return &tailWriter{buf: make([]byte, size), size: size}
}

func (tw *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n >= tw.size {
		copy(tw.buf, p[n-tw.size:])
		tw.pos = 0
		tw.full = true
		return n, nil
	}
	space := tw.size - tw.pos
	if n <= space {
		copy(tw.buf[tw.pos:], p)
		tw.pos += n
	} else {
		copy(tw.buf[tw.pos:], p[:space])
		copy(tw.buf, p[space:])
		tw.pos = n - space
		tw.full = true
	}
	if tw.pos == tw.size {
		tw.pos = 0
		tw.full = true
	}
	return n, nil
}

func (tw *tailWriter) String() string {
	if !tw.full {
		return string(tw.buf[:tw.pos])
	}
	var b bytes.Buffer
	b.Write(tw.buf[tw.pos:])
	b.Write(tw.buf[:tw.pos])
	return b.String()
}
