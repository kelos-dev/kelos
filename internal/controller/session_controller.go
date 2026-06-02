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

package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// AnnotationAssignedTask is set on session pods to indicate which Task is assigned.
	AnnotationAssignedTask = "kelos.dev/assigned-task"

	// AnnotationTaskStatus is set on session pods by the session runner to report task status.
	AnnotationTaskStatus = "kelos.dev/task-status"

	// AnnotationTasksCompleted tracks the number of tasks completed by a session pod.
	AnnotationTasksCompleted = "kelos.dev/tasks-completed"

	// AnnotationSessionStartTime records when the session pod started processing tasks.
	AnnotationSessionStartTime = "kelos.dev/session-start-time"

	// LabelExecutionMode is set on Tasks to indicate their execution mode.
	LabelExecutionMode = "kelos.dev/execution-mode"

	// AnnotationTaskFailureReason is set by the session runner to indicate why a task failed.
	AnnotationTaskFailureReason = "kelos.dev/task-failure-reason"
)

// SessionReconciler assigns queued Tasks to available persistent session pods
// and monitors session pod annotations for task completion signals.
type SessionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskspawners,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch

// Reconcile handles session-related reconciliation for persistent-mode Tasks.
func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var task kelosv1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Only handle persistent-mode tasks.
	if task.Labels[LabelExecutionMode] != string(kelosv1alpha1.ExecutionModePersistent) {
		return ctrl.Result{}, nil
	}

	// Skip terminal tasks — pod assignment is already cleared when the task
	// transitions to a terminal phase in checkTaskCompletion. Re-clearing here
	// is unsafe because the pod may have been reassigned to a different task.
	if task.Status.Phase == kelosv1alpha1.TaskPhaseSucceeded || task.Status.Phase == kelosv1alpha1.TaskPhaseFailed {
		return ctrl.Result{}, nil
	}

	// If task is Queued, try to assign it to an available pod.
	if task.Status.Phase == kelosv1alpha1.TaskPhaseQueued {
		return r.assignTask(ctx, &task)
	}

	// If task has a session pod assigned, check for completion signals.
	// Handle both Pending (waiting for runner to start) and Running phases.
	if task.Status.SessionPodName != "" &&
		(task.Status.Phase == kelosv1alpha1.TaskPhasePending || task.Status.Phase == kelosv1alpha1.TaskPhaseRunning) {
		return r.checkTaskCompletion(ctx, &task)
	}

	return ctrl.Result{}, nil
}

// assignTask tries to assign a Queued task to an available session pod.
func (r *SessionReconciler) assignTask(ctx context.Context, task *kelosv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	spawnerName := task.Labels["kelos.dev/taskspawner"]
	if spawnerName == "" {
		return ctrl.Result{}, fmt.Errorf("task %s missing kelos.dev/taskspawner label", task.Name)
	}

	// List session pods for this spawner.
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(task.Namespace),
		client.MatchingLabels{
			"kelos.dev/taskspawner": spawnerName,
			"kelos.dev/component":   SessionComponentLabel,
		},
	); err != nil {
		return ctrl.Result{}, err
	}

	// Find an available pod (Running, no assigned task).
	var availablePod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		if _, assigned := pod.Annotations[AnnotationAssignedTask]; assigned {
			continue
		}
		availablePod = pod
		break
	}

	if availablePod == nil {
		logger.V(1).Info("No available session pod for task, requeuing", "task", task.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Claim the task first (this is the mutex). Only proceed to annotate
	// the pod if we successfully transition the task from Queued to Pending.
	logger.Info("Assigning task to session pod", "task", task.Name, "pod", availablePod.Name)

	var alreadyAssigned bool
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		if task.Status.Phase != kelosv1alpha1.TaskPhaseQueued {
			alreadyAssigned = true
			return nil
		}
		task.Status.SessionPodName = availablePod.Name
		task.Status.Phase = kelosv1alpha1.TaskPhasePending
		task.Status.Message = fmt.Sprintf("Assigned to session pod %s", availablePod.Name)
		return r.Status().Update(ctx, task)
	}); err != nil {
		return ctrl.Result{}, err
	}

	if alreadyAssigned {
		logger.V(1).Info("Task already assigned by another reconcile", "task", task.Name)
		return ctrl.Result{}, nil
	}

	// Task claimed successfully. Now annotate the pod so the session runner
	// picks it up. If this fails, clear the task assignment to avoid a
	// stuck state. Use RetryOnConflict because kubelet status updates bump
	// resourceVersion frequently, causing 409s even without competing assignment.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKey{Namespace: availablePod.Namespace, Name: availablePod.Name}, availablePod); getErr != nil {
			return getErr
		}
		// Re-check that the pod has not been assigned by another reconcile.
		if existing := availablePod.Annotations[AnnotationAssignedTask]; existing != "" {
			return fmt.Errorf("pod %s already assigned to task %s", availablePod.Name, existing)
		}
		if availablePod.Annotations == nil {
			availablePod.Annotations = make(map[string]string)
		}
		availablePod.Annotations[AnnotationAssignedTask] = task.Name
		return r.Update(ctx, availablePod)
	}); err != nil {
		logger.Error(err, "Failed to annotate pod after task claim, clearing task assignment",
			"pod", availablePod.Name, "task", task.Name)
		// Roll back the task status to Queued so it can be reassigned.
		if rollbackErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
				return getErr
			}
			task.Status.Phase = kelosv1alpha1.TaskPhaseQueued
			task.Status.SessionPodName = ""
			task.Status.Message = "Re-queued: failed to annotate session pod"
			return r.Status().Update(ctx, task)
		}); rollbackErr != nil {
			logger.Error(rollbackErr, "Failed to roll back task status after pod annotation failure")
		}
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(task, corev1.EventTypeNormal, "SessionAssigned", "Task assigned to session pod %s", availablePod.Name)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// checkTaskCompletion reads the session pod's annotations to detect task
// completion signals from the session runner.
func (r *SessionReconciler) checkTaskCompletion(ctx context.Context, task *kelosv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: task.Namespace,
		Name:      task.Status.SessionPodName,
	}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			sessionConfig, cfgErr := r.getSessionConfig(ctx, task)
			if cfgErr != nil {
				return ctrl.Result{}, cfgErr
			}
			if r.shouldRetryOnPodFailure(sessionConfig, task) {
				logger.Info("Session pod not found, re-queuing task",
					"task", task.Name, "pod", task.Status.SessionPodName, "retryCount", task.Status.SessionRetryCount)
				r.Recorder.Eventf(task, corev1.EventTypeWarning, "SessionPodLost",
					"Session pod %s was deleted, re-queuing (attempt %d)", task.Status.SessionPodName, task.Status.SessionRetryCount+1)
				return r.requeueTask(ctx, task, "session pod was deleted")
			}
			logger.Info("Session pod not found, marking task as failed (retry limit reached)",
				"task", task.Name, "pod", task.Status.SessionPodName)
			return r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseFailed,
				fmt.Sprintf("Session pod was deleted (exhausted %d retries)", task.Status.SessionRetryCount))
		}
		return ctrl.Result{}, err
	}

	// Check the task status annotation set by the session runner.
	taskStatus := pod.Annotations[AnnotationTaskStatus]
	switch taskStatus {
	case "succeeded":
		logger.Info("Task completed successfully via session runner", "task", task.Name)
		if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseSucceeded, "Task completed successfully"); err != nil {
			return result, err
		}
		if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
			logger.Error(err, "Failed to clear pod assignment after success")
		}
		return ctrl.Result{}, nil

	case "failed":
		failureReason := pod.Annotations[AnnotationTaskFailureReason]
		if isRetriableFailure(failureReason) {
			sessionConfig, cfgErr := r.getSessionConfig(ctx, task)
			if cfgErr != nil {
				return ctrl.Result{}, cfgErr
			}
			if r.shouldRetryOnPodFailure(sessionConfig, task) {
				logger.Info("Task failed with retriable reason, re-queuing",
					"task", task.Name, "pod", pod.Name, "reason", failureReason,
					"retryCount", task.Status.SessionRetryCount)
				r.Recorder.Eventf(task, corev1.EventTypeWarning, "TaskFailedRetriable",
					"Task failed (%s), re-queuing (attempt %d)", failureReason, task.Status.SessionRetryCount+1)
				if clearErr := r.clearPodAssignment(ctx, task.Namespace, pod.Name); clearErr != nil {
					logger.Error(clearErr, "Failed to clear pod assignment before requeue")
				}
				return r.requeueTask(ctx, task, failureReason)
			}
		}
		logger.Info("Task failed via session runner", "task", task.Name)
		if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseFailed, "Task failed"); err != nil {
			return result, err
		}
		if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
			logger.Error(err, "Failed to clear pod assignment after failure")
		}
		return ctrl.Result{}, nil

	case "running":
		if task.Status.Phase != kelosv1alpha1.TaskPhaseRunning {
			return r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseRunning, "Task is running on session pod")
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	default:
		// If the task already has a CompletionTime, the session runner finished
		// and wrote the Task status update but the subsequent pod annotation
		// write may have failed. Wait briefly to allow for the race between
		// task status PATCH and pod annotation PATCH, then infer success —
		// the runner only reaches the annotation write after processTask
		// returns nil; a non-nil error takes the "failed" annotation path.
		if task.Status.CompletionTime != nil {
			grace := 15 * time.Second
			if time.Since(task.Status.CompletionTime.Time) < grace {
				logger.V(1).Info("Task has completion time but no status annotation, waiting for grace period",
					"task", task.Name, "pod", pod.Name)
				return ctrl.Result{RequeueAfter: grace}, nil
			}
			logger.Info("Task has completion time but no status annotation after grace period, inferring success",
				"task", task.Name, "pod", pod.Name)
			if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseSucceeded,
				"Task completed (annotation write missed)"); err != nil {
				return result, err
			}
			if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
				logger.Error(err, "Failed to clear pod assignment after inferred completion")
			}
			return ctrl.Result{}, nil
		}

		if pod.Status.Phase == corev1.PodFailed {
			sessionConfig, cfgErr := r.getSessionConfig(ctx, task)
			if cfgErr != nil {
				return ctrl.Result{}, cfgErr
			}
			if r.shouldRetryOnPodFailure(sessionConfig, task) {
				logger.Info("Session pod failed, re-queuing task",
					"task", task.Name, "pod", pod.Name, "retryCount", task.Status.SessionRetryCount)
				r.Recorder.Eventf(task, corev1.EventTypeWarning, "SessionPodFailed",
					"Session pod %s failed, re-queuing (attempt %d)", pod.Name, task.Status.SessionRetryCount+1)
				if clearErr := r.clearPodAssignment(ctx, task.Namespace, pod.Name); clearErr != nil {
					logger.Error(clearErr, "Failed to clear pod assignment before requeue")
				}
				return r.requeueTask(ctx, task, "session pod failed")
			}
			logger.Info("Session pod failed, marking task as failed (retry limit reached)",
				"task", task.Name, "pod", pod.Name)
			if result, err := r.updateTaskPhase(ctx, task, kelosv1alpha1.TaskPhaseFailed,
				fmt.Sprintf("Session pod failed (exhausted %d retries)", task.Status.SessionRetryCount)); err != nil {
				return result, err
			}
			if err := r.clearPodAssignment(ctx, task.Namespace, pod.Name); err != nil {
				logger.Error(err, "Failed to clear pod assignment after pod failure")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
}

// getSessionConfig fetches the SessionConfig for a task's owning TaskSpawner.
// It returns an error on transient failures so callers requeue rather than
// silently falling back to defaults.
func (r *SessionReconciler) getSessionConfig(ctx context.Context, task *kelosv1alpha1.Task) (*kelosv1alpha1.SessionConfig, error) {
	spawnerName := task.Labels["kelos.dev/taskspawner"]
	if spawnerName == "" {
		return nil, nil
	}
	var spawner kelosv1alpha1.TaskSpawner
	if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: spawnerName}, &spawner); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching TaskSpawner %s for session config: %w", spawnerName, err)
	}
	return spawner.Spec.SessionConfig, nil
}

// shouldRetryOnPodFailure determines whether a task should be re-queued based
// on the SessionConfig and current retry count.
func (r *SessionReconciler) shouldRetryOnPodFailure(config *kelosv1alpha1.SessionConfig, task *kelosv1alpha1.Task) bool {
	if config == nil {
		return task.Status.SessionRetryCount < 3
	}
	if config.RetryOnPodFailure != nil && !*config.RetryOnPodFailure {
		return false
	}
	maxRetries := int32(3)
	if config.MaxSessionRetries != nil {
		maxRetries = *config.MaxSessionRetries
	}
	return task.Status.SessionRetryCount < maxRetries
}

// retriableFailureReasons are failure reasons that warrant re-queuing.
var retriableFailureReasons = map[string]bool{
	"token-expired": true,
}

// isRetriableFailure returns true if the failure reason indicates a transient issue.
func isRetriableFailure(reason string) bool {
	return retriableFailureReasons[reason]
}

// requeueTask resets a task back to Queued phase, clearing its pod assignment
// and incrementing the retry counter.
func (r *SessionReconciler) requeueTask(ctx context.Context, task *kelosv1alpha1.Task, reason string) (ctrl.Result, error) {
	failedPod := task.Status.SessionPodName
	return ctrl.Result{}, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		sessionConfig, cfgErr := r.getSessionConfig(ctx, task)
		if cfgErr != nil {
			return cfgErr
		}
		if !r.shouldRetryOnPodFailure(sessionConfig, task) {
			task.Status.Phase = kelosv1alpha1.TaskPhaseFailed
			task.Status.SessionPodName = ""
			task.Status.LastSessionFailure = failedPod
			task.Status.Message = fmt.Sprintf("Session pod failure: %s (exhausted %d retries)", reason, task.Status.SessionRetryCount)
			return r.Status().Update(ctx, task)
		}
		task.Status.Phase = kelosv1alpha1.TaskPhaseQueued
		task.Status.SessionPodName = ""
		task.Status.SessionRetryCount++
		task.Status.LastSessionFailure = failedPod
		task.Status.StartTime = nil
		task.Status.CompletionTime = nil
		task.Status.Outputs = nil
		task.Status.Results = nil
		task.Status.Message = fmt.Sprintf("Re-queued after session pod failure: %s (attempt %d)", reason, task.Status.SessionRetryCount)
		return r.Status().Update(ctx, task)
	})
}

// updateTaskPhase updates a Task's phase and message.
func (r *SessionReconciler) updateTaskPhase(ctx context.Context, task *kelosv1alpha1.Task, phase kelosv1alpha1.TaskPhase, message string) (ctrl.Result, error) {
	return ctrl.Result{}, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.Phase = phase
		task.Status.Message = message
		return r.Status().Update(ctx, task)
	})
}

// clearPodAssignment removes the task assignment annotations from a session pod.
// Uses RetryOnConflict because the session runner and kubelet concurrently
// update the same pod, causing frequent 409 Conflict responses.
func (r *SessionReconciler) clearPodAssignment(ctx context.Context, namespace, podName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pod corev1.Pod
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		if pod.Annotations == nil {
			return nil
		}

		delete(pod.Annotations, AnnotationAssignedTask)
		delete(pod.Annotations, AnnotationTaskStatus)
		delete(pod.Annotations, AnnotationTaskFailureReason)
		return r.Update(ctx, &pod)
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("session").
		For(&kelosv1alpha1.Task{},
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[LabelExecutionMode] == string(kelosv1alpha1.ExecutionModePersistent)
			}))).
		// Watch session pods for annotation changes (task completion signals).
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findTaskForSessionPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()["kelos.dev/component"] == SessionComponentLabel
			}))).
		Complete(r)
}

// findTaskForSessionPod maps a session pod change to the Task assigned to it.
func (r *SessionReconciler) findTaskForSessionPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	taskName := pod.Annotations[AnnotationAssignedTask]
	if taskName == "" {
		// Pod has no task assigned. Check if there are Queued tasks that need
		// assignment - trigger reconciliation of the oldest one.
		return r.findOldestQueuedTask(ctx, pod)
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      taskName,
		},
	}}
}

// findOldestQueuedTask returns a reconcile request for the oldest Queued task
// belonging to the same TaskSpawner as the given session pod.
func (r *SessionReconciler) findOldestQueuedTask(ctx context.Context, pod *corev1.Pod) []reconcile.Request {
	spawnerName := pod.Labels["kelos.dev/taskspawner"]
	if spawnerName == "" {
		return nil
	}

	var taskList kelosv1alpha1.TaskList
	if err := r.List(ctx, &taskList,
		client.InNamespace(pod.Namespace),
		client.MatchingLabels{
			"kelos.dev/taskspawner": spawnerName,
			LabelExecutionMode:      string(kelosv1alpha1.ExecutionModePersistent),
		},
	); err != nil {
		return nil
	}

	// Filter to Queued tasks and sort by creation time.
	var queued []kelosv1alpha1.Task
	for _, t := range taskList.Items {
		if t.Status.Phase == kelosv1alpha1.TaskPhaseQueued {
			queued = append(queued, t)
		}
	}

	if len(queued) == 0 {
		return nil
	}

	sort.Slice(queued, func(i, j int) bool {
		return queued[i].CreationTimestamp.Before(&queued[j].CreationTimestamp)
	})

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: queued[0].Namespace,
			Name:      queued[0].Name,
		},
	}}
}
