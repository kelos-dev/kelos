package scoring

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func metricEvent(actor string) Event {
	return Event{
		Task: TaskIdentity{
			Namespace:   "agents",
			Name:        "review-1",
			UID:         "task-uid-metrics",
			SpawnerName: "reviewer",
		},
		Verdict: kelos.ScoreVerdictPositive,
		Source: kelos.ScoreSource{
			Type:   kelos.ScoreSourceSlackReaction,
			Actor:  actor,
			Signal: "+1",
		},
	}
}

func TestRecordCountsOnlyRealCreates(t *testing.T) {
	scoreRecordedTotal.Reset()
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ev := metricEvent("U-record")

	if err := Record(context.Background(), cl, ev); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if got := testutil.ToFloat64(scoreRecordedTotal.WithLabelValues(
		"agents", "reviewer", "Positive", "SlackReaction")); got != 1 {
		t.Fatalf("counter = %v after one create, want 1", got)
	}

	// Redelivery of the same event must not inflate the counter.
	if err := Record(context.Background(), cl, ev); err != nil {
		t.Fatalf("Record() redelivery error = %v", err)
	}
	if got := testutil.ToFloat64(scoreRecordedTotal.WithLabelValues(
		"agents", "reviewer", "Positive", "SlackReaction")); got != 1 {
		t.Errorf("counter = %v after redelivery, want 1", got)
	}
}

func TestRetractCountsWithStoredVerdict(t *testing.T) {
	scoreRetractedTotal.Reset()
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ev := metricEvent("U-retract")

	if err := Record(context.Background(), cl, ev); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	// The caller does not supply a verdict on retraction; it comes from the object.
	retraction := ev
	retraction.Verdict = ""
	if err := Retract(context.Background(), cl, retraction); err != nil {
		t.Fatalf("Retract() error = %v", err)
	}
	if got := testutil.ToFloat64(scoreRetractedTotal.WithLabelValues(
		"agents", "reviewer", "Positive", "SlackReaction")); got != 1 {
		t.Errorf("counter = %v for the stored verdict, want 1", got)
	}

	// Retracting an absent score is a no-op and must not be counted.
	if err := Retract(context.Background(), cl, retraction); err != nil {
		t.Fatalf("Retract() on absent score error = %v", err)
	}
	if got := testutil.ToFloat64(scoreRetractedTotal.WithLabelValues(
		"agents", "reviewer", "Positive", "SlackReaction")); got != 1 {
		t.Errorf("counter = %v after retracting an absent score, want 1", got)
	}
}

// The documented fallback: when the score is not readable, the delete still
// happens and the verdict label is empty rather than the retraction being skipped.
func TestRetractCountsWithEmptyVerdictWhenUnreadable(t *testing.T) {
	scoreRetractedTotal.Reset()
	ev := metricEvent("U-unreadable")
	name := ScoreName(ev.Task.UID, ev.Source.Type, ev.Source.Actor, ev.Source.Signal)

	// A client that serves reads from an empty cache but deletes against a store
	// that has the object — the read-your-writes gap Retract must tolerate.
	stored := &kelos.TaskScore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ev.Task.Namespace},
		Spec: kelos.TaskScoreSpec{
			TaskRef: kelos.TaskReference{Name: ev.Task.Name, UID: ev.Task.UID},
			Verdict: kelos.ScoreVerdictPositive,
			Source:  ev.Source,
		},
	}
	cl := &staleReadClient{
		Client: fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(stored).Build(),
	}

	// The real retract path supplies no verdict; it comes from the stored object,
	// which is exactly what is unreadable here.
	ev.Verdict = ""
	if err := Retract(context.Background(), cl, ev); err != nil {
		t.Fatalf("Retract() error = %v", err)
	}
	if got := testutil.ToFloat64(scoreRetractedTotal.WithLabelValues(
		"agents", "reviewer", "", "SlackReaction")); got != 1 {
		t.Errorf("counter with empty verdict = %v, want 1", got)
	}
}

// staleReadClient serves Get as NotFound while delegating writes, standing in for
// a cache that has not yet observed a very recent create.
type staleReadClient struct {
	client.Client
}

func (c *staleReadClient) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "kelos.dev", Resource: "taskscores"}, key.Name)
}
