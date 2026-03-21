package cli

import (
	"bytes"
	"context"
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
	vals := buildHelmValues("v0.0.0-test", "", false, "", "", "", "")
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

func TestRenderChart_VersionSubstitution(t *testing.T) {
	vals := buildHelmValues("v0.5.0", "", false, "", "", "", "")
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
	vals := buildHelmValues("v0.3.0", "", false, "", "", "", "")
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
		"--token-refresher-image=ghcr.io/kelos-dev/kelos-token-refresher:v0.3.0",
	}
	for _, arg := range versionedArgs {
		if !bytes.Contains(data, []byte(arg)) {
			t.Errorf("expected rendered chart to contain %q", arg)
		}
	}
}

func TestRenderChart_ImagePullPolicy(t *testing.T) {
	vals := buildHelmValues("v0.1.0", "Always", false, "", "", "", "")
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
		"--token-refresher-image-pull-policy=Always",
	} {
		if !bytes.Contains(data, []byte(arg)) {
			t.Errorf("expected %q in rendered output", arg)
		}
	}
}

func TestRenderChart_NoPullPolicyByDefault(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "")
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
	vals := buildHelmValues("latest", "", true, "", "", "", "")
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
	vals := buildHelmValues("latest", "", false, "", "", "", "")
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

func TestInstallCommand_TokenRefresherResourceRequestsFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--token-refresher-resource-requests", "cpu=100m,memory=128Mi"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "--token-refresher-resource-requests=cpu=100m,memory=128Mi") {
		t.Errorf("expected --token-refresher-resource-requests arg in output")
	}
}

func TestInstallCommand_TokenRefresherResourceLimitsFlag(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run", "--token-refresher-resource-limits", "cpu=200m,memory=256Mi"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "--token-refresher-resource-limits=cpu=200m,memory=256Mi") {
		t.Errorf("expected --token-refresher-resource-limits arg in output")
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

func TestInstallCommand_NoTokenRefresherResourcesByDefault(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"install", "--dry-run"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if strings.Contains(output, "--token-refresher-resource-requests") {
		t.Error("expected no --token-refresher-resource-requests when not set")
	}
	if strings.Contains(output, "--token-refresher-resource-limits") {
		t.Error("expected no --token-refresher-resource-limits when not set")
	}
}

func TestRenderChart_SpawnerResources(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "cpu=250m,memory=512Mi", "cpu=1,memory=1Gi", "", "")
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

func TestRenderChart_TokenRefresherResources(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "cpu=100m,memory=128Mi", "cpu=200m,memory=256Mi")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if !bytes.Contains(data, []byte("--token-refresher-resource-requests=cpu=100m,memory=128Mi")) {
		t.Error("expected --token-refresher-resource-requests in rendered output")
	}
	if !bytes.Contains(data, []byte("--token-refresher-resource-limits=cpu=200m,memory=256Mi")) {
		t.Error("expected --token-refresher-resource-limits in rendered output")
	}
}

func TestRenderChart_NoSpawnerResourcesByDefault(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "")
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

func TestRenderChart_NoTokenRefresherResourcesByDefault(t *testing.T) {
	vals := buildHelmValues("latest", "", false, "", "", "", "")
	data, err := helmchart.Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if bytes.Contains(data, []byte("token-refresher-resource-requests")) {
		t.Error("expected no token-refresher-resource-requests when not set")
	}
	if bytes.Contains(data, []byte("token-refresher-resource-limits")) {
		t.Error("expected no token-refresher-resource-limits when not set")
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
		name                           string
		version                        string
		pullPolicy                     string
		disableHeartbeat               bool
		spawnerResourceRequests        string
		spawnerResourceLimits          string
		tokenRefresherResourceRequests string
		tokenRefresherResourceLimits   string
		checkFn                        func(t *testing.T, vals map[string]interface{})
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
				if _, ok := vals["spawnerResourceRequests"]; ok {
					t.Error("expected no spawnerResourceRequests when empty")
				}
				if _, ok := vals["spawnerResourceLimits"]; ok {
					t.Error("expected no spawnerResourceLimits when empty")
				}
				if _, ok := vals["tokenRefresherResourceRequests"]; ok {
					t.Error("expected no tokenRefresherResourceRequests when empty")
				}
				if _, ok := vals["tokenRefresherResourceLimits"]; ok {
					t.Error("expected no tokenRefresherResourceLimits when empty")
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
				if vals["spawnerResourceRequests"] != "cpu=250m,memory=512Mi" {
					t.Errorf("expected spawnerResourceRequests=cpu=250m,memory=512Mi, got %v", vals["spawnerResourceRequests"])
				}
			},
		},
		{
			name:                  "with spawner resource limits",
			version:               "latest",
			spawnerResourceLimits: "cpu=1,memory=1Gi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				if vals["spawnerResourceLimits"] != "cpu=1,memory=1Gi" {
					t.Errorf("expected spawnerResourceLimits=cpu=1,memory=1Gi, got %v", vals["spawnerResourceLimits"])
				}
			},
		},
		{
			name:                           "with token refresher resource requests",
			version:                        "latest",
			tokenRefresherResourceRequests: "cpu=100m,memory=128Mi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				if vals["tokenRefresherResourceRequests"] != "cpu=100m,memory=128Mi" {
					t.Errorf("expected tokenRefresherResourceRequests=cpu=100m,memory=128Mi, got %v", vals["tokenRefresherResourceRequests"])
				}
			},
		},
		{
			name:                         "with token refresher resource limits",
			version:                      "latest",
			tokenRefresherResourceLimits: "cpu=200m,memory=256Mi",
			checkFn: func(t *testing.T, vals map[string]interface{}) {
				if vals["tokenRefresherResourceLimits"] != "cpu=200m,memory=256Mi" {
					t.Errorf("expected tokenRefresherResourceLimits=cpu=200m,memory=256Mi, got %v", vals["tokenRefresherResourceLimits"])
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
				tt.tokenRefresherResourceRequests,
				tt.tokenRefresherResourceLimits,
			)
			tt.checkFn(t, vals)
		})
	}
}
