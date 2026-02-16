package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
	var flagVersion string
	var imagePullPolicy string
	var skipRBAC bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install axon CRDs and controller into the cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagVersion != "" {
				version.Version = flagVersion
			}
			controllerManifest := versionedManifest(manifests.InstallController, version.Version)
			if imagePullPolicy != "" {
				controllerManifest = withImagePullPolicy(controllerManifest, imagePullPolicy)
			}

			var filter func(*unstructured.Unstructured) bool
			if skipRBAC {
				filter = excludeClusterRBAC
			}

			if dryRun {
				return dryRunManifests(os.Stdout, manifests.InstallCRD, controllerManifest, filter)
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
			if err := applyManifests(ctx, dc, dyn, manifests.InstallCRD, filter); err != nil {
				return fmt.Errorf("installing CRDs: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Installing axon controller (version: %s)\n", version.Version)
			if err := applyManifests(ctx, dc, dyn, controllerManifest, filter); err != nil {
				return fmt.Errorf("installing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Axon installed successfully\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the manifests that would be applied without installing")
	cmd.Flags().StringVar(&flagVersion, "version", "", "override the version used for image tags (defaults to the binary version)")
	cmd.Flags().StringVar(&imagePullPolicy, "image-pull-policy", "", "set imagePullPolicy on controller containers (e.g. Always, IfNotPresent, Never)")
	cmd.Flags().BoolVar(&skipRBAC, "skip-rbac", false, "skip applying cluster-scoped RBAC resources (ClusterRole, ClusterRoleBinding)")

	return cmd
}

// versionedManifest replaces ":latest" image tags with the given version
// tag in the controller manifest. When ver is "latest" (development
// builds), the manifest is returned as-is.
func versionedManifest(data []byte, ver string) []byte {
	if ver == "latest" {
		return data
	}
	return bytes.ReplaceAll(data, []byte(":latest"), []byte(":"+ver))
}

// withImagePullPolicy inserts an imagePullPolicy field after each "image:"
// line and a corresponding --*-image-pull-policy arg after each --*-image=
// arg in the manifest YAML, preserving the original indentation.
func withImagePullPolicy(data []byte, policy string) []byte {
	var buf bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		buf.Write(line)
		buf.WriteByte('\n')
		trimmed := bytes.TrimLeft(line, " ")
		indent := line[:len(line)-len(trimmed)]
		if bytes.HasPrefix(trimmed, []byte("image:")) {
			buf.Write(indent)
			buf.WriteString("imagePullPolicy: ")
			buf.WriteString(policy)
			buf.WriteByte('\n')
		} else if bytes.HasPrefix(trimmed, []byte("- --")) && bytes.Contains(trimmed, []byte("-image=")) {
			eqIdx := bytes.IndexByte(trimmed, '=')
			flagName := string(trimmed[2:eqIdx])
			buf.Write(indent)
			buf.WriteString("- ")
			buf.WriteString(flagName)
			buf.WriteString("-pull-policy=")
			buf.WriteString(policy)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
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
			if err := deleteManifests(ctx, dc, dyn, manifests.InstallController, nil); err != nil {
				return fmt.Errorf("removing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Removing axon CRDs\n")
			if err := deleteManifests(ctx, dc, dyn, manifests.InstallCRD, nil); err != nil {
				return fmt.Errorf("removing CRDs: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Axon uninstalled successfully\n")
			return nil
		},
	}

	return cmd
}

// dryRunManifests writes the combined manifests to w, optionally filtering
// out objects matched by exclude. When exclude is nil the raw bytes are
// written directly; otherwise each document is parsed, filtered, and
// re-serialized.
func dryRunManifests(w io.Writer, crdData, controllerData []byte, exclude func(*unstructured.Unstructured) bool) error {
	if exclude == nil {
		if _, err := w.Write(crdData); err != nil {
			return err
		}
		fmt.Fprintln(w, "---")
		_, err := w.Write(controllerData)
		return err
	}

	allData := [][]byte{crdData, controllerData}
	first := true
	for _, data := range allData {
		objs, err := parseManifests(data)
		if err != nil {
			return err
		}
		for _, obj := range objs {
			if exclude(obj) {
				continue
			}
			if !first {
				fmt.Fprintln(w, "---")
			}
			out, err := yaml.Marshal(obj.Object)
			if err != nil {
				return fmt.Errorf("marshaling %s %s: %w", obj.GetKind(), obj.GetName(), err)
			}
			if _, err := w.Write(out); err != nil {
				return err
			}
			first = false
		}
	}
	return nil
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

// clusterRBACKinds is the set of cluster-scoped RBAC resource kinds that
// are skipped when --skip-rbac is used.
var clusterRBACKinds = map[string]bool{
	"ClusterRole":        true,
	"ClusterRoleBinding": true,
}

// excludeClusterRBAC returns true for objects that should be skipped when
// the --skip-rbac flag is set.
func excludeClusterRBAC(obj *unstructured.Unstructured) bool {
	return clusterRBACKinds[obj.GetKind()]
}

// applyManifests parses multi-document YAML and applies each object using
// server-side apply. If exclude is non-nil, objects for which it returns
// true are skipped.
func applyManifests(ctx context.Context, dc discovery.DiscoveryInterface, dyn dynamic.Interface, data []byte, exclude func(*unstructured.Unstructured) bool) error {
	objs, err := parseManifests(data)
	if err != nil {
		return err
	}
	mapper, err := newRESTMapper(dc)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		if exclude != nil && exclude(obj) {
			continue
		}
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
// ignoring not-found errors for idempotent uninstalls. If exclude is
// non-nil, objects for which it returns true are skipped.
func deleteManifests(ctx context.Context, dc discovery.DiscoveryInterface, dyn dynamic.Interface, data []byte, exclude func(*unstructured.Unstructured) bool) error {
	objs, err := parseManifests(data)
	if err != nil {
		return err
	}
	mapper, err := newRESTMapper(dc)
	if err != nil {
		return err
	}
	for _, obj := range objs {
		if exclude != nil && exclude(obj) {
			continue
		}
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
