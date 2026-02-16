package integration

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/controller"
)

// getMetricValue retrieves the current value of a counter metric from the
// controller-runtime metrics registry, matching by metric name and label pairs.
func getMetricValue(name string, labels map[string]string) float64 {
	families, err := metrics.Registry.Gather()
	if err != nil {
		return 0
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			labelPairs := m.GetLabel()
			if len(labelPairs) != len(labels) {
				continue
			}
			match := true
			for _, lp := range labelPairs {
				if v, ok := labels[lp.GetName()]; !ok || v != lp.GetValue() {
					match = false
					break
				}
			}
			if match && m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

const (
	metricsTimeout  = time.Second * 10
	metricsInterval = time.Millisecond * 250
)

// createNamespaceWithSecret creates a namespace and an API key secret within it.
func createNamespaceWithSecret(nsName string) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}
	Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "anthropic-api-key",
			Namespace: nsName,
		},
		StringData: map[string]string{
			"ANTHROPIC_API_KEY": "test-api-key",
		},
	}
	Expect(k8sClient.Create(ctx, secret)).Should(Succeed())
}

// createAndCompleteTask creates a Task, waits for its Job to be created,
// simulates Job completion, and waits for the Task to reach Succeeded phase.
// Returns the completed Task object.
func createAndCompleteTask(nsName, taskName, spawner, model string) *axonv1alpha1.Task {
	labels := map[string]string{}
	if spawner != "" {
		labels["axon.io/taskspawner"] = spawner
	}

	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskName,
			Namespace: nsName,
			Labels:    labels,
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   "claude-code",
			Prompt: fmt.Sprintf("Test task %s", taskName),
			Credentials: axonv1alpha1.Credentials{
				Type: axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{
					Name: "anthropic-api-key",
				},
			},
			Model: model,
		},
	}
	Expect(k8sClient.Create(ctx, task)).Should(Succeed())

	jobKey := types.NamespacedName{Name: taskName, Namespace: nsName}
	createdJob := &batchv1.Job{}
	Eventually(func() bool {
		return k8sClient.Get(ctx, jobKey, createdJob) == nil
	}, metricsTimeout, metricsInterval).Should(BeTrue())

	Eventually(func() error {
		if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
			return err
		}
		createdJob.Status.Succeeded = 1
		return k8sClient.Status().Update(ctx, createdJob)
	}, metricsTimeout, metricsInterval).Should(Succeed())

	taskKey := types.NamespacedName{Name: taskName, Namespace: nsName}
	Eventually(func() axonv1alpha1.TaskPhase {
		var t axonv1alpha1.Task
		if err := k8sClient.Get(ctx, taskKey, &t); err != nil {
			return ""
		}
		return t.Status.Phase
	}, metricsTimeout, metricsInterval).Should(Equal(axonv1alpha1.TaskPhaseSucceeded))

	completedTask := &axonv1alpha1.Task{}
	Expect(k8sClient.Get(ctx, taskKey, completedTask)).Should(Succeed())
	return completedTask
}

var _ = Describe("Cost and Token Metrics", func() {
	Context("When a Task completes with cost and token results", func() {
		It("Should record cost and token Prometheus metrics", func() {
			nsName := "test-metrics-cost"
			createNamespaceWithSecret(nsName)

			metricLabels := map[string]string{
				"namespace": nsName,
				"type":      "claude-code",
				"spawner":   "test-spawner",
				"model":     "opus",
			}
			costBefore := getMetricValue("axon_task_cost_usd_total", metricLabels)
			inputBefore := getMetricValue("axon_task_input_tokens_total", metricLabels)
			outputBefore := getMetricValue("axon_task_output_tokens_total", metricLabels)

			completedTask := createAndCompleteTask(nsName, "metrics-task", "test-spawner", "opus")

			By("Recording cost and token metrics for the completed Task")
			controller.RecordCostTokenMetrics(completedTask, map[string]string{
				"cost-usd":      "2.50",
				"input-tokens":  "12000",
				"output-tokens": "3500",
			})

			By("Verifying all three metrics were incremented")
			Expect(getMetricValue("axon_task_cost_usd_total", metricLabels) - costBefore).To(BeNumerically("~", 2.50, 0.001))
			Expect(getMetricValue("axon_task_input_tokens_total", metricLabels) - inputBefore).To(BeNumerically("~", 12000, 0.001))
			Expect(getMetricValue("axon_task_output_tokens_total", metricLabels) - outputBefore).To(BeNumerically("~", 3500, 0.001))
		})
	})

	Context("When a Task completes with partial results", func() {
		It("Should only record metrics for present values", func() {
			nsName := "test-metrics-partial"
			createNamespaceWithSecret(nsName)

			metricLabels := map[string]string{
				"namespace": nsName,
				"type":      "claude-code",
				"spawner":   "",
				"model":     "sonnet",
			}
			costBefore := getMetricValue("axon_task_cost_usd_total", metricLabels)
			inputBefore := getMetricValue("axon_task_input_tokens_total", metricLabels)
			outputBefore := getMetricValue("axon_task_output_tokens_total", metricLabels)

			completedTask := createAndCompleteTask(nsName, "metrics-partial-task", "", "sonnet")

			By("Recording metrics with only cost (no tokens)")
			controller.RecordCostTokenMetrics(completedTask, map[string]string{
				"cost-usd": "0.75",
			})

			By("Verifying only cost metric was incremented")
			Expect(getMetricValue("axon_task_cost_usd_total", metricLabels) - costBefore).To(BeNumerically("~", 0.75, 0.001))
			Expect(getMetricValue("axon_task_input_tokens_total", metricLabels) - inputBefore).To(BeNumerically("~", 0, 0.001))
			Expect(getMetricValue("axon_task_output_tokens_total", metricLabels) - outputBefore).To(BeNumerically("~", 0, 0.001))
		})
	})

	Context("When multiple Tasks complete", func() {
		It("Should accumulate metrics across tasks", func() {
			nsName := "test-metrics-accumulate"
			createNamespaceWithSecret(nsName)

			metricLabels := map[string]string{
				"namespace": nsName,
				"type":      "claude-code",
				"spawner":   "multi-spawner",
				"model":     "opus",
			}
			costBefore := getMetricValue("axon_task_cost_usd_total", metricLabels)

			By("Creating and completing two tasks with different costs")
			for i, name := range []string{"accumulate-task-1", "accumulate-task-2"} {
				completedTask := createAndCompleteTask(nsName, name, "multi-spawner", "opus")
				costs := []string{"1.00", "2.00"}
				controller.RecordCostTokenMetrics(completedTask, map[string]string{
					"cost-usd": costs[i],
				})
			}

			By("Verifying metrics accumulated from both tasks")
			Expect(getMetricValue("axon_task_cost_usd_total", metricLabels) - costBefore).To(BeNumerically("~", 3.00, 0.001))
		})
	})
})
