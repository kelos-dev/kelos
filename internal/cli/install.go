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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
	"github.com/kelos-dev/kelos/internal/version"
)

const fieldManager = "kelos"

func newInstallCommand(cfg *ClientConfig) *cobra.Command {
	var dryRun bool
	var flagVersion string
	var imagePullPolicy string
	var disableHeartbeat bool
	var spawnerResourceRequests string
	var spawnerResourceLimits string
	var tokenRefresherResourceRequests string
	var tokenRefresherResourceLimits string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install kelos CRDs and controller into the cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagVersion != "" {
				version.Version = flagVersion
			}

			vals := buildHelmValues(
				version.Version,
				imagePullPolicy,
				disableHeartbeat,
				spawnerResourceRequests,
				spawnerResourceLimits,
				tokenRefresherResourceRequests,
				tokenRefresherResourceLimits,
			)
			controllerManifest, err := helmchart.Render(manifests.ChartFS, vals)
			if err != nil {
				return fmt.Errorf("rendering chart: %w", err)
			}

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

			fmt.Fprintf(os.Stdout, "Installing kelos CRDs\n")
			if err := applyManifests(ctx, dc, dyn, manifests.InstallCRD); err != nil {
				return fmt.Errorf("installing CRDs: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Installing kelos controller (version: %s)\n", version.Version)
			if err := applyManifests(ctx, dc, dyn, controllerManifest); err != nil {
				return fmt.Errorf("installing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Kelos installed successfully\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the manifests that would be applied without installing")
	cmd.Flags().StringVar(&flagVersion, "version", "", "override the version used for image tags (defaults to the binary version)")
	cmd.Flags().StringVar(&imagePullPolicy, "image-pull-policy", "", "set imagePullPolicy on controller containers (e.g. Always, IfNotPresent, Never)")
	cmd.Flags().BoolVar(&disableHeartbeat, "disable-heartbeat", false, "do not install the telemetry heartbeat CronJob")
	cmd.Flags().StringVar(&spawnerResourceRequests, "spawner-resource-requests", "", "resource requests for spawner containers (e.g., cpu=250m,memory=512Mi)")
	cmd.Flags().StringVar(&spawnerResourceLimits, "spawner-resource-limits", "", "resource limits for spawner containers (e.g., cpu=1,memory=1Gi)")
	cmd.Flags().StringVar(&tokenRefresherResourceRequests, "token-refresher-resource-requests", "", "resource requests for token refresher sidecars (e.g., cpu=100m,memory=128Mi)")
	cmd.Flags().StringVar(&tokenRefresherResourceLimits, "token-refresher-resource-limits", "", "resource limits for token refresher sidecars (e.g., cpu=200m,memory=256Mi)")

	return cmd
}

// buildHelmValues constructs the values map for Helm chart rendering from CLI flags.
func buildHelmValues(ver string, pullPolicy string, disableHeartbeat bool, spawnerResourceRequests string, spawnerResourceLimits string, tokenRefresherResourceRequests string, tokenRefresherResourceLimits string) map[string]interface{} {
	imageVals := map[string]interface{}{
		"tag": ver,
	}
	if pullPolicy != "" {
		imageVals["pullPolicy"] = pullPolicy
	}
	vals := map[string]interface{}{
		"image": imageVals,
	}
	if disableHeartbeat {
		vals["telemetry"] = map[string]interface{}{
			"enabled": false,
		}
	}
	if spawnerResourceRequests != "" {
		vals["spawnerResourceRequests"] = spawnerResourceRequests
	}
	if spawnerResourceLimits != "" {
		vals["spawnerResourceLimits"] = spawnerResourceLimits
	}
	if tokenRefresherResourceRequests != "" {
		vals["tokenRefresherResourceRequests"] = tokenRefresherResourceRequests
	}
	if tokenRefresherResourceLimits != "" {
		vals["tokenRefresherResourceLimits"] = tokenRefresherResourceLimits
	}
	return vals
}

// kelosGVRs lists the kelos custom resource GVRs that need to be cleaned up
// before the controller and CRDs can be safely removed. Resources with
// finalizers (tasks, taskspawners) must be deleted while the controller is
// still running so it can process the finalizer removal.
var kelosGVRs = []schema.GroupVersionResource{
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "tasks"},
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "taskspawners"},
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "workspaces"},
	{Group: "kelos.dev", Version: "v1alpha1", Resource: "agentconfigs"},
}

// crDeletionTimeout is the maximum time to wait for all custom resources
// to be fully deleted (finalizers processed) before proceeding.
const crDeletionTimeout = 5 * time.Minute

func newUninstallCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall kelos controller and CRDs from the cluster",
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

			// Render the chart with default values to identify resources to delete.
			// Resource names and kinds do not change with values, so defaults
			// suffice. This renders all resources (including telemetry CronJob)
			// which is safe because deleteManifests ignores not-found errors.
			controllerManifest, err := helmchart.Render(manifests.ChartFS, nil)
			if err != nil {
				return fmt.Errorf("rendering chart for uninstall: %w", err)
			}

			ctx := cmd.Context()

			// Delete all custom resources first while the controller is
			// still running. The controller handles finalizer removal on
			// Tasks and TaskSpawners; deleting the controller first would
			// leave those resources stuck with unresolvable finalizers.
			fmt.Fprintf(os.Stdout, "Removing kelos custom resources\n")
			if err := deleteAllCustomResources(ctx, dyn); err != nil {
				return fmt.Errorf("removing custom resources: %w", err)
			}

			// Wait for all custom resources to be fully deleted. The
			// controller must process finalizers before the resources
			// disappear, so we poll until nothing remains.
			fmt.Fprintf(os.Stdout, "Waiting for custom resources to be deleted\n")
			if err := waitForCustomResourceDeletion(ctx, dyn); err != nil {
				return fmt.Errorf("waiting for custom resource deletion: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Removing kelos controller\n")
			if err := deleteManifests(ctx, dc, dyn, controllerManifest); err != nil {
				return fmt.Errorf("removing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Removing kelos CRDs\n")
			if err := deleteManifests(ctx, dc, dyn, manifests.InstallCRD); err != nil {
				return fmt.Errorf("removing CRDs: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Kelos uninstalled successfully\n")
			return nil
		},
	}

	return cmd
}

// deleteAllCustomResources deletes all instances of kelos custom resources
// across all namespaces. It skips resources whose CRD does not exist
// (e.g. if CRDs were already removed).
func deleteAllCustomResources(ctx context.Context, dyn dynamic.Interface) error {
	for _, gvr := range kelosGVRs {
		list, err := dyn.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			if errors.IsNotFound(err) || meta.IsNoMatchError(err) {
				continue
			}
			return fmt.Errorf("listing %s: %w", gvr.Resource, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			if obj.GetDeletionTimestamp() != nil {
				continue
			}
			if err := dyn.Resource(gvr).Namespace(obj.GetNamespace()).Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("deleting %s %s/%s: %w", gvr.Resource, obj.GetNamespace(), obj.GetName(), err)
			}
		}
	}
	return nil
}

// waitForCustomResourceDeletion polls until no kelos custom resources remain.
// This gives the controller time to process finalizers on Tasks and TaskSpawners.
func waitForCustomResourceDeletion(ctx context.Context, dyn dynamic.Interface) error {
	deadline := time.Now().Add(crDeletionTimeout)
	for {
		allGone := true
		for _, gvr := range kelosGVRs {
			list, err := dyn.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{Limit: 1})
			if err != nil {
				if errors.IsNotFound(err) || meta.IsNoMatchError(err) {
					continue
				}
				return fmt.Errorf("listing %s: %w", gvr.Resource, err)
			}
			if len(list.Items) > 0 {
				allGone = false
				break
			}
		}
		if allGone {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for custom resources to be deleted (finalizers may not be processed -- is the controller running?)")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
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

// applyManifests parses multi-document YAML and applies each object using
// server-side apply.
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
