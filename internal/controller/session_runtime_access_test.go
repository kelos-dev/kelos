package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestEnsureSessionRuntimeAccess(t *testing.T) {
	tests := []struct {
		name               string
		serviceAccountName string
		managed            bool
	}{
		{name: "managed service account", managed: true},
		{name: "configured service account", serviceAccountName: "workload-identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = rbacv1.AddToScheme(scheme)
			_ = kelos.AddToScheme(scheme)
			session := testSession("chat", "codex")
			serviceAccountName := tt.serviceAccountName
			if tt.serviceAccountName != "" {
				session.Spec.Worker.PodOverrides = &kelos.PodOverrides{ServiceAccountName: tt.serviceAccountName}
			} else {
				serviceAccountName = sessionRuntimeAccessName(session)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()
			reconciler := &SessionReconciler{Client: cl, Scheme: scheme}
			if err := reconciler.ensureSessionRuntimeAccess(context.Background(), session, serviceAccountName); err != nil {
				t.Fatal(err)
			}

			key := client.ObjectKey{Namespace: session.Namespace, Name: sessionRuntimeAccessName(session)}
			var serviceAccount corev1.ServiceAccount
			err := cl.Get(context.Background(), key, &serviceAccount)
			if tt.managed {
				if err != nil || !metav1.IsControlledBy(&serviceAccount, session) {
					t.Fatalf("managed ServiceAccount error = %v, object = %#v", err, serviceAccount)
				}
			} else if !apierrors.IsNotFound(err) {
				t.Fatalf("configured ServiceAccount get error = %v, want NotFound", err)
			}

			var role rbacv1.Role
			if err := cl.Get(context.Background(), key, &role); err != nil {
				t.Fatal(err)
			}
			wantRules := []rbacv1.PolicyRule{
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
			}
			if !reflect.DeepEqual(role.Rules, wantRules) {
				t.Fatalf("Session runtime Role rules = %#v, want %#v", role.Rules, wantRules)
			}

			var roleBinding rbacv1.RoleBinding
			if err := cl.Get(context.Background(), key, &roleBinding); err != nil {
				t.Fatal(err)
			}
			if len(roleBinding.Subjects) != 1 || roleBinding.Subjects[0].Name != serviceAccountName || roleBinding.RoleRef.Name != key.Name {
				t.Fatalf("Session runtime RoleBinding = %#v", roleBinding)
			}
		})
	}
}
