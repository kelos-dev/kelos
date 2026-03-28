package helmchart

import (
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/manifests"
	sigyaml "sigs.k8s.io/yaml"
)

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
		"kind: CustomResourceDefinition",
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
	if !strings.Contains(output, ":latest") {
		t.Error("expected :latest tags in rendered output when using default values")
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
		"kind: CustomResourceDefinition",
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
	if strings.Contains(output, ":latest") {
		t.Error("expected no :latest tags in rendered output")
	}
	if !strings.Contains(output, ":v1.2.3") {
		t.Error("expected :v1.2.3 tags in rendered output")
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
	data, err := Render(manifests.ChartFS, nil)
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

func TestRender_TaskSpawnerTemplatePlaceholdersRemainLiteral(t *testing.T) {
	data, err := Render(manifests.ChartFS, nil)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, `Supports Go text/template variables from the work item, e.g. "kelos-task-{{.Number}}".`) {
		t.Error("expected branch placeholder example to remain literal in rendered CRD output")
	}
	for _, expected := range []string{
		"Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}",
		"GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}",
		"GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}",
		"Cron sources: {{.Time}}, {{.Schedule}}",
	} {
		if count := strings.Count(output, expected); count != 2 {
			t.Errorf("expected %q to appear twice in TaskSpawner CRD descriptions, got %d", expected, count)
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
	// Verify each non-empty YAML document is actually parseable.
	docs := strings.Split(string(data), "---\n")
	validDocs := 0
	for _, doc := range docs {
		trimmed := strings.TrimSpace(doc)
		if len(trimmed) == 0 {
			continue
		}
		var obj map[string]interface{}
		if err := sigyaml.Unmarshal([]byte(trimmed), &obj); err != nil {
			t.Errorf("invalid YAML document: %v\n---\n%s", err, trimmed)
		}
		validDocs++
	}
	if validDocs == 0 {
		t.Fatal("expected at least one valid YAML document in rendered output")
	}
}
