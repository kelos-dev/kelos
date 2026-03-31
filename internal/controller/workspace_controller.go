package controller

import (
	"context"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

// WorkspaceReconciler reconciles workspace-scoped ghproxy resources.
type WorkspaceReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	ProxyBuilder *WorkspaceGHProxyBuilder
	Recorder     record.EventRecorder
}

// +kubebuilder:rbac:groups=kelos.dev,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile ensures each Workspace has a ghproxy Deployment and Service.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var workspace kelosv1alpha1.Workspace
	if err := r.Get(ctx, req.NamespacedName, &workspace); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch Workspace")
		reconcileErrorsTotal.WithLabelValues("workspace").Inc()
		return ctrl.Result{}, err
	}

	isGitHubApp := false
	if workspace.Spec.SecretRef != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: workspace.Namespace, Name: workspace.Spec.SecretRef.Name}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("Workspace secret not found yet, requeuing", "workspace", workspace.Name, "secret", workspace.Spec.SecretRef.Name)
				r.recordEvent(&workspace, corev1.EventTypeNormal, "WorkspaceSecretNotFound", "Workspace secret %s not found, requeuing", workspace.Spec.SecretRef.Name)
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			logger.Error(err, "Unable to fetch Workspace secret", "workspace", workspace.Name, "secret", workspace.Spec.SecretRef.Name)
			return ctrl.Result{}, err
		}
		isGitHubApp = githubapp.IsGitHubApp(secret.Data)
	}

	if err := r.reconcileService(ctx, &workspace); err != nil {
		logger.Error(err, "Unable to reconcile workspace proxy Service", "workspace", workspace.Name)
		return ctrl.Result{}, err
	}
	if err := r.reconcileDeployment(ctx, &workspace, isGitHubApp); err != nil {
		logger.Error(err, "Unable to reconcile workspace proxy Deployment", "workspace", workspace.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkspaceReconciler) reconcileService(ctx context.Context, workspace *kelosv1alpha1.Workspace) error {
	desired := r.ProxyBuilder.BuildService(workspace)
	if err := controllerutil.SetControllerReference(workspace, desired, r.Scheme); err != nil {
		return err
	}

	var current corev1.Service
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return err
		}
		r.recordEvent(workspace, corev1.EventTypeNormal, "WorkspaceProxyServiceCreated", "Created workspace ghproxy Service %s", desired.Name)
		return nil
	}
	if err != nil {
		return err
	}

	if reflect.DeepEqual(current.Spec.Ports, desired.Spec.Ports) &&
		reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) &&
		reflect.DeepEqual(current.Labels, desired.Labels) {
		return nil
	}

	current.Labels = desired.Labels
	current.Spec.Ports = desired.Spec.Ports
	current.Spec.Selector = desired.Spec.Selector
	if err := r.Update(ctx, &current); err != nil {
		return err
	}
	r.recordEvent(workspace, corev1.EventTypeNormal, "WorkspaceProxyServiceUpdated", "Updated workspace ghproxy Service %s", desired.Name)
	return nil
}

func (r *WorkspaceReconciler) reconcileDeployment(ctx context.Context, workspace *kelosv1alpha1.Workspace, isGitHubApp bool) error {
	desired := r.ProxyBuilder.BuildDeployment(workspace, isGitHubApp)
	if err := controllerutil.SetControllerReference(workspace, desired, r.Scheme); err != nil {
		return err
	}

	var current appsv1.Deployment
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return err
		}
		r.recordEvent(workspace, corev1.EventTypeNormal, "WorkspaceProxyDeploymentCreated", "Created workspace ghproxy Deployment %s", desired.Name)
		return nil
	}
	if err != nil {
		return err
	}

	needsUpdate := false
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		needsUpdate = true
	}
	if current.Spec.Replicas == nil || desired.Spec.Replicas == nil || *current.Spec.Replicas != *desired.Spec.Replicas {
		current.Spec.Replicas = desired.Spec.Replicas
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.Template.Labels, desired.Spec.Template.Labels) {
		current.Spec.Template.Labels = desired.Spec.Template.Labels
		needsUpdate = true
	}
	if !containersEqual(current.Spec.Template.Spec.InitContainers, desired.Spec.Template.Spec.InitContainers) {
		current.Spec.Template.Spec.InitContainers = desired.Spec.Template.Spec.InitContainers
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.Template.Spec.Volumes, desired.Spec.Template.Spec.Volumes) {
		current.Spec.Template.Spec.Volumes = desired.Spec.Template.Spec.Volumes
		needsUpdate = true
	}
	if len(current.Spec.Template.Spec.Containers) != len(desired.Spec.Template.Spec.Containers) ||
		!containersEqual(current.Spec.Template.Spec.Containers, desired.Spec.Template.Spec.Containers) {
		current.Spec.Template.Spec.Containers = desired.Spec.Template.Spec.Containers
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	if err := r.Update(ctx, &current); err != nil {
		return err
	}
	r.recordEvent(workspace, corev1.EventTypeNormal, "WorkspaceProxyDeploymentUpdated", "Updated workspace ghproxy Deployment %s", desired.Name)
	return nil
}

func (r *WorkspaceReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelosv1alpha1.Workspace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findWorkspacesForSecret)).
		Complete(r)
}

func (r *WorkspaceReconciler) findWorkspacesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var workspaceList kelosv1alpha1.WorkspaceList
	if err := r.List(ctx, &workspaceList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, workspace := range workspaceList.Items {
		if workspace.Spec.SecretRef != nil && workspace.Spec.SecretRef.Name == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: workspace.Namespace,
					Name:      workspace.Name,
				},
			})
		}
	}
	return requests
}

func containersEqual(a, b []corev1.Container) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !containerEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func containerEqual(a, b corev1.Container) bool {
	if !resourceRequirementsEqual(a.Resources, b.Resources) {
		return false
	}
	a.Resources = corev1.ResourceRequirements{}
	b.Resources = corev1.ResourceRequirements{}
	return reflect.DeepEqual(a, b)
}
