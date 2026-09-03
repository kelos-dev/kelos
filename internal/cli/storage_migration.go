package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	"github.com/kelos-dev/kelos/internal/manifests"
)

type kelosCRDDefinition struct {
	name              string
	resource          schema.GroupVersionResource
	namespaced        bool
	versions          map[string]struct{}
	conversionWebhook bool
}

type storageMigrationResult struct {
	resourcesUpdated int
	crdsUpdated      int
}

func newMigrateStorageCommand(cfg *ClientConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-storage",
		Short: "Migrate Kelos resources to their current storage API versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			restConfig, _, err := cfg.resolveConfig()
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(restConfig)
			if err != nil {
				return fmt.Errorf("creating dynamic client: %w", err)
			}
			definitions, err := parseKelosCRDDefinitions(manifests.InstallCRD)
			if err != nil {
				return err
			}
			result, err := migrateKelosStorage(cmd.Context(), dyn, definitions)
			if err != nil {
				return err
			}
			if result.crdsUpdated == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Kelos resource storage is current")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Migrated %d resources across %d CRDs\n", result.resourcesUpdated, result.crdsUpdated)
			return nil
		},
	}
}

func parseKelosCRDDefinitions(data []byte) ([]kelosCRDDefinition, error) {
	objects, err := parseManifests(data)
	if err != nil {
		return nil, fmt.Errorf("parsing Kelos CRDs: %w", err)
	}

	definitions := make([]kelosCRDDefinition, 0, len(objects))
	for _, object := range objects {
		if object.GetAPIVersion() != "apiextensions.k8s.io/v1" || object.GetKind() != "CustomResourceDefinition" {
			continue
		}
		group, found, err := unstructured.NestedString(object.Object, "spec", "group")
		if err != nil || !found || group == "" {
			return nil, fmt.Errorf("reading group for CRD %s", object.GetName())
		}
		resource, found, err := unstructured.NestedString(object.Object, "spec", "names", "plural")
		if err != nil || !found || resource == "" {
			return nil, fmt.Errorf("reading plural name for CRD %s", object.GetName())
		}
		scope, found, err := unstructured.NestedString(object.Object, "spec", "scope")
		if err != nil || !found {
			return nil, fmt.Errorf("reading scope for CRD %s", object.GetName())
		}
		versions, found, err := unstructured.NestedSlice(object.Object, "spec", "versions")
		if err != nil || !found || len(versions) == 0 {
			return nil, fmt.Errorf("reading versions for CRD %s", object.GetName())
		}

		definition := kelosCRDDefinition{
			name:       object.GetName(),
			namespaced: scope == "Namespaced",
			versions:   make(map[string]struct{}, len(versions)),
		}
		for _, rawVersion := range versions {
			version, ok := rawVersion.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("reading version for CRD %s", object.GetName())
			}
			name, ok := version["name"].(string)
			if !ok || name == "" {
				return nil, fmt.Errorf("reading version name for CRD %s", object.GetName())
			}
			definition.versions[name] = struct{}{}
			if storage, _ := version["storage"].(bool); storage {
				if definition.resource.Version != "" {
					return nil, fmt.Errorf("CRD %s has multiple storage versions", object.GetName())
				}
				definition.resource = schema.GroupVersionResource{Group: group, Version: name, Resource: resource}
			}
		}
		if definition.resource.Version == "" {
			return nil, fmt.Errorf("CRD %s has no storage version", object.GetName())
		}
		strategy, _, err := unstructured.NestedString(object.Object, "spec", "conversion", "strategy")
		if err != nil {
			return nil, fmt.Errorf("reading conversion strategy for CRD %s: %w", object.GetName(), err)
		}
		definition.conversionWebhook = strategy == "Webhook"
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func kelosCRDVersionsWillBeRemoved(ctx context.Context, dyn dynamic.Interface, definitions []kelosCRDDefinition) (bool, error) {
	for _, definition := range definitions {
		crd, err := dyn.Resource(crdGVR).Get(ctx, definition.name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("checking CRD %s: %w", definition.name, err)
		}
		versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
		if err != nil || !found {
			return false, fmt.Errorf("reading live versions for CRD %s", definition.name)
		}
		for _, rawVersion := range versions {
			version, ok := rawVersion.(map[string]interface{})
			if !ok {
				return false, fmt.Errorf("reading live version for CRD %s", definition.name)
			}
			name, ok := version["name"].(string)
			if !ok {
				return false, fmt.Errorf("reading live version name for CRD %s", definition.name)
			}
			if _, retained := definition.versions[name]; !retained {
				return true, nil
			}
		}
	}
	return false, nil
}

func migrateKelosStorage(ctx context.Context, dyn dynamic.Interface, definitions []kelosCRDDefinition) (storageMigrationResult, error) {
	var result storageMigrationResult
	for _, definition := range definitions {
		crd, err := dyn.Resource(crdGVR).Get(ctx, definition.name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("checking CRD %s: %w", definition.name, err)
		}
		storedVersions, found, err := unstructured.NestedStringSlice(crd.Object, "status", "storedVersions")
		if err != nil {
			return result, fmt.Errorf("reading stored versions for CRD %s: %w", definition.name, err)
		}
		if !found || storageVersionsCurrent(storedVersions, definition.resource.Version) {
			continue
		}
		if !crdServesVersion(crd, definition.resource.Version) {
			return result, fmt.Errorf("CRD %s does not serve storage version %s; install an intermediate Kelos release before migrating", definition.name, definition.resource.Version)
		}

		updated, err := rewriteResourcesAtStorageVersion(ctx, dyn, definition)
		if err != nil {
			return result, err
		}
		patch, err := json.Marshal(map[string]interface{}{
			"status": map[string]interface{}{"storedVersions": []string{definition.resource.Version}},
		})
		if err != nil {
			return result, fmt.Errorf("building stored version patch for CRD %s: %w", definition.name, err)
		}
		if _, err := dyn.Resource(crdGVR).Patch(ctx, definition.name, types.MergePatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
			return result, fmt.Errorf("updating stored versions for CRD %s: %w", definition.name, err)
		}
		result.resourcesUpdated += updated
		result.crdsUpdated++
	}
	return result, nil
}

func storageVersionsCurrent(versions []string, current string) bool {
	return len(versions) == 1 && versions[0] == current
}

func crdServesVersion(crd *unstructured.Unstructured, target string) bool {
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return false
	}
	for _, rawVersion := range versions {
		version, ok := rawVersion.(map[string]interface{})
		if !ok || version["name"] != target {
			continue
		}
		served, _ := version["served"].(bool)
		return served
	}
	return false
}

func rewriteResourcesAtStorageVersion(ctx context.Context, dyn dynamic.Interface, definition kelosCRDDefinition) (int, error) {
	resource := dyn.Resource(definition.resource)
	var list *unstructured.UnstructuredList
	var err error
	if definition.namespaced {
		list, err = resource.Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	} else {
		list, err = resource.List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return 0, fmt.Errorf("listing %s at %s: %w", definition.resource.Resource, definition.resource.GroupVersion(), err)
	}

	updated := 0
	for i := range list.Items {
		item := &list.Items[i]
		var itemResource dynamic.ResourceInterface = resource
		if definition.namespaced {
			itemResource = resource.Namespace(item.GetNamespace())
		}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, err := itemResource.Get(ctx, item.GetName(), metav1.GetOptions{})
			if errors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			_, err = itemResource.Update(ctx, current, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return updated, fmt.Errorf("rewriting %s %s/%s at %s: %w", definition.resource.Resource, item.GetNamespace(), item.GetName(), definition.resource.GroupVersion(), err)
		}
		updated++
	}
	return updated, nil
}

func conversionCRDNames(definitions []kelosCRDDefinition) []string {
	var names []string
	for _, definition := range definitions {
		if definition.conversionWebhook {
			names = append(names, definition.name)
		}
	}
	return names
}
