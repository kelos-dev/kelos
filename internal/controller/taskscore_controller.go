package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
)

// TaskScoreReconciler maintains the score summary on a TaskRecord from the
// TaskScores recorded against its Task, and adopts those scores so they are
// garbage-collected with the record.
//
// It reconciles TaskRecords rather than TaskScores so a retraction — which
// deletes a TaskScore — still recomputes the summary. Reconciling the deleted
// object could not, because its taskRef is gone by then.
type TaskScoreReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Deletions are not granted here: scores are retracted by kelos-slack-server
// (its own role has delete) and reclaimed by garbage collection once adopted.
// +kubebuilder:rbac:groups=kelos.dev,resources=taskscores,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=kelos.dev,resources=taskrecords/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskrecords/finalizers,verbs=update

// Reconcile recomputes the score summary for one TaskRecord.
func (r *TaskScoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var record kelos.TaskRecord
	if err := r.Get(ctx, req.NamespacedName, &record); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var scores kelos.TaskScoreList
	if err := r.List(ctx, &scores,
		client.InNamespace(record.Namespace),
		client.MatchingLabels{scoring.LabelTaskUID: string(record.Spec.TaskRef.UID)},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing TaskScores for record %s: %w", record.Name, err)
	}

	if err := r.adoptScores(ctx, &record, scores.Items); err != nil {
		return ctrl.Result{}, err
	}

	summary := summarizeScores(record.Spec.TaskRef.UID, scores.Items)
	if scoreSummaryEqual(record.Status.Scores, summary) {
		return ctrl.Result{}, nil
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelos.TaskRecord
		if err := r.Get(ctx, req.NamespacedName, &current); err != nil {
			return err
		}
		current.Status.Scores = summary
		return r.Status().Update(ctx, &current)
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating score summary on record %s: %w", record.Name, err)
	}

	// summary is nil once the last score is retracted, which is a real transition
	// (status was set, now it is cleared) rather than the unscored no-op above.
	total := 0
	if summary != nil {
		total = int(summary.Total)
	}
	logger.V(1).Info("Updated TaskRecord score summary", "record", record.Name,
		"task", record.Spec.TaskRef.Name, "total", total)
	return ctrl.Result{}, nil
}

// adoptScores sets an owner reference from each score to the record so scores are
// reclaimed when the record's TTL removes it. A TaskScore can be created before
// its record exists, so adoption happens here rather than at creation time.
func (r *TaskScoreReconciler) adoptScores(ctx context.Context, record *kelos.TaskRecord, scores []kelos.TaskScore) error {
	for i := range scores {
		score := &scores[i]
		if len(score.OwnerReferences) > 0 {
			continue
		}
		if err := ctrl.SetControllerReference(record, score, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on TaskScore %s: %w", score.Name, err)
		}
		if err := r.Update(ctx, score); err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
				// Retracted or updated concurrently; the next reconcile adopts it.
				continue
			}
			return fmt.Errorf("adopting TaskScore %s onto record %s: %w", score.Name, record.Name, err)
		}
	}
	return nil
}

// summarizeScores counts verdicts and tracks the latest observation time. It
// returns nil when there are no scores so an unscored record keeps an empty
// status rather than a summary of zeros.
//
// Scores are selected by the kelos.dev/task-uid label, which nothing validates
// against spec.taskRef.uid — CEL rules cannot reach metadata.labels. The spec is
// therefore treated as authoritative here, so a mislabelled or hand-written score
// cannot contaminate another Task's counts.
func summarizeScores(taskUID types.UID, scores []kelos.TaskScore) *kelos.TaskScoreSummary {
	var summary *kelos.TaskScoreSummary
	for i := range scores {
		score := &scores[i]
		if score.Spec.TaskRef.UID != taskUID {
			continue
		}
		if summary == nil {
			summary = &kelos.TaskScoreSummary{}
		}
		summary.Total++
		switch score.Spec.Verdict {
		case kelos.ScoreVerdictPositive:
			summary.Positive++
		case kelos.ScoreVerdictNegative:
			summary.Negative++
		}
		if observed := score.Spec.ObservedAt; observed != nil {
			if summary.LastObservedAt == nil || observed.After(summary.LastObservedAt.Time) {
				summary.LastObservedAt = observed.DeepCopy()
			}
		}
	}
	return summary
}

func scoreSummaryEqual(a, b *kelos.TaskScoreSummary) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Positive != b.Positive || a.Negative != b.Negative || a.Total != b.Total {
		return false
	}
	switch {
	case a.LastObservedAt == nil && b.LastObservedAt == nil:
		return true
	case a.LastObservedAt == nil || b.LastObservedAt == nil:
		return false
	default:
		return a.LastObservedAt.Equal(b.LastObservedAt)
	}
}

// SetupWithManager sets up the controller with the Manager. It is named
// explicitly because TaskRecordReconciler also reconciles TaskRecords.
func (r *TaskScoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("taskscore").
		For(&kelos.TaskRecord{}).
		Watches(&kelos.TaskScore{}, handler.EnqueueRequestsFromMapFunc(enqueueRecordForScore)).
		Complete(r)
}

// enqueueRecordForScore maps a TaskScore to the TaskRecord for its Task. Records
// are named after the Task UID, so no lookup is needed — which matters because
// this also runs for deleted scores.
func enqueueRecordForScore(_ context.Context, obj client.Object) []reconcile.Request {
	score, ok := obj.(*kelos.TaskScore)
	if !ok {
		return nil
	}
	if score.Spec.TaskRef.UID == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{
			Namespace: score.Namespace,
			Name:      string(score.Spec.TaskRef.UID),
		},
	}}
}
