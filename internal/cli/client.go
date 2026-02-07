package cli

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(axonv1alpha1.AddToScheme(scheme))
}

// ClientConfig holds configuration for Kubernetes client creation.
type ClientConfig struct {
	Kubeconfig string
	Namespace  string
	Config     *Config
}

// NewClient creates a controller-runtime client and resolves the namespace.
func (c *ClientConfig) NewClient() (client.Client, string, error) {
	restConfig, ns, err := c.resolveConfig()
	if err != nil {
		return nil, "", err
	}
	cl, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("creating client: %w", err)
	}
	return cl, ns, nil
}

// NewClientset creates a kubernetes.Clientset and resolves the namespace.
func (c *ClientConfig) NewClientset() (*kubernetes.Clientset, string, error) {
	restConfig, ns, err := c.resolveConfig()
	if err != nil {
		return nil, "", err
	}
	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, "", fmt.Errorf("creating clientset: %w", err)
	}
	return cs, ns, nil
}

func (c *ClientConfig) resolveConfig() (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.Kubeconfig != "" {
		rules.ExplicitPath = c.Kubeconfig
	}
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})

	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}

	ns := c.Namespace
	if ns == "" {
		ns, _, err = config.Namespace()
		if err != nil {
			return nil, "", fmt.Errorf("resolving namespace: %w", err)
		}
		if ns == "" {
			ns = "default"
		}
	}

	return restConfig, ns, nil
}
