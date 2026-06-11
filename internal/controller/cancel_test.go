package controller

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestTaskReconciler_CancelledTaskDeletesJob(t *testing.T) {
	scheme := newTestScheme()

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "cancel-me",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
			Credentials: kelosv1alpha1.Credentials{
				Type: kelosv1alpha1.CredentialTypeNone,
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:       kelosv1alpha1.TaskPhaseCancelled,
			CancelledBy: "user",
		},
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cancel-me",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, job).
		WithStatusSubresource(task).
		Build()

	r := &TaskReconciler{
		Client:       fakeClient,
		Scheme:       scheme,
		BranchLocker: NewBranchLocker(),
		Recorder:     record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cancel-me", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	// Verify Job was deleted.
	var updatedJob batchv1.Job
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cancel-me", Namespace: "default"}, &updatedJob)
	if !apierrors.IsNotFound(err) {
		t.Errorf("Expected NotFound error for deleted Job, got: %v", err)
	}
}

func TestTaskReconciler_CancelledTaskReleasesBranchLock(t *testing.T) {
	scheme := newTestScheme()

	workspace := &kelosv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-workspace",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
		},
	}

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "cancel-branch",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
			Branch: "feat/test",
			Credentials: kelosv1alpha1.Credentials{
				Type: kelosv1alpha1.CredentialTypeNone,
			},
			WorkspaceRef: &kelosv1alpha1.WorkspaceReference{
				Name: "my-workspace",
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:       kelosv1alpha1.TaskPhaseCancelled,
			CancelledBy: "user",
		},
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cancel-branch",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task, job, workspace).
		WithStatusSubresource(task).
		Build()

	bl := NewBranchLocker()
	lockKey := branchLockKey(task)
	bl.TryAcquire(lockKey, task.Name)

	r := &TaskReconciler{
		Client:       fakeClient,
		Scheme:       scheme,
		BranchLocker: bl,
		Recorder:     record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cancel-branch", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	// Verify branch lock was released — another task should be able to acquire it.
	acquired, _ := bl.TryAcquire(lockKey, "another-task")
	if !acquired {
		t.Error("Expected branch lock to be released after task cancellation")
	}
}

func TestTaskReconciler_CancelledTaskWithNoJob(t *testing.T) {
	scheme := newTestScheme()

	now := metav1.Now()
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "already-cancelled",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
			Credentials: kelosv1alpha1.Credentials{
				Type: kelosv1alpha1.CredentialTypeNone,
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseCancelled,
			CancelledBy:    "user",
			CompletionTime: &now,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task).
		WithStatusSubresource(task).
		Build()

	r := &TaskReconciler{
		Client:       fakeClient,
		Scheme:       scheme,
		BranchLocker: NewBranchLocker(),
		Recorder:     record.NewFakeRecorder(10),
	}

	// Should not error even when no Job exists.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "already-cancelled", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
}

func TestTTLExpired_IncludesCancelled(t *testing.T) {
	scheme := newTestScheme()
	r := &TaskReconciler{Scheme: scheme}

	ttl := int32(0)
	now := metav1.Now()
	task := &kelosv1alpha1.Task{
		Spec: kelosv1alpha1.TaskSpec{
			TTLSecondsAfterFinished: &ttl,
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseCancelled,
			CompletionTime: &now,
		},
	}

	expired, _ := r.ttlExpired(task)
	if !expired {
		t.Error("Expected TTL to be expired for Cancelled task with TTL=0")
	}
}

// Verify that the reconciler handles a Cancelled task that is still Pending
// (i.e., the Job never started). This covers the case where a user cancels
// before the agent even begins.
func TestTaskReconciler_CancelsPendingTask(t *testing.T) {
	scheme := newTestScheme()

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "pending-cancel",
			Namespace:  "default",
			Finalizers: []string{taskFinalizer},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: "do something",
			Credentials: kelosv1alpha1.Credentials{
				Type: kelosv1alpha1.CredentialTypeNone,
			},
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:       kelosv1alpha1.TaskPhaseCancelled,
			CancelledBy: "user",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(task).
		WithStatusSubresource(task).
		Build()

	r := &TaskReconciler{
		Client:       fakeClient,
		Scheme:       scheme,
		BranchLocker: NewBranchLocker(),
		Recorder:     record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pending-cancel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}
}
