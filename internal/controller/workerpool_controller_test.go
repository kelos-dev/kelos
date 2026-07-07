package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newWorkerPoolTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	return scheme
}

func newTestWorkerPool(name, namespace string, replicas int32) *kelos.WorkerPool {
	return &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: kelos.WorkerPoolSpec{
			Worker: kelos.WorkerSpec{
				Type: AgentTypeClaudeCode,
				Credentials: &kelos.Credentials{
					Type: kelos.CredentialTypeNone,
				},
				WorkspaceRef: &kelos.WorkspaceReference{
					Name: "test-workspace",
				},
			},
			Replicas: &replicas,
			VolumeClaimTemplate: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
			},
		},
	}
}

func newTestWorkspace(namespace string) *kelos.Workspace {
	return &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workspace",
			Namespace: namespace,
		},
		Spec: kelos.WorkspaceSpec{
			Repo: "https://github.com/example/repo.git",
			Ref:  "main",
		},
	}
}

func newWorkerPoolReconciler(cl client.Client, scheme *runtime.Scheme) *WorkerPoolReconciler {
	return &WorkerPoolReconciler{
		Client:            cl,
		Scheme:            scheme,
		Recorder:          record.NewFakeRecorder(10),
		WorkerRunnerImage: "test-runner:latest",
		ClaudeCodeImage:   "test-claude-code:latest",
	}
}

func workerPoolLabelsForTest(poolName string) map[string]string {
	return map[string]string{
		"kelos.dev/workerpool":     poolName,
		"kelos.dev/component":      "worker",
		"kelos.dev/managed-by":     "kelos-controller",
		"kelos.dev/name":           "kelos",
		"kelos.dev/execution-mode": "persistent",
	}
}

func TestWorkerPoolReconciler_CreatesStatefulSet(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 3)
	ws := newTestWorkspace("default")

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.WorkerPool{}).
		WithObjects(pool, ws).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.NoError(t, err)

	var sts appsv1.StatefulSet
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool", Namespace: "default"}, &sts)
	require.NoError(t, err)

	assert.Equal(t, int32(3), *sts.Spec.Replicas)
	assert.Equal(t, workerPoolLabelsForTest("my-pool"), sts.Spec.Selector.MatchLabels)
	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	assert.Equal(t, WorkspaceVolumeName, sts.Spec.VolumeClaimTemplates[0].Name)
	expectedSize := resource.MustParse("10Gi")
	assert.True(t, sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage].Equal(expectedSize),
		"PVC storage size mismatch")
}

func TestWorkerPoolReconciler_LongPoolNameUsesBoundedLabelValue(t *testing.T) {
	poolName := strings.Repeat("a", 70)
	labels := workerPoolLabels(poolName)

	assert.LessOrEqual(t, len(labels[labelWorkerPool]), 63)
	assert.NotEqual(t, poolName, labels[labelWorkerPool])

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-long-0",
			Namespace: "default",
			Labels:    labels,
			Annotations: map[string]string{
				annotationPoolName: poolName,
			},
		},
	}
	requests := (&WorkerPoolReconciler{}).findPoolForPod(context.Background(), pod)

	require.Len(t, requests, 1)
	assert.Equal(t, poolName, requests[0].Name)
}

func TestWorkerPoolReconciler_ReconcilesTaskWithSameNameAsWorkerPool(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("shared", "default", 1)
	ws := newTestWorkspace("default")
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Prompt: "Do something",
			WorkerPoolRef: &kelos.WorkerPoolReference{
				Name: "shared",
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-shared-0",
			Namespace: "default",
			Labels:    workerPoolLabelsForTest("shared"),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, ws, task, pod).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "shared", Namespace: "default"},
	})
	require.NoError(t, err)

	var updatedTask kelos.Task
	err = cl.Get(context.Background(), types.NamespacedName{Name: "shared", Namespace: "default"}, &updatedTask)
	require.NoError(t, err)
	assert.Equal(t, "wp-shared-0", updatedTask.Status.PodName)
}

func TestWorkerPoolReconciler_CommitRefWorkspaceUsesCheckoutScript(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 1)
	ws := newTestWorkspace("default")
	ws.Spec.Ref = "0123456789abcdef0123456789abcdef01234567"

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.WorkerPool{}).
		WithObjects(pool, ws).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.NoError(t, err)

	var sts appsv1.StatefulSet
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool", Namespace: "default"}, &sts)
	require.NoError(t, err)

	var gitClone *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "git-clone" {
			gitClone = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	require.NotNil(t, gitClone)
	require.Len(t, gitClone.Command, 3)
	assert.Contains(t, gitClone.Command[2], `fetch --depth 1 origin "$ref"`)
	assert.Equal(t, []string{"--", ws.Spec.Repo, WorkspaceMountPath + "/repo", ws.Spec.Ref}, gitClone.Args)
	for _, arg := range gitClone.Args {
		assert.NotEqual(t, "--branch", arg)
	}
}

func TestWorkerPoolReconciler_RejectsExtraContainerInitContainerNameCollision(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 1)
	pool.Spec.Worker.PodOverrides = &kelos.PodOverrides{
		ExtraContainers: []corev1.Container{
			{Name: "sidecar", Image: "helper:latest"},
		},
		ExtraInitContainers: []corev1.Container{
			{Name: "sidecar", Image: "helper:latest"},
		},
	}
	ws := newTestWorkspace("default")

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.WorkerPool{}).
		WithObjects(pool, ws).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "appears in both extraContainers and extraInitContainers")
}

func TestWorkerPoolReconciler_CreatesService(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 2)
	ws := newTestWorkspace("default")

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.WorkerPool{}).
		WithObjects(pool, ws).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.NoError(t, err)

	var svc corev1.Service
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool", Namespace: "default"}, &svc)
	require.NoError(t, err)

	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.Equal(t, workerPoolLabelsForTest("my-pool"), svc.Spec.Selector)
}

func TestWorkerPoolReconciler_UpdatesReplicas(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 3)
	ws := newTestWorkspace("default")

	existingSTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool",
			Namespace: "default",
			Labels:    workerPoolLabelsForTest("my-pool"),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    int32Ptr(2),
			ServiceName: "wp-my-pool",
			Selector: &metav1.LabelSelector{
				MatchLabels: workerPoolLabelsForTest("my-pool"),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: workerPoolLabelsForTest("my-pool")},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "worker-runner", Image: "test"}}},
			},
		},
	}

	existingSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool",
			Namespace: "default",
			Labels:    workerPoolLabelsForTest("my-pool"),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  workerPoolLabelsForTest("my-pool"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.WorkerPool{}).
		WithObjects(pool, ws, existingSTS, existingSvc).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.NoError(t, err)

	var sts appsv1.StatefulSet
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool", Namespace: "default"}, &sts)
	require.NoError(t, err)
	assert.Equal(t, int32(3), *sts.Spec.Replicas)
}

func TestWorkerPoolReconciler_StatusPhases(t *testing.T) {
	tests := []struct {
		name          string
		replicas      int32
		stsReplicas   int32
		readyReplicas int32
		wantPhase     kelos.WorkerPoolPhase
	}{
		{
			name:          "Pending when both zero",
			replicas:      3,
			stsReplicas:   0,
			readyReplicas: 0,
			wantPhase:     kelos.WorkerPoolPhasePending,
		},
		{
			name:          "Scaling when partial",
			replicas:      3,
			stsReplicas:   3,
			readyReplicas: 1,
			wantPhase:     kelos.WorkerPoolPhaseScaling,
		},
		{
			name:          "Ready when all ready",
			replicas:      3,
			stsReplicas:   3,
			readyReplicas: 3,
			wantPhase:     kelos.WorkerPoolPhaseReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newWorkerPoolTestScheme()
			pool := newTestWorkerPool("my-pool", "default", tt.replicas)
			ws := newTestWorkspace("default")

			existingSTS := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wp-my-pool",
					Namespace: "default",
					Labels:    workerPoolLabelsForTest("my-pool"),
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &tt.replicas,
					ServiceName: "wp-my-pool",
					Selector: &metav1.LabelSelector{
						MatchLabels: workerPoolLabelsForTest("my-pool"),
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: workerPoolLabelsForTest("my-pool")},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "worker-runner", Image: "test"}}},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      tt.stsReplicas,
					ReadyReplicas: tt.readyReplicas,
				},
			}

			existingSvc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wp-my-pool",
					Namespace: "default",
					Labels:    workerPoolLabelsForTest("my-pool"),
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: corev1.ClusterIPNone,
					Selector:  workerPoolLabelsForTest("my-pool"),
				},
			}

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.WorkerPool{}).
				WithObjects(pool, ws, existingSTS, existingSvc).
				Build()

			r := newWorkerPoolReconciler(cl, scheme)

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
			})
			require.NoError(t, err)

			var updatedPool kelos.WorkerPool
			err = cl.Get(context.Background(), types.NamespacedName{Name: "my-pool", Namespace: "default"}, &updatedPool)
			require.NoError(t, err)
			assert.Equal(t, tt.wantPhase, updatedPool.Status.Phase)
		})
	}
}

func TestWorkerPoolReconciler_AssignsTask(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 2)

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do something",
			WorkerPoolRef: &kelos.WorkerPoolReference{
				Name: "my-pool",
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool-0",
			Namespace: "default",
			Labels:    workerPoolLabelsForTest("my-pool"),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, task, pod).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
	})
	require.NoError(t, err)

	var updatedTask kelos.Task
	err = cl.Get(context.Background(), types.NamespacedName{Name: "test-task", Namespace: "default"}, &updatedTask)
	require.NoError(t, err)
	assert.Equal(t, "wp-my-pool-0", updatedTask.Status.PodName)
	assert.Equal(t, kelos.TaskPhasePending, updatedTask.Status.Phase)

	var updatedPod corev1.Pod
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool-0", Namespace: "default"}, &updatedPod)
	require.NoError(t, err)
	assert.Equal(t, "test-task", updatedPod.Annotations[kelos.AnnotationWorkerAssignedTask])
	assert.Empty(t, updatedPod.Annotations[kelos.AnnotationWorkerTaskStatus])
}

func TestWorkerPoolReconciler_TaskCompletionSucceeded(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 1)

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do something",
			WorkerPoolRef: &kelos.WorkerPoolReference{
				Name: "my-pool",
			},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "wp-my-pool-0",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool-0",
			Namespace: "default",
			Labels:    workerPoolLabelsForTest("my-pool"),
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "test-task",
				kelos.AnnotationWorkerTaskStatus:   "succeeded",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, task, pod).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
	})
	require.NoError(t, err)

	var updatedTask kelos.Task
	err = cl.Get(context.Background(), types.NamespacedName{Name: "test-task", Namespace: "default"}, &updatedTask)
	require.NoError(t, err)
	assert.Equal(t, kelos.TaskPhaseSucceeded, updatedTask.Status.Phase)
	assert.NotNil(t, updatedTask.Status.CompletionTime)
}

func TestWorkerPoolReconciler_TaskCompletionFailed(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 1)

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task",
			Namespace: "default",
		},
		Spec: kelos.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do something",
			WorkerPoolRef: &kelos.WorkerPoolReference{
				Name: "my-pool",
			},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "wp-my-pool-0",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool-0",
			Namespace: "default",
			Labels:    workerPoolLabelsForTest("my-pool"),
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask:   "test-task",
				kelos.AnnotationWorkerTaskStatus:     "failed",
				kelos.AnnotationWorkerTaskFailReason: "OOM killed",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, task, pod).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
	})
	require.NoError(t, err)

	var updatedTask kelos.Task
	err = cl.Get(context.Background(), types.NamespacedName{Name: "test-task", Namespace: "default"}, &updatedTask)
	require.NoError(t, err)
	assert.Equal(t, kelos.TaskPhaseFailed, updatedTask.Status.Phase)
	assert.Equal(t, "OOM killed", updatedTask.Status.Message)
	assert.NotNil(t, updatedTask.Status.CompletionTime)
}

func TestRequestWorkerPodTaskCancellationMarksAssignedTask(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "test-task",
				kelos.AnnotationWorkerTaskStatus:   "running",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	released, err := requestWorkerPodTaskCancellation(context.Background(), cl, pod, "test-task")
	require.NoError(t, err)
	assert.False(t, released)

	var updatedPod corev1.Pod
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool-0", Namespace: "default"}, &updatedPod)
	require.NoError(t, err)
	assert.Equal(t, "test-task", updatedPod.Annotations[kelos.AnnotationWorkerAssignedTask])
	assert.Equal(t, "running", updatedPod.Annotations[kelos.AnnotationWorkerTaskStatus])
	assert.Equal(t, "test-task", updatedPod.Annotations[kelos.AnnotationWorkerCancelTask])
}

func TestRequestWorkerPodTaskCancellationClearsTerminalAssignment(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-my-pool-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask:   "test-task",
				kelos.AnnotationWorkerTaskStatus:     "failed",
				kelos.AnnotationWorkerTaskFailReason: "task was cancelled",
				kelos.AnnotationWorkerCancelTask:     "test-task",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	released, err := requestWorkerPodTaskCancellation(context.Background(), cl, pod, "test-task")
	require.NoError(t, err)
	assert.True(t, released)

	var updatedPod corev1.Pod
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool-0", Namespace: "default"}, &updatedPod)
	require.NoError(t, err)
	assert.NotContains(t, updatedPod.Annotations, kelos.AnnotationWorkerAssignedTask)
	assert.NotContains(t, updatedPod.Annotations, kelos.AnnotationWorkerTaskStatus)
	assert.NotContains(t, updatedPod.Annotations, kelos.AnnotationWorkerTaskFailReason)
	assert.NotContains(t, updatedPod.Annotations, kelos.AnnotationWorkerCancelTask)
	assert.Equal(t, "1", updatedPod.Annotations[kelos.AnnotationWorkerTasksCompleted])
}

func TestWorkerPoolReconciler_MapsWorkspaceChangesToWorkerPools(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	ws := newTestWorkspace("default")
	pool := newTestWorkerPool("matching-pool", "default", 1)
	other := newTestWorkerPool("other-pool", "default", 1)
	other.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "other-workspace"}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ws, pool, other).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	requests := r.findPoolsForWorkspace(context.Background(), ws)

	assert.ElementsMatch(t, []reconcileRequestName{{namespace: "default", name: "matching-pool"}}, requestNames(requests))
}

func TestWorkerPoolReconciler_MapsAgentConfigChangesToWorkerPools(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	agentConfig := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "default"},
	}
	pool := newTestWorkerPool("matching-pool", "default", 1)
	pool.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "shared-config"}}
	other := newTestWorkerPool("other-pool", "default", 1)
	other.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "other-config"}}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agentConfig, pool, other).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	requests := r.findPoolsForAgentConfig(context.Background(), agentConfig)

	assert.ElementsMatch(t, []reconcileRequestName{{namespace: "default", name: "matching-pool"}}, requestNames(requests))
}

func TestWorkerPoolReconciler_MapsSecretChangesToWorkerPools(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "default"},
	}
	ws := newTestWorkspace("default")
	ws.Spec.SecretRef = &kelos.SecretReference{Name: "shared-secret"}
	workspacePool := newTestWorkerPool("workspace-pool", "default", 1)
	credentialsPool := newTestWorkerPool("credentials-pool", "default", 1)
	credentialsPool.Spec.Worker.Credentials = &kelos.Credentials{
		Type:      kelos.CredentialTypeAPIKey,
		SecretRef: &kelos.SecretReference{Name: "shared-secret"},
	}
	other := newTestWorkerPool("other-pool", "default", 1)
	other.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "other-workspace"}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret, ws, workspacePool, credentialsPool, other).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	requests := r.findPoolsForSecret(context.Background(), secret)

	assert.ElementsMatch(t, []reconcileRequestName{
		{namespace: "default", name: "workspace-pool"},
		{namespace: "default", name: "credentials-pool"},
	}, requestNames(requests))
}

func TestWorkerTaskLogSegmentScopesOutputsToTask(t *testing.T) {
	logData := "---KELOS_TASK_START--- task-a\n" +
		"---KELOS_OUTPUTS_START---\n" +
		"old: output\n" +
		"---KELOS_OUTPUTS_END---\n" +
		"---KELOS_TASK_END--- task-a\n" +
		"---KELOS_TASK_START--- task-b\n" +
		"setup failed\n" +
		"---KELOS_TASK_END--- task-b\n"

	segment := workerTaskLogSegment(logData, "task-b")
	assert.Contains(t, segment, "setup failed")
	assert.NotContains(t, segment, "old: output")
	assert.Nil(t, ParseOutputs(segment))
}

func TestWorkerPoolReconciler_SkipsUnavailablePods(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "Pod not running",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wp-my-pool-0",
					Namespace: "default",
					Labels:    workerPoolLabelsForTest("my-pool"),
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
		},
		{
			name: "Pod has DeletionTimestamp",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "wp-my-pool-0",
					Namespace:         "default",
					Labels:            workerPoolLabelsForTest("my-pool"),
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{"test-finalizer"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
		},
		{
			name: "Pod already has assigned task",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wp-my-pool-0",
					Namespace: "default",
					Labels:    workerPoolLabelsForTest("my-pool"),
					Annotations: map[string]string{
						kelos.AnnotationWorkerAssignedTask: "other-task",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newWorkerPoolTestScheme()
			pool := newTestWorkerPool("my-pool", "default", 1)

			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task",
					Namespace: "default",
				},
				Spec: kelos.TaskSpec{
					Type:   AgentTypeClaudeCode,
					Prompt: "Do something",
					WorkerPoolRef: &kelos.WorkerPoolReference{
						Name: "my-pool",
					},
				},
			}

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
				WithObjects(pool, task, tt.pod).
				Build()

			r := newWorkerPoolReconciler(cl, scheme)

			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
			})
			require.NoError(t, err)
			assert.NotZero(t, result.RequeueAfter, "Expected requeue when no pods available")

			var updatedTask kelos.Task
			err = cl.Get(context.Background(), types.NamespacedName{Name: "test-task", Namespace: "default"}, &updatedTask)
			require.NoError(t, err)
			assert.Empty(t, updatedTask.Status.PodName)
		})
	}
}

func TestIsPodAvailable_EmptyAnnotationTreatedAsAvailable(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-pool-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	assert.True(t, isPodAvailable(pod), "pod with empty assigned-task annotation should be available")

	pod.Annotations[kelos.AnnotationWorkerAssignedTask] = "some-task"
	assert.False(t, isPodAvailable(pod), "pod with non-empty assigned-task annotation should not be available")
}

func TestWorkerPoolReconciler_FindWorkerPoolTasksForBudgetOnlyBudgetBlocked(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	budget := &kelos.TaskBudget{ObjectMeta: metav1.ObjectMeta{Name: "budget", Namespace: "default"}}
	blocked := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "blocked", Namespace: "default"},
		Spec: kelos.TaskSpec{
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"},
		},
		Status: kelos.TaskStatus{
			Phase: kelos.TaskPhaseWaiting,
			Conditions: []metav1.Condition{{
				Type:   "BudgetBlocked",
				Status: metav1.ConditionTrue,
			}},
		},
	}
	waitingOtherReason := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "waiting-other-reason", Namespace: "default"},
		Spec: kelos.TaskSpec{
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"},
		},
		Status: kelos.TaskStatus{Phase: kelos.TaskPhaseWaiting},
	}
	jobBacked := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "job-backed", Namespace: "default"},
		Status: kelos.TaskStatus{
			Phase: kelos.TaskPhaseWaiting,
			Conditions: []metav1.Condition{{
				Type:   "BudgetBlocked",
				Status: metav1.ConditionTrue,
			}},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}).
		WithObjects(budget, blocked, waitingOtherReason, jobBacked).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	requests := r.findWorkerPoolTasksForBudget(context.Background(), budget)
	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Name: "blocked", Namespace: "default"}, requests[0].NamespacedName)
}

func TestWorkerPoolReconciler_ClearsAssignmentWhenTaskRecordCreateFails(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	costUSD := resource.MustParse("1")
	pool := newTestWorkerPool("pool", "default", 1)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default", UID: "task-uid"},
		Spec: kelos.TaskSpec{
			Prompt: "test",
			WorkerPoolRef: &kelos.WorkerPoolReference{
				Name: "pool",
			},
		},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "wp-pool-0",
			Usage:   &kelos.TaskUsage{CostUSD: &costUSD},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-pool-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "task-1",
				kelos.AnnotationWorkerTaskStatus:   "succeeded",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, task, pod).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*kelos.TaskRecord); ok {
					return errors.New("transient taskrecord create failure")
				}
				return cl.Create(ctx, obj, opts...)
			},
		}).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "task-1", Namespace: "default"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transient taskrecord create failure")

	var updatedPod corev1.Pod
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "wp-pool-0", Namespace: "default"}, &updatedPod))
	assert.Empty(t, updatedPod.Annotations[kelos.AnnotationWorkerAssignedTask])
}

func TestWorkerPoolReconciler_TerminalRetryClearsLeakedAssignment(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	costUSD := resource.MustParse("1")
	completionTime := metav1.Now()
	pool := newTestWorkerPool("pool", "default", 1)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default", UID: "task-uid"},
		Spec: kelos.TaskSpec{
			Prompt: "test",
			WorkerPoolRef: &kelos.WorkerPoolReference{
				Name: "pool",
			},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			PodName:        "wp-pool-0",
			CompletionTime: &completionTime,
			Usage:          &kelos.TaskUsage{CostUSD: &costUSD},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wp-pool-0",
			Namespace: "default",
			Annotations: map[string]string{
				kelos.AnnotationWorkerAssignedTask: "task-1",
				kelos.AnnotationWorkerTaskStatus:   "succeeded",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, task, pod).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "task-1", Namespace: "default"},
	})
	require.NoError(t, err)

	var updatedPod corev1.Pod
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "wp-pool-0", Namespace: "default"}, &updatedPod))
	assert.Empty(t, updatedPod.Annotations[kelos.AnnotationWorkerAssignedTask])

	var record kelos.TaskRecord
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "task-uid", Namespace: "default"}, &record))
}

func TestPodTemplateSpecEqual_IgnoresAPIDefaults(t *testing.T) {
	gracePeriod := int64(30)
	desired := corev1.PodSpec{
		ServiceAccountName: WorkerRunnerServiceAccount,
		Containers:         []corev1.Container{{Name: kelos.AgentContainerName, Image: "agent:v1"}},
	}
	stored := desired.DeepCopy()
	stored.RestartPolicy = corev1.RestartPolicyAlways
	stored.DNSPolicy = corev1.DNSClusterFirst
	stored.SchedulerName = "default-scheduler"
	stored.TerminationGracePeriodSeconds = &gracePeriod

	assert.True(t, podTemplateSpecEqual(*stored, desired))

	changed := desired.DeepCopy()
	changed.Containers[0].Image = "agent:v2"
	assert.False(t, podTemplateSpecEqual(desired, *changed))
}

func TestWorkerPoolReconciler_RejectsGitHubAppSecret(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 1)

	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workspace",
			Namespace: "default",
		},
		Spec: kelos.WorkspaceSpec{
			Repo: "https://github.com/example/repo.git",
			Ref:  "main",
			SecretRef: &kelos.SecretReference{
				Name: "github-app-secret",
			},
		},
	}

	// GitHub App secret has appID, installationID, privateKey
	appSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     []byte("-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.WorkerPool{}).
		WithObjects(pool, ws, appSecret).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitHub App secret which is not supported for persistent worker pools")
}

func int32Ptr(v int32) *int32 { return &v }

type reconcileRequestName struct {
	namespace string
	name      string
}

func requestNames(requests []ctrl.Request) []reconcileRequestName {
	names := make([]reconcileRequestName, 0, len(requests))
	for _, request := range requests {
		names = append(names, reconcileRequestName{
			namespace: request.Namespace,
			name:      request.Name,
		})
	}
	return names
}
