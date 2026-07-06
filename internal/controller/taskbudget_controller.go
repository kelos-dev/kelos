package controller

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// TaskBudgetReconciler keeps TaskBudget.status.used current independently of
// Task admission. Admission only refreshes status for budgets it evaluates, so
// after an admitted Task completes and writes a TaskRecord, this reconciler
// recomputes usage (triggered by the TaskRecord change) and requeues at the
// period boundary so status resets when the period rolls over.
type TaskBudgetReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NowFunc returns the current time. Defaults to time.Now.
	// Overridable in tests for deterministic behavior.
	NowFunc func() time.Time
}

// +kubebuilder:rbac:groups=kelos.dev,resources=taskbudgets,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskbudgets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskrecords,verbs=get;list;watch

func (r *TaskBudgetReconciler) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}

func (r *TaskBudgetReconciler) budget() *budgetEnforcer {
	return &budgetEnforcer{Client: r.Client, now: r.now}
}

// Reconcile recomputes a TaskBudget's status.used for the current period.
func (r *TaskBudgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var budget kelos.TaskBudget
	if err := r.Get(ctx, req.NamespacedName, &budget); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	enforcer := r.budget()

	selector, err := metav1.LabelSelectorAsSelector(&budget.Spec.TaskSelector)
	if err != nil {
		// Selectors are validated at the API boundary, so this is unreachable
		// for stored budgets; nothing to refresh.
		logger.Error(err, "Skipping TaskBudget with invalid selector", "budget", budget.Name)
		return ctrl.Result{}, nil
	}

	periodStart, periodEnd, err := computePeriodBoundaries(budget.Spec.Period, r.now())
	if err != nil {
		// Period type and timezone are validated at the API boundary; unreachable.
		logger.Error(err, "Skipping TaskBudget with invalid period configuration", "budget", budget.Name)
		return ctrl.Result{}, nil
	}

	used, err := enforcer.sumPeriodUsage(ctx, budget.Namespace, selector, periodStart, periodEnd)
	if err != nil {
		return ctrl.Result{}, err
	}

	enforcer.clearBudgetDegradedCondition(ctx, &budget)
	enforcer.updateBudgetStatus(ctx, &budget, periodStart, periodEnd, used)

	// Requeue at the period boundary so status.used resets when the period rolls.
	requeueAfter := periodEnd.Sub(r.now())
	if requeueAfter <= 0 {
		requeueAfter = time.Second
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TaskBudgetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// GenerationChangedPredicate ignores the reconciler's own status writes,
		// which would otherwise re-trigger reconcile in a loop.
		For(&kelos.TaskBudget{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&kelos.TaskRecord{}, handler.EnqueueRequestsFromMapFunc(r.enqueueBudgetsForRecord)).
		Complete(r)
}

// enqueueBudgetsForRecord enqueues all TaskBudgets in the TaskRecord's namespace
// so their usage is recomputed when a record is created, updated, or deleted.
func (r *TaskBudgetReconciler) enqueueBudgetsForRecord(ctx context.Context, obj client.Object) []reconcile.Request {
	record, ok := obj.(*kelos.TaskRecord)
	if !ok {
		return nil
	}

	var budgetList kelos.TaskBudgetList
	if err := r.List(ctx, &budgetList, client.InNamespace(record.Namespace)); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(budgetList.Items))
	for i := range budgetList.Items {
		b := &budgetList.Items[i]
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(b),
		})
	}
	return requests
}
