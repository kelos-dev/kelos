package cli

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestGetAgentConfigFallsBackToV1alpha1(t *testing.T) {
	ctx := context.Background()
	ac := &kelosv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"},
		Spec: kelosv1alpha1.AgentConfigSpec{
			AgentsMD: "legacy instructions",
			MCPServers: []kelosv1alpha1.MCPServerSpec{{
				Name: "server",
				Type: "stdio",
				Env:  map[string]string{"TOKEN": "literal"},
			}},
		},
	}
	cl := agentConfigV1alpha1OnlyClient(ac)

	got, raw, err := getAgentConfig(ctx, cl, client.ObjectKey{Name: "legacy", Namespace: "default"})
	if err != nil {
		t.Fatalf("getAgentConfig: %v", err)
	}
	if got.APIVersion != "kelos.dev/v1alpha2" {
		t.Errorf("converted APIVersion = %q, want kelos.dev/v1alpha2", got.APIVersion)
	}
	if got.Spec.AgentsMD != "legacy instructions" {
		t.Errorf("AgentsMD = %q, want legacy instructions", got.Spec.AgentsMD)
	}
	if got.Spec.MCPServers[0].Env[0].Name != "TOKEN" || got.Spec.MCPServers[0].Env[0].Value != "literal" {
		t.Errorf("converted Env = %#v, want TOKEN literal", got.Spec.MCPServers[0].Env)
	}
	if raw.GetObjectKind().GroupVersionKind().GroupVersion().String() != "kelos.dev/v1alpha1" {
		t.Errorf("raw APIVersion = %q, want kelos.dev/v1alpha1", raw.GetObjectKind().GroupVersionKind().GroupVersion())
	}
}

func TestListAgentConfigsFallsBackToV1alpha1(t *testing.T) {
	ctx := context.Background()
	cl := agentConfigV1alpha1OnlyClient(
		&kelosv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"},
			Spec:       kelosv1alpha1.AgentConfigSpec{AgentsMD: "legacy instructions"},
		},
	)

	items, raw, err := listAgentConfigs(ctx, cl, client.InNamespace("default"))
	if err != nil {
		t.Fatalf("listAgentConfigs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].APIVersion != "kelos.dev/v1alpha2" {
		t.Errorf("converted APIVersion = %q, want kelos.dev/v1alpha2", items[0].APIVersion)
	}
	if items[0].Spec.AgentsMD != "legacy instructions" {
		t.Errorf("AgentsMD = %q, want legacy instructions", items[0].Spec.AgentsMD)
	}
	if raw.GetObjectKind().GroupVersionKind().String() != "kelos.dev/v1alpha1, Kind=AgentConfigList" {
		t.Errorf("raw GVK = %s, want kelos.dev/v1alpha1 AgentConfigList", raw.GetObjectKind().GroupVersionKind())
	}
}

func TestCreateAgentConfigFallsBackToV1alpha1(t *testing.T) {
	ctx := context.Background()
	cl := agentConfigV1alpha1OnlyClient()
	ac := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"},
		Spec: kelos.AgentConfigSpec{
			MCPServers: []kelos.MCPServerSpec{{
				Name: "server",
				Type: "stdio",
				Env:  []corev1.EnvVar{{Name: "TOKEN", Value: "literal"}},
			}},
		},
	}

	if err := createAgentConfig(ctx, cl, ac); err != nil {
		t.Fatalf("createAgentConfig: %v", err)
	}

	got := &kelosv1alpha1.AgentConfig{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "legacy", Namespace: "default"}, got); err != nil {
		t.Fatalf("Get fallback object: %v", err)
	}
	if got.Spec.MCPServers[0].Env["TOKEN"] != "literal" {
		t.Errorf("created v1alpha1 env = %#v, want TOKEN literal", got.Spec.MCPServers[0].Env)
	}
}

func TestCreateAgentConfigRejectsValueFromWithoutV1alpha2(t *testing.T) {
	ctx := context.Background()
	cl := agentConfigV1alpha1OnlyClient()
	ac := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "needs-v1alpha2", Namespace: "default"},
		Spec: kelos.AgentConfigSpec{
			MCPServers: []kelos.MCPServerSpec{{
				Name: "server",
				Type: "stdio",
				Env: []corev1.EnvVar{{
					Name: "TOKEN",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "mcp-secret"},
							Key:                  "token",
						},
					},
				}},
			}},
		},
	}

	err := createAgentConfig(ctx, cl, ac)
	if err == nil {
		t.Fatal("createAgentConfig returned nil, want v1alpha2 requirement error")
	}
	if !strings.Contains(err.Error(), "requires kelos.dev/v1alpha2 CRDs") {
		t.Errorf("error = %v, want v1alpha2 requirement", err)
	}
	if !strings.Contains(err.Error(), "needs-v1alpha2") {
		t.Errorf("error = %v, want AgentConfig name", err)
	}
}

func TestCreateAgentConfigRejectsKanonWithoutV1alpha2(t *testing.T) {
	ctx := context.Background()
	cl := agentConfigV1alpha1OnlyClient()
	ac := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "kanon", Namespace: "default"},
		Spec: kelos.AgentConfigSpec{
			Kanon: &kelos.KanonSourceSpec{Repo: "https://github.com/example/kanon-config.git"},
		},
	}

	err := createAgentConfig(ctx, cl, ac)
	if err == nil {
		t.Fatal("createAgentConfig returned nil, want v1alpha2 requirement error")
	}
	if !strings.Contains(err.Error(), "requires kelos.dev/v1alpha2 CRDs") {
		t.Errorf("error = %v, want v1alpha2 requirement", err)
	}
	if !strings.Contains(err.Error(), "kanon") {
		t.Errorf("error = %v, want AgentConfig name", err)
	}
}

func TestDeleteAgentConfigFallsBackToV1alpha1(t *testing.T) {
	ctx := context.Background()
	cl := agentConfigV1alpha1OnlyClient(
		&kelosv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"},
		},
	)

	if err := deleteAgentConfig(ctx, cl, "legacy", "default"); err != nil {
		t.Fatalf("deleteAgentConfig: %v", err)
	}

	got := &kelosv1alpha1.AgentConfig{}
	err := cl.Get(ctx, client.ObjectKey{Name: "legacy", Namespace: "default"}, got)
	if err == nil {
		t.Fatal("fallback object still exists after delete")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Get fallback object error = %v, want NotFound", err)
	}
}

func agentConfigV1alpha1OnlyClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if isV1alpha2AgentConfigObject(obj) {
					return agentConfigV1alpha2NoMatchError()
				}
				return inner.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, inner client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kelos.AgentConfigList); ok {
					return agentConfigV1alpha2NoMatchError()
				}
				return inner.List(ctx, list, opts...)
			},
			Create: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if isV1alpha2AgentConfigObject(obj) {
					return agentConfigV1alpha2NoMatchError()
				}
				return inner.Create(ctx, obj, opts...)
			},
			Delete: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if isV1alpha2AgentConfigObject(obj) {
					return agentConfigV1alpha2NoMatchError()
				}
				return inner.Delete(ctx, obj, opts...)
			},
		}).
		Build()
}

func isV1alpha2AgentConfigObject(obj client.Object) bool {
	_, ok := obj.(*kelos.AgentConfig)
	return ok
}

func agentConfigV1alpha2NoMatchError() error {
	return &meta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: "kelos.dev", Kind: "AgentConfig"},
		SearchedVersions: []string{"v1alpha2"},
	}
}
