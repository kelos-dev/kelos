package reporting

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubeSecretReader reads secrets from the Kubernetes API.
type KubeSecretReader struct {
	Client client.Client
}

func (r *KubeSecretReader) ReadHeaders(ctx context.Context, namespace, name string) (map[string]string, error) {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, fmt.Errorf("getting secret %s/%s: %w", namespace, name, err)
	}
	headers := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		headers[k] = string(v)
	}
	return headers, nil
}
