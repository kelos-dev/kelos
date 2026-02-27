package cli

import (
	"bytes"
	"strings"
	"testing"

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

func TestParseManifests_EmbeddedController(t *testing.T) {
	objs, err := parseManifests(manifests.InstallController)
	if err != nil {
		t.Fatalf("parsing embedded controller manifest: %v", err)
	}
	if len(objs) == 0 {
		t.Fatal("expected at least one controller object")
	}
	kinds := make(map[string]bool)
	for _, obj := range objs {
		kinds[obj.GetKind()] = true
	}
	for _, expected := range []string{"Namespace", "ServiceAccount", "ClusterRole", "Deployment"} {
		if !kinds[expected] {
			t.Errorf("expected to find %s in controller manifest", expected)
		}
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

func TestVersionedManifest_Latest(t *testing.T) {
	data := []byte("image: gjkim42/kelos-controller:latest")
	result := versionedManifest(data, "latest")
	if !bytes.Equal(result, data) {
		t.Errorf("expected manifest unchanged for latest version, got %s", string(result))
	}
}

func TestVersionedManifest_Tagged(t *testing.T) {
	data := []byte("image: gjkim42/kelos-controller:latest")
	result := versionedManifest(data, "v0.1.0")
	expected := []byte("image: gjkim42/kelos-controller:v0.1.0")
	if !bytes.Equal(result, expected) {
		t.Errorf("expected %s, got %s", string(expected), string(result))
	}
}

func TestVersionedManifest_MultipleImages(t *testing.T) {
	data := []byte(`image: gjkim42/kelos-controller:latest
args:
  - --spawner-image=gjkim42/kelos-spawner:latest
  - --claude-code-image=gjkim42/claude-code:latest`)
	result := versionedManifest(data, "v0.2.0")
	if bytes.Contains(result, []byte(":latest")) {
		t.Errorf("expected all :latest tags to be replaced, got %s", string(result))
	}
	if !bytes.Contains(result, []byte(":v0.2.0")) {
		t.Errorf("expected :v0.2.0 tags in result, got %s", string(result))
	}
}

func TestVersionedManifest_EmbeddedController(t *testing.T) {
	result := versionedManifest(manifests.InstallController, "v1.0.0")
	if bytes.Contains(result, []byte(":latest")) {
		t.Error("Expected all :latest tags to be replaced in embedded controller manifest")
	}
	if !bytes.Contains(result, []byte(":v1.0.0")) {
		t.Error("Expected :v1.0.0 tags in versioned controller manifest")
	}
}

func TestVersionedManifest_EmbeddedControllerImageArgs(t *testing.T) {
	// Verify the embedded manifest contains image flags that will be versioned.
	expectedArgs := []string{
		"--claude-code-image=gjkim42/claude-code:",
		"--codex-image=gjkim42/codex:",
		"--gemini-image=gjkim42/gemini:",
		"--opencode-image=gjkim42/opencode:",
		"--spawner-image=gjkim42/kelos-spawner:",
		"--token-refresher-image=gjkim42/kelos-token-refresher:",
	}
	for _, arg := range expectedArgs {
		if !bytes.Contains(manifests.InstallController, []byte(arg)) {
			t.Errorf("expected embedded controller manifest to contain %q", arg)
		}
	}

	// Verify all image args get the pinned version after substitution.
	result := versionedManifest(manifests.InstallController, "v0.3.0")
	versionedArgs := []string{
		"--claude-code-image=gjkim42/claude-code:v0.3.0",
		"--codex-image=gjkim42/codex:v0.3.0",
		"--gemini-image=gjkim42/gemini:v0.3.0",
		"--opencode-image=gjkim42/opencode:v0.3.0",
		"--spawner-image=gjkim42/kelos-spawner:v0.3.0",
		"--token-refresher-image=gjkim42/kelos-token-refresher:v0.3.0",
	}
	for _, arg := range versionedArgs {
		if !bytes.Contains(result, []byte(arg)) {
			t.Errorf("expected versioned manifest to contain %q", arg)
		}
	}
}

func TestWithImagePullPolicy(t *testing.T) {
	data := []byte(`      containers:
        - name: manager
          image: gjkim42/kelos-controller:v0.1.0
          args:
            - --leader-elect
            - --claude-code-image=gjkim42/claude-code:v0.1.0
            - --spawner-image=gjkim42/kelos-spawner:v0.1.0`)
	result := withImagePullPolicy(data, "Always")
	// Verify container imagePullPolicy appears right after the image line.
	expected := []byte("          image: gjkim42/kelos-controller:v0.1.0\n          imagePullPolicy: Always\n")
	if !bytes.Contains(result, expected) {
		t.Errorf("expected imagePullPolicy right after image line, got:\n%s", string(result))
	}
	// Verify per-image pull policy args are inserted after each --*-image= arg.
	for _, arg := range []string{
		"--claude-code-image-pull-policy=Always",
		"--spawner-image-pull-policy=Always",
	} {
		if !bytes.Contains(result, []byte(arg)) {
			t.Errorf("expected %q in result, got:\n%s", arg, string(result))
		}
	}
	// Verify --leader-elect does not get a pull policy arg.
	if bytes.Contains(result, []byte("--leader-elect-pull-policy")) {
		t.Errorf("unexpected pull policy for --leader-elect, got:\n%s", string(result))
	}
}

func TestWithImagePullPolicy_EmbeddedController(t *testing.T) {
	result := withImagePullPolicy(manifests.InstallController, "IfNotPresent")
	if !bytes.Contains(result, []byte("imagePullPolicy: IfNotPresent")) {
		t.Errorf("expected imagePullPolicy: IfNotPresent in embedded controller manifest, got:\n%s", string(result[:min(len(result), 500)]))
	}
	for _, arg := range []string{
		"--claude-code-image-pull-policy=IfNotPresent",
		"--codex-image-pull-policy=IfNotPresent",
		"--gemini-image-pull-policy=IfNotPresent",
		"--opencode-image-pull-policy=IfNotPresent",
		"--spawner-image-pull-policy=IfNotPresent",
		"--token-refresher-image-pull-policy=IfNotPresent",
	} {
		if !bytes.Contains(result, []byte(arg)) {
			t.Errorf("expected %q in result", arg)
		}
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

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
}
