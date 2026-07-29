package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	taskSpawnerFinalizer = "kelos.dev/taskspawner-finalizer"
)

// TaskSpawnerReconciler reconciles a TaskSpawner object.
type TaskSpawnerReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	DeploymentBuilder *DeploymentBuilder
	Recorder          record.EventRecorder
}

// +kubebuilder:rbac:groups=kelos.dev,resources=taskspawners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kelos.dev,resources=taskspawners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskspawners/finalizers,verbs=update
// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=workerpools,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// isCronBased returns true if the TaskSpawner uses a cron schedule.
func isCronBased(ts *kelos.TaskSpawner) bool {
	return ts.Spec.When.Cron != nil
}

// isWebhookBased returns true if the TaskSpawner is webhook-driven.
// Slack uses Socket Mode (outbound WebSocket) handled by the centralized
// kelos-slack-server, so it follows the same no-deployment pattern.
func isWebhookBased(ts *kelos.TaskSpawner) bool {
	return ts.Spec.When.GitHubWebhook != nil || ts.Spec.When.LinearWebhook != nil || ts.Spec.When.GenericWebhook != nil || ts.Spec.When.Slack != nil
}

// Reconcile handles TaskSpawner reconciliation.
func (r *TaskSpawnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	ctx, span := startReconcileSpan(ctx, "TaskSpawner", req.NamespacedName)
	defer span.End()

	var ts kelos.TaskSpawner
	if err := r.Get(ctx, req.NamespacedName, &ts); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch TaskSpawner")
		reconcileErrorsTotal.WithLabelValues("taskspawner").Inc()
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !ts.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &ts)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&ts, taskSpawnerFinalizer) {
		controllerutil.AddFinalizer(&ts, taskSpawnerFinalizer)
		if err := r.Update(ctx, &ts); err != nil {
			logger.Error(err, "unable to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Ensure ServiceAccount and RoleBinding exist in the namespace
	if err := r.ensureSpawnerRBAC(ctx, ts.Namespace); err != nil {
		logger.Error(err, "unable to ensure spawner RBAC")
		return ctrl.Result{}, err
	}

	isSuspended := ts.Spec.Suspend != nil && *ts.Spec.Suspend

	// Cron-based TaskSpawners use a CronJob instead of a Deployment.
	if isCronBased(&ts) {
		return r.reconcileCronJob(ctx, req, &ts, isSuspended)
	}

	// Webhook-based TaskSpawners don't need deployments or cronjobs.
	if isWebhookBased(&ts) {
		return r.reconcileWebhook(ctx, req, &ts, isSuspended)
	}

	return r.reconcileDeployment(ctx, req, &ts, isSuspended)
}

// reconcileWebhook handles webhook-based TaskSpawners by cleaning up any stale resources.
func (r *TaskSpawnerReconciler) reconcileWebhook(ctx context.Context, req ctrl.Request, ts *kelos.TaskSpawner, isSuspended bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Clean up any stale Deployment or CronJob from previous configurations
	if err := r.deleteStaleResource(ctx, req.NamespacedName, &appsv1.Deployment{}, "Deployment"); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deleteStaleResource(ctx, req.NamespacedName, &batchv1.CronJob{}, "CronJob"); err != nil {
		return ctrl.Result{}, err
	}

	// Determine the desired phase for webhook TaskSpawners
	desiredPhase := kelos.TaskSpawnerPhaseRunning
	desiredMessage := "Webhook-driven TaskSpawner ready"
	if isSuspended {
		desiredPhase = kelos.TaskSpawnerPhaseSuspended
		desiredMessage = "Suspended by user"
	}

	// Count active tasks for this webhook TaskSpawner
	activeTasks, err := r.countActiveTasks(ctx, req.NamespacedName)
	if err != nil {
		logger.Error(err, "Failed to count active tasks")
		return ctrl.Result{}, err
	}

	// Update status to clear any deployment/cronjob names and set appropriate phase
	needsStatusUpdate := ts.Status.DeploymentName != "" || ts.Status.CronJobName != "" ||
		ts.Status.Phase != desiredPhase || ts.Status.ActiveTasks != activeTasks
	if needsStatusUpdate {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, req.NamespacedName, ts); getErr != nil {
				return getErr
			}
			ts.Status.DeploymentName = ""
			ts.Status.CronJobName = ""
			ts.Status.Phase = desiredPhase
			ts.Status.Message = desiredMessage
			ts.Status.ActiveTasks = activeTasks
			return r.Status().Update(ctx, ts)
		}); err != nil {
			logger.Error(err, "Unable to update TaskSpawner status")
			return ctrl.Result{}, err
		}
		logger.Info("Updated webhook TaskSpawner status", "phase", desiredPhase, "activeTasks", activeTasks)
	}

	return ctrl.Result{}, nil
}

// countActiveTasks counts the number of active (non-terminal) Tasks for a TaskSpawner.
func (r *TaskSpawnerReconciler) countActiveTasks(ctx context.Context, taskSpawnerName types.NamespacedName) (int, error) {
	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList,
		client.InNamespace(taskSpawnerName.Namespace),
		client.MatchingLabels{"kelos.dev/taskspawner": taskSpawnerName.Name},
	); err != nil {
		return 0, fmt.Errorf("listing tasks for TaskSpawner %s: %w", taskSpawnerName.Name, err)
	}

	activeTasks := 0
	for i := range taskList.Items {
		task := &taskList.Items[i]
		// Count tasks that are not in terminal phases
		if task.Status.Phase != kelos.TaskPhaseSucceeded && task.Status.Phase != kelos.TaskPhaseFailed {
			activeTasks++
		}
	}

	return activeTasks, nil
}

func (r *TaskSpawnerReconciler) resolveTaskSpawnerWorkspace(ctx context.Context, ts *kelos.TaskSpawner) (*kelos.WorkspaceSpec, *kelos.WorkspaceReference, bool, ctrl.Result, error) {
	logger := log.FromContext(ctx)

	workspaceRef, result, err := r.resolveTaskSpawnerWorkspaceRef(ctx, ts)
	if err != nil || result != (ctrl.Result{}) || workspaceRef == nil {
		return nil, workspaceRef, false, result, err
	}

	var ws kelos.Workspace
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: ts.Namespace,
		Name:      workspaceRef.Name,
	}, &ws); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Workspace not found yet, requeuing", "workspace", workspaceRef.Name)
			r.recordEvent(ts, corev1.EventTypeNormal, "WorkspaceNotFound", "Workspace %s not found, requeuing", workspaceRef.Name)
			return nil, workspaceRef, false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		logger.Error(err, "Unable to fetch Workspace for TaskSpawner", "workspace", workspaceRef.Name)
		return nil, workspaceRef, false, ctrl.Result{}, err
	}

	workspace := &ws.Spec
	isGitHubApp := false
	if workspace.SecretRef != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: ts.Namespace,
			Name:      workspace.SecretRef.Name,
		}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Workspace secret not found yet, requeuing", "workspace", workspaceRef.Name, "secret", workspace.SecretRef.Name)
				r.recordEvent(ts, corev1.EventTypeNormal, "WorkspaceSecretNotFound", "Workspace secret %s not found, requeuing", workspace.SecretRef.Name)
				return nil, workspaceRef, false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			logger.Error(err, "Unable to fetch workspace secret", "secret", workspace.SecretRef.Name)
			return nil, workspaceRef, false, ctrl.Result{}, err
		} else {
			isGitHubApp = githubapp.IsGitHubApp(secret.Data)
			if isGitHubApp {
				logger.Info("Detected GitHub App secret for TaskSpawner", "secret", workspace.SecretRef.Name)
			}
		}
	}

	return workspace, workspaceRef, isGitHubApp, ctrl.Result{}, nil
}

func (r *TaskSpawnerReconciler) resolveTaskSpawnerWorkspaceRef(ctx context.Context, ts *kelos.TaskSpawner) (*kelos.WorkspaceReference, ctrl.Result, error) {
	logger := log.FromContext(ctx)
	template := ts.Spec.TaskTemplate
	if template.WorkspaceRef != nil {
		return template.WorkspaceRef, ctrl.Result{}, nil
	}
	if template.Worker != nil && template.Worker.WorkspaceRef != nil {
		return template.Worker.WorkspaceRef, ctrl.Result{}, nil
	}
	if template.WorkerPoolRef == nil {
		return nil, ctrl.Result{}, nil
	}

	var pool kelos.WorkerPool
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: ts.Namespace,
		Name:      template.WorkerPoolRef.Name,
	}, &pool); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("WorkerPool not found yet, requeuing", "workerpool", template.WorkerPoolRef.Name)
			r.recordEvent(ts, corev1.EventTypeNormal, "WorkerPoolNotFound", "WorkerPool %s not found, requeuing", template.WorkerPoolRef.Name)
			return nil, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		logger.Error(err, "Unable to fetch WorkerPool for TaskSpawner", "workerpool", template.WorkerPoolRef.Name)
		return nil, ctrl.Result{}, err
	}

	return pool.Spec.Worker.WorkspaceRef, ctrl.Result{}, nil
}

func taskSpawnerWithEffectiveWorkspaceRef(ts *kelos.TaskSpawner, workspaceRef *kelos.WorkspaceReference) *kelos.TaskSpawner {
	if workspaceRef == nil || ts.Spec.TaskTemplate.WorkspaceRef != nil {
		return ts
	}

	copy := ts.DeepCopy()
	copy.Spec.TaskTemplate.WorkspaceRef = &kelos.WorkspaceReference{Name: workspaceRef.Name}
	return copy
}

// reconcileDeployment handles the Deployment lifecycle for polling-based TaskSpawners.
func (r *TaskSpawnerReconciler) reconcileDeployment(ctx context.Context, req ctrl.Request, ts *kelos.TaskSpawner, isSuspended bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Clean up any existing CronJob from a previous cron-based configuration.
	if err := r.deleteStaleResource(ctx, req.NamespacedName, &batchv1.CronJob{}, "CronJob"); err != nil {
		return ctrl.Result{}, err
	}

	// Check if Deployment already exists
	var deploy appsv1.Deployment
	deployExists := true
	if err := r.Get(ctx, req.NamespacedName, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			deployExists = false
		} else {
			logger.Error(err, "unable to fetch Deployment")
			return ctrl.Result{}, err
		}
	}

	// FIXME: Remove this migration block after a couple of releases.
	// Delete Deployments with old app.kubernetes.io/* selector labels so they
	// get recreated with the new kelos.dev/* labels. spec.selector.matchLabels
	// is immutable, so an in-place update is not possible.
	if deployExists {
		if _, ok := deploy.Spec.Selector.MatchLabels["app.kubernetes.io/component"]; ok {
			logger.Info("Deleting Deployment with stale app.kubernetes.io/* selector labels for migration", "deployment", deploy.Name)
			if err := r.Delete(ctx, &deploy); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Unable to delete Deployment with stale selector labels", "deployment", deploy.Name)
				return ctrl.Result{}, err
			}
			r.recordEvent(ts, corev1.EventTypeNormal, "DeploymentMigrated", "Deleted Deployment %s with old app.kubernetes.io/* labels; it will be recreated with kelos.dev/* labels", deploy.Name)
			deployExists = false
		}
	}

	workspace, workspaceRef, isGitHubApp, result, err := r.resolveTaskSpawnerWorkspace(ctx, ts)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result != (ctrl.Result{}) {
		return result, nil
	}
	buildTS := taskSpawnerWithEffectiveWorkspaceRef(ts, workspaceRef)
	if workspaceUsesGHProxy(workspace) {
		if err := validateWorkspaceGHProxyRepoOverride(ts, workspace); err != nil {
			if deployExists {
				if deleteErr := r.Delete(ctx, &deploy); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
					logger.Error(deleteErr, "Unable to delete Deployment for invalid repo override", "deployment", deploy.Name)
					return ctrl.Result{}, deleteErr
				}
				deployExists = false
			}
			r.recordEvent(ts, corev1.EventTypeWarning, "InvalidGitHubRepoOverride", "%s", err.Error())
			if statusErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, req.NamespacedName, ts); getErr != nil {
					return getErr
				}
				ts.Status.Phase = kelos.TaskSpawnerPhaseFailed
				ts.Status.Message = err.Error()
				ts.Status.DeploymentName = ""
				ts.Status.CronJobName = ""
				return r.Status().Update(ctx, ts)
			}); statusErr != nil {
				logger.Error(statusErr, "Unable to update TaskSpawner status for invalid repo override")
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{}, nil
		}
	}

	// Determine desired replica count based on suspend state
	desiredReplicas := int32(1)
	if isSuspended {
		desiredReplicas = 0
	}

	// Create Deployment if it doesn't exist
	if !deployExists {
		return r.createDeployment(ctx, buildTS, workspace, isGitHubApp, desiredReplicas)
	}

	// Update Deployment if spec changed
	if err := r.updateDeployment(ctx, buildTS, &deploy, workspace, isGitHubApp, desiredReplicas); err != nil {
		logger.Error(err, "unable to update Deployment")
		return ctrl.Result{}, err
	}

	// Determine the desired phase based on current state
	desiredPhase := ts.Status.Phase
	if isSuspended && ts.Status.Phase != kelos.TaskSpawnerPhaseSuspended {
		desiredPhase = kelos.TaskSpawnerPhaseSuspended
	} else if !isSuspended && ts.Status.Phase == kelos.TaskSpawnerPhaseSuspended {
		desiredPhase = kelos.TaskSpawnerPhaseRunning
	}

	// Update status with deployment name or phase if needed
	needsStatusUpdate := ts.Status.DeploymentName != deploy.Name || ts.Status.CronJobName != "" || ts.Status.Phase != desiredPhase
	if needsStatusUpdate {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, req.NamespacedName, ts); getErr != nil {
				return getErr
			}
			ts.Status.DeploymentName = deploy.Name
			ts.Status.CronJobName = ""
			if isSuspended {
				ts.Status.Phase = kelos.TaskSpawnerPhaseSuspended
				ts.Status.Message = "Suspended by user"
			} else if ts.Status.Phase == kelos.TaskSpawnerPhaseSuspended {
				ts.Status.Phase = kelos.TaskSpawnerPhaseRunning
				ts.Status.Message = "Resumed"
			} else if ts.Status.Phase == "" {
				ts.Status.Phase = kelos.TaskSpawnerPhasePending
			}
			return r.Status().Update(ctx, ts)
		}); err != nil {
			logger.Error(err, "Unable to update TaskSpawner status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// reconcileCronJob handles the CronJob lifecycle for cron-based TaskSpawners.
func (r *TaskSpawnerReconciler) reconcileCronJob(ctx context.Context, req ctrl.Request, ts *kelos.TaskSpawner, isSuspended bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Clean up any existing Deployment from a previous polling-based configuration.
	if err := r.deleteStaleResource(ctx, req.NamespacedName, &appsv1.Deployment{}, "Deployment"); err != nil {
		return ctrl.Result{}, err
	}

	var cronJob batchv1.CronJob
	cronJobExists := true
	if err := r.Get(ctx, req.NamespacedName, &cronJob); err != nil {
		if apierrors.IsNotFound(err) {
			cronJobExists = false
		} else {
			logger.Error(err, "Unable to fetch CronJob")
			return ctrl.Result{}, err
		}
	}

	workspace, workspaceRef, isGitHubApp, result, err := r.resolveTaskSpawnerWorkspace(ctx, ts)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result != (ctrl.Result{}) {
		return result, nil
	}
	buildTS := taskSpawnerWithEffectiveWorkspaceRef(ts, workspaceRef)
	if workspaceUsesGHProxy(workspace) {
		if err := validateWorkspaceGHProxyRepoOverride(ts, workspace); err != nil {
			if cronJobExists {
				if deleteErr := r.Delete(ctx, &cronJob); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
					logger.Error(deleteErr, "Unable to delete CronJob for invalid repo override", "cronJob", cronJob.Name)
					return ctrl.Result{}, deleteErr
				}
				cronJobExists = false
			}
			r.recordEvent(ts, corev1.EventTypeWarning, "InvalidGitHubRepoOverride", "%s", err.Error())
			if statusErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if getErr := r.Get(ctx, req.NamespacedName, ts); getErr != nil {
					return getErr
				}
				ts.Status.Phase = kelos.TaskSpawnerPhaseFailed
				ts.Status.Message = err.Error()
				ts.Status.DeploymentName = ""
				ts.Status.CronJobName = ""
				return r.Status().Update(ctx, ts)
			}); statusErr != nil {
				logger.Error(statusErr, "Unable to update TaskSpawner status for invalid repo override")
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{}, nil
		}
	}

	if !cronJobExists {
		return r.createCronJob(ctx, buildTS, workspace, isGitHubApp, isSuspended)
	}

	if err := r.updateCronJob(ctx, buildTS, &cronJob, workspace, isGitHubApp, isSuspended); err != nil {
		logger.Error(err, "Unable to update CronJob")
		return ctrl.Result{}, err
	}

	// Determine the desired phase based on current state.
	// CronJobs are considered Running once they exist and are not suspended.
	desiredPhase := ts.Status.Phase
	if isSuspended {
		desiredPhase = kelos.TaskSpawnerPhaseSuspended
	} else if ts.Status.Phase != kelos.TaskSpawnerPhaseRunning {
		desiredPhase = kelos.TaskSpawnerPhaseRunning
	}

	needsStatusUpdate := ts.Status.CronJobName != cronJob.Name || ts.Status.DeploymentName != "" || ts.Status.Phase != desiredPhase
	if needsStatusUpdate {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, req.NamespacedName, ts); getErr != nil {
				return getErr
			}
			ts.Status.CronJobName = cronJob.Name
			ts.Status.DeploymentName = ""
			if isSuspended {
				ts.Status.Phase = kelos.TaskSpawnerPhaseSuspended
				ts.Status.Message = "Suspended by user"
			} else {
				ts.Status.Phase = kelos.TaskSpawnerPhaseRunning
				ts.Status.Message = ""
			}
			return r.Status().Update(ctx, ts)
		}); err != nil {
			logger.Error(err, "Unable to update TaskSpawner status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles TaskSpawner deletion.
func (r *TaskSpawnerReconciler) handleDeletion(ctx context.Context, ts *kelos.TaskSpawner) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(ts, taskSpawnerFinalizer) {
		// The Deployment or CronJob will be garbage collected via owner reference,
		// but we remove the finalizer to allow the TaskSpawner to be deleted.
		controllerutil.RemoveFinalizer(ts, taskSpawnerFinalizer)
		if err := r.Update(ctx, ts); err != nil {
			logger.Error(err, "unable to remove finalizer")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// createDeployment creates a Deployment for the TaskSpawner.
func (r *TaskSpawnerReconciler) createDeployment(ctx context.Context, ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool, replicas int32) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	deploy := r.DeploymentBuilder.Build(ts, workspace, isGitHubApp)
	deploy.Spec.Replicas = &replicas

	// Set owner reference
	if err := controllerutil.SetControllerReference(ts, deploy, r.Scheme); err != nil {
		logger.Error(err, "unable to set owner reference")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, deploy); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "unable to create Deployment")
		return ctrl.Result{}, err
	}

	logger.Info("created Deployment", "deployment", deploy.Name)
	r.recordEvent(ts, corev1.EventTypeNormal, "DeploymentCreated", "Created spawner Deployment %s", deploy.Name)

	// Update status
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(ts), ts); getErr != nil {
			return getErr
		}
		if replicas == 0 {
			ts.Status.Phase = kelos.TaskSpawnerPhaseSuspended
			ts.Status.Message = "Suspended by user"
		} else {
			ts.Status.Phase = kelos.TaskSpawnerPhasePending
		}
		ts.Status.DeploymentName = deploy.Name
		ts.Status.CronJobName = ""
		return r.Status().Update(ctx, ts)
	}); err != nil {
		logger.Error(err, "Unable to update TaskSpawner status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// updateDeployment updates the Deployment to match the desired spec if it has drifted.
func (r *TaskSpawnerReconciler) updateDeployment(ctx context.Context, ts *kelos.TaskSpawner, deploy *appsv1.Deployment, workspace *kelos.WorkspaceSpec, isGitHubApp bool, desiredReplicas int32) error {
	logger := log.FromContext(ctx)

	desired := r.DeploymentBuilder.Build(ts, workspace, isGitHubApp)

	needsUpdate := false

	// Compare main container spec (image, args, env, volumeMounts)
	if len(deploy.Spec.Template.Spec.Containers) > 0 {
		current := deploy.Spec.Template.Spec.Containers[0]
		target := desired.Spec.Template.Spec.Containers[0]

		if current.Image != target.Image ||
			!equalStringSlices(current.Args, target.Args) ||
			!equalEnvVars(current.Env, target.Env) ||
			!reflect.DeepEqual(current.VolumeMounts, target.VolumeMounts) ||
			!reflect.DeepEqual(current.Ports, target.Ports) ||
			!resourceRequirementsEqual(current.Resources, target.Resources) {
			deploy.Spec.Template.Spec.Containers[0].Image = target.Image
			deploy.Spec.Template.Spec.Containers[0].Args = target.Args
			deploy.Spec.Template.Spec.Containers[0].Env = target.Env
			deploy.Spec.Template.Spec.Containers[0].VolumeMounts = target.VolumeMounts
			deploy.Spec.Template.Spec.Containers[0].Ports = target.Ports
			deploy.Spec.Template.Spec.Containers[0].Resources = target.Resources
			needsUpdate = true
		}
	}

	// Clean up stale init containers and volumes (e.g. from removed token-refresher sidecar)
	if !reflect.DeepEqual(deploy.Spec.Template.Spec.InitContainers, desired.Spec.Template.Spec.InitContainers) {
		deploy.Spec.Template.Spec.InitContainers = desired.Spec.Template.Spec.InitContainers
		needsUpdate = true
	}
	if !reflect.DeepEqual(deploy.Spec.Template.Spec.Volumes, desired.Spec.Template.Spec.Volumes) {
		deploy.Spec.Template.Spec.Volumes = desired.Spec.Template.Spec.Volumes
		needsUpdate = true
	}

	// Check replica count for suspend/resume
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != desiredReplicas {
		deploy.Spec.Replicas = &desiredReplicas
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	if err := r.Update(ctx, deploy); err != nil {
		return err
	}

	logger.Info("Updated Deployment", "deployment", deploy.Name, "replicas", desiredReplicas)
	r.recordEvent(ts, corev1.EventTypeNormal, "DeploymentUpdated", "Updated spawner Deployment %s", deploy.Name)
	return nil
}

// createCronJob creates a CronJob for a cron-based TaskSpawner.
func (r *TaskSpawnerReconciler) createCronJob(ctx context.Context, ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool, isSuspended bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cronJob := r.DeploymentBuilder.BuildCronJob(ts, workspace, isGitHubApp)
	cronJob.Spec.Suspend = &isSuspended

	// Set owner reference
	if err := controllerutil.SetControllerReference(ts, cronJob, r.Scheme); err != nil {
		logger.Error(err, "Unable to set owner reference on CronJob")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, cronJob); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Unable to create CronJob")
		return ctrl.Result{}, err
	}

	logger.Info("Created CronJob", "cronJob", cronJob.Name, "schedule", ts.Spec.When.Cron.Schedule)
	r.recordEvent(ts, corev1.EventTypeNormal, "CronJobCreated", "Created spawner CronJob %s with schedule %s", cronJob.Name, ts.Spec.When.Cron.Schedule)

	// Update status
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(ts), ts); getErr != nil {
			return getErr
		}
		if isSuspended {
			ts.Status.Phase = kelos.TaskSpawnerPhaseSuspended
			ts.Status.Message = "Suspended by user"
		} else {
			ts.Status.Phase = kelos.TaskSpawnerPhaseRunning
		}
		ts.Status.CronJobName = cronJob.Name
		ts.Status.DeploymentName = ""
		return r.Status().Update(ctx, ts)
	}); err != nil {
		logger.Error(err, "Unable to update TaskSpawner status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// updateCronJob updates the CronJob if the schedule or suspend state changed.
func (r *TaskSpawnerReconciler) updateCronJob(ctx context.Context, ts *kelos.TaskSpawner, cronJob *batchv1.CronJob, workspace *kelos.WorkspaceSpec, isGitHubApp bool, isSuspended bool) error {
	logger := log.FromContext(ctx)

	desired := r.DeploymentBuilder.BuildCronJob(ts, workspace, isGitHubApp)
	needsUpdate := false

	if cronJob.Spec.Schedule != desired.Spec.Schedule {
		cronJob.Spec.Schedule = desired.Spec.Schedule
		needsUpdate = true
	}

	if cronJob.Spec.Suspend == nil || *cronJob.Spec.Suspend != isSuspended {
		cronJob.Spec.Suspend = &isSuspended
		needsUpdate = true
	}

	currentPodSpec := &cronJob.Spec.JobTemplate.Spec.Template.Spec
	desiredPodSpec := &desired.Spec.JobTemplate.Spec.Template.Spec

	// Update container spec if changed (image, args, env, volumeMounts)
	if len(currentPodSpec.Containers) > 0 {
		current := currentPodSpec.Containers[0]
		target := desiredPodSpec.Containers[0]

		if current.Image != target.Image ||
			!equalStringSlices(current.Args, target.Args) ||
			!equalEnvVars(current.Env, target.Env) ||
			!reflect.DeepEqual(current.VolumeMounts, target.VolumeMounts) ||
			!resourceRequirementsEqual(current.Resources, target.Resources) {
			currentPodSpec.Containers[0].Image = target.Image
			currentPodSpec.Containers[0].Args = target.Args
			currentPodSpec.Containers[0].Env = target.Env
			currentPodSpec.Containers[0].VolumeMounts = target.VolumeMounts
			currentPodSpec.Containers[0].Resources = target.Resources
			needsUpdate = true
		}
	}

	// Clean up stale init containers and volumes (e.g. from removed token-refresher sidecar)
	if !reflect.DeepEqual(currentPodSpec.InitContainers, desiredPodSpec.InitContainers) {
		currentPodSpec.InitContainers = desiredPodSpec.InitContainers
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.Volumes, desiredPodSpec.Volumes) {
		currentPodSpec.Volumes = desiredPodSpec.Volumes
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	if err := r.Update(ctx, cronJob); err != nil {
		return err
	}

	logger.Info("Updated CronJob", "cronJob", cronJob.Name, "schedule", cronJob.Spec.Schedule, "suspended", isSuspended)
	r.recordEvent(ts, corev1.EventTypeNormal, "CronJobUpdated", "Updated spawner CronJob %s", cronJob.Name)
	return nil
}

// deleteStaleResource deletes a resource by NamespacedName if it exists and is owned by a TaskSpawner.
// This is used to clean up the old resource type when switching between
// Deployment-based, CronJob-based, and webhook-based TaskSpawners.
func (r *TaskSpawnerReconciler) deleteStaleResource(ctx context.Context, key types.NamespacedName, obj client.Object, kind string) error {
	logger := log.FromContext(ctx)

	if err := r.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Only delete if this resource is owned by a TaskSpawner to avoid deleting unrelated resources
	ownerRefs := obj.GetOwnerReferences()
	isOwnedByTaskSpawner := false
	for _, ref := range ownerRefs {
		if (ref.APIVersion == "kelos.dev/v1alpha1" || ref.APIVersion == "kelos.dev/v1alpha2") && ref.Kind == "TaskSpawner" && ref.Controller != nil && *ref.Controller {
			isOwnedByTaskSpawner = true
			break
		}
	}

	if !isOwnedByTaskSpawner {
		logger.Info("Skipping deletion of "+kind+" not owned by TaskSpawner", "name", key.Name)
		return nil
	}

	if err := r.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		logger.Error(err, "Unable to delete stale "+kind, "name", key.Name)
		return err
	}

	logger.Info("Deleted stale "+kind+" after switching TaskSpawner type", "name", key.Name)
	return nil
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalEnvVars(a, b []corev1.EnvVar) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
		if a[i].Value != b[i].Value {
			return false
		}
		if (a[i].ValueFrom == nil) != (b[i].ValueFrom == nil) {
			return false
		}
		if a[i].ValueFrom != nil && b[i].ValueFrom != nil {
			if (a[i].ValueFrom.SecretKeyRef == nil) != (b[i].ValueFrom.SecretKeyRef == nil) {
				return false
			}
			if a[i].ValueFrom.SecretKeyRef != nil && b[i].ValueFrom.SecretKeyRef != nil {
				if a[i].ValueFrom.SecretKeyRef.Name != b[i].ValueFrom.SecretKeyRef.Name ||
					a[i].ValueFrom.SecretKeyRef.Key != b[i].ValueFrom.SecretKeyRef.Key {
					return false
				}
			}
		}
	}
	return true
}

// ensureSpawnerRBAC ensures a ServiceAccount and RoleBinding exist in the namespace.
func (r *TaskSpawnerReconciler) ensureSpawnerRBAC(ctx context.Context, namespace string) error {
	logger := log.FromContext(ctx)

	// Ensure ServiceAccount
	var sa corev1.ServiceAccount
	if err := r.Get(ctx, types.NamespacedName{Name: SpawnerServiceAccount, Namespace: namespace}, &sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		sa = corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SpawnerServiceAccount,
				Namespace: namespace,
			},
		}
		if err := r.Create(ctx, &sa); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		} else {
			logger.Info("created ServiceAccount", "namespace", namespace, "name", SpawnerServiceAccount)
		}
	}

	// Ensure RoleBinding
	rbName := SpawnerServiceAccount
	var rb rbacv1.RoleBinding
	if err := r.Get(ctx, types.NamespacedName{Name: rbName, Namespace: namespace}, &rb); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		rb = rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbName,
				Namespace: namespace,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     SpawnerClusterRole,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      SpawnerServiceAccount,
					Namespace: namespace,
				},
			},
		}
		if err := r.Create(ctx, &rb); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		} else {
			logger.Info("created RoleBinding", "namespace", namespace, "name", rbName)
		}
	}

	return nil
}

// recordEvent records a Kubernetes Event on the given object if a Recorder is configured.
func (r *TaskSpawnerReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *TaskSpawnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.TaskSpawner{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.CronJob{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findTaskSpawnersForSecret)).
		Watches(&kelos.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.findTaskSpawnersForWorkspace)).
		Watches(&kelos.WorkerPool{}, handler.EnqueueRequestsFromMapFunc(r.findTaskSpawnersForWorkerPool)).
		Watches(&kelos.Task{}, handler.EnqueueRequestsFromMapFunc(r.findTaskSpawnersForTask),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				_, hasLabel := obj.GetLabels()["kelos.dev/taskspawner"]
				return hasLabel
			}))).
		Complete(r)
}

// findTaskSpawnersForSecret maps a Secret change to the TaskSpawners that
// reference it via their Workspace's secretRef.
func (r *TaskSpawnerReconciler) findTaskSpawnersForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	// Find workspaces that reference this secret
	var workspaceList kelos.WorkspaceList
	if err := r.List(ctx, &workspaceList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	var workspaceNames []string
	for _, ws := range workspaceList.Items {
		if ws.Spec.SecretRef != nil && ws.Spec.SecretRef.Name == secret.Name {
			workspaceNames = append(workspaceNames, ws.Name)
		}
	}
	if len(workspaceNames) == 0 {
		return nil
	}

	// Find task spawners that reference those workspaces
	var tsList kelos.TaskSpawnerList
	if err := r.List(ctx, &tsList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	wsNameSet := make(map[string]bool, len(workspaceNames))
	for _, name := range workspaceNames {
		wsNameSet[name] = true
	}

	poolWorkspaceNames := workerPoolWorkspaceNames(ctx, r.Client, secret.Namespace)
	var requests []reconcile.Request
	for _, ts := range tsList.Items {
		if wsNameSet[taskSpawnerEffectiveWorkspaceName(&ts, poolWorkspaceNames)] {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: ts.Namespace,
					Name:      ts.Name,
				},
			})
		}
	}
	return requests
}

// findTaskSpawnersForWorkspace maps a Workspace change to the TaskSpawners
// that reference it directly or through a WorkerPool.
func (r *TaskSpawnerReconciler) findTaskSpawnersForWorkspace(ctx context.Context, obj client.Object) []reconcile.Request {
	ws, ok := obj.(*kelos.Workspace)
	if !ok {
		return nil
	}

	var tsList kelos.TaskSpawnerList
	if err := r.List(ctx, &tsList, client.InNamespace(ws.Namespace)); err != nil {
		return nil
	}

	poolWorkspaceNames := workerPoolWorkspaceNames(ctx, r.Client, ws.Namespace)
	var requests []reconcile.Request
	for _, ts := range tsList.Items {
		if taskSpawnerEffectiveWorkspaceName(&ts, poolWorkspaceNames) == ws.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: ts.Namespace,
					Name:      ts.Name,
				},
			})
		}
	}
	return requests
}

func (r *TaskSpawnerReconciler) findTaskSpawnersForWorkerPool(ctx context.Context, obj client.Object) []reconcile.Request {
	pool, ok := obj.(*kelos.WorkerPool)
	if !ok {
		return nil
	}

	var tsList kelos.TaskSpawnerList
	if err := r.List(ctx, &tsList, client.InNamespace(pool.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, ts := range tsList.Items {
		if ts.Spec.TaskTemplate.WorkerPoolRef != nil && ts.Spec.TaskTemplate.WorkerPoolRef.Name == pool.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: ts.Namespace,
					Name:      ts.Name,
				},
			})
		}
	}
	return requests
}

func taskSpawnerEffectiveWorkspaceName(ts *kelos.TaskSpawner, poolWorkspaceNames map[string]string) string {
	template := ts.Spec.TaskTemplate
	if template.WorkspaceRef != nil {
		return template.WorkspaceRef.Name
	}
	if template.Worker != nil && template.Worker.WorkspaceRef != nil {
		return template.Worker.WorkspaceRef.Name
	}
	if template.WorkerPoolRef == nil {
		return ""
	}
	return poolWorkspaceNames[template.WorkerPoolRef.Name]
}

func workerPoolWorkspaceNames(ctx context.Context, cl client.Client, namespace string) map[string]string {
	var poolList kelos.WorkerPoolList
	if err := cl.List(ctx, &poolList, client.InNamespace(namespace)); err != nil {
		return nil
	}

	names := make(map[string]string, len(poolList.Items))
	for _, pool := range poolList.Items {
		if pool.Spec.Worker.WorkspaceRef != nil {
			names[pool.Name] = pool.Spec.Worker.WorkspaceRef.Name
		}
	}
	return names
}

// findTaskSpawnersForTask maps a Task change to the TaskSpawner that owns it.
// This allows webhook TaskSpawners to update their activeTasks count when Tasks complete/fail.
func (r *TaskSpawnerReconciler) findTaskSpawnersForTask(ctx context.Context, obj client.Object) []reconcile.Request {
	task, ok := obj.(*kelos.Task)
	if !ok {
		return nil
	}

	// Tasks have a label that identifies their TaskSpawner
	taskSpawnerName, ok := task.Labels["kelos.dev/taskspawner"]
	if !ok {
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: task.Namespace,
			Name:      taskSpawnerName,
		},
	}}
}

// resourceRequirementsEqual compares two ResourceRequirements using semantic
// equality for quantities instead of reflect.DeepEqual, which can report false
// negatives when the internal representation of equal quantities differs.
func resourceRequirementsEqual(a, b corev1.ResourceRequirements) bool {
	return reflect.DeepEqual(a.Claims, b.Claims) &&
		resourceListEqual(a.Requests, b.Requests) &&
		resourceListEqual(a.Limits, b.Limits)
}

func resourceListEqual(a, b corev1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}
	for name, aQty := range a {
		bQty, ok := b[name]
		if !ok || !aQty.Equal(bQty) {
			return false
		}
	}
	return true
}
