package controller

import (
	"context"
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
