package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// These specs build TaskSpawners as unstructured objects because a Go client
// always serializes spec.when, even when empty. Only an unstructured object can
// express the manifest a user writes when they omit the field entirely, which
// is what the triggerMode relaxation is about.
var _ = Describe("TaskSpawner triggerMode validation", func() {
	const ns = "test-taskspawner-triggermode"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	spawner := func(name string, spec map[string]interface{}) *unstructured.Unstructured {
		spec["taskTemplate"] = map[string]interface{}{
			"type":        "claude-code",
			"credentials": map[string]interface{}{"type": "none"},
		}
		object := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": kelos.GroupVersion.String(),
			"kind":       "TaskSpawner",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec":       spec,
		}}
		return object
	}

	It("accepts an OnDemand spawner with no source", func() {
		object := spawner("ondemand-no-source", map[string]interface{}{"triggerMode": "OnDemand"})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).To(Succeed())
	})

	It("accepts an OnDemand spawner that keeps its source", func() {
		object := spawner("ondemand-with-source", map[string]interface{}{
			"triggerMode": "OnDemand",
			"when":        map[string]interface{}{"cron": map[string]interface{}{"schedule": "0 9 * * 1"}},
		})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).To(Succeed())
	})

	It("rejects a source-triggered spawner with no source", func() {
		object := spawner("source-no-source", map[string]interface{}{})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an explicitly source-triggered spawner with no source", func() {
		object := spawner("explicit-source-no-source", map[string]interface{}{"triggerMode": "Source"})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an unknown trigger mode", func() {
		object := spawner("bad-trigger-mode", map[string]interface{}{
			"triggerMode": "Whenever",
			"when":        map[string]interface{}{"cron": map[string]interface{}{"schedule": "0 9 * * 1"}},
		})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).NotTo(Succeed())
	})

	It("keeps accepting an existing manifest and defaults it to Source", func() {
		object := spawner("existing-manifest", map[string]interface{}{
			"when": map[string]interface{}{"cron": map[string]interface{}{"schedule": "0 9 * * 1"}},
		})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).To(Succeed())
		Expect(object.Object["spec"].(map[string]interface{})["triggerMode"]).To(Equal("Source"))
	})

	It("still requires a workspace for a GitHub source", func() {
		object := spawner("github-without-workspace", map[string]interface{}{
			"when": map[string]interface{}{"githubIssues": map[string]interface{}{"state": "open"}},
		})
		Expect(k8sClient.Create(ctx, object, client.DryRunAll)).NotTo(Succeed())
	})
})
