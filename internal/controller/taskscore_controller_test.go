package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
)

const (
	scoreTestNamespace = "agents"
	scoreTestTaskUID   = "task-uid-1"
	scoreTestTaskName  = "review-1"
)

func newScoreTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	return scheme
}

func scoreTestRecord() *kelos.TaskRecord {
	return &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scoreTestTaskUID,
			Namespace: scoreTestNamespace,
			UID:       "record-uid",
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef: kelos.TaskReference{Name: scoreTestTaskName, UID: scoreTestTaskUID},
			Phase:   kelos.TaskPhaseSucceeded,
		},
	}
}

func testScore(name string, verdict kelos.ScoreVerdict, observedAt *metav1.Time) *kelos.TaskScore {
	return &kelos.TaskScore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: scoreTestNamespace,
			Labels:    map[string]string{scoring.LabelTaskUID: scoreTestTaskUID},
		},
		Spec: kelos.TaskScoreSpec{
			TaskRef:    kelos.TaskReference{Name: scoreTestTaskName, UID: scoreTestTaskUID},
			Verdict:    verdict,
			Source:     kelos.ScoreSource{Type: kelos.ScoreSourceSlackReaction, Actor: name},
			ObservedAt: observedAt,
		},
	}
}

func reconcileScores(t *testing.T, objects ...client.Object) (client.Client, kelos.TaskRecord) {
	t.Helper()
	scheme := newScoreTestScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskRecord{}).
		WithObjects(objects...).
		Build()

	r := &TaskScoreReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKey{
		Namespace: scoreTestNamespace,
		Name:      scoreTestTaskUID,
	}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), req.NamespacedName, &record); err != nil {
		t.Fatalf("getting reconciled TaskRecord: %v", err)
	}
	return cl, record
}

func TestTaskScoreReconcilerSummarizesScores(t *testing.T) {
	earlier := metav1.NewTime(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	later := metav1.NewTime(time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC))

	_, record := reconcileScores(t,
		scoreTestRecord(),
		testScore("a", kelos.ScoreVerdictPositive, &earlier),
		testScore("b", kelos.ScoreVerdictPositive, &later),
		testScore("c", kelos.ScoreVerdictNegative, &earlier),
	)

	summary := record.Status.Scores
	if summary == nil {
		t.Fatal("status.scores = nil, want a summary")
	}
	if summary.Total != 3 {
		t.Errorf("total = %d, want 3", summary.Total)
	}
	if summary.Positive != 2 {
		t.Errorf("positive = %d, want 2", summary.Positive)
	}
	if summary.Negative != 1 {
		t.Errorf("negative = %d, want 1", summary.Negative)
	}
	if summary.LastObservedAt == nil || !summary.LastObservedAt.Equal(&later) {
		t.Errorf("lastObservedAt = %v, want %v", summary.LastObservedAt, later)
	}
}

// Scores belonging to another Task in the same namespace must not be counted.
func TestTaskScoreReconcilerIgnoresOtherTasksScores(t *testing.T) {
	otherScore := testScore("other", kelos.ScoreVerdictNegative, nil)
	otherScore.Labels[scoring.LabelTaskUID] = "different-task-uid"
	otherScore.Spec.TaskRef = kelos.TaskReference{Name: "review-2", UID: "different-task-uid"}

	_, record := reconcileScores(t,
		scoreTestRecord(),
		testScore("mine", kelos.ScoreVerdictPositive, nil),
		otherScore,
	)

	summary := record.Status.Scores
	if summary == nil {
		t.Fatal("status.scores = nil, want a summary")
	}
	if summary.Total != 1 || summary.Positive != 1 || summary.Negative != 0 {
		t.Errorf("summary = %+v, want only the one Positive score for this Task", summary)
	}
}

// An unscored record keeps an empty status rather than a summary of zeros, so
// "never scored" is distinguishable from "scored and evenly split".
func TestTaskScoreReconcilerLeavesUnscoredRecordEmpty(t *testing.T) {
	_, record := reconcileScores(t, scoreTestRecord())

	if record.Status.Scores != nil {
		t.Errorf("status.scores = %+v, want nil for an unscored record", record.Status.Scores)
	}
}

// Retraction deletes the TaskScore, so the summary has to shrink back.
func TestTaskScoreReconcilerRecomputesAfterRetraction(t *testing.T) {
	scheme := newScoreTestScheme(t)
	positive := testScore("a", kelos.ScoreVerdictPositive, nil)
	negative := testScore("b", kelos.ScoreVerdictNegative, nil)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskRecord{}).
		WithObjects(scoreTestRecord(), positive, negative).
		Build()

	r := &TaskScoreReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKey{
		Namespace: scoreTestNamespace,
		Name:      scoreTestTaskUID,
	}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}

	if err := cl.Delete(context.Background(), negative); err != nil {
		t.Fatalf("deleting the retracted score: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), req.NamespacedName, &record); err != nil {
		t.Fatalf("getting reconciled TaskRecord: %v", err)
	}
	summary := record.Status.Scores
	if summary == nil {
		t.Fatal("status.scores = nil, want a summary with the remaining score")
	}
	if summary.Total != 1 || summary.Negative != 0 || summary.Positive != 1 {
		t.Errorf("summary = %+v, want 1 total / 1 positive / 0 negative after retraction", summary)
	}
}

// Adoption is what bounds TaskScore growth: scores are reclaimed when the
// record's TTL removes it.
func TestTaskScoreReconcilerAdoptsScores(t *testing.T) {
	cl, record := reconcileScores(t, scoreTestRecord(), testScore("a", kelos.ScoreVerdictPositive, nil))

	var score kelos.TaskScore
	key := client.ObjectKey{Namespace: scoreTestNamespace, Name: "a"}
	if err := cl.Get(context.Background(), key, &score); err != nil {
		t.Fatalf("getting adopted TaskScore: %v", err)
	}

	if len(score.OwnerReferences) != 1 {
		t.Fatalf("ownerReferences = %d, want 1", len(score.OwnerReferences))
	}
	owner := score.OwnerReferences[0]
	if owner.Kind != "TaskRecord" || owner.Name != record.Name || owner.UID != record.UID {
		t.Errorf("owner = %+v, want the TaskRecord %s/%s", owner, record.Name, record.UID)
	}
}

func TestTaskScoreReconcilerMissingRecordIsNoOp(t *testing.T) {
	scheme := newScoreTestScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskRecord{}).
		WithObjects(testScore("a", kelos.ScoreVerdictPositive, nil)).
		Build()

	r := &TaskScoreReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKey{
		Namespace: scoreTestNamespace,
		Name:      scoreTestTaskUID,
	}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Errorf("Reconcile() with no TaskRecord error = %v, want nil", err)
	}
}

func TestEnqueueRecordForScore(t *testing.T) {
	tests := []struct {
		name     string
		obj      client.Object
		wantReqs int
		wantName string
	}{
		{
			name:     "maps to the record named after the task UID",
			obj:      testScore("a", kelos.ScoreVerdictPositive, nil),
			wantReqs: 1,
			wantName: scoreTestTaskUID,
		},
		{
			name: "score with no task UID is dropped",
			obj: &kelos.TaskScore{
				ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: scoreTestNamespace},
			},
			wantReqs: 0,
		},
		{
			name:     "unrelated object is dropped",
			obj:      &kelos.TaskRecord{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: scoreTestNamespace}},
			wantReqs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := enqueueRecordForScore(context.Background(), tt.obj)
			if len(reqs) != tt.wantReqs {
				t.Fatalf("enqueueRecordForScore() returned %d requests, want %d", len(reqs), tt.wantReqs)
			}
			if tt.wantReqs == 0 {
				return
			}
			if reqs[0].Name != tt.wantName || reqs[0].Namespace != scoreTestNamespace {
				t.Errorf("request = %s/%s, want %s/%s",
					reqs[0].Namespace, reqs[0].Name, scoreTestNamespace, tt.wantName)
			}
		})
	}
}

// scoreSummaryEqual exists to keep watch-driven reconciles from churning the
// status; without an assertion that it actually suppresses the write, the
// short-circuit could regress silently.
func TestTaskScoreReconcilerSkipsUnchangedStatusWrite(t *testing.T) {
	scheme := newScoreTestScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskRecord{}).
		WithObjects(scoreTestRecord(), testScore("a", kelos.ScoreVerdictPositive, nil)).
		Build()

	r := &TaskScoreReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKey{
		Namespace: scoreTestNamespace,
		Name:      scoreTestTaskUID,
	}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	var afterFirst kelos.TaskRecord
	if err := cl.Get(context.Background(), req.NamespacedName, &afterFirst); err != nil {
		t.Fatalf("getting record: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	var afterSecond kelos.TaskRecord
	if err := cl.Get(context.Background(), req.NamespacedName, &afterSecond); err != nil {
		t.Fatalf("getting record: %v", err)
	}

	if afterFirst.ResourceVersion != afterSecond.ResourceVersion {
		t.Errorf("record was rewritten on an unchanged reconcile (resourceVersion %s -> %s)",
			afterFirst.ResourceVersion, afterSecond.ResourceVersion)
	}
}

// The 1→0 transition takes a different branch from 2→1: summarizeScores returns
// nil, so the status field is cleared rather than decremented.
func TestTaskScoreReconcilerClearsSummaryWhenLastScoreRetracted(t *testing.T) {
	scheme := newScoreTestScheme(t)
	only := testScore("a", kelos.ScoreVerdictPositive, nil)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskRecord{}).
		WithObjects(scoreTestRecord(), only).
		Build()

	r := &TaskScoreReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKey{
		Namespace: scoreTestNamespace,
		Name:      scoreTestTaskUID,
	}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}

	if err := cl.Delete(context.Background(), only); err != nil {
		t.Fatalf("deleting the last score: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), req.NamespacedName, &record); err != nil {
		t.Fatalf("getting record: %v", err)
	}
	if record.Status.Scores != nil {
		t.Errorf("status.scores = %+v, want nil once the last score is retracted", record.Status.Scores)
	}
}

// The task-uid label is a prefilter that nothing validates against the spec, so a
// mislabelled score must not contaminate another Task's counts.
func TestTaskScoreReconcilerIgnoresMislabelledScore(t *testing.T) {
	mislabelled := testScore("mislabelled", kelos.ScoreVerdictNegative, nil)
	// Carries this record's label but names a different Task in its spec.
	mislabelled.Spec.TaskRef = kelos.TaskReference{Name: "other", UID: "other-task-uid"}

	_, record := reconcileScores(t,
		scoreTestRecord(),
		testScore("mine", kelos.ScoreVerdictPositive, nil),
		mislabelled,
	)

	summary := record.Status.Scores
	if summary == nil {
		t.Fatal("status.scores = nil, want the one legitimate score")
	}
	if summary.Total != 1 || summary.Positive != 1 || summary.Negative != 0 {
		t.Errorf("summary = %+v, want only the score whose spec names this Task", summary)
	}
}
