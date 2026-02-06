package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

const (
	taskSpawnerFinalizer = "axon.io/taskspawner-finalizer"
)

// TaskSpawnerReconciler reconciles a TaskSpawner object.
type TaskSpawnerReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	DeploymentBuilder *DeploymentBuilder
}

// +kubebuilder:rbac:groups=axon.io,resources=taskspawners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=axon.io,resources=taskspawners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=axon.io,resources=taskspawners/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create

// Reconcile handles TaskSpawner reconciliation.
func (r *TaskSpawnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ts axonv1alpha1.TaskSpawner
	if err := r.Get(ctx, req.NamespacedName, &ts); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch TaskSpawner")
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

	// Resolve workspace for GitHub Issues source
	var workspace *axonv1alpha1.WorkspaceSpec
	if gh := ts.Spec.When.GitHubIssues; gh != nil && gh.WorkspaceRef != nil {
		var ws axonv1alpha1.Workspace
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: ts.Namespace,
			Name:      gh.WorkspaceRef.Name,
		}, &ws); err != nil {
			logger.Error(err, "Unable to fetch Workspace for TaskSpawner", "workspace", gh.WorkspaceRef.Name)
			if apierrors.IsNotFound(err) {
				ts.Status.Phase = axonv1alpha1.TaskSpawnerPhaseFailed
				ts.Status.Message = fmt.Sprintf("Workspace %q not found", gh.WorkspaceRef.Name)
				if updateErr := r.Status().Update(ctx, &ts); updateErr != nil {
					logger.Error(updateErr, "Unable to update TaskSpawner status")
					return ctrl.Result{}, updateErr
				}
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		workspace = &ws.Spec
	}

	// Create Deployment if it doesn't exist
	if !deployExists {
		return r.createDeployment(ctx, &ts, workspace)
	}

	// Update Deployment if spec changed
	if err := r.updateDeployment(ctx, &ts, &deploy, workspace); err != nil {
		logger.Error(err, "unable to update Deployment")
		return ctrl.Result{}, err
	}

	// Update status with deployment name if not set
	if ts.Status.DeploymentName != deploy.Name {
		ts.Status.DeploymentName = deploy.Name
		if ts.Status.Phase == "" {
			ts.Status.Phase = axonv1alpha1.TaskSpawnerPhasePending
		}
		if err := r.Status().Update(ctx, &ts); err != nil {
			logger.Error(err, "unable to update TaskSpawner status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles TaskSpawner deletion.
func (r *TaskSpawnerReconciler) handleDeletion(ctx context.Context, ts *axonv1alpha1.TaskSpawner) (ctrl.Result, error) {
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
func (r *TaskSpawnerReconciler) createDeployment(ctx context.Context, ts *axonv1alpha1.TaskSpawner, workspace *axonv1alpha1.WorkspaceSpec) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	deploy := r.DeploymentBuilder.Build(ts, workspace)

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

	// Update status
	ts.Status.Phase = axonv1alpha1.TaskSpawnerPhasePending
	ts.Status.DeploymentName = deploy.Name
	if err := r.Status().Update(ctx, ts); err != nil {
		logger.Error(err, "unable to update TaskSpawner status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// updateDeployment updates the Deployment to match the desired spec if it has drifted.
func (r *TaskSpawnerReconciler) updateDeployment(ctx context.Context, ts *axonv1alpha1.TaskSpawner, deploy *appsv1.Deployment, workspace *axonv1alpha1.WorkspaceSpec) error {
	logger := log.FromContext(ctx)

	desired := r.DeploymentBuilder.Build(ts, workspace)

	// Compare container spec (image, args, env)
	if len(deploy.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	current := deploy.Spec.Template.Spec.Containers[0]
	target := desired.Spec.Template.Spec.Containers[0]

	needsUpdate := current.Image != target.Image ||
		!equalStringSlices(current.Args, target.Args) ||
		!equalEnvVars(current.Env, target.Env)

	if !needsUpdate {
		return nil
	}

	deploy.Spec.Template.Spec.Containers[0].Image = target.Image
	deploy.Spec.Template.Spec.Containers[0].Args = target.Args
	deploy.Spec.Template.Spec.Containers[0].Env = target.Env

	if err := r.Update(ctx, deploy); err != nil {
		return err
	}

	logger.Info("updated Deployment", "deployment", deploy.Name)
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

// SetupWithManager sets up the controller with the Manager.
func (r *TaskSpawnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&axonv1alpha1.TaskSpawner{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
