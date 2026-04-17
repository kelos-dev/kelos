package cli

import (
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
)

func TestRenderChart_WebhookServers(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"image": "ghcr.io/kelos-dev/kelos-webhook-server",
			"sources": map[string]interface{}{
				"github": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "github-webhook-secret",
				},
				"linear": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "linear-webhook-secret",
				},
			},
			"ingress": map[string]interface{}{
				"enabled":   true,
				"className": "nginx",
				"host":      "webhooks.example.com",
			},
		},
		"image": map[string]interface{}{
			"tag": "latest",
		},
	}

	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}

	content := string(data)

	// Check for webhook components
	expectedComponents := []string{
		"name: kelos-webhook-github",
		"name: kelos-webhook-linear",
		"kind: Ingress",
		"name: kelos-webhook-role",
		"name: kelos-webhook",
		"app.kubernetes.io/component: webhook-github",
		"app.kubernetes.io/component: webhook-linear",
		"--source=github",
		"--source=linear",
		"github-webhook-secret",
		"linear-webhook-secret",
	}

	for _, component := range expectedComponents {
		if !strings.Contains(content, component) {
			t.Errorf("expected rendered chart to contain %q", component)
		}
	}
}

func TestRenderChart_WebhookGateway(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"image": "ghcr.io/kelos-dev/kelos-webhook-server",
			"sources": map[string]interface{}{
				"github": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "github-webhook-secret",
				},
				"linear": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "linear-webhook-secret",
				},
			},
			"gateway": map[string]interface{}{
				"enabled":          true,
				"gatewayClassName": "istio",
				"gatewayName":      "kelos-webhook-gateway",
				"host":             "webhooks.example.com",
				"tls": map[string]interface{}{
					"enabled": true,
				},
			},
		},
		"image": map[string]interface{}{
			"tag": "latest",
		},
	}

	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}

	content := string(data)

	// Check for Gateway API components
	expectedComponents := []string{
		"kind: Gateway",
		"kind: HTTPRoute",
		"name: kelos-webhook-gateway",
		"name: kelos-webhook-routes",
		"gatewayClassName: istio",
		"/webhook/github",
		"kelos-webhook-github",
		"/webhook/linear",
		"kelos-webhook-linear",
	}

	for _, component := range expectedComponents {
		if !strings.Contains(content, component) {
			t.Errorf("expected rendered chart to contain %q", component)
		}
	}
}

func TestRenderChart_WebhookLinearOnly(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"image": "ghcr.io/kelos-dev/kelos-webhook-server",
			"sources": map[string]interface{}{
				"github": map[string]interface{}{
					"enabled": false,
				},
				"linear": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "linear-webhook-secret",
				},
			},
		},
		"image": map[string]interface{}{
			"tag": "latest",
		},
	}

	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}

	content := string(data)

	expectedComponents := []string{
		"name: kelos-webhook-linear",
		"name: kelos-webhook-role",
		"name: kelos-webhook-rolebinding",
		"name: kelos-webhook",
		"--source=linear",
		"linear-webhook-secret",
	}
	for _, component := range expectedComponents {
		if !strings.Contains(content, component) {
			t.Errorf("expected rendered chart to contain %q", component)
		}
	}

	unexpectedComponents := []string{
		"name: kelos-webhook-github",
		"--source=github",
	}
	for _, component := range unexpectedComponents {
		if strings.Contains(content, component) {
			t.Errorf("did not expect rendered chart to contain %q for linear-only webhook install", component)
		}
	}
}

func TestRenderChart_WebhookServersDisabled(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"sources": map[string]interface{}{
				"github": map[string]interface{}{
					"enabled": false,
				},
				"linear": map[string]interface{}{
					"enabled": false,
				},
			},
		},
		"image": map[string]interface{}{
			"tag": "latest",
		},
	}

	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}

	content := string(data)

	// Should not contain webhook components when disabled
	unexpectedComponents := []string{
		"name: kelos-webhook-github",
		"name: kelos-webhook-linear",
		"--source=github",
		"--source=linear",
		"name: kelos-webhook-role",
	}

	for _, component := range unexpectedComponents {
		if strings.Contains(content, component) {
			t.Errorf("did not expect rendered chart to contain %q when webhooks disabled", component)
		}
	}
}
