package controller

import (
	"context"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles TaskSpawner reconciliation.
func (r *TaskSpawnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ts kelosv1alpha1.TaskSpawner
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

	// Ensure ServiceAccount and RoleBinding exist in the namespace
	if err := r.ensureSpawnerRBAC(ctx, ts.Namespace); err != nil {
		logger.Error(err, "unable to ensure spawner RBAC")
		return ctrl.Result{}, err
	}

	// Resolve workspace if workspaceRef is set in taskTemplate
	var workspace *kelosv1alpha1.WorkspaceSpec
	var isGitHubApp bool
	if ts.Spec.TaskTemplate.WorkspaceRef != nil {
		workspaceRefName := ts.Spec.TaskTemplate.WorkspaceRef.Name
		var ws kelosv1alpha1.Workspace
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: ts.Namespace,
			Name:      workspaceRefName,
		}, &ws); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Workspace not found yet, requeuing", "workspace", workspaceRefName)
				r.recordEvent(&ts, corev1.EventTypeNormal, "WorkspaceNotFound", "Workspace %s not found, requeuing", workspaceRefName)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			logger.Error(err, "Unable to fetch Workspace for TaskSpawner", "workspace", workspaceRefName)
			return ctrl.Result{}, err
		}
		workspace = &ws.Spec

		// Detect GitHub App auth
		if workspace.SecretRef != nil {
			var secret corev1.Secret
			if err := r.Get(ctx, client.ObjectKey{
				Namespace: ts.Namespace,
				Name:      workspace.SecretRef.Name,
			}, &secret); err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "Unable to fetch workspace secret", "secret", workspace.SecretRef.Name)
					return ctrl.Result{}, err
				}
			} else {
				isGitHubApp = githubapp.IsGitHubApp(secret.Data)
				if isGitHubApp {
					logger.Info("Detected GitHub App secret for TaskSpawner", "secret", workspace.SecretRef.Name)
				}
			}
		}
	}

	// Determine desired replica count based on suspend state
	isSuspended := ts.Spec.Suspend != nil && *ts.Spec.Suspend
	desiredReplicas := int32(1)
	if isSuspended {
		desiredReplicas = 0
	}

	// Create Deployment if it doesn't exist
	if !deployExists {
		return r.createDeployment(ctx, &ts, workspace, isGitHubApp, desiredReplicas)
	}

	// Update Deployment if spec changed
	if err := r.updateDeployment(ctx, &ts, &deploy, workspace, isGitHubApp, desiredReplicas); err != nil {
		logger.Error(err, "unable to update Deployment")
		return ctrl.Result{}, err
	}

	// Determine the desired phase based on current state
	desiredPhase := ts.Status.Phase
	if isSuspended && ts.Status.Phase != kelosv1alpha1.TaskSpawnerPhaseSuspended {
		desiredPhase = kelosv1alpha1.TaskSpawnerPhaseSuspended
	} else if !isSuspended && ts.Status.Phase == kelosv1alpha1.TaskSpawnerPhaseSuspended {
		desiredPhase = kelosv1alpha1.TaskSpawnerPhaseRunning
	}

	// Update status with deployment name or phase if needed
	needsStatusUpdate := ts.Status.DeploymentName != deploy.Name || ts.Status.Phase != desiredPhase
	if needsStatusUpdate {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if getErr := r.Get(ctx, req.NamespacedName, &ts); getErr != nil {
				return getErr
			}
			ts.Status.DeploymentName = deploy.Name
			if isSuspended {
				ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhaseSuspended
				ts.Status.Message = "Suspended by user"
			} else if ts.Status.Phase == kelosv1alpha1.TaskSpawnerPhaseSuspended {
				ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhaseRunning
				ts.Status.Message = "Resumed"
			} else if ts.Status.Phase == "" {
				ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhasePending
			}
			return r.Status().Update(ctx, &ts)
		}); err != nil {
			logger.Error(err, "Unable to update TaskSpawner status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles TaskSpawner deletion.
func (r *TaskSpawnerReconciler) handleDeletion(ctx context.Context, ts *kelosv1alpha1.TaskSpawner) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(ts, taskSpawnerFinalizer) {
		// The Deployment will be garbage collected via owner reference,
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
func (r *TaskSpawnerReconciler) createDeployment(ctx context.Context, ts *kelosv1alpha1.TaskSpawner, workspace *kelosv1alpha1.WorkspaceSpec, isGitHubApp bool, replicas int32) (ctrl.Result, error) {
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
			ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhaseSuspended
			ts.Status.Message = "Suspended by user"
		} else {
			ts.Status.Phase = kelosv1alpha1.TaskSpawnerPhasePending
		}
		ts.Status.DeploymentName = deploy.Name
		return r.Status().Update(ctx, ts)
	}); err != nil {
		logger.Error(err, "Unable to update TaskSpawner status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// updateDeployment updates the Deployment to match the desired spec if it has drifted.
func (r *TaskSpawnerReconciler) updateDeployment(ctx context.Context, ts *kelosv1alpha1.TaskSpawner, deploy *appsv1.Deployment, workspace *kelosv1alpha1.WorkspaceSpec, isGitHubApp bool, desiredReplicas int32) error {
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
			!reflect.DeepEqual(current.VolumeMounts, target.VolumeMounts) {
			deploy.Spec.Template.Spec.Containers[0].Image = target.Image
			deploy.Spec.Template.Spec.Containers[0].Args = target.Args
			deploy.Spec.Template.Spec.Containers[0].Env = target.Env
			deploy.Spec.Template.Spec.Containers[0].VolumeMounts = target.VolumeMounts
			needsUpdate = true
		}
	}

	// Compare init containers (token-refresher sidecar)
	if !reflect.DeepEqual(deploy.Spec.Template.Spec.InitContainers, desired.Spec.Template.Spec.InitContainers) {
		deploy.Spec.Template.Spec.InitContainers = desired.Spec.Template.Spec.InitContainers
		needsUpdate = true
	}

	// Compare volumes (shared token emptyDir, github-app-secret)
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
		For(&kelosv1alpha1.TaskSpawner{}).
		Owns(&appsv1.Deployment{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findTaskSpawnersForSecret)).
		Watches(&kelosv1alpha1.Workspace{}, handler.EnqueueRequestsFromMapFunc(r.findTaskSpawnersForWorkspace)).
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
	var workspaceList kelosv1alpha1.WorkspaceList
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
	var tsList kelosv1alpha1.TaskSpawnerList
	if err := r.List(ctx, &tsList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	wsNameSet := make(map[string]bool, len(workspaceNames))
	for _, name := range workspaceNames {
		wsNameSet[name] = true
	}

	var requests []reconcile.Request
	for _, ts := range tsList.Items {
		if ts.Spec.TaskTemplate.WorkspaceRef != nil && wsNameSet[ts.Spec.TaskTemplate.WorkspaceRef.Name] {
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
// that reference it via taskTemplate.workspaceRef.
func (r *TaskSpawnerReconciler) findTaskSpawnersForWorkspace(ctx context.Context, obj client.Object) []reconcile.Request {
	ws, ok := obj.(*kelosv1alpha1.Workspace)
	if !ok {
		return nil
	}

	var tsList kelosv1alpha1.TaskSpawnerList
	if err := r.List(ctx, &tsList, client.InNamespace(ws.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, ts := range tsList.Items {
		if ts.Spec.TaskTemplate.WorkspaceRef != nil && ts.Spec.TaskTemplate.WorkspaceRef.Name == ws.Name {
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
