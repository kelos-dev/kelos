package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
)

func TestParseManifests_SingleDocument(t *testing.T) {
	data := []byte(`apiVersion: v1
kind: Namespace
metadata:
  name: test-ns
`)
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	if objs[0].GetKind() != "Namespace" {
		t.Errorf("expected kind Namespace, got %s", objs[0].GetKind())
	}
	if objs[0].GetName() != "test-ns" {
		t.Errorf("expected name test-ns, got %s", objs[0].GetName())
	}
}

func TestParseManifests_MultiDocument(t *testing.T) {
	data := []byte(`---
apiVersion: v1
kind: Namespace
metadata:
  name: ns1
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sa1
  namespace: ns1
`)
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}
	if objs[0].GetKind() != "Namespace" {
		t.Errorf("expected first object to be Namespace, got %s", objs[0].GetKind())
	}
	if objs[1].GetKind() != "ServiceAccount" {
		t.Errorf("expected second object to be ServiceAccount, got %s", objs[1].GetKind())
	}
	if objs[1].GetNamespace() != "ns1" {
		t.Errorf("expected namespace ns1, got %s", objs[1].GetNamespace())
	}
}

func TestParseManifests_SkipsEmptyDocuments(t *testing.T) {
	data := []byte(`---
---
apiVersion: v1
kind: Namespace
metadata:
  name: test
---
---
`)
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
}

func TestParseManifests_EmptyInput(t *testing.T) {
	objs, err := parseManifests([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 0 {
		t.Fatalf("expected 0 objects, got %d", len(objs))
	}
}

func TestParseManifests_PreservesOrder(t *testing.T) {
	data := []byte(`---
apiVersion: v1
kind: Namespace
metadata:
  name: first
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: second
  namespace: default
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: third
  namespace: default
`)
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objs))
	}
	names := []string{objs[0].GetName(), objs[1].GetName(), objs[2].GetName()}
	expected := []string{"first", "second", "third"}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("object %d: expected name %s, got %s", i, expected[i], name)
		}
	}
}

func TestParseManifests_EmbeddedCRDs(t *testing.T) {
	objs, err := parseManifests(manifests.InstallCRD)
	if err != nil {
		t.Fatalf("parsing embedded CRD manifest: %v", err)
	}
	if len(objs) == 0 {
		t.Fatal("expected at least one CRD object")
	}
	for _, obj := range objs {
		if obj.GetKind() != "CustomResourceDefinition" {
			t.Errorf("expected kind CustomResourceDefinition, got %s", obj.GetKind())
		}
	}
}

func renderDefaultChart(t *testing.T) []byte {
	t.Helper()
	vals := buildHelmValues("v0.0.0-test", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	return data
}

func TestRenderChart_DefaultValues(t *testing.T) {
	data := renderDefaultChart(t)
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("parsing rendered chart: %v", err)
	}
	if len(objs) == 0 {
		t.Fatal("expected at least one object from chart rendering")
	}
	kinds := make(map[string]bool)
	for _, obj := range objs {
		kinds[obj.GetKind()] = true
	}
	for _, expected := range []string{"Namespace", "ServiceAccount", "ClusterRole", "Deployment", "CronJob"} {
		if !kinds[expected] {
			t.Errorf("expected to find %s in rendered chart", expected)
		}
	}
}

func TestDisableChartCRDs(t *testing.T) {
	vals := disableChartCRDs(buildHelmValues("latest", "", false, "", "", "", "", ""))
	crds, ok := vals["crds"].(map[string]interface{})
	if !ok {
		t.Fatal("expected crds values to be present")
	}
	install, ok := crds["install"].(bool)
	if !ok {
		t.Fatal("expected crds.install to be a bool")
	}
	if install {
		t.Fatal("expected chart CRDs to be disabled")
	}
	image := vals["image"].(map[string]interface{})
	if image["tag"] != "latest" {
		t.Fatalf("expected image tag to be preserved, got %v", image["tag"])
	}
}

func TestRenderChart_ControllerOnlyExcludesCRDs(t *testing.T) {
	vals := disableChartCRDs(buildHelmValues("v0.0.0-test", "", false, "", "", "", "", ""))
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("parsing rendered chart: %v", err)
	}
	kinds := make(map[string]bool)
	for _, obj := range objs {
		if obj.GetKind() == "CustomResourceDefinition" {
			t.Fatalf("expected controller-only chart render to exclude CRDs, found %s", obj.GetName())
		}
		kinds[obj.GetKind()] = true
	}
	for _, expected := range []string{"Namespace", "ServiceAccount", "ClusterRole", "Deployment", "CronJob"} {
		if !kinds[expected] {
			t.Errorf("expected to find %s in controller-only rendered chart", expected)
		}
	}
}

func TestRenderChart_VersionSubstitution(t *testing.T) {
	vals := buildHelmValues("v0.5.0", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if bytes.Contains(data, []byte(":latest")) {
		t.Error("expected all :latest tags to be replaced")
	}
	if !bytes.Contains(data, []byte(":v0.5.0")) {
		t.Error("expected :v0.5.0 tags in rendered output")
	}
}

func TestRenderChart_ImageArgs(t *testing.T) {
	vals := buildHelmValues("v0.3.0", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	versionedArgs := []string{
		"--claude-code-image=ghcr.io/kelos-dev/claude-code:v0.3.0",
		"--codex-image=ghcr.io/kelos-dev/codex:v0.3.0",
		"--gemini-image=ghcr.io/kelos-dev/gemini:v0.3.0",
		"--opencode-image=ghcr.io/kelos-dev/opencode:v0.3.0",
		"--spawner-image=ghcr.io/kelos-dev/kelos-spawner:v0.3.0",
	}
	for _, arg := range versionedArgs {
		if !bytes.Contains(data, []byte(arg)) {
			t.Errorf("expected rendered chart to contain %q", arg)
		}
	}
}

func TestRenderChart_ImagePullPolicy(t *testing.T) {
	vals := buildHelmValues("v0.1.0", "Always", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if !bytes.Contains(data, []byte("imagePullPolicy: Always")) {
		t.Error("expected imagePullPolicy: Always in rendered output")
	}
	for _, arg := range []string{
		"--claude-code-image-pull-policy=Always",
		"--codex-image-pull-policy=Always",
		"--gemini-image-pull-policy=Always",
		"--opencode-image-pull-policy=Always",
		"--spawner-image-pull-policy=Always",
	} {
		if !bytes.Contains(data, []byte(arg)) {
			t.Errorf("expected %q in rendered output", arg)
		}
	}
}

func TestRenderChart_NoPullPolicyByDefault(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if bytes.Contains(data, []byte("imagePullPolicy:")) {
		t.Error("expected no imagePullPolicy when not set")
	}
	if bytes.Contains(data, []byte("-pull-policy=")) {
		t.Error("expected no -pull-policy args when not set")
	}
}

func TestRenderChart_DisableHeartbeat(t *testing.T) {
	vals := buildHelmValues("latest", "", true, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("parsing rendered chart: %v", err)
	}
	for _, obj := range objs {
		if obj.GetKind() == "CronJob" && obj.GetName() == "kelos-telemetry" {
			t.Error("expected kelos-telemetry CronJob to be excluded")
		}
	}
	// Other resources should still be present.
	kinds := make(map[string]bool)
	for _, obj := range objs {
		kinds[obj.GetKind()] = true
	}
	for _, expected := range []string{"Namespace", "ServiceAccount", "Deployment"} {
		if !kinds[expected] {
			t.Errorf("expected %s to still be present after disabling heartbeat", expected)
		}
	}
}

func TestRenderChart_EnableHeartbeat(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if !bytes.Contains(data, []byte("kelos-telemetry")) {
		t.Error("expected kelos-telemetry CronJob to be present by default")
	}
}

func TestSpawnerRole_CanDeleteTasks(t *testing.T) {
	data := renderDefaultChart(t)
	objs, err := parseManifests(data)
	if err != nil {
		t.Fatalf("parsing rendered chart: %v", err)
	}

	var found bool
	for _, obj := range objs {
		if obj.GetKind() != "ClusterRole" || obj.GetName() != "kelos-spawner-role" {
			continue
		}
		found = true

		rules, ok, err := unstructured.NestedSlice(obj.Object, "rules")
		if err != nil || !ok {
			t.Fatal("expected rules in kelos-spawner-role")
		}

		var hasDeleteTasks bool
		for _, r := range rules {
			rule, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
			verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")

			var hasTasks, hasDelete bool
			for _, res := range resources {
				if res == "tasks" {
					hasTasks = true
				}
			}
			for _, v := range verbs {
				if v == "delete" {
					hasDelete = true
				}
			}
			if hasTasks && hasDelete {
				hasDeleteTasks = true
			}
		}
		if !hasDeleteTasks {
			t.Error("kelos-spawner-role must have delete permission on tasks resource")
		}
	}
	if !found {
		t.Fatal("kelos-spawner-role ClusterRole not found in rendered chart")
	}
}

func TestInstallCommand_SkipsConfigLoading(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"install",
		"--config", "/nonexistent/path/config.yaml",
		"--kubeconfig", "/nonexistent/path/kubeconfig",
	})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected install to fail with invalid kubeconfig")
	}
	if err.Error() == "loading config: open /nonexistent/path/config.yaml: no such file or directory" {
		t.Fatal("install should not fail on missing config file")
	}
	if !strings.Contains(err.Error(), "loading kubeconfig:") {
		t.Fatalf("expected kubeconfig loading error, got %v", err)
	}
}

func TestUninstallCommand_SkipsConfigLoading(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"uninstall",
		"--config", "/nonexistent/path/config.yaml",
		"--kubeconfig", "/nonexistent/path/kubeconfig",
	})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected uninstall to fail with invalid kubeconfig")
	}
	if err.Error() == "loading config: open /nonexistent/path/config.yaml: no such file or directory" {
		t.Fatal("uninstall should not fail on missing config file")
	}
	if !strings.Contains(err.Error(), "loading kubeconfig:") {
		t.Fatalf("expected kubeconfig loading error, got %v", err)
	}
}

func TestInstallCommand_RejectsExtraArgs(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "extra-arg"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when extra arguments are provided")
	}
}

func TestUninstallCommand_RejectsExtraArgs(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"uninstall", "extra-arg"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when extra arguments are provided")
	}
}

func TestInstallCommand_ImagePullPolicyFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--image-pull-policy", "Always"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "imagePullPolicy: Always") {
		t.Errorf("expected imagePullPolicy: Always in output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_DryRunIncludesEachCRDOnce(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	objs, err := parseManifests([]byte(output))
	if err != nil {
		t.Fatalf("parsing dry-run output: %v", err)
	}

	crdNames := map[string]int{}
	crdCount := 0
	for _, obj := range objs {
		if obj.GetKind() != "CustomResourceDefinition" {
			continue
		}
		crdCount++
		crdNames[obj.GetName()]++
	}

	if crdCount != 4 {
		t.Fatalf("expected 4 CRDs in dry-run output, got %d", crdCount)
	}
	for _, name := range []string{
		"agentconfigs.kelos.dev",
		"tasks.kelos.dev",
		"taskspawners.kelos.dev",
		"workspaces.kelos.dev",
	} {
		if crdNames[name] != 1 {
			t.Errorf("expected dry-run output to contain %s exactly once, got %d", name, crdNames[name])
		}
	}
}

func TestInstallCommand_VersionFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--version", "v0.5.0"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(output, ":latest") {
		t.Errorf("expected all :latest tags to be replaced, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, ":v0.5.0") {
		t.Errorf("expected :v0.5.0 tags in output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_ValuesFileFlag(t *testing.T) {
	dir := t.TempDir()
	valuesPath := filepath.Join(dir, "values.yaml")
	values := `webhookServer:
  sources:
    github:
      enabled: true
      secretName: github-webhook-secret
`
	if err := os.WriteFile(valuesPath, []byte(values), 0o644); err != nil {
		t.Fatalf("writing values file: %v", err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--values", valuesPath})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "name: kelos-webhook-github") {
		t.Fatalf("expected webhook deployment in output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "name: github-webhook-secret") {
		t.Fatalf("expected webhook secret name from values file in output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_ValuesFromStdin(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetIn(strings.NewReader(`webhookServer:
  sources:
    github:
      enabled: true
      secretName: stdin-webhook-secret
`))
	cmd.SetArgs([]string{"install", "--dry-run", "--values", "-"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "name: kelos-webhook-github") {
		t.Fatalf("expected webhook deployment in output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "name: stdin-webhook-secret") {
		t.Fatalf("expected webhook secret name from stdin values in output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_SetFileFlag(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret-name.txt")
	if err := os.WriteFile(secretPath, []byte("file-webhook-secret"), 0o644); err != nil {
		t.Fatalf("writing set-file input: %v", err)
	}

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"install",
		"--dry-run",
		"--set", "webhookServer.sources.github.enabled=true",
		"--set-file", "webhookServer.sources.github.secretName=" + secretPath,
	})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "name: kelos-webhook-github") {
		t.Fatalf("expected webhook deployment in output, got:\n%s", output[:min(len(output), 500)])
	}
	if !strings.Contains(output, "name: file-webhook-secret") {
		t.Fatalf("expected webhook secret name from --set-file in output, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_SetOverridesCompatibilityFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"install",
		"--dry-run",
		"--image-pull-policy", "Always",
		"--set", "image.pullPolicy=Never",
	})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "imagePullPolicy: Never") {
		t.Fatalf("expected --set to override compatibility flag, got:\n%s", output[:min(len(output), 500)])
	}
	if strings.Contains(output, "imagePullPolicy: Always") {
		t.Fatalf("did not expect compatibility flag value to win, got:\n%s", output[:min(len(output), 500)])
	}
}

func TestInstallCommand_DisableHeartbeatFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--disable-heartbeat"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(output, "kelos-telemetry") {
		t.Error("expected kelos-telemetry CronJob to be excluded from output")
	}
}

func TestInstallCommand_SpawnerResourceRequestsFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--spawner-resource-requests", "cpu=250m,memory=512Mi"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "--spawner-resource-requests=cpu=250m,memory=512Mi") {
		t.Errorf("expected --spawner-resource-requests arg in output")
	}
}

func TestInstallCommand_SpawnerResourceLimitsFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--spawner-resource-limits", "cpu=1,memory=1Gi"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "--spawner-resource-limits=cpu=1,memory=1Gi") {
		t.Errorf("expected --spawner-resource-limits arg in output")
	}
}

func TestInstallCommand_NoSpawnerResourcesByDefault(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(output, "--spawner-resource-requests") {
		t.Error("expected no --spawner-resource-requests when not set")
	}
	if strings.Contains(output, "--spawner-resource-limits") {
		t.Error("expected no --spawner-resource-limits when not set")
	}
}

func TestInstallCommand_GHProxyCacheTTLFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--ghproxy-cache-ttl", "30s"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "--ghproxy-cache-ttl=30s") {
		t.Errorf("expected --ghproxy-cache-ttl=30s arg in output")
	}
}

func TestInstallCommand_NoGHProxyCacheTTLByDefault(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(output, "--ghproxy-cache-ttl") {
		t.Error("expected no --ghproxy-cache-ttl when not set")
	}
}

func TestInstallCommand_ControllerResourceRequestsFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--controller-resource-requests", "cpu=10m,memory=64Mi"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "cpu: 10m") {
		t.Errorf("expected cpu: 10m in output")
	}
	if !strings.Contains(output, "memory: 64Mi") {
		t.Errorf("expected memory: 64Mi in output")
	}
}

func TestInstallCommand_ControllerResourceLimitsFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--controller-resource-limits", "cpu=500m,memory=128Mi"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "cpu: 500m") {
		t.Errorf("expected cpu: 500m in output")
	}
	if !strings.Contains(output, "memory: 128Mi") {
		t.Errorf("expected memory: 128Mi in output")
	}
}

func TestInstallCommand_NoControllerResourcesByDefault(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Extract only the Deployment document so we don't match resources from
	// the telemetry CronJob (which legitimately contains cpu: 10m / memory: 64Mi).
	deployment := extractYAMLDocument(t, []byte(output), "kind: Deployment")

	// Verify neither old limit defaults nor old request defaults are rendered.
	for _, needle := range []string{"cpu: 500m", "memory: 128Mi", "cpu: 10m", "memory: 64Mi"} {
		if strings.Contains(deployment, needle) {
			t.Errorf("expected no hardcoded %s in controller Deployment when resources not set", needle)
		}
	}
}

func TestRenderChart_ControllerResources(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "cpu=100m,memory=256Mi", "cpu=1,memory=512Mi", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if !bytes.Contains(data, []byte("cpu: 100m")) {
		t.Error("expected cpu: 100m in rendered output for controller requests")
	}
	if !bytes.Contains(data, []byte("memory: 256Mi")) {
		t.Error("expected memory: 256Mi in rendered output for controller requests")
	}
	if !bytes.Contains(data, []byte("cpu: 1\n")) {
		t.Error("expected cpu: 1 in rendered output for controller limits")
	}
	if !bytes.Contains(data, []byte("memory: 512Mi")) {
		t.Error("expected memory: 512Mi in rendered output for controller limits")
	}
}

func TestRenderChart_NoControllerResourcesByDefault(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	// Extract only the Deployment document so we don't match resources from
	// the telemetry CronJob (which legitimately contains cpu: 10m / memory: 64Mi).
	deployment := extractYAMLDocument(t, data, "kind: Deployment")

	// Verify neither old limit defaults nor old request defaults are rendered.
	for _, needle := range []string{"cpu: 500m", "memory: 128Mi", "cpu: 10m", "memory: 64Mi"} {
		if strings.Contains(deployment, needle) {
			t.Errorf("expected no hardcoded %s in controller Deployment when resources not set", needle)
		}
	}
}

func TestRenderChart_SpawnerResources(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "cpu=250m,memory=512Mi", "cpu=1,memory=1Gi", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if !bytes.Contains(data, []byte("--spawner-resource-requests=cpu=250m,memory=512Mi")) {
		t.Error("expected --spawner-resource-requests in rendered output")
	}
	if !bytes.Contains(data, []byte("--spawner-resource-limits=cpu=1,memory=1Gi")) {
		t.Error("expected --spawner-resource-limits in rendered output")
	}
}

func TestRenderChart_NoSpawnerResourcesByDefault(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if bytes.Contains(data, []byte("spawner-resource-requests")) {
		t.Error("expected no spawner-resource-requests when not set")
	}
	if bytes.Contains(data, []byte("spawner-resource-limits")) {
		t.Error("expected no spawner-resource-limits when not set")
	}
}

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
}

// kelosListKinds maps kelos GVRs to their list kinds for the fake dynamic client.
var kelosListKinds = map[schema.GroupVersionResource]string{
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "tasks"}:        "TaskList",
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "taskspawners"}: "TaskSpawnerList",
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "workspaces"}:   "WorkspaceList",
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "agentconfigs"}: "AgentConfigList",
}

func TestDeleteAllCustomResources_NoResources(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kelosListKinds)
	if err := deleteAllCustomResources(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteAllCustomResources_DeletesExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()

	task := &unstructured.Unstructured{}
	task.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kelos.dev", Version: "v1alpha1", Kind: "Task",
	})
	task.SetName("my-task")
	task.SetNamespace("default")

	workspace := &unstructured.Unstructured{}
	workspace.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kelos.dev", Version: "v1alpha1", Kind: "Workspace",
	})
	workspace.SetName("my-workspace")
	workspace.SetNamespace("default")

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kelosListKinds, task, workspace)
	if err := deleteAllCustomResources(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify resources were deleted
	taskGVR := schema.GroupVersionResource{Group: "kelos.dev", Version: "v1alpha1", Resource: "tasks"}
	list, err := client.Resource(taskGVR).Namespace("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error listing tasks: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(list.Items))
	}

	wsGVR := schema.GroupVersionResource{Group: "kelos.dev", Version: "v1alpha1", Resource: "workspaces"}
	list, err = client.Resource(wsGVR).Namespace("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error listing workspaces: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected 0 workspaces, got %d", len(list.Items))
	}
}

func TestDeleteAllCustomResources_SkipsAlreadyDeletingResources(t *testing.T) {
	scheme := runtime.NewScheme()

	now := metav1.Now()
	task := &unstructured.Unstructured{}
	task.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kelos.dev", Version: "v1alpha1", Kind: "Task",
	})
	task.SetName("deleting-task")
	task.SetNamespace("default")
	task.SetDeletionTimestamp(&now)
	task.SetFinalizers([]string{"kelos.dev/finalizer"})

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kelosListKinds, task)

	if err := deleteAllCustomResources(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Resource should still exist because it was already deleting (has deletionTimestamp)
	// and we skip those
	taskGVR := schema.GroupVersionResource{Group: "kelos.dev", Version: "v1alpha1", Resource: "tasks"}
	list, err := client.Resource(taskGVR).Namespace("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error listing tasks: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected 1 task (still deleting), got %d", len(list.Items))
	}
}

func TestWaitForCustomResourceDeletion_AlreadyEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kelosListKinds)

	if err := waitForCustomResourceDeletion(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForCustomResourceDeletion_RespectsContextCancellation(t *testing.T) {
	scheme := runtime.NewScheme()

	task := &unstructured.Unstructured{}
	task.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "kelos.dev", Version: "v1alpha1", Kind: "Task",
	})
	task.SetName("stuck-task")
	task.SetNamespace("default")

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kelosListKinds, task)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := waitForCustomResourceDeletion(ctx, client)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestBuildHelmValues(t *testing.T) {
	tests := []struct {
		name                       string
		version                    string
		pullPolicy                 string
		disableHeartbeat           bool
		spawnerResourceRequests    string
		spawnerResourceLimits      string
		controllerResourceRequests string
		controllerResourceLimits   string
		checkFn                    func(t *testing.T, vals map[string]interface{})
	}{
		{
			name:    "default values",
			version: "latest",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				img := vals["image"].(map[string]interface{})
				if img["tag"] != "latest" {
					t.Errorf("expected tag latest, got %v", img["tag"])
				}
				if _, ok := img["pullPolicy"]; ok {
					t.Error("expected no pullPolicy when empty")
				}
				if _, ok := vals["telemetry"]; ok {
					t.Error("expected no telemetry key when not disabled")
				}
				if _, ok := vals["spawner"]; ok {
					t.Error("expected no spawner key when empty")
				}
				if _, ok := vals["controller"]; ok {
					t.Error("expected no controller key when empty")
				}
			},
		},
		{
			name:       "with pull policy",
			version:    "v1.0.0",
			pullPolicy: "Never",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				img := vals["image"].(map[string]interface{})
				if img["pullPolicy"] != "Never" {
					t.Errorf("expected pullPolicy Never, got %v", img["pullPolicy"])
				}
			},
		},
		{
			name:             "disable heartbeat",
			version:          "latest",
			disableHeartbeat: true,
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				tel := vals["telemetry"].(map[string]interface{})
				if tel["enabled"] != false {
					t.Errorf("expected telemetry.enabled=false, got %v", tel["enabled"])
				}
			},
		},
		{
			name:                    "with spawner resource requests",
			version:                 "latest",
			spawnerResourceRequests: "cpu=250m,memory=512Mi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				spawner := vals["spawner"].(map[string]interface{})
				res := spawner["resources"].(map[string]interface{})
				if res["requests"] != "cpu=250m,memory=512Mi" {
					t.Errorf("expected spawner.resources.requests=cpu=250m,memory=512Mi, got %v", res["requests"])
				}
			},
		},
		{
			name:                  "with spawner resource limits",
			version:               "latest",
			spawnerResourceLimits: "cpu=1,memory=1Gi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				spawner := vals["spawner"].(map[string]interface{})
				res := spawner["resources"].(map[string]interface{})
				if res["limits"] != "cpu=1,memory=1Gi" {
					t.Errorf("expected spawner.resources.limits=cpu=1,memory=1Gi, got %v", res["limits"])
				}
			},
		},
		{
			name:                       "with controller resource requests",
			version:                    "latest",
			controllerResourceRequests: "cpu=10m,memory=64Mi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				ctrl := vals["controller"].(map[string]interface{})
				res := ctrl["resources"].(map[string]interface{})
				req := res["requests"].(map[string]interface{})
				if req["cpu"] != "10m" || req["memory"] != "64Mi" {
					t.Errorf("expected controller.resources.requests={cpu:10m,memory:64Mi}, got %v", req)
				}
			},
		},
		{
			name:                     "with controller resource limits",
			version:                  "latest",
			controllerResourceLimits: "cpu=500m,memory=128Mi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				ctrl := vals["controller"].(map[string]interface{})
				res := ctrl["resources"].(map[string]interface{})
				lim := res["limits"].(map[string]interface{})
				if lim["cpu"] != "500m" || lim["memory"] != "128Mi" {
					t.Errorf("expected controller.resources.limits={cpu:500m,memory:128Mi}, got %v", lim)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := buildHelmValues(
				tt.version,
				tt.pullPolicy,
				tt.disableHeartbeat,
				tt.spawnerResourceRequests,
				tt.spawnerResourceLimits,
				tt.controllerResourceRequests,
				tt.controllerResourceLimits,
				"",
			)
			tt.checkFn(t, vals)
		})
	}
}

func TestBuildInstallValues_MergesWithPrecedence(t *testing.T) {
	dir := t.TempDir()
	valuesOnePath := filepath.Join(dir, "values-one.yaml")
	valuesTwoPath := filepath.Join(dir, "values-two.yaml")
	secretPath := filepath.Join(dir, "secret-name.txt")

	valuesOne := `image:
  tag: values-tag-1
  pullPolicy: Never
ghproxy:
  cacheTTL: 10s
webhookServer:
  sources:
    github:
      enabled: true
`
	valuesTwo := `image:
  tag: values-tag-2
ghproxy:
  cacheTTL: 20s
`
	if err := os.WriteFile(valuesOnePath, []byte(valuesOne), 0o644); err != nil {
		t.Fatalf("writing first values file: %v", err)
	}
	if err := os.WriteFile(valuesTwoPath, []byte(valuesTwo), 0o644); err != nil {
		t.Fatalf("writing second values file: %v", err)
	}
	if err := os.WriteFile(secretPath, []byte("github-secret-from-file"), 0o644); err != nil {
		t.Fatalf("writing set-file content: %v", err)
	}

	vals, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "default-tag",
		valuesFiles:     []string{valuesOnePath, valuesTwoPath},
		setValues:       []string{"image.pullPolicy=IfNotPresent", "ghproxy.cacheTTL=30s"},
		setStringValues: []string{"image.tag=set-string-tag", "ghproxy.allowedUpstreams=https://github.example.com/api/v3"},
		setFileValues:   []string{"webhookServer.sources.github.secretName=" + secretPath},
		flagValues: helmValuesOptions{
			imageTag:        nonEmptyStringPtr("flag-tag"),
			pullPolicy:      "Always",
			ghproxyCacheTTL: "45s",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	image := vals["image"].(map[string]interface{})
	if image["tag"] != "set-string-tag" {
		t.Fatalf("expected image.tag from explicit override, got %v", image["tag"])
	}
	if image["pullPolicy"] != "IfNotPresent" {
		t.Fatalf("expected image.pullPolicy from explicit override, got %v", image["pullPolicy"])
	}

	ghproxy := vals["ghproxy"].(map[string]interface{})
	if ghproxy["cacheTTL"] != "30s" {
		t.Fatalf("expected ghproxy.cacheTTL from explicit override, got %v", ghproxy["cacheTTL"])
	}
	if ghproxy["allowedUpstreams"] != "https://github.example.com/api/v3" {
		t.Fatalf("expected ghproxy.allowedUpstreams from --set-string, got %v", ghproxy["allowedUpstreams"])
	}

	webhookServer := vals["webhookServer"].(map[string]interface{})
	sources := webhookServer["sources"].(map[string]interface{})
	github := sources["github"].(map[string]interface{})
	if github["enabled"] != true {
		t.Fatalf("expected webhook GitHub source enabled from values file, got %v", github["enabled"])
	}
	if github["secretName"] != "github-secret-from-file" {
		t.Fatalf("expected webhook secret name from --set-file, got %v", github["secretName"])
	}

	crds := vals["crds"].(map[string]interface{})
	if crds["install"] != false {
		t.Fatalf("expected crds.install to be forced false, got %v", crds["install"])
	}
}

func TestBuildInstallValues_UsesDefaultImageTagOnlyWhenUnset(t *testing.T) {
	vals, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	image := vals["image"].(map[string]interface{})
	if image["tag"] != "v1.2.3" {
		t.Fatalf("expected default image tag, got %v", image["tag"])
	}

	overrideVals, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		setValues:       []string{"image.tag=custom-tag"},
	})
	if err != nil {
		t.Fatalf("unexpected error building override values: %v", err)
	}
	overrideImage := overrideVals["image"].(map[string]interface{})
	if overrideImage["tag"] != "custom-tag" {
		t.Fatalf("expected explicit image tag to win, got %v", overrideImage["tag"])
	}
}

func TestBuildInstallValues_ReadsValuesFromStdin(t *testing.T) {
	vals, err := buildInstallValues(strings.NewReader(`webhookServer:
  sources:
    github:
      enabled: true
      secretName: stdin-webhook-secret
`), installValuesOptions{
		defaultImageTag: "v1.2.3",
		valuesFiles:     []string{"-"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	webhookServer := vals["webhookServer"].(map[string]interface{})
	sources := webhookServer["sources"].(map[string]interface{})
	github := sources["github"].(map[string]interface{})
	if github["secretName"] != "stdin-webhook-secret" {
		t.Fatalf("expected secret name from stdin values, got %v", github["secretName"])
	}
}

func TestBuildInstallValues_SetStringPreservesStrings(t *testing.T) {
	vals, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		setStringValues: []string{"webhookServer.sources.github.replicas=2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	webhookServer := vals["webhookServer"].(map[string]interface{})
	sources := webhookServer["sources"].(map[string]interface{})
	github := sources["github"].(map[string]interface{})
	replicas, ok := github["replicas"].(string)
	if !ok {
		t.Fatalf("expected replicas to remain a string, got %T", github["replicas"])
	}
	if replicas != "2" {
		t.Fatalf("expected replicas string value 2, got %q", replicas)
	}
}

func TestBuildInstallValues_RejectsCRDsInstallTrue(t *testing.T) {
	_, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		setValues:       []string{"crds.install=true"},
	})
	if err == nil {
		t.Fatal("expected crds.install=true to be rejected")
	}
	if !strings.Contains(err.Error(), "crds.install") {
		t.Fatalf("expected crds.install error, got %v", err)
	}
}

func TestBuildInstallValues_RejectsNonMapCRDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-crds.yaml")
	if err := os.WriteFile(path, []byte("crds: notamap\n"), 0o644); err != nil {
		t.Fatalf("writing values file: %v", err)
	}

	_, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		valuesFiles:     []string{path},
	})
	if err == nil {
		t.Fatal("expected non-map crds to be rejected")
	}
	if !strings.Contains(err.Error(), "crds must be a map") {
		t.Fatalf("expected 'crds must be a map' error, got %v", err)
	}
}

func TestBuildInstallValues_MultipleFilesLaterWins(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.yaml")
	secondPath := filepath.Join(dir, "second.yaml")
	if err := os.WriteFile(firstPath, []byte("image:\n  pullPolicy: Always\n"), 0o644); err != nil {
		t.Fatalf("writing first values file: %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("image:\n  pullPolicy: Never\n"), 0o644); err != nil {
		t.Fatalf("writing second values file: %v", err)
	}

	vals, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		valuesFiles:     []string{firstPath, secondPath},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	image := vals["image"].(map[string]interface{})
	if image["pullPolicy"] != "Never" {
		t.Fatalf("expected later values file to win, got %v", image["pullPolicy"])
	}
}

func TestBuildInstallValues_MissingValuesFile(t *testing.T) {
	_, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		valuesFiles:     []string{filepath.Join(t.TempDir(), "does-not-exist.yaml")},
	})
	if err == nil {
		t.Fatal("expected missing values file to error")
	}
	if !strings.Contains(err.Error(), "reading values file") {
		t.Fatalf("expected 'reading values file' error, got %v", err)
	}
}

func TestBuildInstallValues_InvalidYAMLValuesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(path, []byte("image:\n  tag: v1\n  - not yaml\n"), 0o644); err != nil {
		t.Fatalf("writing values file: %v", err)
	}

	_, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		valuesFiles:     []string{path},
	})
	if err == nil {
		t.Fatal("expected invalid YAML values file to error")
	}
	if !strings.Contains(err.Error(), "reading values file") {
		t.Fatalf("expected 'reading values file' error, got %v", err)
	}
}

func TestBuildInstallValues_DoubleStdinRejected(t *testing.T) {
	_, err := buildInstallValues(strings.NewReader("image:\n  tag: v1\n"), installValuesOptions{
		defaultImageTag: "v1.2.3",
		valuesFiles:     []string{"-", "-"},
	})
	if err == nil {
		t.Fatal("expected double stdin to error")
	}
	if !strings.Contains(err.Error(), "'-' can only be used once") {
		t.Fatalf("expected double-stdin error, got %v", err)
	}
}

func TestBuildInstallValues_SetStringOverridesSet(t *testing.T) {
	vals, err := buildInstallValues(strings.NewReader(""), installValuesOptions{
		defaultImageTag: "v1.2.3",
		setValues:       []string{"image.tag=from-set"},
		setStringValues: []string{"image.tag=from-set-string"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	image := vals["image"].(map[string]interface{})
	if image["tag"] != "from-set-string" {
		t.Fatalf("expected --set-string to win over --set for same key, got %v", image["tag"])
	}
}

// extractYAMLDocument returns the first YAML document from data whose content
// contains the given marker string. Documents are separated by "---".
func extractYAMLDocument(t *testing.T, data []byte, marker string) string {
	t.Helper()
	docs := strings.Split(string(data), "---")
	for _, doc := range docs {
		if strings.Contains(doc, marker) {
			return doc
		}
	}
	t.Fatalf("no YAML document containing %q found in rendered output", marker)
	return ""
}
