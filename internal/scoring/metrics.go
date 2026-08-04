package scoring

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// scoreRecordedTotal counts scoring events accepted from external actors.
	// It is incremented only when a TaskScore is actually created, so redelivery
	// of the same event does not inflate the count.
	//
	// Actor is deliberately not a label: it is unbounded, and per-person
	// scoreboards are not what this measures.
	scoreRecordedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_score_recorded_total",
			Help: "Total number of task scores recorded from external actors",
		},
		[]string{"namespace", "spawner", "verdict", "source_type"},
	)

	// scoreRetractedTotal counts scores taken back by the actor who gave them,
	// for example by removing a Slack reaction.
	scoreRetractedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_score_retracted_total",
			Help: "Total number of task scores retracted by the actor who recorded them",
		},
		[]string{"namespace", "spawner", "verdict", "source_type"},
	)
)

func init() {
	metrics.Registry.MustRegister(scoreRecordedTotal, scoreRetractedTotal)
}
