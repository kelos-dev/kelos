package integration

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The examples are the first thing a user copies, so an API change that
// invalidates one must fail here rather than in their cluster. Only the Kelos
// resources are checked; Secrets and Workspaces the examples reference are not
// created.
var _ = Describe("Examples", func() {
	const ns = "test-examples"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	DescribeTable("apply against the current schema",
		func(dir string) {
			paths, err := filepath.Glob(filepath.Join("..", "..", "examples", dir, "*.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(paths).NotTo(BeEmpty(), "no manifests found for %s", dir)

			applied := 0
			for _, path := range paths {
				contents, err := os.ReadFile(path)
				Expect(err).NotTo(HaveOccurred())

				for _, doc := range strings.Split(string(contents), "\n---\n") {
					if strings.TrimSpace(doc) == "" {
						continue
					}
					object := &unstructured.Unstructured{}
					Expect(yaml.Unmarshal([]byte(doc), &object.Object)).To(Succeed(), "parsing %s", path)
					if object.GetAPIVersion() == "" || !strings.HasPrefix(object.GetAPIVersion(), "kelos.dev/") {
						continue
					}
					object.SetNamespace(ns)
					Expect(k8sClient.Create(ctx, object, client.DryRunAll)).
						To(Succeed(), "%s in %s does not apply", object.GetKind(), path)
					applied++
				}
			}
			Expect(applied).To(BeNumerically(">", 0), "no Kelos resources checked for %s", dir)
		},
		Entry("19-taskrouter", "19-taskrouter"),
	)
})
