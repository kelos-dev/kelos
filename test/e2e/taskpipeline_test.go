package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("TaskPipeline", func() {
	f := framework.NewFramework("taskpipeline")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should fan out a matrix stage and pass its results to the next stage", func() {
		By("creating the agent credentials secret")
		f.CreateSecret("claude-credentials", "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		worker := func() *kelos.WorkerSpec {
			return &kelos.WorkerSpec{
				Type:  "claude-code",
				Model: claudeCodeModel,
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			}
		}

		By("creating a pipeline with a matrix stage and a summary stage")
		f.CreateTaskPipeline(&kelos.TaskPipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "matrix-pipeline"},
			Spec: kelos.TaskPipelineSpec{Stages: []kelos.PipelineStage{
				{
					Name: "review",
					Matrix: &kelos.PipelineMatrix{Parameters: []kelos.PipelineMatrixParameter{
						{Name: "value", Values: []string{"alpha", "beta"}},
					}},
					TaskTemplate: kelos.PipelineTaskTemplate{
						Worker: worker(),
						Prompt: `Reply with exactly this value and no other text: {{index .Matrix "value"}}`,
					},
				},
				{
					Name: "summarize",
					TaskTemplate: kelos.PipelineTaskTemplate{
						Worker: worker(),
						Prompt: `The matrix stage returned these base64-encoded responses:
{{range index .Stages "review" -}}
- {{index .Matrix "value"}}: {{index .Results "response"}}
{{end}}
Reply with exactly "complete".`,
					},
				},
			}},
		})

		By("verifying the matrix expands into two review Tasks")
		Eventually(func() []string {
			tasks, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).List(context.TODO(), metav1.ListOptions{
				LabelSelector: "kelos.dev/taskpipeline=matrix-pipeline,kelos.dev/pipeline-stage=review",
			})
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(tasks.Items))
			for _, task := range tasks.Items {
				names = append(names, task.Name)
			}
			return names
		}, 30*time.Second, time.Second).Should(ConsistOf(
			"matrix-pipeline-review-0",
			"matrix-pipeline-review-1",
		))

		By("waiting for both matrix Tasks to succeed")
		f.WaitForTaskPhase("matrix-pipeline-review-0", string(kelos.TaskPhaseSucceeded))
		f.WaitForTaskPhase("matrix-pipeline-review-1", string(kelos.TaskPhaseSucceeded))

		reviewTasks := make([]*kelos.Task, 2)
		responses := make([]string, 2)
		for index := range reviewTasks {
			task, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(
				context.TODO(), fmt.Sprintf("matrix-pipeline-review-%d", index), metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(task.Status.CompletionTime).NotTo(BeNil())
			Expect(task.Status.Results).To(HaveKey("response"))

			decoded, err := base64.StdEncoding.DecodeString(task.Status.Results["response"])
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded).NotTo(BeEmpty())

			reviewTasks[index] = task
			responses[index] = task.Status.Results["response"]
		}

		By("verifying both matrix Tasks were created before either completed")
		earliestCompletion := reviewTasks[0].Status.CompletionTime.Time
		if reviewTasks[1].Status.CompletionTime.Time.Before(earliestCompletion) {
			earliestCompletion = reviewTasks[1].Status.CompletionTime.Time
		}
		for _, task := range reviewTasks {
			Expect(task.CreationTimestamp.Time.After(earliestCompletion)).To(BeFalse(),
				"review Task %s was created after another matrix Task completed", task.Name)
		}

		By("verifying the summary Task receives every matrix result")
		var summary *kelos.Task
		Eventually(func() string {
			task, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(
				context.TODO(), "matrix-pipeline-summarize", metav1.GetOptions{})
			if err != nil {
				return ""
			}
			summary = task
			return task.Spec.Prompt
		}, 30*time.Second, time.Second).Should(And(
			ContainSubstring("- alpha: "+responses[0]),
			ContainSubstring("- beta: "+responses[1]),
		))

		for _, task := range reviewTasks {
			Expect(summary.CreationTimestamp.Time.Before(task.Status.CompletionTime.Time)).To(BeFalse(),
				"summary Task was created before review Task %s completed", task.Name)
		}

		By("waiting for the summary Task and pipeline to succeed")
		f.WaitForTaskPhase("matrix-pipeline-summarize", string(kelos.TaskPhaseSucceeded))
		f.WaitForTaskPipelinePhase("matrix-pipeline", string(kelos.TaskPipelinePhaseSucceeded))

		By("verifying the final pipeline status")
		pipeline, err := f.KelosClientset.ApiV1alpha2().TaskPipelines(f.Namespace).Get(
			context.TODO(), "matrix-pipeline", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(pipeline.Status.ObservedGeneration).To(Equal(pipeline.Generation))
		Expect(pipeline.Status.StageStatuses).To(Equal([]kelos.PipelineStageStatus{
			{Name: "review", Phase: kelos.TaskPhaseSucceeded, Total: 2, Succeeded: 2},
			{Name: "summarize", Phase: kelos.TaskPhaseSucceeded, Total: 1, Succeeded: 1},
		}))
		ready := apiMeta.FindStatusCondition(pipeline.Status.Conditions, kelos.TaskPipelineConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})
})
