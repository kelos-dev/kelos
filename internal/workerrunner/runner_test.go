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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	kelosfake "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env: map[string]string{
				"KELOS_POD_NAME":      "worker-0",
				"KELOS_POD_NAMESPACE": "default",
				"KELOS_AGENT_TYPE":    "claude-code",
			},
			want: Config{
				PodName:      "worker-0",
				PodNamespace: "default",
				AgentType:    "claude-code",
				IdleTimeout:  defaultIdleTimeout,
			},
		},
		{
			name: "custom idle timeout",
			env: map[string]string{
				"KELOS_POD_NAME":      "worker-0",
				"KELOS_POD_NAMESPACE": "default",
				"KELOS_AGENT_TYPE":    "claude-code",
				"KELOS_IDLE_TIMEOUT":  "5m",
			},
			want: Config{
				PodName:      "worker-0",
				PodNamespace: "default",
				AgentType:    "claude-code",
				IdleTimeout:  5 * time.Minute,
			},
		},
		{
			name: "custom max tasks",
			env: map[string]string{
				"KELOS_POD_NAME":             "worker-0",
				"KELOS_POD_NAMESPACE":        "default",
				"KELOS_AGENT_TYPE":           "claude-code",
				"KELOS_MAX_TASKS_PER_WORKER": "10",
			},
			want: Config{
				PodName:           "worker-0",
				PodNamespace:      "default",
				AgentType:         "claude-code",
				IdleTimeout:       defaultIdleTimeout,
				MaxTasksPerWorker: 10,
			},
		},
		{
			name: "invalid idle timeout",
			env: map[string]string{
				"KELOS_POD_NAME":      "worker-0",
				"KELOS_POD_NAMESPACE": "default",
				"KELOS_AGENT_TYPE":    "claude-code",
				"KELOS_IDLE_TIMEOUT":  "not-a-duration",
			},
			wantErr: true,
		},
		{
			name: "invalid max tasks",
			env: map[string]string{
				"KELOS_POD_NAME":             "worker-0",
				"KELOS_POD_NAMESPACE":        "default",
				"KELOS_AGENT_TYPE":           "claude-code",
				"KELOS_MAX_TASKS_PER_WORKER": "abc",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := ConfigFromEnv()
			if tt.wantErr {
				if err == nil {
					t.Fatal("ConfigFromEnv() returned nil error, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfigFromEnv() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ConfigFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func newTestRunner(t *testing.T, cfg Config, pod *corev1.Pod, task *kelos.Task) *Runner {
	t.Helper()

	var kubeObjects []runtime.Object
	if pod != nil {
		kubeObjects = append(kubeObjects, pod)
	}

	var kelosObjects []runtime.Object
	if task != nil {
		kelosObjects = append(kelosObjects, task)
	}

	kubeClient := kubefake.NewSimpleClientset(kubeObjects...)
	kelosClient := kelosfake.NewSimpleClientset(kelosObjects...)
	return NewRunnerWithClients(cfg, kubeClient, kelosClient)
}

func lastEnvValue(env []string, name string) string {
	prefix := name + "="
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	return value
}

func TestTaskAgentEnvIncludesPerTaskModelAndEffort(t *testing.T) {
	task := &kelos.Task{
		Spec: kelos.TaskSpec{
			Prompt:       "Fix the bug",
			Model:        "gpt-5",
			Effort:       "high",
			UpstreamRepo: "upstream/repo",
		},
	}

	env := taskAgentEnv([]string{"KELOS_MODEL=pool-model", "KELOS_UPSTREAM_REPO=pool/repo", "OTHER=value"}, task)

	if got := lastEnvValue(env, "KELOS_PROMPT"); got != "Fix the bug" {
		t.Errorf("KELOS_PROMPT = %q, want task prompt", got)
	}
	if got := lastEnvValue(env, "KELOS_MODEL"); got != "gpt-5" {
		t.Errorf("KELOS_MODEL = %q, want task model", got)
	}
	if got := lastEnvValue(env, "KELOS_EFFORT"); got != "high" {
		t.Errorf("KELOS_EFFORT = %q, want task effort", got)
	}
	if got := lastEnvValue(env, "KELOS_UPSTREAM_REPO"); got != "upstream/repo" {
		t.Errorf("KELOS_UPSTREAM_REPO = %q, want task upstream repo", got)
	}
}

func TestTaskAgentEnvRefreshesGitHubTokenFromFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("ghs_fresh_token\n"), 0o600); err != nil {
		t.Fatalf("writing token file: %v", err)
	}
	t.Setenv("KELOS_GITHUB_TOKEN_FILE", tokenFile)

	task := &kelos.Task{Spec: kelos.TaskSpec{Prompt: "Fix the bug"}}

	// The pod-start (frozen) token env vars should be overridden by the
	// current file contents; GH_ENTERPRISE_TOKEN is absent so it stays unset.
	base := []string{"GITHUB_TOKEN=stale", "GH_TOKEN=stale", "OTHER=value"}
	env := taskAgentEnv(base, task)

	if got := lastEnvValue(env, "GITHUB_TOKEN"); got != "ghs_fresh_token" {
		t.Errorf("GITHUB_TOKEN = %q, want refreshed token", got)
	}
	if got := lastEnvValue(env, "GH_TOKEN"); got != "ghs_fresh_token" {
		t.Errorf("GH_TOKEN = %q, want refreshed token", got)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "GH_ENTERPRISE_TOKEN=") {
			t.Errorf("GH_ENTERPRISE_TOKEN unexpectedly set: %q", kv)
		}
	}
}

func TestTaskAgentEnvNoTokenFileLeavesEnvUnchanged(t *testing.T) {
	t.Setenv("KELOS_GITHUB_TOKEN_FILE", "")

	task := &kelos.Task{Spec: kelos.TaskSpec{Prompt: "Fix the bug"}}
	env := taskAgentEnv([]string{"GITHUB_TOKEN=pat-value"}, task)

	if got := lastEnvValue(env, "GITHUB_TOKEN"); got != "pat-value" {
		t.Errorf("GITHUB_TOKEN = %q, want original PAT value", got)
	}
}

func TestRunIdleTimeout(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
		AgentType:    "claude-code",
		IdleTimeout:  50 * time.Millisecond,
	}

	runner := newTestRunner(t, cfg, pod, nil)

	err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestRunContextCancellation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
		AgentType:    "claude-code",
		IdleTimeout:  10 * time.Second,
	}

	runner := newTestRunner(t, cfg, pod, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestRunTaskAssignmentExecution(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "my-task",
			},
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Prompt: "Fix the bug",
		},
	}
	cfg := Config{
		PodName:           "worker-0",
		PodNamespace:      "default",
		AgentType:         "claude-code",
		IdleTimeout:       10 * time.Second,
		MaxTasksPerWorker: 1,
	}

	runner := newTestRunner(t, cfg, pod, task)

	var executedTask *kelos.Task
	runner.runAgentFunc = func(_ context.Context, t *kelos.Task) error {
		executedTask = t
		return nil
	}

	// Clear the annotation after execution so waitForAssignmentRelease completes.
	// We do this by launching a goroutine that watches for the status annotation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			p, err := runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if p.Annotations[kelos.AnnotationWorkerTaskStatus] == "succeeded" {
				p.Annotations[kelos.AnnotationWorkerAssignedTask] = ""
				_, _ = runner.kubeClient.CoreV1().Pods("default").Update(ctx, p, metav1.UpdateOptions{})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if executedTask == nil {
		t.Fatal("Expected task to be executed, but runAgentFunc was not called")
	}
	if executedTask.Name != "my-task" {
		t.Errorf("Expected task name %q, got %q", "my-task", executedTask.Name)
	}
	if executedTask.Spec.Prompt != "Fix the bug" {
		t.Errorf("Expected prompt %q, got %q", "Fix the bug", executedTask.Spec.Prompt)
	}

	// Verify the status annotation was set to succeeded
	p, err := runner.kubeClient.CoreV1().Pods("default").Get(context.Background(), "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if p.Annotations[kelos.AnnotationWorkerTaskStatus] != "succeeded" {
		t.Errorf("Expected task status annotation %q, got %q", "succeeded", p.Annotations[kelos.AnnotationWorkerTaskStatus])
	}
	if p.Annotations[kelos.AnnotationWorkerAssignedTask] != "" {
		t.Errorf("Expected assigned task annotation to be cleared before max-task exit, got %q", p.Annotations[kelos.AnnotationWorkerAssignedTask])
	}
}

func TestRunTaskExecutionFailure(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "failing-task",
			},
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "failing-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Prompt: "Do something impossible",
		},
	}
	cfg := Config{
		PodName:           "worker-0",
		PodNamespace:      "default",
		AgentType:         "claude-code",
		IdleTimeout:       10 * time.Second,
		MaxTasksPerWorker: 1,
	}

	runner := newTestRunner(t, cfg, pod, task)
	runner.runAgentFunc = func(_ context.Context, _ *kelos.Task) error {
		return fmt.Errorf("agent crashed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			p, err := runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if p.Annotations[kelos.AnnotationWorkerTaskStatus] == "failed" {
				p.Annotations[kelos.AnnotationWorkerAssignedTask] = ""
				_, _ = runner.kubeClient.CoreV1().Pods("default").Update(ctx, p, metav1.UpdateOptions{})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	p, err := runner.kubeClient.CoreV1().Pods("default").Get(context.Background(), "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if p.Annotations[kelos.AnnotationWorkerTaskStatus] != "failed" {
		t.Errorf("Expected task status annotation %q, got %q", "failed", p.Annotations[kelos.AnnotationWorkerTaskStatus])
	}
	if p.Annotations[kelos.AnnotationWorkerTaskFailReason] == "" {
		t.Error("Expected failure reason annotation to be set")
	}
}

func TestSetTaskStatus(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "my-task",
			},
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
	}
	runner := newTestRunner(t, cfg, pod, nil)

	ctx := context.Background()

	if err := runner.setTaskStatus(ctx, "my-task", "running", ""); err != nil {
		t.Fatalf("setTaskStatus(running) returned error: %v", err)
	}

	p, err := runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if p.Annotations[kelos.AnnotationWorkerTaskStatus] != "running" {
		t.Errorf("Expected status %q, got %q", "running", p.Annotations[kelos.AnnotationWorkerTaskStatus])
	}
	if _, exists := p.Annotations[kelos.AnnotationWorkerTaskFailReason]; exists {
		t.Error("Expected no failure reason annotation when status is running")
	}

	if err := runner.setTaskStatus(ctx, "my-task", "failed", "something broke"); err != nil {
		t.Fatalf("setTaskStatus(failed) returned error: %v", err)
	}

	p, err = runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if p.Annotations[kelos.AnnotationWorkerTaskStatus] != "failed" {
		t.Errorf("Expected status %q, got %q", "failed", p.Annotations[kelos.AnnotationWorkerTaskStatus])
	}
	if p.Annotations[kelos.AnnotationWorkerTaskFailReason] != "something broke" {
		t.Errorf("Expected failure reason %q, got %q", "something broke", p.Annotations[kelos.AnnotationWorkerTaskFailReason])
	}

	// Transitioning to succeeded should clear the failure reason
	if err := runner.setTaskStatus(ctx, "my-task", "succeeded", ""); err != nil {
		t.Fatalf("setTaskStatus(succeeded) returned error: %v", err)
	}

	p, err = runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if p.Annotations[kelos.AnnotationWorkerTaskStatus] != "succeeded" {
		t.Errorf("Expected status %q, got %q", "succeeded", p.Annotations[kelos.AnnotationWorkerTaskStatus])
	}
	if _, exists := p.Annotations[kelos.AnnotationWorkerTaskFailReason]; exists {
		t.Error("Expected failure reason to be cleared on succeeded")
	}
}

func TestSetTaskStatusSkipsChangedAssignment(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "next-task",
			},
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
	}
	runner := newTestRunner(t, cfg, pod, nil)

	err := runner.setTaskStatus(context.Background(), "old-task", "succeeded", "")
	if !errors.Is(err, errAssignmentChanged) {
		t.Fatalf("setTaskStatus() error = %v, want %v", err, errAssignmentChanged)
	}

	p, err := runner.kubeClient.CoreV1().Pods("default").Get(context.Background(), "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if got := p.Annotations[kelos.AnnotationWorkerTaskStatus]; got != "" {
		t.Errorf("Expected no task status annotation, got %q", got)
	}
}

func TestExecuteTaskSkipsCancelledAssignment(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "my-task",
				kelos.AnnotationWorkerCancelTask:   "my-task",
			},
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{Prompt: "Do not run"},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
	}
	runner := newTestRunner(t, cfg, pod, task)
	runner.runAgentFunc = func(_ context.Context, _ *kelos.Task) error {
		t.Fatal("runAgentFunc should not run for a cancelled task")
		return nil
	}

	err := runner.executeTask(context.Background(), "my-task")
	if err == nil {
		t.Fatal("executeTask() returned nil error, want cancellation error")
	}

	p, err := runner.kubeClient.CoreV1().Pods("default").Get(context.Background(), "worker-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Error getting pod: %v", err)
	}
	if got := p.Annotations[kelos.AnnotationWorkerTaskStatus]; got != "failed" {
		t.Errorf("Expected status %q, got %q", "failed", got)
	}
	if got := p.Annotations[kelos.AnnotationWorkerTaskFailReason]; got != "task was cancelled" {
		t.Errorf("Expected cancellation reason, got %q", got)
	}
}

func TestWaitForAssignmentReleaseWhenCleared(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "some-task",
			},
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
	}
	runner := newTestRunner(t, cfg, pod, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Clear the assignment — the poll loop checks every 3s.
	go func() {
		time.Sleep(100 * time.Millisecond)
		p, _ := runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
		p.Annotations[kelos.AnnotationWorkerAssignedTask] = ""
		_, _ = runner.kubeClient.CoreV1().Pods("default").Update(ctx, p, metav1.UpdateOptions{})
	}()

	err := runner.waitForAssignmentRelease(ctx, "some-task")
	if err != nil {
		t.Fatalf("waitForAssignmentRelease() returned error: %v", err)
	}
}

func TestWaitForAssignmentReleaseWhenNextTaskAssigned(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "some-task",
			},
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
	}
	runner := newTestRunner(t, cfg, pod, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		time.Sleep(100 * time.Millisecond)
		p, _ := runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
		p.Annotations[kelos.AnnotationWorkerAssignedTask] = "next-task"
		_, _ = runner.kubeClient.CoreV1().Pods("default").Update(ctx, p, metav1.UpdateOptions{})
	}()

	err := runner.waitForAssignmentRelease(ctx, "some-task")
	if err != nil {
		t.Fatalf("waitForAssignmentRelease() returned error: %v", err)
	}
}

func TestWaitForAssignmentReleaseContextCancelled(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "some-task",
			},
		},
	}
	cfg := Config{
		PodName:      "worker-0",
		PodNamespace: "default",
	}
	runner := newTestRunner(t, cfg, pod, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := runner.waitForAssignmentRelease(ctx, "some-task")
	if err == nil {
		t.Fatal("Expected error from context cancellation")
	}
}

func TestRunMaxTasksPerWorker(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "task-1",
			},
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Prompt: "First task",
		},
	}
	cfg := Config{
		PodName:           "worker-0",
		PodNamespace:      "default",
		AgentType:         "claude-code",
		IdleTimeout:       10 * time.Second,
		MaxTasksPerWorker: 1,
	}

	runner := newTestRunner(t, cfg, pod, task)

	var callCount int
	runner.runAgentFunc = func(_ context.Context, _ *kelos.Task) error {
		callCount++
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			p, err := runner.kubeClient.CoreV1().Pods("default").Get(ctx, "worker-0", metav1.GetOptions{})
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if p.Annotations[kelos.AnnotationWorkerTaskStatus] == "succeeded" {
				p.Annotations[kelos.AnnotationWorkerAssignedTask] = ""
				_, _ = runner.kubeClient.CoreV1().Pods("default").Update(ctx, p, metav1.UpdateOptions{})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected runAgentFunc to be called 1 time, got %d", callCount)
	}
}

func TestGetAssignedTask(t *testing.T) {
	tests := []struct {
		name       string
		annotation string
		want       string
	}{
		{
			name:       "no annotation",
			annotation: "",
			want:       "",
		},
		{
			name:       "task assigned",
			annotation: "my-task",
			want:       "my-task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := map[string]string{}
			if tt.annotation != "" {
				annotations[kelos.AnnotationWorkerAssignedTask] = tt.annotation
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "worker-0",
					Namespace:   "default",
					Annotations: annotations,
				},
			}
			cfg := Config{
				PodName:      "worker-0",
				PodNamespace: "default",
			}
			runner := newTestRunner(t, cfg, pod, nil)

			got, err := runner.getAssignedTask(context.Background())
			if err != nil {
				t.Fatalf("getAssignedTask() returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("getAssignedTask() = %q, want %q", got, tt.want)
			}
		})
	}
}
