package cli

import (
	"testing"

	"github.com/axon-core/axon/internal/manifests"
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
