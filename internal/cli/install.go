package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/strvals"
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

type helmValuesOptions struct {
	imageTag                   *string
	pullPolicy                 string
	disableHeartbeat           bool
	spawnerResourceRequests    string
	spawnerResourceLimits      string
	ghproxyResourceRequests    string
	ghproxyResourceLimits      string
	controllerResourceRequests string
	controllerResourceLimits   string
	ghproxyAllowedUpstreams    string
	ghproxyCacheTTL            string
}

type installValuesOptions struct {
	defaultImageTag string
	valuesFiles     []string
	setValues       []string
	setStringValues []string
	setFileValues   []string
	flagValues      helmValuesOptions
}

func newInstallCommand(cfg *ClientConfig) *cobra.Command {
	var dryRun bool
	var flagVersion string
	var valuesFiles []string
	var setValues []string
	var setStringValues []string
	var setFileValues []string
	var imagePullPolicy string
	var disableHeartbeat bool
	var spawnerResourceRequests string
	var spawnerResourceLimits string
	var ghproxyResourceRequests string
	var ghproxyResourceLimits string
	var controllerResourceRequests string
	var controllerResourceLimits string
	var ghproxyAllowedUpstreams string
	var ghproxyCacheTTL string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install kelos CRDs and controller into the cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			installVersion := version.Version
			if flagVersion != "" {
				installVersion = flagVersion
			}

			vals, err := buildInstallValues(cmd.InOrStdin(), installValuesOptions{
				defaultImageTag: installVersion,
				valuesFiles:     valuesFiles,
				setValues:       setValues,
				setStringValues: setStringValues,
				setFileValues:   setFileValues,
				flagValues: helmValuesOptions{
					imageTag:                   nonEmptyStringPtr(flagVersion),
					pullPolicy:                 imagePullPolicy,
					disableHeartbeat:           disableHeartbeat,
					spawnerResourceRequests:    spawnerResourceRequests,
					spawnerResourceLimits:      spawnerResourceLimits,
					ghproxyResourceRequests:    ghproxyResourceRequests,
					ghproxyResourceLimits:      ghproxyResourceLimits,
					controllerResourceRequests: controllerResourceRequests,
					controllerResourceLimits:   controllerResourceLimits,
					ghproxyAllowedUpstreams:    ghproxyAllowedUpstreams,
					ghproxyCacheTTL:            ghproxyCacheTTL,
				},
			})
			if err != nil {
				return err
			}

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

			fmt.Fprintf(os.Stdout, "Installing kelos controller (version: %s)\n", installVersion)
			if err := applyManifests(ctx, dc, dyn, controllerManifest); err != nil {
				return fmt.Errorf("installing controller: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Kelos installed successfully\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the manifests that would be applied without installing")
	cmd.Flags().StringArrayVarP(&valuesFiles, "values", "f", nil, "specify values in a YAML file (use '-' to read from stdin)")
	cmd.Flags().StringArrayVar(&setValues, "set", nil, "set chart values on the command line (key1=val1,key2=val2)")
	cmd.Flags().StringArrayVar(&setStringValues, "set-string", nil, "set string chart values on the command line (key1=val1,key2=val2)")
	cmd.Flags().StringArrayVar(&setFileValues, "set-file", nil, "set chart values from files (key1=path1,key2=path2)")
	cmd.Flags().StringVar(&flagVersion, "version", "", "override the version used for image tags (defaults to the binary version)")
	cmd.Flags().StringVar(&imagePullPolicy, "image-pull-policy", "", "set imagePullPolicy on controller containers (e.g. Always, IfNotPresent, Never)")
	cmd.Flags().BoolVar(&disableHeartbeat, "disable-heartbeat", false, "do not install the telemetry heartbeat CronJob")
	cmd.Flags().StringVar(&spawnerResourceRequests, "spawner-resource-requests", "", "resource requests for spawner containers (e.g., cpu=250m,memory=512Mi)")
	cmd.Flags().StringVar(&spawnerResourceLimits, "spawner-resource-limits", "", "resource limits for spawner containers (e.g., cpu=1,memory=1Gi)")
	cmd.Flags().StringVar(&ghproxyResourceRequests, "ghproxy-resource-requests", "", "resource requests for workspace ghproxy containers (e.g., cpu=50m,memory=64Mi)")
	cmd.Flags().StringVar(&ghproxyResourceLimits, "ghproxy-resource-limits", "", "resource limits for workspace ghproxy containers (e.g., cpu=200m,memory=128Mi)")
	cmd.Flags().StringVar(&controllerResourceRequests, "controller-resource-requests", "", "resource requests for the controller container (e.g., cpu=10m,memory=64Mi)")
	cmd.Flags().StringVar(&controllerResourceLimits, "controller-resource-limits", "", "resource limits for the controller container (e.g., cpu=500m,memory=128Mi)")
	cmd.Flags().StringVar(&ghproxyAllowedUpstreams, "ghproxy-allowed-upstreams", "", "comma-separated list of allowed upstream base URLs for ghproxy (e.g., https://api.github.com,https://github.example.com/api/v3)")
	cmd.Flags().StringVar(&ghproxyCacheTTL, "ghproxy-cache-ttl", "", "cache TTL for workspace ghproxy instances (e.g., 30s, 1m)")

	return cmd
}

// buildHelmValues constructs the values map for Helm chart rendering from CLI flags.
func buildHelmValues(ver string, pullPolicy string, disableHeartbeat bool, spawnerResourceRequests string, spawnerResourceLimits string, controllerResourceRequests string, controllerResourceLimits string, ghproxyAllowedUpstreams string) map[string]interface{} {
	return buildHelmValuesWithGHProxyResources(ver, pullPolicy, disableHeartbeat, spawnerResourceRequests, spawnerResourceLimits, "", "", controllerResourceRequests, controllerResourceLimits, ghproxyAllowedUpstreams, "")
}

func buildHelmValuesWithGHProxyResources(ver string, pullPolicy string, disableHeartbeat bool, spawnerResourceRequests string, spawnerResourceLimits string, ghproxyResourceRequests string, ghproxyResourceLimits string, controllerResourceRequests string, controllerResourceLimits string, ghproxyAllowedUpstreams string, ghproxyCacheTTL string) map[string]interface{} {
	return buildHelmValuesFromOptions(helmValuesOptions{
		imageTag:                   ptr.To(ver),
		pullPolicy:                 pullPolicy,
		disableHeartbeat:           disableHeartbeat,
		spawnerResourceRequests:    spawnerResourceRequests,
		spawnerResourceLimits:      spawnerResourceLimits,
		ghproxyResourceRequests:    ghproxyResourceRequests,
		ghproxyResourceLimits:      ghproxyResourceLimits,
		controllerResourceRequests: controllerResourceRequests,
		controllerResourceLimits:   controllerResourceLimits,
		ghproxyAllowedUpstreams:    ghproxyAllowedUpstreams,
		ghproxyCacheTTL:            ghproxyCacheTTL,
	})
}

func buildHelmValuesFromOptions(opts helmValuesOptions) map[string]interface{} {
	vals := map[string]interface{}{}

	imageVals := map[string]interface{}{}
	if opts.imageTag != nil {
		imageVals["tag"] = *opts.imageTag
	}
	if opts.pullPolicy != "" {
		imageVals["pullPolicy"] = opts.pullPolicy
	}
	if len(imageVals) > 0 {
		vals["image"] = imageVals
	}

	if opts.disableHeartbeat {
		vals["telemetry"] = map[string]interface{}{
			"enabled": false,
		}
	}

	spawnerResources := map[string]interface{}{}
	if opts.spawnerResourceRequests != "" {
		spawnerResources["requests"] = opts.spawnerResourceRequests
	}
	if opts.spawnerResourceLimits != "" {
		spawnerResources["limits"] = opts.spawnerResourceLimits
	}
	if len(spawnerResources) > 0 {
		vals["spawner"] = map[string]interface{}{
			"resources": spawnerResources,
		}
	}

	ghproxyResources := map[string]interface{}{}
	if opts.ghproxyResourceRequests != "" {
		ghproxyResources["requests"] = opts.ghproxyResourceRequests
	}
	if opts.ghproxyResourceLimits != "" {
		ghproxyResources["limits"] = opts.ghproxyResourceLimits
	}
	if len(ghproxyResources) > 0 {
		ghproxyVals, _ := vals["ghproxy"].(map[string]interface{})
		if ghproxyVals == nil {
			ghproxyVals = map[string]interface{}{}
		}
		ghproxyVals["resources"] = ghproxyResources
		vals["ghproxy"] = ghproxyVals
	}
	controllerResources := map[string]interface{}{}
	if opts.controllerResourceRequests != "" {
		controllerResources["requests"] = parseResourceString(opts.controllerResourceRequests)
	}
	if opts.controllerResourceLimits != "" {
		controllerResources["limits"] = parseResourceString(opts.controllerResourceLimits)
	}
	if len(controllerResources) > 0 {
		vals["controller"] = map[string]interface{}{
			"resources": controllerResources,
		}
	}
	if opts.ghproxyAllowedUpstreams != "" {
		ghproxyVals, _ := vals["ghproxy"].(map[string]interface{})
		if ghproxyVals == nil {
			ghproxyVals = map[string]interface{}{}
		}
		ghproxyVals["allowedUpstreams"] = opts.ghproxyAllowedUpstreams
		vals["ghproxy"] = ghproxyVals
	}
	if opts.ghproxyCacheTTL != "" {
		ghproxyVals, _ := vals["ghproxy"].(map[string]interface{})
		if ghproxyVals == nil {
			ghproxyVals = map[string]interface{}{}
		}
		ghproxyVals["cacheTTL"] = opts.ghproxyCacheTTL
		vals["ghproxy"] = ghproxyVals
	}
	return vals
}

func buildInstallValues(stdin io.Reader, opts installValuesOptions) (map[string]interface{}, error) {
	vals := map[string]interface{}{}
	stdinConsumed := false

	for _, path := range opts.valuesFiles {
		fileVals, err := loadValuesFile(path, stdin, &stdinConsumed)
		if err != nil {
			return nil, err
		}
		vals = chartutil.MergeTables(fileVals, vals)
	}

	vals = chartutil.MergeTables(buildHelmValuesFromOptions(opts.flagValues), vals)

	for _, setArg := range opts.setValues {
		if err := strvals.ParseInto(setArg, vals); err != nil {
			return nil, fmt.Errorf("parsing --set %q: %w", setArg, err)
		}
	}

	for _, setStringArg := range opts.setStringValues {
		if err := strvals.ParseIntoString(setStringArg, vals); err != nil {
			return nil, fmt.Errorf("parsing --set-string %q: %w", setStringArg, err)
		}
	}

	for _, setFileArg := range opts.setFileValues {
		if err := strvals.ParseIntoFile(setFileArg, vals, readSetFileValue); err != nil {
			return nil, fmt.Errorf("parsing --set-file %q: %w", setFileArg, err)
		}
	}

	if opts.defaultImageTag != "" && !hasNestedKey(vals, "image", "tag") {
		vals = chartutil.MergeTables(map[string]interface{}{
			"image": map[string]interface{}{
				"tag": opts.defaultImageTag,
			},
		}, vals)
	}

	if err := validateInstallValues(vals); err != nil {
		return nil, err
	}

	return disableChartCRDs(vals), nil
}

func loadValuesFile(path string, stdin io.Reader, stdinConsumed *bool) (map[string]interface{}, error) {
	if path == "-" {
		if *stdinConsumed {
			return nil, fmt.Errorf("reading values from stdin: '-' can only be used once")
		}
		*stdinConsumed = true

		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading values from stdin: %w", err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return map[string]interface{}{}, nil
		}

		vals, err := chartutil.ReadValues(data)
		if err != nil {
			return nil, fmt.Errorf("parsing values from stdin: %w", err)
		}
		return vals, nil
	}

	vals, err := chartutil.ReadValuesFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading values file %q: %w", path, err)
	}
	return vals, nil
}

func readSetFileValue(path []rune) (interface{}, error) {
	data, err := os.ReadFile(string(path))
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func hasNestedKey(vals map[string]interface{}, path ...string) bool {
	current := vals
	for i, key := range path {
		value, ok := current[key]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		next, ok := value.(map[string]interface{})
		if !ok {
			return false
		}
		current = next
	}
	return false
}

func validateInstallValues(vals map[string]interface{}) error {
	crds, ok := vals["crds"]
	if !ok {
		return nil
	}

	crdMap, ok := crds.(map[string]interface{})
	if !ok {
		return fmt.Errorf("crds must be a map, got %T", crds)
	}

	installValue, ok := crdMap["install"]
	if !ok {
		return nil
	}

	installEnabled, ok := installValue.(bool)
	if !ok {
		return fmt.Errorf("crds.install must be a boolean, got %T", installValue)
	}
	if installEnabled {
		return fmt.Errorf("kelos install manages CRDs separately; crds.install must be omitted or false")
	}
	return nil
}

func disableChartCRDs(vals map[string]interface{}) map[string]interface{} {
	if vals == nil {
		vals = map[string]interface{}{}
	}
	vals["crds"] = map[string]interface{}{
		"install": false,
	}
	return vals
}

// parseResourceString converts a comma-separated key=value string (e.g.
// "cpu=100m,memory=256Mi") into a map suitable for Helm values.
func parseResourceString(s string) map[string]interface{} {
	result := map[string]interface{}{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

// nonEmptyStringPtr returns a pointer to s, or nil if s is empty. The nil
// signal is load-bearing: it lets buildHelmValuesFromOptions distinguish
// "user did not pass --version" (leave image.tag alone) from "user passed
// --version with an explicit value" (override image.tag).
func nonEmptyStringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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

			// Render the chart with CRDs disabled to identify resources to
			// delete. Uninstall does not persist install values, so we force
			// optional webhookServer.sources.*.enabled on here to ensure any
			// webhook-related RBAC and namespaced resources are included in
			// cleanup. Only webhookServer.sources.*.enabled currently gates
			// cluster-scoped resources; other optional flags (ingress,
			// gateway) produce only namespaced resources that the namespace
			// cascade reclaims when the kelos-system namespace is deleted.
			// Any future optional feature that adds a cluster-scoped resource
			// must be forced on here too. Deleting resources that were never
			// installed is safe because deleteManifests ignores not-found
			// errors.
			controllerManifest, err := helmchart.Render(manifests.ChartFS, disableChartCRDs(map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						"github": map[string]interface{}{
							"enabled":    true,
							"secretName": "kelos-uninstall-placeholder",
						},
						"linear": map[string]interface{}{
							"enabled":    true,
							"secretName": "kelos-uninstall-placeholder",
						},
					},
				},
			}))
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
