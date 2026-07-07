package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// TaskRecordReconciler garbage-collects expired TaskRecords independently of the
// Task lifecycle. Each record schedules its own deletion via RequeueAfter when
// first observed, so records with a TTL are reclaimed even in quiet namespaces
// where no Task reconcile fires and after the source Task has been deleted.
type TaskRecordReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NowFunc returns the current time. Defaults to time.Now.
	// Overridable in tests for deterministic behavior.
	NowFunc func() time.Time
}

// +kubebuilder:rbac:groups=kelos.dev,resources=taskrecords,verbs=get;list;watch;delete

func (r *TaskRecordReconciler) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}

// Reconcile deletes the TaskRecord if its TTL has expired, otherwise requeues
// at the expiry time.
func (r *TaskRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var record kelos.TaskRecord
	if err := r.Get(ctx, req.NamespacedName, &record); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if record.Spec.TTLSecondsAfterCompletion == nil {
		// No TTL configured: retained indefinitely.
		return ctrl.Result{}, nil
	}
	if record.Spec.CompletionTime == nil {
		// A TTL was requested but there is no completion time to anchor it to, so
		// the record can never be garbage-collected. Surface the malformed state
		// rather than silently retaining it forever.
		logger.Info("TaskRecord has a TTL but no completionTime; it will not be garbage-collected", "record", record.Name)
		return ctrl.Result{}, nil
	}

	expiry := record.Spec.CompletionTime.Add(time.Duration(*record.Spec.TTLSecondsAfterCompletion) * time.Second)
	remaining := expiry.Sub(r.now())
	if remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	if err := r.Delete(ctx, &record); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "Deleting expired TaskRecord", "record", record.Name)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TaskRecordReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.TaskRecord{}).
		Complete(r)
}
