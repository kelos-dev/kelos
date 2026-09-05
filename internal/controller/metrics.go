package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var (
	// taskCreatedTotal counts the total number of Tasks for which a Job was created.
	taskCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_created_total",
			Help: "Total number of Tasks for which a Job was created",
		},
		[]string{"namespace", "type", "spawner"},
	)

	// taskCompletedTotal counts the total number of Tasks that reached a terminal phase.
	taskCompletedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_completed_total",
			Help: "Total number of Tasks that reached a terminal phase",
		},
		[]string{"namespace", "type", "spawner", "phase"},
	)

	// taskDurationSeconds records the duration of Task execution.
	taskDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kelos_task_duration_seconds",
			Help:    "Duration of Task execution from start to completion",
			Buckets: []float64{30, 60, 120, 300, 600, 1200, 1800, 3600},
		},
		[]string{"namespace", "type", "spawner", "phase"},
	)

	// reconcileErrorsTotal counts the total number of reconciliation errors.
	reconcileErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_reconcile_errors_total",
			Help: "Total number of reconciliation errors",
		},
		[]string{"controller"},
	)

	// taskCostUSD records the cost in USD of completed Tasks.
	taskCostUSD = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_cost_usd_total",
			Help: "Total cost in USD of completed Tasks",
		},
		[]string{"namespace", "type", "spawner", "model"},
	)

	// taskInputTokens records the total input tokens consumed by completed Tasks.
	taskInputTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_input_tokens_total",
			Help: "Total input tokens consumed by completed Tasks",
		},
		[]string{"namespace", "type", "spawner", "model"},
	)

	// taskOutputTokens records the total output tokens consumed by completed Tasks.
	taskOutputTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_output_tokens_total",
			Help: "Total output tokens consumed by completed Tasks",
		},
		[]string{"namespace", "type", "spawner", "model"},
	)
)

// recordTaskCompleted increments taskCompletedTotal for a Task that has just
// reached a terminal phase. Callers are responsible for the transition guard;
// this only owns the label tuple.
//
// taskCompletedTotal and taskDurationSeconds use positional label values, so a
// reordering of their label slices above would silently mislabel every series
// if the tuple were respelled at each call site. Both are built here instead.
func recordTaskCompleted(task *kelos.Task, phase kelos.TaskPhase) {
	taskCompletedTotal.WithLabelValues(task.Namespace, resolveTaskType(task), resolveTaskSpawner(task), string(phase)).Inc()
}

// observeTaskDuration records the execution duration of a Task that reached the
// given terminal phase.
func observeTaskDuration(task *kelos.Task, phase kelos.TaskPhase, seconds float64) {
	taskDurationSeconds.WithLabelValues(task.Namespace, resolveTaskType(task), resolveTaskSpawner(task), string(phase)).Observe(seconds)
}

func init() {
	metrics.Registry.MustRegister(
		taskCreatedTotal,
		taskCompletedTotal,
		taskDurationSeconds,
		reconcileErrorsTotal,
		taskCostUSD,
		taskInputTokens,
		taskOutputTokens,
	)
}
