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

func TestRender_SessionServer(t *testing.T) {
	data, err := Render(manifests.ChartFS, map[string]interface{}{
		"sessionServer": map[string]interface{}{
			"enabled":          true,
			"secretName":       "session-auth",
			"defaultNamespace": "team-a",
		},
	})
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	for _, expected := range []string{
		"name: kelos-session-server",
		"secretName: session-auth",
		"resources:\n      - pods/exec",
		"resources:\n      - agentconfigs\n      - workspaces\n    verbs:\n      - list",
		"resources:\n      - sessions\n    verbs:\n      - create\n      - delete\n      - get\n      - list\n      - patch\n      - watch",
		"--token-file=/var/run/secrets/kelos-session/token",
		"--default-namespace=team-a",
		"kind: ClusterRole\nmetadata:\n  name: kelos-session-server-role",
		"kind: ClusterRoleBinding\nmetadata:\n  name: kelos-session-server-rolebinding",
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: kelos-session-server-role",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected Session server render to contain %q", expected)
		}
	}
}

func TestRender_SessionServerRequiresSecret(t *testing.T) {
	_, err := Render(manifests.ChartFS, map[string]interface{}{
		"sessionServer": map[string]interface{}{"enabled": true},
	})
	if err == nil || !strings.Contains(err.Error(), "sessionServer.secretName is required") {
		t.Fatalf("Render() error = %v", err)
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
	// Each placeholder appears in the Branch and PromptTemplate godoc of
	// TaskTemplate across both served TaskSpawner CRD schemas (4), plus the
	// NameTemplate godoc that exists only in the latest version (1).
	for _, expected := range []string{
		"Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}",
		"GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}",
		"GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}",
		"Cron sources: {{.Time}}, {{.Schedule}}",
	} {
		if count := strings.Count(output, expected); count != 5 {
			t.Errorf("expected %q to appear five times in TaskSpawner CRD descriptions, got %d", expected, count)
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
	t.Helper()
	docs := strings.Split(output, "---\n")
	marker := "name: " + name + "\n"
	for _, doc := range docs {
		if !strings.Contains(doc, "kind: Service") {
			continue
		}
		if !strings.Contains(doc, marker) {
			continue
		}
		return doc
	}
	t.Fatalf("Service %q not found in rendered output", name)
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

	spec := extractPodMonitorSpec(t, output, "kelos-controlplane")
	if !strings.Contains(spec, "apiVersion: monitoring.coreos.com/v1") {
		t.Errorf("expected monitoring.coreos.com/v1 PodMonitor, got:\n%s", spec)
	}
	if !strings.Contains(spec, "app.kubernetes.io/name: kelos") {
		t.Errorf("expected control-plane selector on app.kubernetes.io/name: kelos, got:\n%s", spec)
	}
	// session-server shares the name label but is not a metrics endpoint, so
	// the selector must exclude it explicitly rather than rely on the port name.
	if !strings.Contains(spec, "session-server") || !strings.Contains(spec, "NotIn") {
		t.Errorf("expected control-plane selector to exclude session-server via NotIn, got:\n%s", spec)
	}
	if !strings.Contains(spec, "port: metrics") {
		t.Errorf("expected metrics port endpoint, got:\n%s", spec)
	}
	if !strings.Contains(spec, "interval: 45s") {
		t.Errorf("expected interval override 45s, got:\n%s", spec)
	}
	if !strings.Contains(spec, "scrapeTimeout: 12s") {
		t.Errorf("expected scrapeTimeout override 12s, got:\n%s", spec)
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

	spec := extractPodMonitorSpec(t, output, "kelos-spawners")
	if !strings.Contains(spec, "kelos.dev/component: spawner") {
		t.Errorf("expected spawner selector on kelos.dev/component: spawner, got:\n%s", spec)
	}
	if !strings.Contains(spec, "any: true") {
		t.Errorf("expected namespaceSelector any: true for cross-namespace spawner scraping, got:\n%s", spec)
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
