package cli

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kelos-dev/kelos/internal/manifests"
)

func TestParseKelosCRDDefinitions(t *testing.T) {
	definitions, err := parseKelosCRDDefinitions(manifests.InstallCRD)
	if err != nil {
		t.Fatalf("parseKelosCRDDefinitions() error = %v", err)
	}
	if len(definitions) != len(kelosCRDNames) {
		t.Fatalf("got %d CRD definitions, want %d", len(definitions), len(kelosCRDNames))
	}
	for _, definition := range definitions {
		if definition.resource.Version != "v1alpha2" {
			t.Errorf("CRD %s storage version = %q, want v1alpha2", definition.name, definition.resource.Version)
		}
		if definition.conversionWebhook {
			t.Errorf("CRD %s unexpectedly uses webhook conversion", definition.name)
		}
	}
}

func TestMigrateKelosStorage(t *testing.T) {
	definition := kelosCRDDefinition{
		name:       "widgets.kelos.dev",
		resource:   schema.GroupVersionResource{Group: "kelos.dev", Version: "v2", Resource: "widgets"},
		namespaced: true,
		versions:   map[string]struct{}{"v2": {}},
	}
	crd := testCRD(definition.name, []map[string]interface{}{
		{"name": "v1", "served": true, "storage": false},
		{"name": "v2", "served": true, "storage": true},
	}, []string{"v1", "v2"})
	widget := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kelos.dev/v2",
		"kind":       "Widget",
		"metadata": map[string]interface{}{
			"name":      "example",
			"namespace": "default",
		},
	}}
	listKinds := map[schema.GroupVersionResource]string{
		crdGVR:              "CustomResourceDefinitionList",
		definition.resource: "WidgetList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, crd, widget)

	removing, err := kelosCRDVersionsWillBeRemoved(context.Background(), client, []kelosCRDDefinition{definition})
	if err != nil {
		t.Fatalf("kelosCRDVersionsWillBeRemoved() error = %v", err)
	}
	if !removing {
		t.Fatal("expected the live v1 version to be removed")
	}

	result, err := migrateKelosStorage(context.Background(), client, []kelosCRDDefinition{definition})
	if err != nil {
		t.Fatalf("migrateKelosStorage() error = %v", err)
	}
	if result.resourcesUpdated != 1 || result.crdsUpdated != 1 {
		t.Fatalf("migration result = %#v, want one resource and one CRD", result)
	}

	updatedCRD, err := client.Resource(crdGVR).Get(context.Background(), definition.name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get migrated CRD: %v", err)
	}
	storedVersions, found, err := unstructured.NestedStringSlice(updatedCRD.Object, "status", "storedVersions")
	if err != nil || !found {
		t.Fatalf("read stored versions: found=%v err=%v", found, err)
	}
	if len(storedVersions) != 1 || storedVersions[0] != "v2" {
		t.Fatalf("stored versions = %v, want [v2]", storedVersions)
	}
}

func TestMigrateKelosStorageRequiresLiveStorageVersion(t *testing.T) {
	definition := kelosCRDDefinition{
		name:       "widgets.kelos.dev",
		resource:   schema.GroupVersionResource{Group: "kelos.dev", Version: "v2", Resource: "widgets"},
		namespaced: true,
		versions:   map[string]struct{}{"v2": {}},
	}
	crd := testCRD(definition.name, []map[string]interface{}{
		{"name": "v1", "served": true, "storage": true},
		{"name": "v2", "served": true, "storage": false},
	}, []string{"v1"})
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), crd)

	_, err := migrateKelosStorage(context.Background(), client, []kelosCRDDefinition{definition})
	if err == nil || !strings.Contains(err.Error(), "does not serve v2 as its storage version") {
		t.Fatalf("migrateKelosStorage() error = %v, want live storage version error", err)
	}
}

func TestMigrateKelosStoragePagesResources(t *testing.T) {
	definition := kelosCRDDefinition{
		name:       "widgets.kelos.dev",
		resource:   schema.GroupVersionResource{Group: "kelos.dev", Version: "v2", Resource: "widgets"},
		namespaced: true,
		versions:   map[string]struct{}{"v2": {}},
	}
	crd := testCRD(definition.name, []map[string]interface{}{
		{"name": "v1", "served": true, "storage": false},
		{"name": "v2", "served": true, "storage": true},
	}, []string{"v1", "v2"})
	first := testWidget("first")
	second := testWidget("second")
	listKinds := map[schema.GroupVersionResource]string{
		crdGVR:              "CustomResourceDefinitionList",
		definition.resource: "WidgetList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, crd, first, second)
	listCalls := 0
	client.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		listCalls++
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(definition.resource.GroupVersion().WithKind("WidgetList"))
		if listCalls == 1 {
			list.Items = []unstructured.Unstructured{*first.DeepCopy()}
			list.SetContinue("next")
		} else {
			list.Items = []unstructured.Unstructured{*second.DeepCopy()}
		}
		return true, list, nil
	})

	result, err := migrateKelosStorage(context.Background(), client, []kelosCRDDefinition{definition})
	if err != nil {
		t.Fatalf("migrateKelosStorage() error = %v", err)
	}
	if listCalls != 2 {
		t.Fatalf("list calls = %d, want 2", listCalls)
	}
	if result.resourcesUpdated != 2 || result.crdsUpdated != 1 {
		t.Fatalf("migration result = %#v, want two resources and one CRD", result)
	}
}

func TestMigrateKelosStorageDoesNotCountDeletedResource(t *testing.T) {
	definition := kelosCRDDefinition{
		name:       "widgets.kelos.dev",
		resource:   schema.GroupVersionResource{Group: "kelos.dev", Version: "v2", Resource: "widgets"},
		namespaced: true,
		versions:   map[string]struct{}{"v2": {}},
	}
	crd := testCRD(definition.name, []map[string]interface{}{
		{"name": "v1", "served": true, "storage": false},
		{"name": "v2", "served": true, "storage": true},
	}, []string{"v1", "v2"})
	widget := testWidget("deleted")
	listKinds := map[schema.GroupVersionResource]string{
		crdGVR:              "CustomResourceDefinitionList",
		definition.resource: "WidgetList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, crd, widget)
	client.PrependReactor("get", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "kelos.dev", Resource: "widgets"}, widget.GetName())
	})

	result, err := migrateKelosStorage(context.Background(), client, []kelosCRDDefinition{definition})
	if err != nil {
		t.Fatalf("migrateKelosStorage() error = %v", err)
	}
	if result.resourcesUpdated != 0 || result.crdsUpdated != 1 {
		t.Fatalf("migration result = %#v, want zero resources and one CRD", result)
	}
}

func testWidget(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kelos.dev/v2",
		"kind":       "Widget",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": "default",
		},
	}}
}

func testCRD(name string, versions []map[string]interface{}, storedVersions []string) *unstructured.Unstructured {
	versionValues := make([]interface{}, 0, len(versions))
	for _, version := range versions {
		versionValues = append(versionValues, version)
	}
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"group": "kelos.dev",
			"names": map[string]interface{}{
				"kind":     "Widget",
				"listKind": "WidgetList",
				"plural":   "widgets",
				"singular": "widget",
			},
			"scope":    "Namespaced",
			"versions": versionValues,
		},
	}}
	if err := unstructured.SetNestedStringSlice(crd.Object, storedVersions, "status", "storedVersions"); err != nil {
		panic(err)
	}
	return crd
}
