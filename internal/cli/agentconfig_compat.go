package cli

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/conversion"
)

func getAgentConfig(ctx context.Context, cl client.Client, key client.ObjectKey) (*kelos.AgentConfig, client.Object, error) {
	ac := &kelos.AgentConfig{}
	if err := cl.Get(ctx, key, ac); err == nil {
		ac.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfig"))
		return ac, ac, nil
	} else if !meta.IsNoMatchError(err) {
		return nil, nil, err
	}

	v1 := &kelosv1alpha1.AgentConfig{}
	if err := cl.Get(ctx, key, v1); err != nil {
		return nil, nil, err
	}
	v1.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("AgentConfig"))
	converted := &kelos.AgentConfig{}
	if err := conversion.AgentConfigToV1alpha2(ctx, v1, converted); err != nil {
		return nil, nil, err
	}
	converted.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfig"))
	return converted, v1, nil
}

func listAgentConfigs(ctx context.Context, cl client.Client, opts ...client.ListOption) ([]kelos.AgentConfig, client.ObjectList, error) {
	acList := &kelos.AgentConfigList{}
	if err := cl.List(ctx, acList, opts...); err == nil {
		acList.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfigList"))
		return acList.Items, acList, nil
	} else if !meta.IsNoMatchError(err) {
		return nil, nil, err
	}

	v1List := &kelosv1alpha1.AgentConfigList{}
	if err := cl.List(ctx, v1List, opts...); err != nil {
		return nil, nil, err
	}
	v1List.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("AgentConfigList"))
	items := make([]kelos.AgentConfig, 0, len(v1List.Items))
	for i := range v1List.Items {
		converted := &kelos.AgentConfig{}
		if err := conversion.AgentConfigToV1alpha2(ctx, &v1List.Items[i], converted); err != nil {
			return nil, nil, err
		}
		converted.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfig"))
		items = append(items, *converted)
	}
	return items, v1List, nil
}

func createAgentConfig(ctx context.Context, cl client.Client, ac *kelos.AgentConfig) error {
	if err := cl.Create(ctx, ac); err == nil {
		return nil
	} else if !meta.IsNoMatchError(err) {
		return err
	}

	if !agentConfigFitsV1alpha1(ac) {
		return fmt.Errorf("creating agentconfig %q requires kelos.dev/v1alpha2 CRDs because it uses fields not supported by v1alpha1; run 'kelos install' to upgrade the CRDs", ac.Name)
	}
	v1 := &kelosv1alpha1.AgentConfig{}
	if err := conversion.AgentConfigFromV1alpha2(ctx, ac, v1); err != nil {
		return err
	}
	v1.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("AgentConfig"))
	return cl.Create(ctx, v1)
}

func deleteAgentConfig(ctx context.Context, cl client.Client, name, namespace string) error {
	ac := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := cl.Delete(ctx, ac); err == nil {
		return nil
	} else if !meta.IsNoMatchError(err) {
		return err
	}

	v1 := &kelosv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	return cl.Delete(ctx, v1)
}

func agentConfigFitsV1alpha1(ac *kelos.AgentConfig) bool {
	if ac.Spec.Kanon != nil {
		return false
	}
	for _, server := range ac.Spec.MCPServers {
		for _, env := range server.Env {
			if env.ValueFrom != nil {
				return false
			}
		}
	}
	return true
}
