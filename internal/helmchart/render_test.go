package helmchart

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/manifests"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"
)

// imageLatestRefRE matches actual image references ending in ":latest" while
// ignoring narrative occurrences in CRD descriptions like "Defaults to Always
// if :latest tag is specified" — the leading non-whitespace requirement
// distinguishes "registry/name:latest" from " :latest" prose.
var imageLatestRefRE = regexp.MustCompile(`\S:latest`)

func TestHelmReleaseSecretFitsKubernetesLimit(t *testing.T) {
	tests := []struct {
		name        string
		installCRDs bool
	}{
		{name: "controller only", installCRDs: false},
		{name: "controller and CRDs", installCRDs: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"crds": map[string]interface{}{"install": tt.installCRDs},
			}
			ch, err := loadChart(manifests.ChartFS)
			if err != nil {
				t.Fatalf("loading chart: %v", err)
			}
			if err := chartutil.ProcessDependenciesWithMerge(ch, vals); err != nil {
				t.Fatalf("processing chart dependencies: %v", err)
			}
			manifest, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering Helm release: %v", err)
			}
			rel := &release.Release{
				Name:      "kelos",
				Namespace: "kelos-system",
				Chart:     ch,
				Config:    vals,
				Info:      &release.Info{Status: release.StatusPendingInstall},
				Manifest:  string(manifest),
				Version:   1,
			}

			releaseJSON, err := json.Marshal(rel)
			if err != nil {
				t.Fatalf("marshaling Helm release: %v", err)
			}
			var compressed bytes.Buffer
			writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
			if err != nil {
				t.Fatalf("creating gzip writer: %v", err)
			}
			if _, err := writer.Write(releaseJSON); err != nil {
				t.Fatalf("compressing Helm release: %v", err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("closing gzip writer: %v", err)
			}

			secretDataSize := base64.StdEncoding.EncodedLen(compressed.Len())
			t.Logf("Helm release Secret data size: %d bytes", secretDataSize)
			if secretDataSize > corev1.MaxSecretSize {
				t.Errorf("Helm release Secret data is %d bytes, want at most %d", secretDataSize, corev1.MaxSecretSize)
			}
		})
	}
}

func TestRender_NilValues(t *testing.T) {
	data, err := Render(manifests.ChartFS, nil)
	if err != nil {
		t.Fatalf("rendering chart with nil values: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty rendered output")
	}
	output := string(data)
	for _, expected := range []string{
		"kind: Namespace",
		"kind: ServiceAccount",
		"kind: ClusterRole",
		"kind: Deployment",
		"kind: CronJob",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected rendered output to contain %q", expected)
		}
	}
	if strings.Contains(output, "kind: CustomResourceDefinition") {
		t.Error("expected default chart render to omit CRDs")
	}
	if !imageLatestRefRE.MatchString(output) {
		t.Error("expected :latest image refs in rendered output when using default values")
	}
}

func TestRender_DefaultValues(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag": "v0.0.0-test",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty rendered output")
	}
	output := string(data)
	for _, expected := range []string{
		"kind: Namespace",
		"kind: ServiceAccount",
		"kind: ClusterRole",
		"kind: Deployment",
		"kind: CronJob",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected rendered output to contain %q", expected)
		}
	}
	if strings.Contains(output, "kind: CustomResourceDefinition") {
		t.Error("expected default chart render to omit CRDs")
	}
}

func TestRender_VersionOverride(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag": "v1.2.3",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if imageLatestRefRE.MatchString(output) {
		t.Error("expected no :latest image refs in rendered output")
	}
	if !strings.Contains(output, ":v1.2.3") {
		t.Error("expected :v1.2.3 tags in rendered output")
	}
	for _, expected := range []string{
		"--version=v1.2.3",
		"--claude-code-image=ghcr.io/kelos-dev/claude-code",
		"--codex-image=ghcr.io/kelos-dev/codex",
		"--gemini-image=ghcr.io/kelos-dev/gemini",
		"--opencode-image=ghcr.io/kelos-dev/opencode",
		"--cursor-image=ghcr.io/kelos-dev/cursor",
		"--spawner-image=ghcr.io/kelos-dev/kelos-spawner",
		"--worker-runner-image=ghcr.io/kelos-dev/kelos-worker-runner",
		"--session-runtime-image=ghcr.io/kelos-dev/kelos-session-runtime",
		"--ghproxy-image=ghcr.io/kelos-dev/ghproxy",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected controller arguments to contain %q", expected)
		}
	}
	if strings.Contains(output, "--session-runtime-image=ghcr.io/kelos-dev/kelos-session-runtime:v1.2.3") {
		t.Error("expected the controller to apply the shared version to the Session runtime image")
	}
}

func TestRender_TaggedManagedImageOverride(t *testing.T) {
	data, err := Render(manifests.ChartFS, map[string]interface{}{
		"image": map[string]interface{}{"tag": "v1.2.3"},
		"sessionRuntime": map[string]interface{}{
			"image": "example.com/session-runtime:custom",
		},
	})
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if !strings.Contains(string(data), "--session-runtime-image=example.com/session-runtime:custom") {
		t.Error("expected tagged Session runtime image override to remain unchanged")
	}
}

func TestRender_PullPolicy(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag":        "latest",
			"pullPolicy": "IfNotPresent",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "imagePullPolicy: IfNotPresent") {
		t.Error("expected imagePullPolicy: IfNotPresent in rendered output")
	}
}

func TestRender_DisableTelemetry(t *testing.T) {
	vals := map[string]interface{}{
		"telemetry": map[string]interface{}{
			"enabled": false,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "kelos-telemetry") {
		t.Error("expected kelos-telemetry CronJob to be excluded")
	}
}

func TestRender_ResourceOrdering(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	// CRDs must appear before Namespace, and Namespace must appear before
	// Deployment and CronJob so that dependencies exist when resources are applied.
	crdIdx := strings.Index(output, "kind: CustomResourceDefinition")
	nsIdx := strings.Index(output, "kind: Namespace")
	deployIdx := strings.Index(output, "kind: Deployment")
	cronIdx := strings.Index(output, "kind: CronJob")
	if crdIdx < 0 || nsIdx < 0 || deployIdx < 0 || cronIdx < 0 {
		t.Fatal("expected CustomResourceDefinition, Namespace, Deployment, and CronJob in rendered output")
	}
	if crdIdx >= nsIdx {
		t.Error("expected CustomResourceDefinition to appear before Namespace")
	}
	if nsIdx >= deployIdx {
		t.Error("expected Namespace to appear before Deployment")
	}
	if nsIdx >= cronIdx {
		t.Error("expected Namespace to appear before CronJob")
	}
}

func TestRender_DisableCRDs(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": false,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "kind: CustomResourceDefinition") {
		t.Error("expected no CRDs when crds.install is false")
	}
	if !strings.Contains(output, "kind: Namespace") {
		t.Error("expected Namespace to still be present")
	}
}

func TestRender_IncludesSessionCRDs(t *testing.T) {
	data, err := Render(manifests.ChartFS, map[string]interface{}{
		"crds": map[string]interface{}{"install": true},
	})
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	for _, name := range []string{"sessions.kelos.dev", "sessionspawners.kelos.dev"} {
		if !strings.Contains(string(data), "name: "+name) {
			t.Errorf("expected rendered chart to include the %s CRD", name)
		}
	}
}

func TestRender_ConsoleServer(t *testing.T) {
	data, err := Render(manifests.ChartFS, map[string]interface{}{
		"consoleServer": map[string]interface{}{
			"enabled":          true,
			"secretName":       "console-auth",
			"defaultNamespace": "team-a",
		},
	})
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	for _, expected := range []string{
		"name: kelos-console-server",
		"secretName: console-auth",
		"resources:\n      - pods\n    verbs:\n      - get\n  - apiGroups:\n      - \"\"\n    resources:\n      - pods/log\n    verbs:\n      - get\n  - apiGroups:\n      - \"\"\n    resources:\n      - pods/exec",
		"resources:\n      - agentconfigs\n      - sessions\n      - sessionspawners\n      - taskbudgets\n      - taskrecords\n      - tasks\n      - taskspawners\n      - workerpools\n      - workspaces\n    verbs:\n      - get\n      - list",
		"resources:\n      - sessions\n    verbs:\n      - create\n      - delete\n      - patch\n      - watch",
		"--token-file=/var/run/secrets/kelos-console/token",
		"--default-namespace=team-a",
		"kind: ClusterRole\nmetadata:\n  name: kelos-console-server-role",
		"kind: ClusterRoleBinding\nmetadata:\n  name: kelos-console-server-rolebinding",
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: kelos-console-server-role",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected Console server render to contain %q", expected)
		}
	}
}

func TestRender_ConsoleServerRequiresSecret(t *testing.T) {
	_, err := Render(manifests.ChartFS, map[string]interface{}{
		"consoleServer": map[string]interface{}{"enabled": true},
	})
	if err == nil || !strings.Contains(err.Error(), "consoleServer.secretName is required") {
		t.Fatalf("Render() error = %v", err)
	}
}

func TestRender_RejectsSessionServerValues(t *testing.T) {
	_, err := Render(manifests.ChartFS, map[string]interface{}{
		"sessionServer": map[string]interface{}{"enabled": true},
	})
	if err == nil || !strings.Contains(err.Error(), "sessionServer values are not supported; use consoleServer instead") {
		t.Fatalf("Render() error = %v", err)
	}
}

func TestRender_AllowsAbsentOrDisabledSessionServerValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]interface{}
	}{
		{name: "absent"},
		{name: "disabled", values: map[string]interface{}{
			"sessionServer": map[string]interface{}{"enabled": false},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Render(manifests.ChartFS, tt.values); err != nil {
				t.Fatalf("Render() error = %v", err)
			}
		})
	}
}

func TestRender_TaskSpawnerTemplatePlaceholdersRemainLiteral(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, `Supports Go text/template variables from the work item, e.g. "kelos-task-{{.Number}}".`) {
		t.Error("expected branch placeholder example to remain literal in rendered CRD output")
	}
	// Each placeholder appears in the Branch, PromptTemplate, and NameTemplate
	// godoc of TaskTemplate in the TaskSpawner CRD schema.
	for _, expected := range []string{
		"Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}",
		"GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}",
		"GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}",
		"Cron sources: {{.Time}}, {{.Schedule}}",
	} {
		if count := strings.Count(output, expected); count != 3 {
			t.Errorf("expected %q to appear three times in TaskSpawner CRD descriptions, got %d", expected, count)
		}
	}
}

func TestRender_CRDKeepAnnotation(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
			"keep":    true,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "helm.sh/resource-policy") {
		t.Error("expected helm.sh/resource-policy annotation when crds.keep is true")
	}
}

func TestRender_CRDKeepAnnotationByDefaultWhenCRDsAreInstalled(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "helm.sh/resource-policy") {
		t.Error("expected helm.sh/resource-policy annotation by default")
	}
}

func TestRender_CRDNoKeepAnnotation(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
			"keep":    false,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "helm.sh/resource-policy") {
		t.Error("expected no helm.sh/resource-policy annotation when crds.keep is false")
	}
}

func TestRender_LinearWebhookApiKeySecret(t *testing.T) {
	tests := []struct {
		name             string
		apiKeySecretName string
		wantEnvVar       bool
	}{
		{
			name:             "apiKeySecretName set injects LINEAR_API_KEY env var",
			apiKeySecretName: "my-linear-api-secret",
			wantEnvVar:       true,
		},
		{
			name:             "apiKeySecretName empty omits LINEAR_API_KEY env var",
			apiKeySecretName: "",
			wantEnvVar:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						"linear": map[string]interface{}{
							"enabled":          true,
							"replicas":         1,
							"secretName":       "linear-webhook-secret",
							"apiKeySecretName": tt.apiKeySecretName,
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)
			if tt.wantEnvVar {
				if !strings.Contains(output, "LINEAR_API_KEY") {
					t.Error("expected LINEAR_API_KEY env var in rendered output")
				}
				if !strings.Contains(output, tt.apiKeySecretName) {
					t.Errorf("expected secret name %q in rendered output", tt.apiKeySecretName)
				}
			} else {
				if strings.Contains(output, "LINEAR_API_KEY") {
					t.Error("expected no LINEAR_API_KEY env var in rendered output")
				}
			}
		})
	}
}

func TestRender_WebhookServiceType(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		serviceType string
	}{
		{
			name:        "github service type LoadBalancer",
			source:      "github",
			serviceType: "LoadBalancer",
		},
		{
			name:        "linear service type NodePort",
			source:      "linear",
			serviceType: "NodePort",
		},
		{
			name:        "generic service type LoadBalancer",
			source:      "generic",
			serviceType: "LoadBalancer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						tt.source: map[string]interface{}{
							"enabled":    true,
							"replicas":   1,
							"secretName": tt.source + "-webhook-secret",
							"service": map[string]interface{}{
								"type": tt.serviceType,
							},
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)
			expected := "type: " + tt.serviceType
			if !strings.Contains(output, expected) {
				t.Errorf("expected rendered output to contain %q", expected)
			}
		})
	}
}

func TestRender_WebhookServiceTypeDefault(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"sources": map[string]interface{}{
				"github": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "github-webhook-secret",
				},
			},
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "type: ClusterIP") {
		t.Error("expected default service type to be ClusterIP")
	}
}

func TestRender_GatewayServerMetadataAndServiceAccount(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"gatewayServer": map[string]interface{}{
				"enabled":            true,
				"serviceAccountName": "custom-webhook",
				"labels": map[string]interface{}{
					"example.com/deployment": "enabled",
					"app.kubernetes.io/name": "override",
				},
				"annotations": map[string]interface{}{
					"example.com/deployment-note": "configured",
				},
				"podLabels": map[string]interface{}{
					"example.com/pod":             "enabled",
					"app.kubernetes.io/component": "override",
				},
				"podAnnotations": map[string]interface{}{
					"example.com/pod-note": "configured",
				},
			},
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	deployment := extractResource(t, output, "Deployment", "kelos-webhook-gateway-server")
	for _, expected := range []string{
		"example.com/deployment: enabled",
		"example.com/deployment-note: configured",
		"example.com/pod: enabled",
		"example.com/pod-note: configured",
		"serviceAccountName: custom-webhook",
	} {
		if !strings.Contains(deployment, expected) {
			t.Errorf("expected gateway Deployment to contain %q, got:\n%s", expected, deployment)
		}
	}
	for _, unexpected := range []string{
		"app.kubernetes.io/name: override",
		"app.kubernetes.io/component: override",
	} {
		if strings.Contains(deployment, unexpected) {
			t.Errorf("expected built-in gateway label to take precedence over %q, got:\n%s", unexpected, deployment)
		}
	}
	serviceAccount := extractResource(t, output, "ServiceAccount", "custom-webhook")
	if !strings.Contains(serviceAccount, "namespace: kelos-system") {
		t.Errorf("expected custom gateway ServiceAccount in release namespace, got:\n%s", serviceAccount)
	}
	roleBinding := extractResource(t, output, "ClusterRoleBinding", "kelos-webhook-rolebinding")
	if !strings.Contains(roleBinding, "name: custom-webhook") {
		t.Errorf("expected webhook role binding to include custom gateway ServiceAccount, got:\n%s", roleBinding)
	}
}

func TestRender_GatewayServerRejectsEmptyServiceAccountName(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"gatewayServer": map[string]interface{}{
				"enabled":            true,
				"serviceAccountName": "",
			},
		},
	}
	if _, err := Render(manifests.ChartFS, vals); err == nil {
		t.Fatal("expected error rendering gateway server with empty serviceAccountName")
	} else if !strings.Contains(err.Error(), "serviceAccountName must not be empty") {
		t.Errorf("expected service account validation error, got: %v", err)
	}
}

func TestRender_GatewayServerRejectsReservedServiceAccountNames(t *testing.T) {
	for _, name := range []string{"kelos-controller", "kelos-console-server", "kelos-slack-server"} {
		t.Run(name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"gatewayServer": map[string]interface{}{
						"enabled":            true,
						"serviceAccountName": name,
					},
				},
			}
			if _, err := Render(manifests.ChartFS, vals); err == nil {
				t.Fatalf("expected error rendering gateway server with reserved ServiceAccount %q", name)
			} else if !strings.Contains(err.Error(), "is reserved for another chart component") {
				t.Errorf("expected reserved service account error, got: %v", err)
			}
		})
	}
}

func TestRender_GatewayServerMetricsService(t *testing.T) {
	tests := []struct {
		name                string
		serviceType         string
		wantSeparateMetrics bool
	}{
		{name: "ClusterIP uses primary service", serviceType: "ClusterIP", wantSeparateMetrics: false},
		{name: "LoadBalancer creates internal metrics service", serviceType: "LoadBalancer", wantSeparateMetrics: true},
		{name: "NodePort creates internal metrics service", serviceType: "NodePort", wantSeparateMetrics: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"gatewayServer": map[string]interface{}{
						"enabled": true,
						"service": map[string]interface{}{"type": tt.serviceType},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)
			primary := extractServiceSpec(t, output, "kelos-webhook-gateway-server")
			primaryHasMetrics := strings.Contains(primary, "name: metrics")
			if primaryHasMetrics != !tt.wantSeparateMetrics {
				t.Errorf("primary gateway Service metrics exposure = %v, want %v for type %s", primaryHasMetrics, !tt.wantSeparateMetrics, tt.serviceType)
			}

			metricsName := "kelos-webhook-gateway-server-metrics"
			hasSeparateMetrics := strings.Contains(output, "name: "+metricsName+"\n")
			if hasSeparateMetrics != tt.wantSeparateMetrics {
				t.Errorf("separate metrics Service rendered = %v, want %v", hasSeparateMetrics, tt.wantSeparateMetrics)
			}
			if tt.wantSeparateMetrics {
				metrics := extractServiceSpec(t, output, metricsName)
				for _, expected := range []string{"type: ClusterIP", "name: metrics"} {
					if !strings.Contains(metrics, expected) {
						t.Errorf("expected metrics Service to contain %q, got:\n%s", expected, metrics)
					}
				}
				if strings.Contains(metrics, "name: webhook") {
					t.Errorf("expected metrics Service to omit webhook port, got:\n%s", metrics)
				}
			}
		})
	}
}

func TestRender_WebhookServiceMetricsPortExposure(t *testing.T) {
	tests := []struct {
		name            string
		source          string
		serviceType     string
		wantMetricsPort bool
	}{
		{
			name:            "github ClusterIP exposes metrics port",
			source:          "github",
			serviceType:     "ClusterIP",
			wantMetricsPort: true,
		},
		{
			name:            "github LoadBalancer omits metrics port",
			source:          "github",
			serviceType:     "LoadBalancer",
			wantMetricsPort: false,
		},
		{
			name:            "github NodePort omits metrics port",
			source:          "github",
			serviceType:     "NodePort",
			wantMetricsPort: false,
		},
		{
			name:            "linear LoadBalancer omits metrics port",
			source:          "linear",
			serviceType:     "LoadBalancer",
			wantMetricsPort: false,
		},
		{
			name:            "generic NodePort omits metrics port",
			source:          "generic",
			serviceType:     "NodePort",
			wantMetricsPort: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						tt.source: map[string]interface{}{
							"enabled":    true,
							"replicas":   1,
							"secretName": tt.source + "-webhook-secret",
							"service": map[string]interface{}{
								"type": tt.serviceType,
							},
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)

			serviceName := "kelos-webhook-" + tt.source
			serviceSpec := extractServiceSpec(t, output, serviceName)
			hasMetricsPort := strings.Contains(serviceSpec, "name: metrics")
			if tt.wantMetricsPort && !hasMetricsPort {
				t.Errorf("expected metrics port in %s Service spec, got:\n%s", serviceName, serviceSpec)
			}
			if !tt.wantMetricsPort && hasMetricsPort {
				t.Errorf("expected no metrics port in %s Service spec when type=%s, got:\n%s", serviceName, tt.serviceType, serviceSpec)
			}
			if !strings.Contains(serviceSpec, "name: webhook") {
				t.Errorf("expected webhook port to remain in %s Service spec, got:\n%s", serviceName, serviceSpec)
			}
		})
	}
}

// extractServiceSpec returns the YAML body for the Service named name from the
// rendered chart output, or fails the test if not found.
func extractServiceSpec(t *testing.T, output, name string) string {
	return extractResource(t, output, "Service", name)
}

func extractResource(t *testing.T, output, kind, name string) string {
	t.Helper()
	docs := strings.Split(output, "---\n")
	marker := "name: " + name + "\n"
	for _, doc := range docs {
		if !strings.Contains(doc, "kind: "+kind) {
			continue
		}
		if !strings.Contains(doc, marker) {
			continue
		}
		return doc
	}
	t.Fatalf("%s %q not found in rendered output", kind, name)
	return ""
}

func TestRender_WebhookServiceTypeRejectsUnsupported(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		serviceType string
	}{
		{
			name:        "github ExternalName rejected",
			source:      "github",
			serviceType: "ExternalName",
		},
		{
			name:        "linear bogus type rejected",
			source:      "linear",
			serviceType: "Bogus",
		},
		{
			name:        "generic empty type rejected",
			source:      "generic",
			serviceType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						tt.source: map[string]interface{}{
							"enabled":    true,
							"replicas":   1,
							"secretName": tt.source + "-webhook-secret",
							"service": map[string]interface{}{
								"type": tt.serviceType,
							},
						},
					},
				},
			}
			if _, err := Render(manifests.ChartFS, vals); err == nil {
				t.Fatal("expected error rendering chart with unsupported service type")
			} else if !strings.Contains(err.Error(), "is not supported") {
				t.Errorf("expected validation error, got: %v", err)
			}
		})
	}
}

func TestRender_GatewayServiceTypeRejectsUnsupported(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"gatewayServer": map[string]interface{}{
				"enabled": true,
				"service": map[string]interface{}{"type": "ExternalName"},
			},
		},
	}
	if _, err := Render(manifests.ChartFS, vals); err == nil {
		t.Fatal("expected error rendering gateway server with unsupported service type")
	} else if !strings.Contains(err.Error(), "is not supported") {
		t.Errorf("expected service type validation error, got: %v", err)
	}
}

func TestRender_PodMonitorDisabledByDefault(t *testing.T) {
	data, err := Render(manifests.ChartFS, nil)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "kind: PodMonitor") {
		t.Error("expected no PodMonitor in default render")
	}
	if strings.Contains(output, "monitoring.coreos.com/v1") {
		t.Error("expected no monitoring.coreos.com/v1 resources in default render")
	}
}

func TestRender_PodMonitorEnabled(t *testing.T) {
	vals := map[string]interface{}{
		"podMonitor": map[string]interface{}{
			"enabled":       true,
			"interval":      "45s",
			"scrapeTimeout": "12s",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)

	pm := decodePodMonitor(t, output, "kelos-controlplane")
	if pm["apiVersion"] != "monitoring.coreos.com/v1" {
		t.Errorf("expected apiVersion monitoring.coreos.com/v1, got: %v", pm["apiVersion"])
	}

	// Assert on spec.selector specifically — the app.kubernetes.io/name label is
	// also present under metadata.labels, so a whole-document substring match
	// would not actually verify the selector.
	selector := mapAt(t, specOf(t, pm), "selector")
	matchLabels := mapAt(t, selector, "matchLabels")
	if matchLabels["app.kubernetes.io/name"] != "kelos" {
		t.Errorf("expected spec.selector.matchLabels app.kubernetes.io/name=kelos, got: %v", matchLabels)
	}
	// console-server shares the name label but is not a metrics endpoint, so the
	// selector must exclude it explicitly rather than rely on the port name.
	if !selectorExcludesComponent(selector, "console-server") {
		t.Errorf("expected spec.selector.matchExpressions to exclude console-server via NotIn, got: %v", selector)
	}

	// Control-plane target discovery must cover kelos-system, where the
	// controller manager always runs even when the release namespace differs.
	nsNames := stringSliceAt(t, mapAt(t, specOf(t, pm), "namespaceSelector"), "matchNames")
	if !containsString(nsNames, "kelos-system") {
		t.Errorf("expected spec.namespaceSelector.matchNames to include kelos-system, got: %v", nsNames)
	}

	endpoint := firstEndpoint(t, pm)
	if endpoint["port"] != "metrics" {
		t.Errorf("expected podMetricsEndpoints[0].port=metrics, got: %v", endpoint["port"])
	}
	if endpoint["interval"] != "45s" {
		t.Errorf("expected interval override 45s, got: %v", endpoint["interval"])
	}
	if endpoint["scrapeTimeout"] != "12s" {
		t.Errorf("expected scrapeTimeout override 12s, got: %v", endpoint["scrapeTimeout"])
	}
	if strings.Contains(output, "name: kelos-spawners") {
		t.Error("expected no spawner PodMonitor when podMonitor.spawners.enabled is false")
	}
}

func TestRender_PodMonitorSpawnersEnabled(t *testing.T) {
	vals := map[string]interface{}{
		"podMonitor": map[string]interface{}{
			"enabled": true,
			"spawners": map[string]interface{}{
				"enabled": true,
			},
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)

	// Control-plane PodMonitor still renders.
	extractPodMonitorSpec(t, output, "kelos-controlplane")

	pm := decodePodMonitor(t, output, "kelos-spawners")
	spec := specOf(t, pm)
	matchLabels := mapAt(t, mapAt(t, spec, "selector"), "matchLabels")
	if matchLabels["kelos.dev/component"] != "spawner" {
		t.Errorf("expected spec.selector.matchLabels kelos.dev/component=spawner, got: %v", matchLabels)
	}
	if nsAny := mapAt(t, spec, "namespaceSelector")["any"]; nsAny != true {
		t.Errorf("expected spec.namespaceSelector.any=true for cross-namespace spawner scraping, got: %v", nsAny)
	}
}

func TestRender_PodMonitorSpawnersRequiresParentEnabled(t *testing.T) {
	vals := map[string]interface{}{
		"podMonitor": map[string]interface{}{
			"enabled": false,
			"spawners": map[string]interface{}{
				"enabled": true,
			},
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if strings.Contains(string(data), "kind: PodMonitor") {
		t.Error("expected no PodMonitor when podMonitor.enabled is false, even with spawners.enabled true")
	}
}

func TestRender_PodMonitorLabelsAnnotations(t *testing.T) {
	vals := map[string]interface{}{
		"podMonitor": map[string]interface{}{
			"enabled":     true,
			"labels":      map[string]interface{}{"release": "kube-prometheus-stack"},
			"annotations": map[string]interface{}{"owner": "platform-team"},
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	spec := extractPodMonitorSpec(t, string(data), "kelos-controlplane")
	if !strings.Contains(spec, "release: kube-prometheus-stack") {
		t.Errorf("expected custom label release: kube-prometheus-stack, got:\n%s", spec)
	}
	if !strings.Contains(spec, "owner: platform-team") {
		t.Errorf("expected custom annotation owner: platform-team, got:\n%s", spec)
	}
}

// extractPodMonitorSpec returns the YAML body for the PodMonitor named name from
// the rendered chart output, or fails the test if not found.
func extractPodMonitorSpec(t *testing.T, output, name string) string {
	t.Helper()
	docs := strings.Split(output, "---\n")
	marker := "name: " + name + "\n"
	for _, doc := range docs {
		if !strings.Contains(doc, "kind: PodMonitor") {
			continue
		}
		if !strings.Contains(doc, marker) {
			continue
		}
		return doc
	}
	t.Fatalf("PodMonitor %q not found in rendered output", name)
	return ""
}

// decodePodMonitor extracts and YAML-decodes the PodMonitor named name so tests
// can assert on specific fields (e.g. spec.selector) rather than substring-match
// the whole document, which would collide with metadata.labels.
func decodePodMonitor(t *testing.T, output, name string) map[string]interface{} {
	t.Helper()
	doc := extractPodMonitorSpec(t, output, name)
	var pm map[string]interface{}
	if err := sigyaml.Unmarshal([]byte(doc), &pm); err != nil {
		t.Fatalf("decoding PodMonitor %q: %v\n%s", name, err, doc)
	}
	return pm
}

func specOf(t *testing.T, pm map[string]interface{}) map[string]interface{} {
	t.Helper()
	return mapAt(t, pm, "spec")
}

// mapAt returns m[key] as a map, failing the test if it is missing or not a map.
func mapAt(t *testing.T, m map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	v, ok := m[key].(map[string]interface{})
	if !ok {
		t.Fatalf("expected %q to be a map, got: %v", key, m[key])
	}
	return v
}

// stringSliceAt returns m[key] as a []string, failing the test if it is missing
// or not a list of strings.
func stringSliceAt(t *testing.T, m map[string]interface{}, key string) []string {
	t.Helper()
	raw, ok := m[key].([]interface{})
	if !ok {
		t.Fatalf("expected %q to be a list, got: %v", key, m[key])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("expected %q entries to be strings, got: %v", key, v)
		}
		out = append(out, s)
	}
	return out
}

// firstEndpoint returns spec.podMetricsEndpoints[0] of the PodMonitor.
func firstEndpoint(t *testing.T, pm map[string]interface{}) map[string]interface{} {
	t.Helper()
	eps, ok := specOf(t, pm)["podMetricsEndpoints"].([]interface{})
	if !ok || len(eps) == 0 {
		t.Fatalf("expected at least one podMetricsEndpoints entry, got: %v", specOf(t, pm)["podMetricsEndpoints"])
	}
	ep, ok := eps[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected podMetricsEndpoints[0] to be a map, got: %v", eps[0])
	}
	return ep
}

// selectorExcludesComponent reports whether the label selector carries a
// matchExpressions entry excluding the given app.kubernetes.io/component value.
func selectorExcludesComponent(selector map[string]interface{}, component string) bool {
	exprs, ok := selector["matchExpressions"].([]interface{})
	if !ok {
		return false
	}
	for _, e := range exprs {
		m, ok := e.(map[string]interface{})
		if !ok || m["key"] != "app.kubernetes.io/component" || m["operator"] != "NotIn" {
			continue
		}
		vals, ok := m["values"].([]interface{})
		if !ok {
			continue
		}
		for _, v := range vals {
			if v == component {
				return true
			}
		}
	}
	return false
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func TestRender_ParseableOutput(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag": "v0.0.0-test",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	// Verify each non-empty YAML document is actually parseable. Use the
	// Kubernetes YAML reader rather than splitting on "---\n", since the
	// rendered chart contains literal text like "rw-rw----" inside CRD
	// descriptions that would falsely match a naive separator search.
	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	validDocs := 0
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading YAML document: %v", err)
		}
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) == 0 {
			continue
		}
		var obj map[string]interface{}
		if err := sigyaml.Unmarshal(trimmed, &obj); err != nil {
			t.Errorf("invalid YAML document: %v\n---\n%s", err, trimmed)
		}
		validDocs++
	}
	if validDocs == 0 {
		t.Fatal("expected at least one valid YAML document in rendered output")
	}
}
