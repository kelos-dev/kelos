package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

func newWorkspaceControllerTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
	return scheme
}

func TestWorkspaceReconciler_CreatesGitHubAppProxyResources(t *testing.T) {
	scheme := newWorkspaceControllerTestScheme()

	workspace := &kelosv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-workspace",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.WorkspaceSpec{
			Repo: "https://github.example.com/my-org/my-repo.git",
			SecretRef: &kelosv1alpha1.SecretReference{
				Name: "github-app-creds",
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app-creds",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"appID":          []byte("123"),
			"installationID": []byte("456"),
			"privateKey":     []byte("pem"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(workspace, secret).
		Build()

	r := &WorkspaceReconciler{
		Client:       cl,
		Scheme:       scheme,
		ProxyBuilder: NewWorkspaceGHProxyBuilder(),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "example-workspace"},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var deploy appsv1.Deployment
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: WorkspaceGHProxyName("example-workspace")}, &deploy); err != nil {
		t.Fatalf("getting Deployment: %v", err)
	}
	if len(deploy.OwnerReferences) != 1 || deploy.OwnerReferences[0].Name != "example-workspace" {
		t.Fatalf("expected Deployment ownerReference to Workspace, got %v", deploy.OwnerReferences)
	}
	if len(deploy.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatalf("expected 0 init containers, got %d", len(deploy.Spec.Template.Spec.InitContainers))
	}
	args := deploy.Spec.Template.Spec.Containers[0].Args
	if !containsArg(args, "--upstream-base-url=https://github.example.com/api/v3") {
		t.Fatalf("expected enterprise upstream arg, got %v", args)
	}
	if containsArg(args, "--github-token-file=/shared/token/GITHUB_TOKEN") {
		t.Fatalf("did not expect github-token-file arg, got %v", args)
	}
	env := deploy.Spec.Template.Spec.Containers[0].Env
	foundAppID, foundInstallationID, foundPrivateKey := false, false, false
	for _, e := range env {
		switch e.Name {
		case "GITHUB_APP_ID":
			foundAppID = true
		case "GITHUB_APP_INSTALLATION_ID":
			foundInstallationID = true
		case "GITHUB_APP_PRIVATE_KEY":
			foundPrivateKey = true
		}
	}
	if !foundAppID || !foundInstallationID || !foundPrivateKey {
		t.Fatalf("expected all GitHub App credential env vars, got %v", env)
	}

	var svc corev1.Service
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: WorkspaceGHProxyName("example-workspace")}, &svc); err != nil {
		t.Fatalf("getting Service: %v", err)
	}
	if len(svc.OwnerReferences) != 1 || svc.OwnerReferences[0].Name != "example-workspace" {
		t.Fatalf("expected Service ownerReference to Workspace, got %v", svc.OwnerReferences)
	}
}

func TestWorkspaceGHProxyName_TruncatesLongWorkspaceNames(t *testing.T) {
	name := WorkspaceGHProxyName(strings.Repeat("a", 70))
	if len(name) > 63 {
		t.Fatalf("expected truncated name length <= 63, got %d", len(name))
	}
	if !strings.HasPrefix(name, "ghproxy-") {
		t.Fatalf("expected ghproxy prefix, got %q", name)
	}
}

func TestContainersEqual_UsesSemanticResourceComparison(t *testing.T) {
	a := []corev1.Container{{
		Name:  "ghproxy",
		Image: "ghproxy:latest",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1000m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}}
	b := []corev1.Container{{
		Name:  "ghproxy",
		Image: "ghproxy:latest",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}}

	if !containersEqual(a, b) {
		t.Fatal("expected semantically equal resource quantities to compare equal")
	}
}

func TestWorkspaceGHProxyBuilder_CacheTTL(t *testing.T) {
	workspace := &kelosv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ws", Namespace: "default"},
		Spec:       kelosv1alpha1.WorkspaceSpec{Repo: "https://github.com/org/repo.git"},
	}

	t.Run("includes cache-ttl arg when set", func(t *testing.T) {
		b := NewWorkspaceGHProxyBuilder()
		b.GHProxyCacheTTL = 30 * time.Second
		deploy := b.BuildDeployment(workspace, false)
		args := deploy.Spec.Template.Spec.Containers[0].Args
		if !containsArg(args, "--cache-ttl=30s") {
			t.Fatalf("expected --cache-ttl=30s arg, got %v", args)
		}
	})

	t.Run("omits cache-ttl arg when zero", func(t *testing.T) {
		b := NewWorkspaceGHProxyBuilder()
		deploy := b.BuildDeployment(workspace, false)
		args := deploy.Spec.Template.Spec.Containers[0].Args
		for _, arg := range args {
			if strings.HasPrefix(arg, "--cache-ttl") {
				t.Fatalf("expected no --cache-ttl arg, got %v", args)
			}
		}
	})
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
