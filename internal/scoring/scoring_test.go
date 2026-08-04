package scoring

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	return scheme
}

func TestResolveSlackResultFromTask(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "review-1",
			Namespace: "agents",
			UID:       "task-uid-1",
			Labels: map[string]string{
				LabelSlackResultChannel:  "C123",
				LabelSlackResultTS:       "1712345678.123456",
				taskbuilder.SpawnerLabel: "reviewer",
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(task).Build()

	identity, err := ResolveSlackResult(context.Background(), cl, "C123", "1712345678.123456")
	if err != nil {
		t.Fatalf("ResolveSlackResult() error = %v", err)
	}
	if identity == nil {
		t.Fatal("ResolveSlackResult() = nil, want the matching Task")
	}
	if identity.Name != "review-1" || identity.Namespace != "agents" {
		t.Errorf("resolved %s/%s, want agents/review-1", identity.Namespace, identity.Name)
	}
	if identity.UID != "task-uid-1" {
		t.Errorf("resolved UID = %q, want task-uid-1", identity.UID)
	}
	if identity.SpawnerName != "reviewer" {
		t.Errorf("resolved spawner = %q, want reviewer", identity.SpawnerName)
	}
}

// The Task is deleted by TTL long before scores stop arriving, so resolution
// must fall through to the TaskRecord.
func TestResolveSlackResultFallsBackToTaskRecord(t *testing.T) {
	record := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-uid-2",
			Namespace: "agents",
			Labels: map[string]string{
				LabelSlackResultChannel:  "C123",
				LabelSlackResultTS:       "1712345678.222222",
				taskbuilder.SpawnerLabel: "reviewer",
			},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef: kelos.TaskReference{Name: "review-2", UID: "task-uid-2"},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(record).Build()

	identity, err := ResolveSlackResult(context.Background(), cl, "C123", "1712345678.222222")
	if err != nil {
		t.Fatalf("ResolveSlackResult() error = %v", err)
	}
	if identity == nil {
		t.Fatal("ResolveSlackResult() = nil, want the matching TaskRecord")
	}
	if identity.Name != "review-2" || identity.UID != "task-uid-2" {
		t.Errorf("resolved %s/%s, want review-2/task-uid-2", identity.Name, identity.UID)
	}
	if identity.SpawnerName != "reviewer" {
		t.Errorf("resolved spawner = %q, want reviewer", identity.SpawnerName)
	}
}

func TestResolveSlackResultNoMatch(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "review-1",
			Namespace: "agents",
			UID:       "task-uid-1",
			Labels: map[string]string{
				LabelSlackResultChannel: "C123",
				LabelSlackResultTS:      "1712345678.123456",
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(task).Build()

	tests := []struct {
		name    string
		channel string
		ts      string
	}{
		{name: "different message in the same channel", channel: "C123", ts: "1712345678.999999"},
		{name: "same timestamp in a different channel", channel: "C999", ts: "1712345678.123456"},
		{name: "empty channel", channel: "", ts: "1712345678.123456"},
		{name: "empty timestamp", channel: "C123", ts: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, err := ResolveSlackResult(context.Background(), cl, tt.channel, tt.ts)
			if err != nil {
				t.Fatalf("ResolveSlackResult() error = %v", err)
			}
			if identity != nil {
				t.Errorf("ResolveSlackResult() = %+v, want nil", identity)
			}
		})
	}
}

func TestResolveSlackResultAmbiguousMatchErrors(t *testing.T) {
	labels := map[string]string{
		LabelSlackResultChannel: "C123",
		LabelSlackResultTS:      "1712345678.123456",
	}
	first := &kelos.Task{ObjectMeta: metav1.ObjectMeta{
		Name: "review-1", Namespace: "agents", UID: "uid-1", Labels: labels,
	}}
	second := &kelos.Task{ObjectMeta: metav1.ObjectMeta{
		Name: "review-2", Namespace: "agents", UID: "uid-2", Labels: labels,
	}}
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(first, second).Build()

	if _, err := ResolveSlackResult(context.Background(), cl, "C123", "1712345678.123456"); err == nil {
		t.Fatal("ResolveSlackResult() error = nil, want an error naming the ambiguous ref")
	}
}

func TestRecordCreatesTaskScore(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	observed := metav1.NewTime(metav1.Now().Rfc3339Copy().Time)

	ev := Event{
		Task: TaskIdentity{
			Namespace:   "agents",
			Name:        "review-1",
			UID:         "task-uid-1",
			SpawnerName: "reviewer",
		},
		Verdict: kelos.ScoreVerdictPositive,
		Source: kelos.ScoreSource{
			Type:   kelos.ScoreSourceSlackReaction,
			Actor:  "U777",
			Signal: "+1",
			URI:    SlackURI("C123", "1712345678.123456"),
		},
		ObservedAt: &observed,
	}

	if err := Record(context.Background(), cl, ev); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	name := ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U777", "+1")
	var score kelos.TaskScore
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "agents", Name: name}, &score); err != nil {
		t.Fatalf("getting created TaskScore: %v", err)
	}

	if score.Spec.Verdict != kelos.ScoreVerdictPositive {
		t.Errorf("verdict = %q, want Positive", score.Spec.Verdict)
	}
	if score.Spec.TaskRef.Name != "review-1" || score.Spec.TaskRef.UID != "task-uid-1" {
		t.Errorf("taskRef = %+v, want review-1/task-uid-1", score.Spec.TaskRef)
	}
	if score.Spec.Source.Type != kelos.ScoreSourceSlackReaction {
		t.Errorf("source type = %q, want SlackReaction", score.Spec.Source.Type)
	}
	if score.Spec.Source.Actor != "U777" || score.Spec.Source.Signal != "+1" {
		t.Errorf("source actor/signal = %q/%q, want U777/+1", score.Spec.Source.Actor, score.Spec.Source.Signal)
	}
	if score.Spec.Source.URI != "slack://C123/1712345678.123456" {
		t.Errorf("source uri = %q, want slack://C123/1712345678.123456", score.Spec.Source.URI)
	}
	if score.Spec.ObservedAt == nil || !score.Spec.ObservedAt.Equal(&observed) {
		t.Errorf("observedAt = %v, want %v", score.Spec.ObservedAt, observed)
	}
	if score.Labels[LabelTaskUID] != "task-uid-1" {
		t.Errorf("%s label = %q, want task-uid-1", LabelTaskUID, score.Labels[LabelTaskUID])
	}
	if score.Labels[taskbuilder.SpawnerLabel] != "reviewer" {
		t.Errorf("%s label = %q, want reviewer", taskbuilder.SpawnerLabel, score.Labels[taskbuilder.SpawnerLabel])
	}
}

// Slack delivers events at least once, so a redelivered reaction must not create
// a second score.
func TestRecordIsIdempotent(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ev := Event{
		Task:    TaskIdentity{Namespace: "agents", Name: "review-1", UID: "task-uid-1"},
		Verdict: kelos.ScoreVerdictPositive,
		Source: kelos.ScoreSource{
			Type:   kelos.ScoreSourceSlackReaction,
			Actor:  "U777",
			Signal: "+1",
		},
	}

	for i := 0; i < 3; i++ {
		if err := Record(context.Background(), cl, ev); err != nil {
			t.Fatalf("Record() call %d error = %v", i+1, err)
		}
	}

	var scores kelos.TaskScoreList
	if err := cl.List(context.Background(), &scores); err != nil {
		t.Fatalf("listing TaskScores: %v", err)
	}
	if len(scores.Items) != 1 {
		t.Errorf("created %d TaskScores, want 1", len(scores.Items))
	}
}

func TestRetractDeletesTheScoreItsAdditionCreated(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ev := Event{
		Task:    TaskIdentity{Namespace: "agents", Name: "review-1", UID: "task-uid-1"},
		Verdict: kelos.ScoreVerdictNegative,
		Source: kelos.ScoreSource{
			Type:   kelos.ScoreSourceSlackReaction,
			Actor:  "U777",
			Signal: "-1",
		},
	}
	other := ev
	other.Source.Actor = "U888"

	if err := Record(context.Background(), cl, ev); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := Record(context.Background(), cl, other); err != nil {
		t.Fatalf("Record() for second actor error = %v", err)
	}

	if err := Retract(context.Background(), cl, ev); err != nil {
		t.Fatalf("Retract() error = %v", err)
	}

	name := ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U777", "-1")
	var score kelos.TaskScore
	err := cl.Get(context.Background(), client.ObjectKey{Namespace: "agents", Name: name}, &score)
	if !apierrors.IsNotFound(err) {
		t.Errorf("retracted score still present (err = %v)", err)
	}

	otherName := ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U888", "-1")
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "agents", Name: otherName}, &score); err != nil {
		t.Errorf("retraction removed another actor's score: %v", err)
	}
}

func TestRetractMissingScoreIsNoOp(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ev := Event{
		Task:    TaskIdentity{Namespace: "agents", Name: "review-1", UID: "task-uid-1"},
		Verdict: kelos.ScoreVerdictPositive,
		Source:  kelos.ScoreSource{Type: kelos.ScoreSourceSlackReaction, Actor: "U777", Signal: "+1"},
	}
	if err := Retract(context.Background(), cl, ev); err != nil {
		t.Errorf("Retract() on absent score error = %v, want nil", err)
	}
}

func TestScoreName(t *testing.T) {
	base := ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U777", "+1")

	if got := ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U777", "+1"); got != base {
		t.Errorf("ScoreName() is not deterministic: %q then %q", base, got)
	}

	tests := []struct {
		name  string
		other string
	}{
		{name: "different actor", other: ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U888", "+1")},
		{name: "different raw signal", other: ScoreName("task-uid-1", kelos.ScoreSourceSlackReaction, "U777", "-1")},
		{name: "different task", other: ScoreName("task-uid-2", kelos.ScoreSourceSlackReaction, "U777", "+1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.other == base {
				t.Errorf("ScoreName() collided with %q", base)
			}
		})
	}

	// The name is used as an object name, so it must stay a valid DNS subdomain.
	for _, r := range base {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !valid {
			t.Fatalf("ScoreName() = %q contains invalid object-name character %q", base, r)
		}
	}
}

// Actor and raw signal are hashed together, so a source must not be able to
// forge another's name by shifting the boundary between the two fields.
func TestScoreNameFieldBoundaryIsUnambiguous(t *testing.T) {
	a := ScoreName("uid", kelos.ScoreSourceSlackReaction, "U7", "77+1")
	b := ScoreName("uid", kelos.ScoreSourceSlackReaction, "U777", "+1")
	if a == b {
		t.Errorf("ScoreName() collided across the actor/raw boundary: %q", a)
	}
}
