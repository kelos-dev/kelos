package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/axon-core/axon/internal/manifests"
	"github.com/axon-core/axon/internal/version"
)

const fieldManager = "axon"

func newInstallCommand(cfg *ClientConfig) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install axon CRDs and controller into the cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			controllerManifest := versionedManifest(manifests.InstallController)

			if dryRun {
				if _, err := os.Stdout.Write(manifests.InstallCRD); err != nil {
					return err
				}
				fmt.Fprintln(os.Stdout, "---")
				_, err := os.Stdout.Write(controllerManifest)
				return err
			}

			restConfig, _, err := cfg.resolveConfig()
			if err != nil {
				return err
			}

			dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
			if err != nil {
				return fmt.Errorf("creating discovery client: %w", err)
			}
			dyn, err := dynamic.NewForConfig(restConfig)
			if err != nil {
				return fmt.Errorf("creating dynamic client: %w", err)
			}

			ctx := cmd.Context()

			fmt.Fprintf(os.Stdout, "Installing axon CRDs\n")
			if err := applyManifests(ctx, dc, dyn, manifests.InstallCRD); err != nil {
				return fmt.Errorf("installing CRDs: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Installing axon controller (version: %s)\n", version.Version)
			if err := applyManifests(ctx, dc, dyn, controllerManifest); err != nil {
				return fmt.Errorf("installing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Axon installed successfully\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the manifests that would be applied without installing")

	return cmd
}

// versionedManifest replaces ":latest" image tags with the current version
// tag in the controller manifest. When the version is "latest" (development
// builds), the manifest is returned as-is.
func versionedManifest(data []byte) []byte {
	if version.Version == "latest" {
		return data
	}
	return bytes.ReplaceAll(data, []byte(":latest"), []byte(":"+version.Version))
}

func newUninstallCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall axon controller and CRDs from the cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			restConfig, _, err := cfg.resolveConfig()
			if err != nil {
				return err
			}

			dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
			if err != nil {
				return fmt.Errorf("creating discovery client: %w", err)
			}
			dyn, err := dynamic.NewForConfig(restConfig)
			if err != nil {
				return fmt.Errorf("creating dynamic client: %w", err)
			}

			ctx := cmd.Context()

			fmt.Fprintf(os.Stdout, "Removing axon controller\n")
			if err := deleteManifests(ctx, dc, dyn, manifests.InstallController); err != nil {
				return fmt.Errorf("removing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Removing axon CRDs\n")
			if err := deleteManifests(ctx, dc, dyn, manifests.InstallCRD); err != nil {
				return fmt.Errorf("removing CRDs: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Axon uninstalled successfully\n")
			return nil
		},
	}

	return cmd
}

// parseManifests splits a multi-document YAML byte slice into individual
// unstructured objects, skipping empty documents.
func parseManifests(data []byte) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	for {
		doc, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("reading YAML document: %w", err)
		}
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
			return nil, fmt.Errorf("unmarshaling manifest: %w", err)
		}
		if obj.Object == nil {
			continue
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

// newRESTMapper creates a REST mapper using the discovery client to resolve
// API group resources. This should be called once and the mapper reused
// across multiple objects to avoid redundant API server calls.
func newRESTMapper(dc discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	gr, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, fmt.Errorf("discovering API resources: %w", err)
	}
	return restmapper.NewDiscoveryRESTMapper(gr), nil
}

// resourceClient returns a dynamic resource client for the given object,
// using the provided REST mapper to resolve the GVR and determine whether
// the resource is namespaced.
func resourceClient(mapper meta.RESTMapper, dyn dynamic.Interface, obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("mapping resource for %s: %w", gvk, err)
	}

	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return dyn.Resource(mapping.Resource).Namespace(obj.GetNamespace()), nil
	}
	return dyn.Resource(mapping.Resource), nil
}

// waitForDeletion polls until the given resource is fully deleted or the
// context is cancelled. Server-side apply on a resource that still carries a
// deletionTimestamp succeeds but the resource continues to be deleted, leaving
// the API unavailable. Waiting avoids this race.
func waitForDeletion(ctx context.Context, rc dynamic.ResourceInterface, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := rc.Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
}

// applyManifests parses multi-document YAML and applies each object using
// server-side apply. If an existing resource is being deleted (has a
// deletionTimestamp), it waits for deletion to complete before applying to
// prevent the applied resource from being garbage collected.
func applyManifests(ctx context.Context, dc discovery.DiscoveryInterface, dyn dynamic.Interface, data []byte) error {
	objs, err := parseManifests(data)
	if err != nil {
		return err
	}
	mapper, err := newRESTMapper(dc)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		rc, err := resourceClient(mapper, dyn, obj)
		if err != nil {
			return err
		}

		// Check if the resource is being deleted. If so, wait for
		// deletion to finish before applying; otherwise server-side
		// apply succeeds but the resource is still removed.
		existing, err := rc.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("checking %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if err == nil && existing.GetDeletionTimestamp() != nil {
			fmt.Fprintf(os.Stdout, "Waiting for %s %s to be deleted\n", obj.GetKind(), obj.GetName())
			if err := waitForDeletion(ctx, rc, obj.GetName()); err != nil {
				return fmt.Errorf("waiting for %s %s to be deleted: %w", obj.GetKind(), obj.GetName(), err)
			}
		}

		objData, err := yaml.Marshal(obj.Object)
		if err != nil {
			return fmt.Errorf("marshaling %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if _, err := rc.Patch(ctx, obj.GetName(), types.ApplyPatchType, objData, metav1.PatchOptions{
			FieldManager: fieldManager,
			Force:        ptr.To(true),
		}); err != nil {
			return fmt.Errorf("applying %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}

// deleteManifests parses multi-document YAML and deletes each object,
// ignoring not-found errors for idempotent uninstalls.
func deleteManifests(ctx context.Context, dc discovery.DiscoveryInterface, dyn dynamic.Interface, data []byte) error {
	objs, err := parseManifests(data)
	if err != nil {
		return err
	}
	mapper, err := newRESTMapper(dc)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		rc, err := resourceClient(mapper, dyn, obj)
		if err != nil {
			// If the resource type is not found (e.g. CRDs already deleted),
			// skip it for idempotent uninstalls.
			if meta.IsNoMatchError(err) {
				continue
			}
			return err
		}
		if err := rc.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("deleting %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}
