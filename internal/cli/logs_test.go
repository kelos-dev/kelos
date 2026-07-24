package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestResolveTaskPodName(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		task    *kelos.Task
		objects []client.Object
		want    string
		wantErr bool
	}{
		{
			name: "uses newest live pod",
			task: &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
				Status: kelos.TaskStatus{
					PodName: "task-pod-old",
				},
			},
			objects: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "task-pod-old",
						Namespace:         "default",
						CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
						Labels: map[string]string{
							"kelos.dev/task": "task-1",
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "task-pod-new",
						Namespace:         "default",
						CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
						Labels: map[string]string{
							"kelos.dev/task": "task-1",
						},
					},
				},
			},
			want: "task-pod-new",
		},
		{
			name: "returns empty when no live pod remains",
			task: &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
				Status: kelos.TaskStatus{
					PodName: "task-pod-old",
					Phase:   kelos.TaskPhaseFailed,
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if len(tt.objects) > 0 {
				builder = builder.WithObjects(tt.objects...)
			}
			cl := builder.Build()

			got, err := resolveTaskPodName(context.Background(), cl, "default", tt.task)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveTaskPodName() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveTaskPodName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsTerminalTaskPhase(t *testing.T) {
	tests := []struct {
		phase kelos.TaskPhase
		want  bool
	}{
		{phase: kelos.TaskPhasePending, want: false},
		{phase: kelos.TaskPhaseRunning, want: false},
		{phase: kelos.TaskPhaseSucceeded, want: true},
		{phase: kelos.TaskPhaseFailed, want: true},
	}

	for _, tt := range tests {
		if got := isTerminalTaskPhase(tt.phase); got != tt.want {
			t.Fatalf("isTerminalTaskPhase(%q) = %v, want %v", tt.phase, got, tt.want)
		}
	}
}

func TestOpenTaskLogStreamRetriesWhilePodIsStarting(t *testing.T) {
	tests := []struct {
		name      string
		podStatus corev1.PodStatus
		nodeName  string
		streamErr error
	}{
		{
			name:      "unscheduled pod",
			podStatus: corev1.PodStatus{Phase: corev1.PodPending},
			streamErr: apierrors.NewBadRequest("pod task-pod does not have a host assigned"),
		},
		{
			name:      "scheduled pod without container status",
			nodeName:  "worker-1",
			podStatus: corev1.PodStatus{Phase: corev1.PodPending},
			streamErr: apierrors.NewBadRequest(`container "kelos-agent" in pod "task-pod" is not available`),
		},
		{
			name:     "initializing container",
			nodeName: "worker-1",
			podStatus: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "kelos-agent",
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
						},
					},
				},
			},
			streamErr: apierrors.NewBadRequest(`container "kelos-agent" in pod "task-pod" is waiting to start: PodInitializing`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
				Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "task-pod",
					Namespace: "default",
					Labels:    map[string]string{"kelos.dev/task": task.Name},
				},
				Spec:   corev1.PodSpec{NodeName: tt.nodeName},
				Status: tt.podStatus,
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task, pod).Build()

			podName, err := resolveTaskPodName(context.Background(), cl, "default", task)
			if err != nil {
				t.Fatalf("resolveTaskPodName() error = %v", err)
			}

			attempts := 0
			stream, err := openTaskLogStream(
				context.Background(),
				cl,
				"default",
				task.Name,
				podName,
				"kelos-agent",
				true,
				0,
				func(context.Context) (io.ReadCloser, error) {
					attempts++
					if attempts == 1 {
						return nil, tt.streamErr
					}
					return io.NopCloser(strings.NewReader("task output")), nil
				},
			)
			if err != nil {
				t.Fatalf("openTaskLogStream() error = %v", err)
			}
			defer stream.Close()

			output, err := io.ReadAll(stream)
			if err != nil {
				t.Fatalf("reading stream: %v", err)
			}
			if string(output) != "task output" {
				t.Fatalf("stream output = %q, want %q", output, "task output")
			}
			if attempts != 2 {
				t.Fatalf("stream attempts = %d, want 2", attempts)
			}
		})
	}
}

func TestOpenTaskLogStreamStopsForTerminalTask(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseFailed,
			Message: "pod could not start",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "task-pod", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task, pod).Build()

	attempts := 0
	_, err := openTaskLogStream(
		context.Background(),
		cl,
		"default",
		task.Name,
		pod.Name,
		"kelos-agent",
		true,
		0,
		func(context.Context) (io.ReadCloser, error) {
			attempts++
			return nil, apierrors.NewBadRequest("pod is not ready")
		},
	)
	if err == nil || err.Error() != `task "task-1" failed before logs could be streamed: pod could not start` {
		t.Fatalf("openTaskLogStream() error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("stream attempts = %d, want 1", attempts)
	}
}

func TestOpenTaskLogStreamRetriesContainerReadinessTransition(t *testing.T) {
	tests := []struct {
		name           string
		taskPhase      kelos.TaskPhase
		containerState corev1.ContainerState
		succeed        bool
		wantAttempts   int
		wantErr        bool
	}{
		{
			name:           "opens running container stream on immediate retry",
			taskPhase:      kelos.TaskPhaseRunning,
			containerState: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			succeed:        true,
			wantAttempts:   2,
		},
		{
			name:      "opens completed container stream on immediate retry",
			taskPhase: kelos.TaskPhaseSucceeded,
			containerState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
			},
			succeed:      true,
			wantAttempts: 2,
		},
		{
			name:           "stops after bounded retry",
			taskPhase:      kelos.TaskPhaseRunning,
			containerState: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			wantAttempts:   2,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
				Status:     kelos.TaskStatus{Phase: tt.taskPhase},
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "task-pod", Namespace: "default"},
				Spec:       corev1.PodSpec{NodeName: "worker-1"},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "kelos-agent",
							State: tt.containerState,
						},
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task, pod).Build()
			streamErr := apierrors.NewBadRequest(`container "kelos-agent" in pod "task-pod" is waiting to start: ContainerCreating`)

			attempts := 0
			stream, err := openTaskLogStream(
				context.Background(),
				cl,
				"default",
				task.Name,
				pod.Name,
				"kelos-agent",
				true,
				0,
				func(context.Context) (io.ReadCloser, error) {
					attempts++
					if tt.succeed && attempts == 2 {
						return io.NopCloser(strings.NewReader("task output")), nil
					}
					return nil, streamErr
				},
			)
			if stream != nil {
				defer stream.Close()
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("openTaskLogStream() error = %v, wantErr %v", err, tt.wantErr)
			}
			if attempts != tt.wantAttempts {
				t.Fatalf("stream attempts = %d, want %d", attempts, tt.wantAttempts)
			}
		})
	}
}

func TestOpenTaskLogStreamDoesNotRetryContainerFailure(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
		Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "task-pod", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "worker-1"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "kelos-agent",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
					},
				},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task, pod).Build()
	streamErr := apierrors.NewBadRequest("image cannot be pulled")

	attempts := 0
	_, err := openTaskLogStream(
		context.Background(),
		cl,
		"default",
		task.Name,
		pod.Name,
		"kelos-agent",
		true,
		0,
		func(context.Context) (io.ReadCloser, error) {
			attempts++
			return nil, streamErr
		},
	)
	if !errors.Is(err, streamErr) {
		t.Fatalf("openTaskLogStream() error = %v, want wrapped stream error", err)
	}
	if attempts != 1 {
		t.Fatalf("stream attempts = %d, want 1", attempts)
	}
}

func TestFilterTaskLogSegment(t *testing.T) {
	input := strings.Join([]string{
		"before",
		"---KELOS_TASK_START--- task-a",
		"task a output",
		"---KELOS_TASK_END--- task-a",
		"---KELOS_TASK_START--- task-b",
		"task b output 1",
		"task b output 2",
		"---KELOS_TASK_END--- task-b",
		"after",
	}, "\n")

	var out bytes.Buffer
	if err := filterTaskLogSegment(strings.NewReader(input), &out, "task-b"); err != nil {
		t.Fatalf("filterTaskLogSegment() error = %v", err)
	}

	got := out.String()
	if got != "task b output 1\ntask b output 2\n" {
		t.Fatalf("filtered logs = %q", got)
	}
}

func TestFilterTaskLogSegmentMissingTask(t *testing.T) {
	var out bytes.Buffer
	err := filterTaskLogSegment(strings.NewReader("no matching markers\n"), &out, "task-b")
	if !errors.Is(err, errTaskLogSegmentNotFound) {
		t.Fatalf("filterTaskLogSegment() error = %v, want errTaskLogSegmentNotFound", err)
	}
}

func TestResolveTaskPodNameAfterWait(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "task-pod-old",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	podName, err := resolveTaskPodName(context.Background(), cl, "default", task)
	if err != nil {
		t.Fatalf("resolveTaskPodName() error = %v", err)
	}
	if podName != "" {
		t.Fatalf("resolveTaskPodName() = %q, want empty", podName)
	}

	if err := func() error {
		if podName == "" {
			return fmt.Errorf("task %q pod disappeared before logs could be streamed", task.Name)
		}
		return nil
	}(); err == nil || err.Error() != `task "task-1" pod disappeared before logs could be streamed` {
		t.Fatalf("post-wait pod validation error = %v", err)
	}
}
