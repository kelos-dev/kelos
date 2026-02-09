package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/axon-core/axon/internal/manifests"
	"github.com/axon-core/axon/internal/version"
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
	cmd.SetArgs([]string{"install", "--config", "/nonexistent/path/config.yaml"})
	err := cmd.Execute()
	// We expect an error (no cluster), but not a config-loading error.
	if err != nil && err.Error() == "loading config: open /nonexistent/path/config.yaml: no such file or directory" {
		t.Fatal("install should not fail on missing config file")
	}
}

func TestUninstallCommand_SkipsConfigLoading(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"uninstall", "--config", "/nonexistent/path/config.yaml"})
	err := cmd.Execute()
	if err != nil && err.Error() == "loading config: open /nonexistent/path/config.yaml: no such file or directory" {
		t.Fatal("uninstall should not fail on missing config file")
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
	original := version.Version
	defer func() { version.Version = original }()

	version.Version = "latest"
	data := []byte("image: gjkim42/axon-controller:latest")
	result := versionedManifest(data)
	if !bytes.Equal(result, data) {
		t.Errorf("expected manifest unchanged for latest version, got %s", string(result))
	}
}

func TestVersionedManifest_Tagged(t *testing.T) {
	original := version.Version
	defer func() { version.Version = original }()

	version.Version = "v0.1.0"
	data := []byte("image: gjkim42/axon-controller:latest")
	result := versionedManifest(data)
	expected := []byte("image: gjkim42/axon-controller:v0.1.0")
	if !bytes.Equal(result, expected) {
		t.Errorf("expected %s, got %s", string(expected), string(result))
	}
}

func TestVersionedManifest_MultipleImages(t *testing.T) {
	original := version.Version
	defer func() { version.Version = original }()

	version.Version = "v0.2.0"
	data := []byte(`image: gjkim42/axon-controller:latest
args:
  - --spawner-image=gjkim42/axon-spawner:latest
  - --claude-code-image=gjkim42/claude-code:latest`)
	result := versionedManifest(data)
	if bytes.Contains(result, []byte(":latest")) {
		t.Errorf("expected all :latest tags to be replaced, got %s", string(result))
	}
	if !bytes.Contains(result, []byte(":v0.2.0")) {
		t.Errorf("expected :v0.2.0 tags in result, got %s", string(result))
	}
}

func TestVersionedManifest_EmbeddedController(t *testing.T) {
	original := version.Version
	defer func() { version.Version = original }()

	version.Version = "v1.0.0"
	result := versionedManifest(manifests.InstallController)
	if bytes.Contains(result, []byte(":latest")) {
		t.Error("Expected all :latest tags to be replaced in embedded controller manifest")
	}
	if !bytes.Contains(result, []byte(":v1.0.0")) {
		t.Error("Expected :v1.0.0 tags in versioned controller manifest")
	}
}

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
}

func TestWaitForDeletion_AlreadyGone(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClient(scheme)

	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	rc := client.Resource(gvr).Namespace("default")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := waitForDeletion(ctx, rc, "nonexistent"); err != nil {
		t.Fatalf("unexpected error waiting for already-deleted resource: %v", err)
	}
}

func TestWaitForDeletion_EventuallyDeleted(t *testing.T) {
	scheme := runtime.NewScheme()

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
	obj.SetName("test-cm")
	obj.SetNamespace("default")
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)

	client := dynamicfake.NewSimpleDynamicClient(scheme, obj)

	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	rc := client.Resource(gvr).Namespace("default")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Delete the object asynchronously to simulate eventual deletion.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = rc.Delete(context.Background(), "test-cm", metav1.DeleteOptions{})
	}()

	if err := waitForDeletion(ctx, rc, "test-cm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
