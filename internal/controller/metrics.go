package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// taskCreatedTotal counts the total number of Tasks for which a Job was created.
	taskCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "axon_task_created_total",
			Help: "Total number of Tasks for which a Job was created",
		},
		[]string{"namespace", "type"},
	)

	// taskCompletedTotal counts the total number of Tasks that reached a terminal phase.
	taskCompletedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "axon_task_completed_total",
			Help: "Total number of Tasks that reached a terminal phase",
		},
		[]string{"namespace", "type", "phase"},
	)

	// taskDurationSeconds records the duration of Task execution.
	taskDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "axon_task_duration_seconds",
			Help:    "Duration of Task execution from start to completion",
			Buckets: []float64{30, 60, 120, 300, 600, 1200, 1800, 3600},
		},
		[]string{"namespace", "type", "phase"},
	)

	// reconcileErrorsTotal counts the total number of reconciliation errors.
	reconcileErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "axon_reconcile_errors_total",
			Help: "Total number of reconciliation errors",
		},
		[]string{"controller"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		taskCreatedTotal,
		taskCompletedTotal,
		taskDurationSeconds,
		reconcileErrorsTotal,
	)
}
