package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
