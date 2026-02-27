package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestMetricsRegistered(t *testing.T) {
	// Verify all metrics are registered by checking they can be described
	tests := []struct {
		name      string
		collector prometheus.Collector
	}{
		{"taskCreatedTotal", taskCreatedTotal},
		{"taskCompletedTotal", taskCompletedTotal},
		{"taskDurationSeconds", taskDurationSeconds},
		{"reconcileErrorsTotal", reconcileErrorsTotal},
		{"taskCostUSD", taskCostUSD},
		{"taskInputTokens", taskInputTokens},
		{"taskOutputTokens", taskOutputTokens},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan *prometheus.Desc, 10)
			tt.collector.Describe(ch)
			close(ch)
			if len(ch) == 0 {
				t.Errorf("expected at least one descriptor for %s", tt.name)
			}
		})
	}
}

func TestTaskCreatedTotalCounter(t *testing.T) {
	taskCreatedTotal.WithLabelValues("default", "claude-code").Add(0)
	before := testutil.ToFloat64(taskCreatedTotal.WithLabelValues("default", "claude-code"))

	taskCreatedTotal.WithLabelValues("default", "claude-code").Inc()

	after := testutil.ToFloat64(taskCreatedTotal.WithLabelValues("default", "claude-code"))
	if after != before+1 {
		t.Errorf("expected taskCreatedTotal to increment by 1, got delta %f", after-before)
	}
}

func TestTaskCompletedTotalCounter(t *testing.T) {
	taskCompletedTotal.WithLabelValues("default", "claude-code", "Succeeded").Add(0)
	before := testutil.ToFloat64(taskCompletedTotal.WithLabelValues("default", "claude-code", "Succeeded"))

	taskCompletedTotal.WithLabelValues("default", "claude-code", "Succeeded").Inc()

	after := testutil.ToFloat64(taskCompletedTotal.WithLabelValues("default", "claude-code", "Succeeded"))
	if after != before+1 {
		t.Errorf("expected taskCompletedTotal to increment by 1, got delta %f", after-before)
	}
}

func TestTaskDurationSecondsHistogram(t *testing.T) {
	taskDurationSeconds.WithLabelValues("test-ns", "claude-code", "Succeeded").Observe(120.5)

	// Verify the histogram was observed by checking the underlying collector
	count := testutil.CollectAndCount(taskDurationSeconds)
	if count == 0 {
		t.Error("expected taskDurationSeconds to have collected metrics")
	}
}

func TestReconcileErrorsTotalCounter(t *testing.T) {
	reconcileErrorsTotal.WithLabelValues("task").Add(0)
	before := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues("task"))

	reconcileErrorsTotal.WithLabelValues("task").Inc()

	after := testutil.ToFloat64(reconcileErrorsTotal.WithLabelValues("task"))
	if after != before+1 {
		t.Errorf("expected reconcileErrorsTotal to increment by 1, got delta %f", after-before)
	}
}

func TestTaskCostUSDCounter(t *testing.T) {
	labels := []string{"cost-ns", "claude-code", "my-spawner", "opus"}
	taskCostUSD.WithLabelValues(labels...).Add(0)
	before := testutil.ToFloat64(taskCostUSD.WithLabelValues(labels...))

	taskCostUSD.WithLabelValues(labels...).Add(1.5)

	after := testutil.ToFloat64(taskCostUSD.WithLabelValues(labels...))
	if after != before+1.5 {
		t.Errorf("expected taskCostUSD to increase by 1.5, got delta %f", after-before)
	}
}

func TestTaskInputTokensCounter(t *testing.T) {
	labels := []string{"token-ns", "claude-code", "my-spawner", "opus"}
	taskInputTokens.WithLabelValues(labels...).Add(0)
	before := testutil.ToFloat64(taskInputTokens.WithLabelValues(labels...))

	taskInputTokens.WithLabelValues(labels...).Add(1000)

	after := testutil.ToFloat64(taskInputTokens.WithLabelValues(labels...))
	if after != before+1000 {
		t.Errorf("expected taskInputTokens to increase by 1000, got delta %f", after-before)
	}
}

func TestTaskOutputTokensCounter(t *testing.T) {
	labels := []string{"token-ns", "claude-code", "my-spawner", "opus"}
	taskOutputTokens.WithLabelValues(labels...).Add(0)
	before := testutil.ToFloat64(taskOutputTokens.WithLabelValues(labels...))

	taskOutputTokens.WithLabelValues(labels...).Add(500)

	after := testutil.ToFloat64(taskOutputTokens.WithLabelValues(labels...))
	if after != before+500 {
		t.Errorf("expected taskOutputTokens to increase by 500, got delta %f", after-before)
	}
}

func TestRecordCostTokenMetrics(t *testing.T) {
	tests := []struct {
		name       string
		task       *kelosv1alpha1.Task
		results    map[string]string
		wantCost   float64
		wantInput  float64
		wantOutput float64
	}{
		{
			name: "All metrics present",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "record-ns-1",
					Labels:    map[string]string{"kelos.dev/taskspawner": "spawner-1"},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:  "claude-code",
					Model: "opus",
				},
			},
			results: map[string]string{
				"cost-usd":      "2.35",
				"input-tokens":  "15000",
				"output-tokens": "3000",
			},
			wantCost:   2.35,
			wantInput:  15000,
			wantOutput: 3000,
		},
		{
			name: "Only cost present",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "record-ns-2",
					Labels:    map[string]string{"kelos.dev/taskspawner": "spawner-2"},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:  "claude-code",
					Model: "sonnet",
				},
			},
			results: map[string]string{
				"cost-usd": "0.50",
			},
			wantCost: 0.50,
		},
		{
			name: "No spawner label or model",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "record-ns-3",
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type: "codex",
				},
			},
			results: map[string]string{
				"cost-usd":      "1.00",
				"input-tokens":  "5000",
				"output-tokens": "1000",
			},
			wantCost:   1.00,
			wantInput:  5000,
			wantOutput: 1000,
		},
		{
			name: "Invalid cost value is ignored",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "record-ns-4",
					Labels:    map[string]string{"kelos.dev/taskspawner": "spawner-4"},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:  "claude-code",
					Model: "opus",
				},
			},
			results: map[string]string{
				"cost-usd":     "not-a-number",
				"input-tokens": "1000",
			},
			wantInput: 1000,
		},
		{
			name: "Negative values are ignored",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "record-ns-5",
					Labels:    map[string]string{"kelos.dev/taskspawner": "spawner-5"},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:  "claude-code",
					Model: "opus",
				},
			},
			results: map[string]string{
				"cost-usd":      "-1.00",
				"input-tokens":  "-500",
				"output-tokens": "-200",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spawner := tt.task.Labels["kelos.dev/taskspawner"]
			model := tt.task.Spec.Model
			labels := []string{tt.task.Namespace, tt.task.Spec.Type, spawner, model}

			// Record baseline
			costBefore := testutil.ToFloat64(taskCostUSD.WithLabelValues(labels...))
			inputBefore := testutil.ToFloat64(taskInputTokens.WithLabelValues(labels...))
			outputBefore := testutil.ToFloat64(taskOutputTokens.WithLabelValues(labels...))

			RecordCostTokenMetrics(tt.task, tt.results)

			costAfter := testutil.ToFloat64(taskCostUSD.WithLabelValues(labels...))
			inputAfter := testutil.ToFloat64(taskInputTokens.WithLabelValues(labels...))
			outputAfter := testutil.ToFloat64(taskOutputTokens.WithLabelValues(labels...))

			if delta := costAfter - costBefore; delta != tt.wantCost {
				t.Errorf("cost delta = %f, want %f", delta, tt.wantCost)
			}
			if delta := inputAfter - inputBefore; delta != tt.wantInput {
				t.Errorf("input tokens delta = %f, want %f", delta, tt.wantInput)
			}
			if delta := outputAfter - outputBefore; delta != tt.wantOutput {
				t.Errorf("output tokens delta = %f, want %f", delta, tt.wantOutput)
			}
		})
	}
}
