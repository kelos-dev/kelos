package integration

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("concurrency API validation", func() {
	const ns = "test-concurrency-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	inlineTask := func(name, group string) *kelos.Task {
		return &kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.TaskSpec{
				Type:        "claude-code",
				Prompt:      "Review the branch",
				Branch:      "feature-1",
				Concurrency: &kelos.TaskConcurrency{Group: group},
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeAPIKey,
					SecretRef: &kelos.SecretReference{Name: "anthropic-api-key"},
				},
			},
		}
	}

	It("accepts a group on a Task", func() {
		Expect(k8sClient.Create(ctx, inlineTask("grouped", "pr-7-review"), client.DryRunAll)).To(Succeed())
	})

	It("accepts a group of 256 multibyte characters on a Task", func() {
		group := strings.Repeat("é", kelos.TaskConcurrencyGroupMaxLength)
		Expect(k8sClient.Create(ctx, inlineTask("multibyte", group), client.DryRunAll)).To(Succeed())
	})

	It("rejects an empty group on a Task", func() {
		err := k8sClient.Create(ctx, inlineTask("empty", ""), client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.concurrency.group"))
	})

	It("rejects a group longer than 256 characters on a Task", func() {
		err := k8sClient.Create(ctx, inlineTask("long", strings.Repeat("a", kelos.TaskConcurrencyGroupMaxLength+1)), client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.concurrency.group"))
	})

	It("rejects concurrency with workerPoolRef on a Task", func() {
		task := &kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "pooled", Namespace: ns},
			Spec: kelos.TaskSpec{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
				Prompt:        "Review the branch",
				Concurrency:   &kelos.TaskConcurrency{Group: "pr-7-review"},
			},
		}
		err := k8sClient.Create(ctx, task, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("concurrency is not supported with workerPoolRef"))
	})

	It("rejects concurrency with workerPoolRef on a TaskSpawner taskTemplate", func() {
		spawner := &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "pooled", Namespace: ns},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Cron: &kelos.Cron{Schedule: "0 9 * * 1"}},
				TaskTemplate: kelos.TaskTemplate{
					WorkerPoolRef:  &kelos.WorkerPoolReference{Name: "workers"},
					PromptTemplate: "Review the branch",
					Concurrency:    &kelos.TaskConcurrency{Group: "review-{{.ID}}"},
				},
			},
		}
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("concurrency is not supported with workerPoolRef"))
	})

	It("rejects concurrency with workerPoolRef on a TaskPipeline stage", func() {
		pipeline := &kelos.TaskPipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "pooled", Namespace: ns},
			Spec: kelos.TaskPipelineSpec{Stages: []kelos.PipelineStage{{
				Name: "review",
				TaskTemplate: kelos.PipelineTaskTemplate{
					WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
					Prompt:        "Review the branch",
					Concurrency:   &kelos.TaskConcurrency{Group: "review"},
				},
			}}},
		}
		err := k8sClient.Create(ctx, pipeline, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("concurrency is not supported with workerPoolRef"))
	})
})
