package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("TaskSpawner pipelineTemplate validation", func() {
	const ns = "test-taskspawner-pipeline"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	pipelineSpawner := func(name string) *kelos.TaskSpawner {
		return &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Slack: &kelos.Slack{Channels: []string{"C0123456789"}}},
				PipelineTemplate: &kelos.TaskSpawnerPipelineTemplate{Stages: []kelos.PipelineStage{
					{
						Name: "route",
						TaskTemplate: kelos.PipelineTaskTemplate{
							WorkerPoolRef: &kelos.WorkerPoolReference{Name: "router"},
							Prompt:        "Reply docs or none: {{.Params.Body}}",
						},
					},
					{
						Name: "docs",
						Conditions: []kelos.PipelineStageCondition{{
							Input:    `{{index .Stages "route" 0 "Results" "response" | b64dec}}`,
							Operator: kelos.PipelineConditionOperatorIn,
							Values:   []string{"docs"},
						}},
						TaskTemplate: kelos.PipelineTaskTemplate{
							WorkerPoolRef: &kelos.WorkerPoolReference{Name: "docs"},
							Prompt:        "{{.Params.Body}}",
						},
					},
				}},
			},
		}
	}

	It("accepts a Slack TaskSpawner with pipelineTemplate and no taskTemplate", func() {
		Expect(k8sClient.Create(ctx, pipelineSpawner("valid"), client.DryRunAll)).To(Succeed())
	})

	It("rejects a TaskSpawner with both taskTemplate and pipelineTemplate", func() {
		spawner := pipelineSpawner("both-templates")
		spawner.Spec.TaskTemplate = kelos.TaskTemplate{
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "router"},
		}
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of taskTemplate or pipelineTemplate is required"))
	})

	It("rejects a TaskSpawner with neither template", func() {
		spawner := pipelineSpawner("no-template")
		spawner.Spec.PipelineTemplate = nil
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of taskTemplate or pipelineTemplate is required"))
	})

	It("rejects pipelineTemplate with a non-Slack source", func() {
		spawner := pipelineSpawner("cron-source")
		spawner.Spec.When = kelos.When{Cron: &kelos.Cron{Schedule: "0 * * * *"}}
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("pipelineTemplate is supported only with the slack source"))
	})

	It("rejects pipelineTemplate with spec.credentials", func() {
		spawner := pipelineSpawner("credentials")
		spawner.Spec.Credentials = []kelos.SpawnerCredential{{
			Name:      "account-a",
			Type:      kelos.CredentialTypeOAuth,
			SecretRef: kelos.SecretReference{Name: "secret-a"},
		}}
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.credentials is not supported with pipelineTemplate"))
	})

	It("rejects duplicate pipelineTemplate stage names", func() {
		spawner := pipelineSpawner("duplicate-stages")
		spawner.Spec.PipelineTemplate.Stages[1].Name = "route"
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("stage names must be unique"))
	})

	It("still enforces taskTemplate rules on a taskTemplate spawner", func() {
		spawner := pipelineSpawner("inline-without-credentials")
		spawner.Spec.PipelineTemplate = nil
		spawner.Spec.TaskTemplate = kelos.TaskTemplate{Type: "claude-code"}
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("inline task templates require taskTemplate credentials or spec.credentials"))
	})
})

var _ = Describe("TaskPipeline params validation", func() {
	const ns = "test-taskpipeline-params"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	It("rejects params updates", func() {
		pipeline := &kelos.TaskPipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "immutable-params", Namespace: ns},
			Spec: kelos.TaskPipelineSpec{
				Params: map[string]string{"Body": "first"},
				Stages: []kelos.PipelineStage{{
					Name: "answer",
					TaskTemplate: kelos.PipelineTaskTemplate{
						WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
						Prompt:        "{{.Params.Body}}",
					},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pipeline))).To(Succeed())
			Expect(k8sClient.DeleteAllOf(ctx, &kelos.Task{},
				client.InNamespace(ns),
				client.MatchingLabels{"kelos.dev/taskpipeline": pipeline.Name},
			)).To(Succeed())
		})

		Eventually(func() string {
			var current kelos.TaskPipeline
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pipeline), &current); err != nil {
				return err.Error()
			}
			current.Spec.Params = map[string]string{"Body": "second"}
			err := k8sClient.Update(ctx, &current, client.DryRunAll)
			if err == nil {
				return ""
			}
			return err.Error()
		}, controllerSettleTimeout).Should(ContainSubstring("TaskPipeline params are immutable after creation"))
	})
})
