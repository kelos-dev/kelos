package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("TaskPipeline API validation", func() {
	const ns = "test-taskpipeline-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	validPipeline := func(name string) *kelos.TaskPipeline {
		return &kelos.TaskPipeline{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.TaskPipelineSpec{Tasks: []kelos.PipelineNode{
				{
					Name: "plan",
					TaskTemplate: kelos.PipelineTaskTemplate{
						Worker: &kelos.WorkerSpec{
							Type:        "codex",
							Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
						},
						Prompt: "Plan the work",
					},
				},
				{
					Name:      "implement",
					DependsOn: []string{"plan"},
					TaskTemplate: kelos.PipelineTaskTemplate{
						WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
						Prompt:        `Implement {{index .Tasks "plan" 0 "Results" "plan"}}`,
					},
				},
			}},
		}
	}

	It("accepts a well-formed DAG", func() {
		Expect(k8sClient.Create(ctx, validPipeline("valid"), client.DryRunAll)).To(Succeed())
	})

	It("rejects a dependency outside the pipeline", func() {
		pipeline := validPipeline("unknown-dependency")
		pipeline.Spec.Tasks[1].DependsOn = []string{"missing"}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects a self dependency", func() {
		pipeline := validPipeline("self-dependency")
		pipeline.Spec.Tasks[0].DependsOn = []string{"plan"}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an empty matrix parameter", func() {
		pipeline := validPipeline("empty-matrix")
		pipeline.Spec.Tasks[0].Matrix = &kelos.PipelineMatrix{
			Parameters: map[string][]string{"service": {}},
		}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects multiple execution sources", func() {
		pipeline := validPipeline("multiple-execution-sources")
		pipeline.Spec.Tasks[0].TaskTemplate.WorkerPoolRef = &kelos.WorkerPoolReference{Name: "workers"}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})
})
