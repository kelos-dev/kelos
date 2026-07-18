package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func (r *SessionReconciler) ensureSessionRuntimeAccess(ctx context.Context, session *kelos.Session, serviceAccountName string) error {
	if serviceAccountName == "" {
		return errors.New("Session runtime service account name is empty")
	}
	if sessionUsesManagedRuntimeServiceAccount(session) {
		desired := sessionRuntimeServiceAccount(session)
		if _, err := r.ensureSessionOwnedObject(ctx, session, "ServiceAccount", desired, &corev1.ServiceAccount{}); err != nil {
			return err
		}
	}

	desiredRole := sessionRuntimeRole(session)
	currentRole := &rbacv1.Role{}
	created, err := r.ensureSessionOwnedObject(ctx, session, "Role", desiredRole, currentRole)
	if err != nil {
		return err
	}
	if !created && !reflect.DeepEqual(currentRole.Rules, desiredRole.Rules) {
		currentRole.Rules = desiredRole.Rules
		if err := r.Update(ctx, currentRole); err != nil {
			return fmt.Errorf("updating Session runtime Role %q: %w", currentRole.Name, err)
		}
	}

	desiredBinding := sessionRuntimeRoleBinding(session, serviceAccountName)
	currentBinding := &rbacv1.RoleBinding{}
	created, err = r.ensureSessionOwnedObject(ctx, session, "RoleBinding", desiredBinding, currentBinding)
	if err != nil {
		return err
	}
	if created {
		return nil
	}
	if !reflect.DeepEqual(currentBinding.RoleRef, desiredBinding.RoleRef) {
		return fmt.Errorf("RoleBinding %q has an unexpected role reference", currentBinding.Name)
	}
	if !reflect.DeepEqual(currentBinding.Subjects, desiredBinding.Subjects) {
		currentBinding.Subjects = desiredBinding.Subjects
		if err := r.Update(ctx, currentBinding); err != nil {
			return fmt.Errorf("updating Session runtime RoleBinding %q: %w", currentBinding.Name, err)
		}
	}
	return nil
}

func (r *SessionReconciler) ensureSessionOwnedObject(ctx context.Context, session *kelos.Session, kind string, desired, current client.Object) (bool, error) {
	if err := controllerutil.SetControllerReference(session, desired, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return false, fmt.Errorf("setting Session owner on runtime %s: %w", kind, err)
	}
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, current); apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return false, fmt.Errorf("creating Session runtime %s %q: %w", kind, desired.GetName(), err)
		}
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("getting Session runtime %s %q: %w", kind, desired.GetName(), err)
	}
	if !metav1.IsControlledBy(current, session) {
		return false, fmt.Errorf("%s %q already exists and is not controlled by this Session", kind, current.GetName())
	}
	return false, nil
}

func sessionRuntimeServiceAccount(session *kelos.Session) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: sessionRuntimeAccessObjectMeta(session)}
}

func sessionRuntimeRole(session *kelos.Session) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: sessionRuntimeAccessObjectMeta(session),
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{kelos.GroupVersion.Group},
				Resources:     []string{"sessions"},
				ResourceNames: []string{session.Name},
				Verbs:         []string{"get", "watch", "patch"},
			},
			{
				APIGroups:     []string{kelos.GroupVersion.Group},
				Resources:     []string{"sessions/status"},
				ResourceNames: []string{session.Name},
				Verbs:         []string{"patch"},
			},
		},
	}
}

func sessionRuntimeRoleBinding(session *kelos.Session, serviceAccountName string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: sessionRuntimeAccessObjectMeta(session),
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     sessionRuntimeAccessName(session),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      serviceAccountName,
			Namespace: session.Namespace,
		}},
	}
}

func sessionRuntimeAccessObjectMeta(session *kelos.Session) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      sessionRuntimeAccessName(session),
		Namespace: session.Namespace,
		Labels:    sessionSelectorLabels(session),
	}
}

func sessionUsesManagedRuntimeServiceAccount(session *kelos.Session) bool {
	return session.Spec.Worker.PodOverrides == nil || session.Spec.Worker.PodOverrides.ServiceAccountName == ""
}

func sessionRuntimeAccessName(session *kelos.Session) string {
	return truncateResourceName("session-" + session.Name + "-runtime")
}
