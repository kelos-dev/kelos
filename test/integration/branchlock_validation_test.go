package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("branchLock API validation", func() {
	const ns = "test-branchlock-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	inlineTask := func(name string, branchLock kelos.BranchLockPolicy) *kelos.Task {
		return &kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.TaskSpec{
				Type:       "claude-code",
				Prompt:     "Review the branch",
				Branch:     "feature-1",
				BranchLock: branchLock,
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeAPIKey,
					SecretRef: &kelos.SecretReference{Name: "anthropic-api-key"},
				},
			},
		}
	}

	It("accepts Exclusive and None on a Task", func() {
		Expect(k8sClient.Create(ctx, inlineTask("exclusive", kelos.BranchLockExclusive), client.DryRunAll)).To(Succeed())
		Expect(k8sClient.Create(ctx, inlineTask("none", kelos.BranchLockNone), client.DryRunAll)).To(Succeed())
	})

	It("rejects an unknown value on a Task", func() {
		err := k8sClient.Create(ctx, inlineTask("unknown", "Shared"), client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.branchLock"))
	})

	It("rejects branchLock with workerPoolRef on a Task", func() {
		task := &kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "pooled", Namespace: ns},
			Spec: kelos.TaskSpec{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
				Prompt:        "Review the branch",
				BranchLock:    kelos.BranchLockNone,
			},
		}
		err := k8sClient.Create(ctx, task, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("branchLock is not supported with workerPoolRef"))
	})

	It("rejects branchLock with workerPoolRef on a TaskSpawner taskTemplate", func() {
		spawner := &kelos.TaskSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "pooled", Namespace: ns},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Cron: &kelos.Cron{Schedule: "0 9 * * 1"}},
				TaskTemplate: kelos.TaskTemplate{
					WorkerPoolRef:  &kelos.WorkerPoolReference{Name: "workers"},
					PromptTemplate: "Review the branch",
					BranchLock:     kelos.BranchLockNone,
				},
			},
		}
		err := k8sClient.Create(ctx, spawner, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("branchLock is not supported with workerPoolRef"))
	})

	It("rejects branchLock with workerPoolRef on a TaskPipeline stage", func() {
		pipeline := &kelos.TaskPipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "pooled", Namespace: ns},
			Spec: kelos.TaskPipelineSpec{Stages: []kelos.PipelineStage{{
				Name: "review",
				TaskTemplate: kelos.PipelineTaskTemplate{
					WorkerPoolRef: &kelos.WorkerPoolReference{Name: "workers"},
					Prompt:        "Review the branch",
					BranchLock:    kelos.BranchLockNone,
				},
			}}},
		}
		err := k8sClient.Create(ctx, pipeline, client.DryRunAll)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("branchLock is not supported with workerPoolRef"))
	})
})
