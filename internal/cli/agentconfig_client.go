package cli

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func getAgentConfig(ctx context.Context, cl client.Client, key client.ObjectKey) (*kelos.AgentConfig, client.Object, error) {
	ac := &kelos.AgentConfig{}
	if err := cl.Get(ctx, key, ac); err != nil {
		return nil, nil, err
	}
	ac.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfig"))
	return ac, ac, nil
}

func listAgentConfigs(ctx context.Context, cl client.Client, opts ...client.ListOption) ([]kelos.AgentConfig, client.ObjectList, error) {
	list := &kelos.AgentConfigList{}
	if err := cl.List(ctx, list, opts...); err != nil {
		return nil, nil, err
	}
	list.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfigList"))
	return list.Items, list, nil
}

func createAgentConfig(ctx context.Context, cl client.Client, ac *kelos.AgentConfig) error {
	return cl.Create(ctx, ac)
}

func deleteAgentConfig(ctx context.Context, cl client.Client, name, namespace string) error {
	return cl.Delete(ctx, &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
}
