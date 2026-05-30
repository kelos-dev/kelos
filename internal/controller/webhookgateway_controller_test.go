package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newGatewayControllerTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
	return scheme
}

// reconcileGateway reconciles objs[0] (assumed to be the WebhookGateway) and
// returns the updated gateway plus the reconcile result.
func reconcileGateway(t *testing.T, objs ...client.Object) (*kelosv1alpha1.WebhookGateway, ctrl.Result) {
	t.Helper()
	scheme := newGatewayControllerTestScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelosv1alpha1.WebhookGateway{}).
		Build()

	r := &WebhookGatewayReconciler{Client: cl, Scheme: scheme}
	key := types.NamespacedName{Namespace: objs[0].GetNamespace(), Name: objs[0].GetName()}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var got kelosv1alpha1.WebhookGateway
	if err := cl.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	return &got, res
}

func TestWebhookGatewayReconciler_GenericIsUnauthenticated(t *testing.T) {
	gw := &kelosv1alpha1.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gen", Namespace: "default"},
		Spec:       kelosv1alpha1.WebhookGatewaySpec{Generic: &kelosv1alpha1.GenericGateway{}},
	}
	got, _ := reconcileGateway(t, gw)
	if got.Status.Phase != kelosv1alpha1.WebhookGatewayPhaseUnauthenticated {
		t.Errorf("phase = %q, want Unauthenticated", got.Status.Phase)
	}
	if got.Status.Path != "/webhook/default/gen" {
		t.Errorf("path = %q, want /webhook/default/gen", got.Status.Path)
	}
}

func TestWebhookGatewayReconciler_NoSourceConfigured(t *testing.T) {
	// The CEL "exactly one of" rule rejects this in a real cluster; the fake
	// client does not enforce CEL, so this exercises the controller's guard.
	gw := &kelosv1alpha1.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"},
		Spec:       kelosv1alpha1.WebhookGatewaySpec{},
	}
	got, _ := reconcileGateway(t, gw)
	if got.Status.Phase != kelosv1alpha1.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing for no configured source", got.Status.Phase)
	}
}

func TestWebhookGatewayReconciler_GitHubSecretAbsentRequeues(t *testing.T) {
	gw := &kelosv1alpha1.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
		Spec: kelosv1alpha1.WebhookGatewaySpec{
			GitHub: &kelosv1alpha1.GitHubGateway{
				SecretRef: kelosv1alpha1.SecretReference{Name: "absent"},
			},
		},
	}
	got, res := reconcileGateway(t, gw)
	if got.Status.Phase != kelosv1alpha1.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter when secret is absent")
	}
}

func TestWebhookGatewayReconciler_GitHubAuthenticated(t *testing.T) {
	gw := &kelosv1alpha1.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
		Spec: kelosv1alpha1.WebhookGatewaySpec{
			GitHub: &kelosv1alpha1.GitHubGateway{
				SecretRef:  kelosv1alpha1.SecretReference{Name: "gh-secret"},
				APIBaseURL: "https://ghe.example.com/api/v3",
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gh-secret", Namespace: "default"}}
	got, _ := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelosv1alpha1.WebhookGatewayPhaseAuthenticated {
		t.Errorf("phase = %q, want Authenticated", got.Status.Phase)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}

func TestWebhookGatewayReconciler_LinearAuthenticated(t *testing.T) {
	gw := &kelosv1alpha1.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "lin", Namespace: "default"},
		Spec: kelosv1alpha1.WebhookGatewaySpec{
			Linear: &kelosv1alpha1.LinearGateway{
				SecretRef: kelosv1alpha1.SecretReference{Name: "lin-secret"},
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lin-secret", Namespace: "default"}}
	got, _ := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelosv1alpha1.WebhookGatewayPhaseAuthenticated {
		t.Errorf("phase = %q, want Authenticated", got.Status.Phase)
	}
}

func TestWebhookGatewayReconciler_GitHubCredentialsAbsent(t *testing.T) {
	gw := &kelosv1alpha1.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
		Spec: kelosv1alpha1.WebhookGatewaySpec{
			GitHub: &kelosv1alpha1.GitHubGateway{
				SecretRef:      kelosv1alpha1.SecretReference{Name: "gh-secret"},
				CredentialsRef: &kelosv1alpha1.SecretReference{Name: "absent-creds"},
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gh-secret", Namespace: "default"}}
	got, res := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelosv1alpha1.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing for absent credentials", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter when credentials secret is absent")
	}
}
