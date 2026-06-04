package controller

import (
	"context"
	"reflect"

	"github.com/kelos-dev/kelos/internal/codexauth"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type CodexAuthRefresherReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Builder *CodexAuthRefresherBuilder
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;delete

func (r *CodexAuthRefresherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if r.Builder == nil {
		r.Builder = NewCodexAuthRefresherBuilder()
	}

	var secret corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.deleteManagedResources(ctx, req.Namespace, req.Name)
		}
		logger.Error(err, "Unable to fetch Secret", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, err
	}

	if !IsCodexAuthRefreshable(&secret) {
		return ctrl.Result{}, r.deleteManagedResources(ctx, secret.Namespace, secret.Name)
	}

	if err := r.ensureRefreshAccess(ctx, &secret); err != nil {
		logger.Error(err, "Unable to ensure Codex auth refresher access", "secret", secret.Name, "namespace", secret.Namespace)
		return ctrl.Result{}, err
	}

	desired := r.Builder.Build(&secret)
	if err := controllerutil.SetControllerReference(&secret, desired, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		logger.Error(err, "Unable to set owner reference on Codex auth refresher CronJob", "cronJob", desired.Name)
		return ctrl.Result{}, err
	}
	var current batchv1.CronJob
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Unable to fetch Codex auth refresher CronJob", "cronJob", desired.Name)
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, desired); err != nil {
			logger.Error(err, "Unable to create Codex auth refresher CronJob", "cronJob", desired.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Created Codex auth refresher CronJob", "cronJob", desired.Name, "secret", secret.Name, "namespace", secret.Namespace)
		return ctrl.Result{}, nil
	}

	if updateCodexAuthRefresherCronJob(&current, desired) {
		if err := r.Update(ctx, &current); err != nil {
			logger.Error(err, "Unable to update Codex auth refresher CronJob", "cronJob", current.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Updated Codex auth refresher CronJob", "cronJob", current.Name, "secret", secret.Name, "namespace", secret.Namespace)
	}
	return ctrl.Result{}, nil
}

func (r *CodexAuthRefresherReconciler) ensureRefreshAccess(ctx context.Context, secret *corev1.Secret) error {
	if r.Builder == nil {
		r.Builder = NewCodexAuthRefresherBuilder()
	}
	serviceAccount := r.Builder.BuildServiceAccount(secret)
	if err := controllerutil.SetControllerReference(secret, serviceAccount, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return err
	}
	if err := r.ensureServiceAccount(ctx, serviceAccount); err != nil {
		return err
	}

	role := r.Builder.BuildRole(secret)
	if err := controllerutil.SetControllerReference(secret, role, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return err
	}
	if err := r.ensureRole(ctx, role); err != nil {
		return err
	}

	roleBinding := r.Builder.BuildRoleBinding(secret)
	if err := controllerutil.SetControllerReference(secret, roleBinding, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return err
	}
	return r.ensureRoleBinding(ctx, roleBinding)
}

func (r *CodexAuthRefresherReconciler) ensureServiceAccount(ctx context.Context, desired *corev1.ServiceAccount) error {
	logger := log.FromContext(ctx)
	var current corev1.ServiceAccount
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		}
		logger.Info("Created Codex auth refresher ServiceAccount", "serviceAccount", desired.Name, "namespace", desired.Namespace)
		return nil
	}

	if updateCodexAuthRefresherObjectMetadata(&current, desired) {
		if err := r.Update(ctx, &current); err != nil {
			return err
		}
		logger.Info("Updated Codex auth refresher ServiceAccount", "serviceAccount", current.Name, "namespace", current.Namespace)
	}
	return nil
}

func (r *CodexAuthRefresherReconciler) ensureRole(ctx context.Context, desired *rbacv1.Role) error {
	logger := log.FromContext(ctx)
	var current rbacv1.Role
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		}
		logger.Info("Created Codex auth refresher Role", "role", desired.Name, "namespace", desired.Namespace)
		return nil
	}

	needsUpdate := updateCodexAuthRefresherObjectMetadata(&current, desired)
	if !reflect.DeepEqual(current.Rules, desired.Rules) {
		current.Rules = desired.Rules
		needsUpdate = true
	}
	if needsUpdate {
		if err := r.Update(ctx, &current); err != nil {
			return err
		}
		logger.Info("Updated Codex auth refresher Role", "role", current.Name, "namespace", current.Namespace)
	}
	return nil
}

func (r *CodexAuthRefresherReconciler) ensureRoleBinding(ctx context.Context, desired *rbacv1.RoleBinding) error {
	logger := log.FromContext(ctx)
	var current rbacv1.RoleBinding
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		}
		logger.Info("Created Codex auth refresher RoleBinding", "roleBinding", desired.Name, "namespace", desired.Namespace)
		return nil
	}

	if !reflect.DeepEqual(current.RoleRef, desired.RoleRef) {
		if err := r.Delete(ctx, &current); err != nil {
			return err
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		}
		logger.Info("Recreated Codex auth refresher RoleBinding", "roleBinding", desired.Name, "namespace", desired.Namespace)
		return nil
	}

	needsUpdate := updateCodexAuthRefresherObjectMetadata(&current, desired)
	if !reflect.DeepEqual(current.Subjects, desired.Subjects) {
		current.Subjects = desired.Subjects
		needsUpdate = true
	}
	if needsUpdate {
		if err := r.Update(ctx, &current); err != nil {
			return err
		}
		logger.Info("Updated Codex auth refresher RoleBinding", "roleBinding", current.Name, "namespace", current.Namespace)
	}
	return nil
}

func (r *CodexAuthRefresherReconciler) deleteManagedResources(ctx context.Context, secretNamespace, secretName string) error {
	if r.Builder == nil {
		r.Builder = NewCodexAuthRefresherBuilder()
	}
	if err := r.deleteCronJob(ctx, secretNamespace, secretName); err != nil {
		return err
	}
	if err := r.deleteRoleBinding(ctx, secretNamespace, secretName); err != nil {
		return err
	}
	if err := r.deleteRole(ctx, secretNamespace, secretName); err != nil {
		return err
	}
	return r.deleteServiceAccount(ctx, secretNamespace, secretName)
}

func (r *CodexAuthRefresherReconciler) deleteCronJob(ctx context.Context, secretNamespace, secretName string) error {
	name := CodexAuthRefresherCronJobName(secretNamespace, secretName)

	var cronJob batchv1.CronJob
	if err := r.Get(ctx, client.ObjectKey{Namespace: secretNamespace, Name: name}, &cronJob); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if cronJob.Annotations[codexAuthSecretNamespaceAnnotation] != secretNamespace || cronJob.Annotations[codexAuthSecretNameAnnotation] != secretName {
		return nil
	}
	return r.Delete(ctx, &cronJob)
}

func (r *CodexAuthRefresherReconciler) deleteRoleBinding(ctx context.Context, secretNamespace, secretName string) error {
	name := CodexAuthRefresherCronJobName(secretNamespace, secretName)
	var roleBinding rbacv1.RoleBinding
	if err := r.Get(ctx, client.ObjectKey{Namespace: secretNamespace, Name: name}, &roleBinding); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if roleBinding.Annotations[codexAuthSecretNamespaceAnnotation] != secretNamespace || roleBinding.Annotations[codexAuthSecretNameAnnotation] != secretName {
		return nil
	}
	return r.Delete(ctx, &roleBinding)
}

func (r *CodexAuthRefresherReconciler) deleteRole(ctx context.Context, secretNamespace, secretName string) error {
	name := CodexAuthRefresherCronJobName(secretNamespace, secretName)
	var role rbacv1.Role
	if err := r.Get(ctx, client.ObjectKey{Namespace: secretNamespace, Name: name}, &role); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if role.Annotations[codexAuthSecretNamespaceAnnotation] != secretNamespace || role.Annotations[codexAuthSecretNameAnnotation] != secretName {
		return nil
	}
	return r.Delete(ctx, &role)
}

func (r *CodexAuthRefresherReconciler) deleteServiceAccount(ctx context.Context, secretNamespace, secretName string) error {
	var serviceAccount corev1.ServiceAccount
	key := client.ObjectKey{Namespace: secretNamespace, Name: codexAuthRefresherServiceAccountName(secretNamespace, secretName)}
	if err := r.Get(ctx, key, &serviceAccount); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if serviceAccount.Annotations[codexAuthSecretNamespaceAnnotation] != secretNamespace || serviceAccount.Annotations[codexAuthSecretNameAnnotation] != secretName {
		return nil
	}
	return r.Delete(ctx, &serviceAccount)
}

func updateCodexAuthRefresherCronJob(current, desired *batchv1.CronJob) bool {
	needsUpdate := updateCodexAuthRefresherObjectMetadata(current, desired)
	if current.Spec.Schedule != desired.Spec.Schedule {
		current.Spec.Schedule = desired.Spec.Schedule
		needsUpdate = true
	}
	if current.Spec.ConcurrencyPolicy != desired.Spec.ConcurrencyPolicy {
		current.Spec.ConcurrencyPolicy = desired.Spec.ConcurrencyPolicy
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.SuccessfulJobsHistoryLimit, desired.Spec.SuccessfulJobsHistoryLimit) {
		current.Spec.SuccessfulJobsHistoryLimit = desired.Spec.SuccessfulJobsHistoryLimit
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.FailedJobsHistoryLimit, desired.Spec.FailedJobsHistoryLimit) {
		current.Spec.FailedJobsHistoryLimit = desired.Spec.FailedJobsHistoryLimit
		needsUpdate = true
	}
	if updateCodexAuthRefresherJobTemplate(&current.Spec.JobTemplate, &desired.Spec.JobTemplate) {
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherObjectMetadata(current, desired client.Object) bool {
	needsUpdate := false
	if !reflect.DeepEqual(current.GetLabels(), desired.GetLabels()) {
		current.SetLabels(desired.GetLabels())
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.GetAnnotations(), desired.GetAnnotations()) {
		current.SetAnnotations(desired.GetAnnotations())
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.GetOwnerReferences(), desired.GetOwnerReferences()) {
		current.SetOwnerReferences(desired.GetOwnerReferences())
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherJobTemplate(current, desired *batchv1.JobTemplateSpec) bool {
	needsUpdate := false
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.BackoffLimit, desired.Spec.BackoffLimit) {
		current.Spec.BackoffLimit = desired.Spec.BackoffLimit
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.ActiveDeadlineSeconds, desired.Spec.ActiveDeadlineSeconds) {
		current.Spec.ActiveDeadlineSeconds = desired.Spec.ActiveDeadlineSeconds
		needsUpdate = true
	}
	if updateCodexAuthRefresherPodTemplate(&current.Spec.Template, &desired.Spec.Template) {
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherPodTemplate(current, desired *corev1.PodTemplateSpec) bool {
	needsUpdate := false
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		needsUpdate = true
	}

	currentPodSpec := &current.Spec
	desiredPodSpec := &desired.Spec
	if currentPodSpec.ServiceAccountName != desiredPodSpec.ServiceAccountName {
		currentPodSpec.ServiceAccountName = desiredPodSpec.ServiceAccountName
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.SecurityContext, desiredPodSpec.SecurityContext) {
		currentPodSpec.SecurityContext = desiredPodSpec.SecurityContext
		needsUpdate = true
	}
	if currentPodSpec.RestartPolicy != desiredPodSpec.RestartPolicy {
		currentPodSpec.RestartPolicy = desiredPodSpec.RestartPolicy
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.InitContainers, desiredPodSpec.InitContainers) {
		currentPodSpec.InitContainers = desiredPodSpec.InitContainers
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.Volumes, desiredPodSpec.Volumes) {
		currentPodSpec.Volumes = desiredPodSpec.Volumes
		needsUpdate = true
	}
	if updateCodexAuthRefresherContainers(currentPodSpec, desiredPodSpec) {
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherContainers(currentPodSpec, desiredPodSpec *corev1.PodSpec) bool {
	if len(currentPodSpec.Containers) != len(desiredPodSpec.Containers) {
		currentPodSpec.Containers = desiredPodSpec.Containers
		return true
	}
	if len(desiredPodSpec.Containers) == 0 {
		return false
	}

	needsUpdate := false
	for i := range desiredPodSpec.Containers {
		current := &currentPodSpec.Containers[i]
		desired := desiredPodSpec.Containers[i]
		if current.Name != desired.Name {
			current.Name = desired.Name
			needsUpdate = true
		}
		if current.Image != desired.Image {
			current.Image = desired.Image
			needsUpdate = true
		}
		if desired.ImagePullPolicy != "" && current.ImagePullPolicy != desired.ImagePullPolicy {
			current.ImagePullPolicy = desired.ImagePullPolicy
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.Command, desired.Command) {
			current.Command = desired.Command
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.Args, desired.Args) {
			current.Args = desired.Args
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.Env, desired.Env) {
			current.Env = desired.Env
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.VolumeMounts, desired.VolumeMounts) {
			current.VolumeMounts = desired.VolumeMounts
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.SecurityContext, desired.SecurityContext) {
			current.SecurityContext = desired.SecurityContext
			needsUpdate = true
		}
		if !resourceRequirementsEqual(current.Resources, desired.Resources) {
			current.Resources = desired.Resources
			needsUpdate = true
		}
	}
	return needsUpdate
}

func (r *CodexAuthRefresherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(secretCodexAuthRefreshPredicate{})).
		Watches(&batchv1.CronJob{}, handler.EnqueueRequestsFromMapFunc(r.requestsForCronJob), builder.WithPredicates(codexAuthRefresherCronJobPredicate{})).
		Watches(&corev1.ServiceAccount{}, handler.EnqueueRequestsFromMapFunc(r.requestsForServiceAccount), builder.WithPredicates(codexAuthRefresherServiceAccountPredicate{})).
		Watches(&rbacv1.Role{}, handler.EnqueueRequestsFromMapFunc(r.requestsForManagedObject), builder.WithPredicates(codexAuthRefresherManagedObjectPredicate{})).
		Watches(&rbacv1.RoleBinding{}, handler.EnqueueRequestsFromMapFunc(r.requestsForManagedObject), builder.WithPredicates(codexAuthRefresherManagedObjectPredicate{})).
		Complete(r)
}

func (r *CodexAuthRefresherReconciler) requestsForCronJob(_ context.Context, obj client.Object) []reconcile.Request {
	if _, ok := obj.(*batchv1.CronJob); !ok {
		return nil
	}
	return requestsForCodexAuthSource(obj)
}

func (r *CodexAuthRefresherReconciler) requestsForManagedObject(ctx context.Context, obj client.Object) []reconcile.Request {
	if requests := requestsForNamedCodexAuthSource(obj, CodexAuthRefresherCronJobName); len(requests) == 1 {
		return requests
	}
	return r.requestsForRefreshableSecretByObjectName(ctx, obj, "managed object", CodexAuthRefresherCronJobName)
}

func (r *CodexAuthRefresherReconciler) requestsForServiceAccount(ctx context.Context, obj client.Object) []reconcile.Request {
	serviceAccount, ok := obj.(*corev1.ServiceAccount)
	if !ok || serviceAccount == nil {
		return nil
	}

	if requests := requestsForNamedCodexAuthSource(serviceAccount, codexAuthRefresherServiceAccountName); len(requests) == 1 {
		return requests
	}

	return r.requestsForRefreshableSecretByObjectName(ctx, serviceAccount, "ServiceAccount", codexAuthRefresherServiceAccountName)
}

func (r *CodexAuthRefresherReconciler) requestsForRefreshableSecretByObjectName(ctx context.Context, obj client.Object, objectKind string, nameForSecret func(string, string) string) []reconcile.Request {
	if obj == nil {
		return nil
	}
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, client.InNamespace(obj.GetNamespace()), client.MatchingLabels{codexauth.RefreshLabel: "true"}); err != nil {
		log.FromContext(ctx).Error(err, "Unable to list Codex auth Secrets for managed object", "kind", objectKind, "name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(secrets.Items))
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if !IsCodexAuthRefreshable(secret) || nameForSecret(secret.Namespace, secret.Name) != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name},
		})
	}
	return requests
}

func requestsForNamedCodexAuthSource(obj client.Object, nameForSource func(string, string) string) []reconcile.Request {
	requests := requestsForCodexAuthSource(obj)
	if len(requests) != 1 {
		return nil
	}
	source := requests[0].NamespacedName
	if obj.GetNamespace() != source.Namespace || obj.GetName() != nameForSource(source.Namespace, source.Name) {
		return nil
	}
	return requests
}

func requestsForCodexAuthSource(obj client.Object) []reconcile.Request {
	if obj == nil {
		return nil
	}
	annotations := obj.GetAnnotations()
	secretNamespace := annotations[codexAuthSecretNamespaceAnnotation]
	secretName := annotations[codexAuthSecretNameAnnotation]
	if secretNamespace == "" || secretName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: secretNamespace, Name: secretName},
	}}
}

type secretCodexAuthRefreshPredicate struct{}

func (secretCodexAuthRefreshPredicate) Create(e event.CreateEvent) bool {
	secret, ok := e.Object.(*corev1.Secret)
	return ok && secret.Labels[codexauth.RefreshLabel] == "true"
}

func (secretCodexAuthRefreshPredicate) Delete(e event.DeleteEvent) bool {
	secret, ok := e.Object.(*corev1.Secret)
	return ok && secret.Labels[codexauth.RefreshLabel] == "true"
}

func (secretCodexAuthRefreshPredicate) Update(e event.UpdateEvent) bool {
	oldSecret, oldOK := e.ObjectOld.(*corev1.Secret)
	newSecret, newOK := e.ObjectNew.(*corev1.Secret)
	return oldOK && newOK && (oldSecret.Labels[codexauth.RefreshLabel] == "true" || newSecret.Labels[codexauth.RefreshLabel] == "true")
}

func (secretCodexAuthRefreshPredicate) Generic(e event.GenericEvent) bool {
	secret, ok := e.Object.(*corev1.Secret)
	return ok && secret.Labels[codexauth.RefreshLabel] == "true"
}

type codexAuthRefresherCronJobPredicate struct{}

func (codexAuthRefresherCronJobPredicate) Create(e event.CreateEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.Object)
}

func (codexAuthRefresherCronJobPredicate) Delete(e event.DeleteEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.Object)
}

func (codexAuthRefresherCronJobPredicate) Update(e event.UpdateEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.ObjectOld) || isManagedCodexAuthRefresherCronJob(e.ObjectNew)
}

func (codexAuthRefresherCronJobPredicate) Generic(e event.GenericEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.Object)
}

func isManagedCodexAuthRefresherCronJob(obj client.Object) bool {
	return isManagedCodexAuthRefresherObject(obj)
}

type codexAuthRefresherServiceAccountPredicate struct{}

func (codexAuthRefresherServiceAccountPredicate) Create(e event.CreateEvent) bool {
	return isManagedCodexAuthRefresherServiceAccount(e.Object)
}

func (codexAuthRefresherServiceAccountPredicate) Delete(e event.DeleteEvent) bool {
	return isManagedCodexAuthRefresherServiceAccount(e.Object)
}

func (codexAuthRefresherServiceAccountPredicate) Update(e event.UpdateEvent) bool {
	return isManagedCodexAuthRefresherServiceAccount(e.ObjectOld) || isManagedCodexAuthRefresherServiceAccount(e.ObjectNew)
}

func (codexAuthRefresherServiceAccountPredicate) Generic(e event.GenericEvent) bool {
	return isManagedCodexAuthRefresherServiceAccount(e.Object)
}

func isManagedCodexAuthRefresherServiceAccount(obj client.Object) bool {
	serviceAccount, ok := obj.(*corev1.ServiceAccount)
	if !ok || serviceAccount == nil {
		return false
	}
	labels := serviceAccount.GetLabels()
	if labels["app.kubernetes.io/component"] != codexAuthRefresherComponentLabel || labels["kelos.dev/managed-by"] != "kelos-controller" {
		return false
	}
	annotations := serviceAccount.GetAnnotations()
	secretNamespace := annotations[codexAuthSecretNamespaceAnnotation]
	secretName := annotations[codexAuthSecretNameAnnotation]
	return secretNamespace != "" && secretName != "" && serviceAccount.Name == codexAuthRefresherServiceAccountName(secretNamespace, secretName)
}

type codexAuthRefresherManagedObjectPredicate struct{}

func (codexAuthRefresherManagedObjectPredicate) Create(e event.CreateEvent) bool {
	return isManagedCodexAuthRefresherObject(e.Object)
}

func (codexAuthRefresherManagedObjectPredicate) Delete(e event.DeleteEvent) bool {
	return isManagedCodexAuthRefresherObject(e.Object)
}

func (codexAuthRefresherManagedObjectPredicate) Update(e event.UpdateEvent) bool {
	return isManagedCodexAuthRefresherObject(e.ObjectOld) || isManagedCodexAuthRefresherObject(e.ObjectNew)
}

func (codexAuthRefresherManagedObjectPredicate) Generic(e event.GenericEvent) bool {
	return isManagedCodexAuthRefresherObject(e.Object)
}

func isManagedCodexAuthRefresherObject(obj client.Object) bool {
	if obj == nil {
		return false
	}
	labels := obj.GetLabels()
	if labels["app.kubernetes.io/component"] != codexAuthRefresherComponentLabel || labels["kelos.dev/managed-by"] != "kelos-controller" {
		return false
	}
	annotations := obj.GetAnnotations()
	return annotations[codexAuthSecretNamespaceAnnotation] != "" && annotations[codexAuthSecretNameAnnotation] != ""
}
