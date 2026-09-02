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

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newGatewayControllerTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	return scheme
}

// reconcileGateway reconciles objs[0] (assumed to be the WebhookGateway) and
// returns the updated gateway plus the reconcile result.
func reconcileGateway(t *testing.T, objs ...client.Object) (*kelos.WebhookGateway, ctrl.Result) {
	t.Helper()
	scheme := newGatewayControllerTestScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelos.WebhookGateway{}).
		Build()

	r := &WebhookGatewayReconciler{Client: cl, Scheme: scheme}
	key := types.NamespacedName{Namespace: objs[0].GetNamespace(), Name: objs[0].GetName()}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var got kelos.WebhookGateway
	if err := cl.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	return &got, res
}

func TestWebhookGatewayReconciler_GenericIsUnauthenticated(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gen", Namespace: "default"},
		Spec:       kelos.WebhookGatewaySpec{Generic: &kelos.GenericGateway{}},
	}
	got, _ := reconcileGateway(t, gw)
	if got.Status.Phase != kelos.WebhookGatewayPhaseUnauthenticated {
		t.Errorf("phase = %q, want Unauthenticated", got.Status.Phase)
	}
	if got.Status.Path != "/webhook/default/gen" {
		t.Errorf("path = %q, want /webhook/default/gen", got.Status.Path)
	}
}

func TestWebhookGatewayReconciler_NoSourceConfigured(t *testing.T) {
	// The CEL "exactly one of" rule rejects this in a real cluster; the fake
	// client does not enforce CEL, so this exercises the controller's guard.
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"},
		Spec:       kelos.WebhookGatewaySpec{},
	}
	got, _ := reconcileGateway(t, gw)
	if got.Status.Phase != kelos.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing for no configured source", got.Status.Phase)
	}
}

func TestWebhookGatewayReconciler_GitHubSecretAbsentRequeues(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			GitHub: &kelos.GitHubGateway{
				SecretRef: kelos.SecretReference{Name: "absent"},
			},
		},
	}
	got, res := reconcileGateway(t, gw)
	if got.Status.Phase != kelos.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter when secret is absent")
	}
}

func TestWebhookGatewayReconciler_GitHubAuthenticated(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			GitHub: &kelos.GitHubGateway{
				SecretRef:  kelos.SecretReference{Name: "gh-secret"},
				APIBaseURL: "https://ghe.example.com/api/v3",
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gh-secret", Namespace: "default"}}
	got, _ := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelos.WebhookGatewayPhaseAuthenticated {
		t.Errorf("phase = %q, want Authenticated", got.Status.Phase)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}

func TestWebhookGatewayReconciler_LinearAuthenticated(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "lin", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			Linear: &kelos.LinearGateway{
				SecretRef: kelos.SecretReference{Name: "lin-secret"},
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lin-secret", Namespace: "default"}}
	got, _ := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelos.WebhookGatewayPhaseAuthenticated {
		t.Errorf("phase = %q, want Authenticated", got.Status.Phase)
	}
}

func TestWebhookGatewayReconciler_GitLabAuthenticated(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			GitLab: &kelos.GitLabGateway{
				SecretRef: kelos.SecretReference{Name: "gl-secret"},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gl-secret", Namespace: "default"},
		Data:       map[string][]byte{gatewayWebhookSecretKey: []byte("token")},
	}
	got, _ := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelos.WebhookGatewayPhaseAuthenticated {
		t.Errorf("phase = %q, want Authenticated", got.Status.Phase)
	}
	if !gatewayReferencesSecret(gw, "gl-secret") || gatewayReferencesSecret(gw, "other") {
		t.Error("expected gateway to reference only its token secret")
	}
}

func TestWebhookGatewayReconciler_GitLabSecretMissingKey(t *testing.T) {
	// The webhook handler rejects every delivery when the key is absent, so
	// the gateway must not report Authenticated.
	tests := []struct {
		name   string
		secret *corev1.Secret
		creds  *corev1.Secret
		want   string
	}{
		{
			name:   "webhook secret without key",
			secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gl-secret", Namespace: "default"}},
			want:   `webhook token secret "gl-secret" has no webhook-secret key`,
		},
		{
			name: "blank webhook secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "gl-secret", Namespace: "default"},
				Data:       map[string][]byte{gatewayWebhookSecretKey: []byte(" \n")},
			},
			want: `webhook token secret "gl-secret" has no webhook-secret key`,
		},
		{
			name: "credentials without token key",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "gl-secret", Namespace: "default"},
				Data:       map[string][]byte{gatewayWebhookSecretKey: []byte("token")},
			},
			creds: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gl-creds", Namespace: "default"}},
			want:  `credentials secret "gl-creds" has no GITLAB_TOKEN key`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := &kelos.WebhookGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
				Spec: kelos.WebhookGatewaySpec{GitLab: &kelos.GitLabGateway{
					SecretRef: kelos.SecretReference{Name: "gl-secret"},
				}},
			}
			objs := []client.Object{gw, tt.secret}
			if tt.creds != nil {
				gw.Spec.GitLab.CredentialsRef = &kelos.SecretReference{Name: tt.creds.Name}
				objs = append(objs, tt.creds)
			}
			got, res := reconcileGateway(t, objs...)
			if got.Status.Phase != kelos.WebhookGatewayPhaseSecretMissing {
				t.Errorf("phase = %q, want SecretMissing", got.Status.Phase)
			}
			if got.Status.Message != tt.want {
				t.Errorf("message = %q, want %q", got.Status.Message, tt.want)
			}
			if res.RequeueAfter == 0 {
				t.Error("expected requeue until the key is added")
			}
		})
	}
}

func TestWebhookGatewayReconciler_GitLabCredentialsAbsent(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			GitLab: &kelos.GitLabGateway{
				SecretRef:      kelos.SecretReference{Name: "gl-secret"},
				CredentialsRef: &kelos.SecretReference{Name: "absent-creds"},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gl-secret", Namespace: "default"},
		Data:       map[string][]byte{gatewayWebhookSecretKey: []byte("token")},
	}
	got, res := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelos.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing for absent credentials", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected requeue while the credentials secret is absent")
	}
	if !gatewayReferencesSecret(gw, "absent-creds") {
		t.Error("expected gateway to reference its credentials secret")
	}
}

func TestWebhookGatewayReconciler_GitLabSecretAbsentRequeues(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			GitLab: &kelos.GitLabGateway{SecretRef: kelos.SecretReference{Name: "absent"}},
		},
	}
	got, res := reconcileGateway(t, gw)
	if got.Status.Phase != kelos.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected requeue while the token secret is absent")
	}
}

func TestWebhookGatewayReconciler_GitHubCredentialsAbsent(t *testing.T) {
	gw := &kelos.WebhookGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
		Spec: kelos.WebhookGatewaySpec{
			GitHub: &kelos.GitHubGateway{
				SecretRef:      kelos.SecretReference{Name: "gh-secret"},
				CredentialsRef: &kelos.SecretReference{Name: "absent-creds"},
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gh-secret", Namespace: "default"}}
	got, res := reconcileGateway(t, gw, secret)
	if got.Status.Phase != kelos.WebhookGatewayPhaseSecretMissing {
		t.Errorf("phase = %q, want SecretMissing for absent credentials", got.Status.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter when credentials secret is absent")
	}
}
