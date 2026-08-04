package helmchart

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/manifests"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	// session-server shares the name label but is not a metrics endpoint, so the
	// selector must exclude it explicitly rather than rely on the port name.
	if !selectorExcludesComponent(selector, "session-server") {
		t.Errorf("expected spec.selector.matchExpressions to exclude session-server via NotIn, got: %v", selector)
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

// The CRD adoption loop in the chart README is hand-maintained and has no
// generator, so a new CRD is easy to forget there. Missing one leaves it without
// Helm ownership metadata, and the next `helm upgrade` fails on that CRD.
func TestChartREADMEAdoptionLoopListsEveryCRD(t *testing.T) {
	readme, err := fs.ReadFile(manifests.ChartFS, "README.md")
	if err != nil {
		t.Fatalf("reading chart README: %v", err)
	}

	entries, err := fs.ReadDir(manifests.ChartFS, "charts/kelos-crds/templates")
	if err != nil {
		t.Fatalf("reading CRD templates: %v", err)
	}

	var found int
	for _, entry := range entries {
		data, err := fs.ReadFile(manifests.ChartFS, "charts/kelos-crds/templates/"+entry.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		// Each template holds one CRD whose metadata.name is the resource name.
		for _, line := range strings.Split(string(data), "\n") {
			name, ok := strings.CutPrefix(strings.TrimSpace(line), "name: ")
			if !ok || !strings.HasSuffix(name, ".kelos.dev") {
				continue
			}
			found++
			if !strings.Contains(string(readme), name) {
				t.Errorf("chart README does not list %s in the CRD adoption loop", name)
			}
			break
		}
	}

	if found == 0 {
		t.Fatal("found no CRD names in the chart templates; the test is not checking anything")
	}
}

// The scoring code paths write TaskRecord labels with a merge patch and manage
// TaskScores, and RBAC verbs are not derivable from the code by any test that uses
// the fake client — it does not enforce RBAC. These assertions pin the verbs the
// two roles actually need, so swapping an update for a patch (or vice versa)
// cannot silently produce a 403 in a real cluster.
func TestRender_ScoringRBACVerbs(t *testing.T) {
	data, err := Render(manifests.ChartFS, map[string]interface{}{
		"slackServer": map[string]interface{}{"enabled": true},
	})
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)

	// kelos-controller: creates records, merge-patches correlation labels onto an
	// existing one, deletes on TTL expiry.
	for _, expected := range []string{
		"- taskrecords\n  verbs:\n  - create\n  - delete\n  - get\n  - list\n  - patch\n  - watch",
		"- taskrecords/finalizers\n  - tasks/finalizers\n  - taskspawners/finalizers\n  verbs:\n  - update",
		"- taskscores\n  verbs:\n  - get\n  - list\n  - update\n  - watch",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("controller role missing expected rule:\n%s", expected)
		}
	}

	// The controller must not be able to delete TaskScores: retraction happens in
	// kelos-slack-server and reclamation via garbage collection. Parsed rather than
	// substring-matched, so the check does not depend on where delete would sort
	// within the verb list.
	verbs := roleVerbs(t, data, "kelos-controller-role", "taskscores")
	if verbs == nil {
		t.Fatal("no taskscores rule found in the controller role")
	}
	if _, granted := verbs["delete"]; granted {
		t.Error("controller role grants delete on taskscores, which no code path uses")
	}
	for _, want := range []string{"get", "list", "update", "watch"} {
		if _, granted := verbs[want]; !granted {
			t.Errorf("controller role is missing %q on taskscores", want)
		}
	}

	// kelos-slack-server: reads records to resolve a reaction after the Task's TTL,
	// merge-patches the result labels onto them, and creates/deletes scores.
	for _, expected := range []string{
		"      - taskrecords\n    verbs:\n      - get\n      - list\n      - patch\n      - watch",
		"      - taskscores\n    verbs:\n      - create\n      - delete\n      - get\n      - list\n      - watch",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("slack-server role missing expected rule:\n%s", expected)
		}
	}
}

// hack/verify.sh checks generated files against a hand-maintained list, so a new
// chart CRD template that is missing from it is simply never verified — drift in it
// would pass `make verify` silently.
func TestVerifyScriptListsEveryChartCRD(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "hack", "verify.sh"))
	if err != nil {
		t.Fatalf("reading hack/verify.sh: %v", err)
	}

	entries, err := fs.ReadDir(manifests.ChartFS, "charts/kelos-crds/templates")
	if err != nil {
		t.Fatalf("reading CRD templates: %v", err)
	}

	var checked int
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "-crd.yaml") {
			continue
		}
		checked++
		path := "internal/manifests/charts/kelos/charts/kelos-crds/templates/" + entry.Name()
		if !strings.Contains(string(script), path) {
			t.Errorf("hack/verify.sh does not verify the generated file %s", path)
		}
	}
	if checked == 0 {
		t.Fatal("found no CRD templates; the test is not checking anything")
	}
}

// roleVerbs returns the verb set the named ClusterRole grants on the given
// resource, or nil when no rule mentions it.
func roleVerbs(t *testing.T, rendered []byte, roleName, resource string) map[string]struct{} {
	t.Helper()

	for _, doc := range strings.Split(string(rendered), "\n---\n") {
		var role rbacv1.ClusterRole
		if err := sigyaml.Unmarshal([]byte(doc), &role); err != nil {
			// Documents of other kinds do not unmarshal into a ClusterRole cleanly.
			continue
		}
		if role.Kind != "ClusterRole" || role.Name != roleName {
			continue
		}
		for _, rule := range role.Rules {
			if !slices.Contains(rule.Resources, resource) {
				continue
			}
			verbs := make(map[string]struct{}, len(rule.Verbs))
			for _, verb := range rule.Verbs {
				verbs[verb] = struct{}{}
			}
			return verbs
		}
	}
	return nil
}
