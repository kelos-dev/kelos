package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

const (
	taskFinalizer = "axon.io/finalizer"
)

// TaskReconciler reconciles a Task object.
type TaskReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	JobBuilder *JobBuilder
}

// +kubebuilder:rbac:groups=axon.io,resources=tasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=axon.io,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=axon.io,resources=tasks/finalizers,verbs=update
// +kubebuilder:rbac:groups=axon.io,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile handles Task reconciliation.
func (r *TaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task axonv1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch Task")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !task.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &task)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&task, taskFinalizer) {
		controllerutil.AddFinalizer(&task, taskFinalizer)
		if err := r.Update(ctx, &task); err != nil {
			logger.Error(err, "unable to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if Job already exists
	var job batchv1.Job
	jobExists := true
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		if apierrors.IsNotFound(err) {
			jobExists = false
		} else {
			logger.Error(err, "unable to fetch Job")
			return ctrl.Result{}, err
		}
	}

	// Create Job if it doesn't exist
	if !jobExists {
		return r.createJob(ctx, &task)
	}

	// Update status based on Job status
	result, err := r.updateStatus(ctx, &task, &job)
	if err != nil {
		return result, err
	}

	// Check TTL expiration for finished Tasks
	if expired, requeueAfter := r.ttlExpired(&task); expired {
		logger.Info("Deleting Task due to TTL expiration", "task", task.Name)
		if err := r.Delete(ctx, &task); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			logger.Error(err, "Unable to delete expired Task")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	} else if requeueAfter > 0 {
		// Requeue to check TTL expiration later
		if result.RequeueAfter == 0 || requeueAfter < result.RequeueAfter {
			result.RequeueAfter = requeueAfter
		}
	}

	return result, nil
}

// handleDeletion handles Task deletion.
func (r *TaskReconciler) handleDeletion(ctx context.Context, task *axonv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(task, taskFinalizer) {
		// Delete the Job if it exists
		var job batchv1.Job
		if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Name}, &job); err == nil {
			propagationPolicy := metav1.DeletePropagationBackground
			if err := r.Delete(ctx, &job, &client.DeleteOptions{
				PropagationPolicy: &propagationPolicy,
			}); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "unable to delete Job")
				return ctrl.Result{}, err
			}
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(task, taskFinalizer)
		if err := r.Update(ctx, task); err != nil {
			logger.Error(err, "unable to remove finalizer")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// createJob creates a Job for the Task.
func (r *TaskReconciler) createJob(ctx context.Context, task *axonv1alpha1.Task) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var workspace *axonv1alpha1.WorkspaceSpec
	if task.Spec.WorkspaceRef != nil {
		var ws axonv1alpha1.Workspace
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: task.Namespace,
			Name:      task.Spec.WorkspaceRef.Name,
		}, &ws); err != nil {
			logger.Error(err, "Unable to fetch Workspace", "workspace", task.Spec.WorkspaceRef.Name)
			if apierrors.IsNotFound(err) {
				task.Status.Phase = axonv1alpha1.TaskPhaseFailed
				task.Status.Message = fmt.Sprintf("Workspace %q not found", task.Spec.WorkspaceRef.Name)
				if updateErr := r.Status().Update(ctx, task); updateErr != nil {
					logger.Error(updateErr, "Unable to update Task status")
					return ctrl.Result{}, updateErr
				}
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		workspace = &ws.Spec
	}

	job, err := r.JobBuilder.Build(task, workspace)
	if err != nil {
		logger.Error(err, "unable to build Job")
		task.Status.Phase = axonv1alpha1.TaskPhaseFailed
		task.Status.Message = fmt.Sprintf("Failed to build Job: %v", err)
		if updateErr := r.Status().Update(ctx, task); updateErr != nil {
			logger.Error(updateErr, "unable to update Task status")
		}
		return ctrl.Result{}, err
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(task, job, r.Scheme); err != nil {
		logger.Error(err, "unable to set owner reference")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "unable to create Job")
		return ctrl.Result{}, err
	}

	logger.Info("created Job", "job", job.Name)

	// Update status
	task.Status.Phase = axonv1alpha1.TaskPhasePending
	task.Status.JobName = job.Name
	if err := r.Status().Update(ctx, task); err != nil {
		logger.Error(err, "unable to update Task status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// updateStatus updates Task status based on Job status.
func (r *TaskReconciler) updateStatus(ctx context.Context, task *axonv1alpha1.Task, job *batchv1.Job) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Find pod name
	if task.Status.PodName == "" {
		var pods corev1.PodList
		if err := r.List(ctx, &pods, client.InNamespace(task.Namespace), client.MatchingLabels{
			"axon.io/task": task.Name,
		}); err == nil && len(pods.Items) > 0 {
			task.Status.PodName = pods.Items[0].Name
		}
	}

	// Update phase based on Job status
	var statusChanged bool

	if job.Status.Active > 0 {
		if task.Status.Phase != axonv1alpha1.TaskPhaseRunning {
			task.Status.Phase = axonv1alpha1.TaskPhaseRunning
			now := metav1.Now()
			task.Status.StartTime = &now
			statusChanged = true
		}
	} else if job.Status.Succeeded > 0 {
		if task.Status.Phase != axonv1alpha1.TaskPhaseSucceeded {
			task.Status.Phase = axonv1alpha1.TaskPhaseSucceeded
			now := metav1.Now()
			task.Status.CompletionTime = &now
			task.Status.Message = "Task completed successfully"
			statusChanged = true
		}
	} else if job.Status.Failed > 0 {
		if task.Status.Phase != axonv1alpha1.TaskPhaseFailed {
			task.Status.Phase = axonv1alpha1.TaskPhaseFailed
			now := metav1.Now()
			task.Status.CompletionTime = &now
			task.Status.Message = "Task failed"
			statusChanged = true
		}
	}

	if statusChanged {
		if err := r.Status().Update(ctx, task); err != nil {
			logger.Error(err, "unable to update Task status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// ttlExpired checks whether a finished Task has exceeded its TTL.
// It returns (true, 0) if the Task should be deleted now, or (false, duration)
// if the Task should be requeued after the given duration.
func (r *TaskReconciler) ttlExpired(task *axonv1alpha1.Task) (bool, time.Duration) {
	if task.Spec.TTLSecondsAfterFinished == nil {
		return false, 0
	}
	if task.Status.Phase != axonv1alpha1.TaskPhaseSucceeded && task.Status.Phase != axonv1alpha1.TaskPhaseFailed {
		return false, 0
	}
	if task.Status.CompletionTime == nil {
		return false, 0
	}

	ttl := time.Duration(*task.Spec.TTLSecondsAfterFinished) * time.Second
	expireAt := task.Status.CompletionTime.Add(ttl)
	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return true, 0
	}
	return false, remaining
}

// SetupWithManager sets up the controller with the Manager.
func (r *TaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&axonv1alpha1.Task{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
