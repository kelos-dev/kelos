package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("TaskRecord", func() {
	f := framework.NewFramework("taskrecord")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should create a TaskRecord when a Task with usage completes", func() {
		By("creating credentials secret")
		f.CreateSecret("claude-credentials", "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a Task with labels")
		taskName := "record-test"
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:   taskName,
				Labels: map[string]string{"test": "taskrecord"},
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'hello' to stdout",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("waiting for Task to succeed")
		f.WaitForJobCreation(taskName)
		f.WaitForJobCompletion(taskName)
		f.WaitForTaskPhase(taskName, "Succeeded")

		By("verifying Task has usage data")
		task, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(context.TODO(), taskName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(task.Status.Usage).NotTo(BeNil(), "Task should have usage after completion")
		Expect(task.Status.Usage.InputTokens).NotTo(BeNil())
		Expect(task.Status.Usage.OutputTokens).NotTo(BeNil())

		By("verifying a TaskRecord was created")
		var record *kelos.TaskRecord
		Eventually(func() error {
			rec, err := f.KelosClientset.ApiV1alpha2().TaskRecords(f.Namespace).Get(
				context.TODO(), string(task.UID), metav1.GetOptions{})
			if err != nil {
				return err
			}
			record = rec
			return nil
		}, 30*time.Second, time.Second).Should(Succeed(), "TaskRecord should be created with Task UID as name")

		By("verifying TaskRecord fields match the completed Task")
		Expect(record.Spec.TaskRef.Name).To(Equal(taskName))
		Expect(record.Spec.TaskRef.UID).To(Equal(task.UID))
		Expect(record.Spec.Phase).To(Equal(kelos.TaskPhaseSucceeded))
		Expect(record.Spec.Type).To(Equal("claude-code"))
		Expect(record.Spec.Model).To(Equal(claudeCodeModel))
		Expect(record.Spec.Usage).NotTo(BeNil())
		Expect(record.Spec.Usage.InputTokens).NotTo(BeNil())
		Expect(record.Spec.Usage.OutputTokens).NotTo(BeNil())
		Expect(record.Spec.TTLSecondsAfterCompletion).NotTo(BeNil())
		Expect(*record.Spec.TTLSecondsAfterCompletion).To(Equal(int32(30 * 24 * 60 * 60)))

		By("verifying TaskRecord labels match the Task labels")
		Expect(record.Labels).To(HaveKeyWithValue("test", "taskrecord"))

		GinkgoWriter.Printf("TaskRecord created: name=%s, inputTokens=%d, outputTokens=%d\n",
			record.Name, *record.Spec.Usage.InputTokens, *record.Spec.Usage.OutputTokens)
		if record.Spec.Usage.CostUSD != nil {
			GinkgoWriter.Printf("  costUSD=%s\n", record.Spec.Usage.CostUSD.String())
		}
	})

	It("should be immutable after creation", func() {
		By("creating credentials secret")
		f.CreateSecret("claude-credentials", "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating and completing a Task")
		taskName := "immutable-test"
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: taskName,
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'test' to stdout",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		f.WaitForJobCreation(taskName)
		f.WaitForJobCompletion(taskName)
		f.WaitForTaskPhase(taskName, "Succeeded")

		By("getting the Task to find its UID")
		task, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(context.TODO(), taskName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("waiting for TaskRecord to appear")
		var record *kelos.TaskRecord
		Eventually(func() error {
			rec, err := f.KelosClientset.ApiV1alpha2().TaskRecords(f.Namespace).Get(
				context.TODO(), string(task.UID), metav1.GetOptions{})
			if err != nil {
				return err
			}
			record = rec
			return nil
		}, 30*time.Second, time.Second).Should(Succeed())

		By("attempting to update the TaskRecord spec (should fail)")
		record.Spec.Model = "modified-model"
		_, updateErr := f.KelosClientset.ApiV1alpha2().TaskRecords(f.Namespace).Update(
			context.TODO(), record, metav1.UpdateOptions{})
		Expect(updateErr).To(HaveOccurred(), "TaskRecord spec should be immutable")
		Expect(updateErr.Error()).To(ContainSubstring("immutable"))
	})
})
