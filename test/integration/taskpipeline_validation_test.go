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
			Spec: kelos.TaskPipelineSpec{Stages: []kelos.PipelineStage{
				{
					Name: "plan",
					Matrix: &kelos.PipelineMatrix{Parameters: []kelos.PipelineMatrixParameter{
						{Name: "component", Values: []string{"api", "controller"}},
					}},
					TaskTemplate: kelos.PipelineTaskTemplate{
						Worker: &kelos.WorkerSpec{
							Type:        "codex",
							Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
						},
						Prompt: "Plan the work",
					},
				},
				{
					Name: "implement",
					TaskTemplate: kelos.PipelineTaskTemplate{
						WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
						Prompt:        `Implement {{index .Stages "plan" 0 "Results" "plan"}}`,
					},
				},
			}},
		}
	}

	It("accepts ordered stages with matrix fan-out", func() {
		Expect(k8sClient.Create(ctx, validPipeline("valid"), client.DryRunAll)).To(Succeed())
	})

	It("rejects missing stages", func() {
		pipeline := &kelos.TaskPipeline{ObjectMeta: metav1.ObjectMeta{Name: "missing-stages", Namespace: ns}}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects duplicate stage names", func() {
		pipeline := validPipeline("duplicate-stages")
		pipeline.Spec.Stages[1].Name = pipeline.Spec.Stages[0].Name
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an invalid stage name", func() {
		pipeline := validPipeline("invalid-stage-name")
		pipeline.Spec.Stages[0].Name = "Not DNS"
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects an empty matrix parameter", func() {
		pipeline := validPipeline("empty-matrix")
		pipeline.Spec.Stages[0].Matrix = &kelos.PipelineMatrix{
			Parameters: []kelos.PipelineMatrixParameter{{Name: "service"}},
		}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects multiple execution sources", func() {
		pipeline := validPipeline("multiple-execution-sources")
		pipeline.Spec.Stages[0].TaskTemplate.WorkerPoolRef = &kelos.WorkerPoolReference{Name: "workers"}
		Expect(k8sClient.Create(ctx, pipeline, client.DryRunAll)).NotTo(Succeed())
	})

	It("rejects stage updates", func() {
		pipeline := validPipeline("immutable-stages")
		Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())
		pipeline.Spec.Stages[0].TaskTemplate.Prompt = "Replace the plan"
		Expect(k8sClient.Update(ctx, pipeline)).NotTo(Succeed())
	})
})
