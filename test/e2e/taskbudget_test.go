package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("TaskBudget", func() {
	f := framework.NewFramework("taskbudget")

	BeforeEach(func() {
		if oauthToken == "" {
			Skip("CLAUDE_CODE_OAUTH_TOKEN not set")
		}
	})

	It("should block a Task when budget is exceeded", func() {
		budgetLabel := "budget-test"

		By("creating credentials secret")
		f.CreateSecret("claude-credentials", "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a TaskBudget with very low output token limit")
		maxTokens := int64(1)
		_, err := f.KelosClientset.ApiV1alpha2().TaskBudgets(f.Namespace).Create(
			context.TODO(),
			&kelos.TaskBudget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "low-budget",
				},
				Spec: kelos.TaskBudgetSpec{
					TaskSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"budget-group": budgetLabel},
					},
					Period: kelos.BudgetPeriod{
						Type: kelos.BudgetPeriodDaily,
					},
					MaxOutputTokens: &maxTokens,
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())

		By("creating the first Task (should be admitted since no prior usage)")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "budget-task-1",
				Labels: map[string]string{"budget-group": budgetLabel},
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'first' to stdout",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("waiting for the first Task to complete")
		f.WaitForJobCreation("budget-task-1")
		f.WaitForJobCompletion("budget-task-1")
		f.WaitForTaskPhase("budget-task-1", "Succeeded")

		By("verifying a TaskRecord was created for the first Task")
		task1, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(
			context.TODO(), "budget-task-1", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() error {
			_, err := f.KelosClientset.ApiV1alpha2().TaskRecords(f.Namespace).Get(
				context.TODO(), string(task1.UID), metav1.GetOptions{})
			return err
		}, 30*time.Second, time.Second).Should(Succeed())

		By("creating a second Task (should be blocked by budget)")
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "budget-task-2",
				Labels: map[string]string{"budget-group": budgetLabel},
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'second' to stdout",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("verifying the second Task enters Waiting phase with BudgetBlocked condition")
		f.WaitForTaskPhase("budget-task-2", "Waiting")

		Eventually(func() string {
			task, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(
				context.TODO(), "budget-task-2", metav1.GetOptions{})
			if err != nil {
				return ""
			}
			for _, c := range task.Status.Conditions {
				if c.Type == "BudgetBlocked" && c.Status == metav1.ConditionTrue {
					return c.Message
				}
			}
			return ""
		}, 30*time.Second, time.Second).ShouldNot(BeEmpty(),
			"Task should have BudgetBlocked condition")

		By("verifying the second Task's status message mentions the budget")
		task2, err := f.KelosClientset.ApiV1alpha2().Tasks(f.Namespace).Get(
			context.TODO(), "budget-task-2", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(task2.Status.Message).To(ContainSubstring("low-budget"))

		By("verifying TaskBudget status shows usage")
		Eventually(func() bool {
			budget, err := f.KelosClientset.ApiV1alpha2().TaskBudgets(f.Namespace).Get(
				context.TODO(), "low-budget", metav1.GetOptions{})
			if err != nil {
				return false
			}
			return budget.Status.Used != nil && budget.Status.Used.OutputTokens != nil
		}, 30*time.Second, time.Second).Should(BeTrue(),
			"TaskBudget status should report usage")

		budget, err := f.KelosClientset.ApiV1alpha2().TaskBudgets(f.Namespace).Get(
			context.TODO(), "low-budget", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(budget.Status.CurrentPeriodStart).NotTo(BeNil())
		Expect(budget.Status.CurrentPeriodEnd).NotTo(BeNil())
		Expect(*budget.Status.Used.OutputTokens).To(BeNumerically(">", maxTokens),
			"Used output tokens should exceed the budget limit")

		GinkgoWriter.Printf("TaskBudget status: used outputTokens=%d, limit=%d\n",
			*budget.Status.Used.OutputTokens, maxTokens)
	})

	It("should not block Tasks that do not match the budget selector", func() {
		By("creating credentials secret")
		f.CreateSecret("claude-credentials", "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)

		By("creating a TaskBudget with very low limit targeting a specific label")
		maxTokens := int64(1)
		_, err := f.KelosClientset.ApiV1alpha2().TaskBudgets(f.Namespace).Create(
			context.TODO(),
			&kelos.TaskBudget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "selective-budget",
				},
				Spec: kelos.TaskBudgetSpec{
					TaskSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"budget-group": "restricted"},
					},
					Period: kelos.BudgetPeriod{
						Type: kelos.BudgetPeriodDaily,
					},
					MaxOutputTokens: &maxTokens,
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())

		By(fmt.Sprintf("creating a Task WITHOUT the budget label (should not be blocked)"))
		f.CreateTask(&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "unmatched-task",
				Labels: map[string]string{"budget-group": "other"},
			},
			Spec: kelos.TaskSpec{
				Type:   "claude-code",
				Model:  claudeCodeModel,
				Prompt: "Print 'not restricted' to stdout",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			},
		})

		By("verifying the Task is admitted and completes (not blocked)")
		f.WaitForJobCreation("unmatched-task")
		f.WaitForJobCompletion("unmatched-task")
		f.WaitForTaskPhase("unmatched-task", "Succeeded")
	})
})

var _ = Describe("TaskBudget validating webhook", func() {
	f := framework.NewFramework("taskbudget-webhook")

	It("should reject invalid task selectors", func() {
		maxTokens := int64(1)

		By("creating a TaskBudget with a valid selector")
		_, err := f.KelosClientset.ApiV1alpha2().TaskBudgets(f.Namespace).Create(
			context.TODO(),
			&kelos.TaskBudget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-selector",
				},
				Spec: kelos.TaskBudgetSpec{
					TaskSelector: metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "team", Operator: metav1.LabelSelectorOpExists},
						},
					},
					Period:          kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
					MaxOutputTokens: &maxTokens,
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).NotTo(HaveOccurred())

		By("creating a TaskBudget with an invalid selector")
		_, err = f.KelosClientset.ApiV1alpha2().TaskBudgets(f.Namespace).Create(
			context.TODO(),
			&kelos.TaskBudget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-selector",
				},
				Spec: kelos.TaskBudgetSpec{
					TaskSelector: metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "team", Operator: metav1.LabelSelectorOpExists, Values: []string{"platform"}},
						},
					},
					Period:          kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
					MaxOutputTokens: &maxTokens,
				},
			},
			metav1.CreateOptions{},
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`admission webhook "vtaskbudget.kelos.dev" denied the request`))
		Expect(err.Error()).To(ContainSubstring("taskSelector is invalid"))
	})
})
