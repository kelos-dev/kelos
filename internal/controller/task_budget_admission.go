package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// budgetDegradedRequeue is the requeue delay when a matching budget has a
// configuration or operational error that prevents evaluation.
const budgetDegradedRequeue = 30 * time.Second

const (
	// budgetBlockedMaxRequeue is the maximum requeue delay for budget-blocked tasks.
	budgetBlockedMaxRequeue = 5 * time.Minute
)

// checkBudgetAdmission checks all matching TaskBudgets before job creation.
// Returns (true, _, nil) if admitted, (false, result, nil) if blocked,
// or (false, _, err) on error.
func (r *TaskReconciler) checkBudgetAdmission(ctx context.Context, task *kelos.Task) (bool, ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var budgetList kelos.TaskBudgetList
	if err := r.List(ctx, &budgetList, client.InNamespace(task.Namespace)); err != nil {
		return false, ctrl.Result{}, err
	}

	if len(budgetList.Items) == 0 {
		// No budgets in namespace — clear any stale BudgetBlocked condition
		r.clearBudgetBlockedCondition(ctx, task)
		return true, ctrl.Result{}, nil
	}

	for i := range budgetList.Items {
		budget := &budgetList.Items[i]

		selector, err := metav1.LabelSelectorAsSelector(&budget.Spec.TaskSelector)
		if err != nil {
			logger.Error(err, "Matching budget has invalid selector, blocking task", "budget", budget.Name)
			r.setBudgetDegradedCondition(ctx, budget, "InvalidSelector", err.Error())
			r.setBudgetBlockedPhase(ctx, task, budget.Name, fmt.Sprintf("invalid selector: %v", err))
			return false, ctrl.Result{RequeueAfter: budgetDegradedRequeue}, nil
		}

		if !selector.Matches(labels.Set(task.Labels)) {
			continue
		}

		// Budget matches this task — from here on, any operational error must
		// block admission (fail closed).

		// Compute current period boundaries
		periodStart, periodEnd, err := computePeriodBoundaries(budget.Spec.Period, r.now())
		if err != nil {
			logger.Error(err, "Matching budget has invalid configuration, blocking task", "budget", budget.Name)
			r.setBudgetDegradedCondition(ctx, budget, "InvalidTimezone", err.Error())
			r.setBudgetBlockedPhase(ctx, task, budget.Name, fmt.Sprintf("budget configuration error: %v", err))
			return false, ctrl.Result{RequeueAfter: budgetDegradedRequeue}, nil
		}

		// List TaskRecords matching this budget's selector within the current period
		used, err := r.sumPeriodUsage(ctx, task.Namespace, selector, periodStart, periodEnd)
		if err != nil {
			logger.Error(err, "Unable to sum period usage, blocking task", "budget", budget.Name)
			r.setBudgetDegradedCondition(ctx, budget, "ListError", err.Error())
			return false, ctrl.Result{}, fmt.Errorf("summing period usage for budget %s: %w", budget.Name, err)
		}

		// Budget evaluated successfully — clear any stale Degraded condition
		r.clearBudgetDegradedCondition(ctx, budget)

		// Check limits
		exceeded, reason := checkLimitsExceeded(budget, used)
		if exceeded {
			logger.Info("Budget exceeded, blocking task", "budget", budget.Name, "task", task.Name, "reason", reason)

			// Set Waiting phase with BudgetBlocked condition
			r.setBudgetBlockedPhase(ctx, task, budget.Name, reason)

			// Best-effort update TaskBudget status
			r.updateBudgetStatus(ctx, budget, periodStart, periodEnd, used)

			// Requeue after min(5 minutes, time until period end)
			requeueAfter := periodEnd.Sub(r.now())
			if requeueAfter > budgetBlockedMaxRequeue {
				requeueAfter = budgetBlockedMaxRequeue
			}
			if requeueAfter <= 0 {
				requeueAfter = time.Second
			}

			return false, ctrl.Result{RequeueAfter: requeueAfter}, nil
		}

		// Best-effort update TaskBudget status even when not exceeded
		r.updateBudgetStatus(ctx, budget, periodStart, periodEnd, used)
	}

	// All matching budgets are within limits — clear any stale BudgetBlocked condition
	r.clearBudgetBlockedCondition(ctx, task)
	return true, ctrl.Result{}, nil
}

// computePeriodBoundaries computes the start (inclusive) and end (exclusive) of
// the current budget period based on the period type and timezone.
func computePeriodBoundaries(period kelos.BudgetPeriod, now time.Time) (time.Time, time.Time, error) {
	tz := period.Timezone
	if tz == "" {
		tz = "UTC"
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("loading timezone %q: %w", tz, err)
	}

	localNow := now.In(loc)

	switch period.Type {
	case kelos.BudgetPeriodDaily:
		start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
		end := start.AddDate(0, 0, 1)
		return start, end, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported period type %q", period.Type)
	}
}

// sumPeriodUsage lists TaskRecords in the namespace matching the selector and
// sums usage from records whose CompletionTime falls within [periodStart, periodEnd).
// The label selector is passed server-side so the API server pre-filters records.
func (r *TaskReconciler) sumPeriodUsage(ctx context.Context, namespace string, selector labels.Selector, periodStart, periodEnd time.Time) (*kelos.TaskUsage, error) {
	var recordList kelos.TaskRecordList
	if err := r.List(ctx, &recordList,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return nil, err
	}

	var totalCost resource.Quantity
	var totalInput, totalOutput int64

	for _, rec := range recordList.Items {
		if rec.Spec.CompletionTime == nil {
			continue
		}
		ct := rec.Spec.CompletionTime.Time
		if ct.Before(periodStart) || !ct.Before(periodEnd) {
			continue
		}
		if rec.Spec.Usage == nil {
			continue
		}
		if rec.Spec.Usage.CostUSD != nil {
			totalCost.Add(*rec.Spec.Usage.CostUSD)
		}
		if rec.Spec.Usage.InputTokens != nil {
			totalInput += *rec.Spec.Usage.InputTokens
		}
		if rec.Spec.Usage.OutputTokens != nil {
			totalOutput += *rec.Spec.Usage.OutputTokens
		}
	}

	usage := &kelos.TaskUsage{}
	if !totalCost.IsZero() {
		usage.CostUSD = &totalCost
	}
	if totalInput > 0 {
		usage.InputTokens = &totalInput
	}
	if totalOutput > 0 {
		usage.OutputTokens = &totalOutput
	}
	return usage, nil
}

// checkLimitsExceeded checks whether the accumulated usage exceeds any budget limits.
// Returns true and a human-readable reason if exceeded. Missing used values are
// treated as zero when the corresponding limit is set, so a zero limit blocks admission
// even before any usage is recorded.
func checkLimitsExceeded(budget *kelos.TaskBudget, used *kelos.TaskUsage) (bool, string) {
	if budget.Spec.MaxCostUSD != nil {
		usedCost := used.CostUSD
		if usedCost == nil {
			zero := resource.Quantity{}
			usedCost = &zero
		}
		if usedCost.Cmp(*budget.Spec.MaxCostUSD) >= 0 {
			return true, fmt.Sprintf("cost %s >= limit %s", usedCost.String(), budget.Spec.MaxCostUSD.String())
		}
	}
	if budget.Spec.MaxInputTokens != nil {
		var usedInput int64
		if used.InputTokens != nil {
			usedInput = *used.InputTokens
		}
		if usedInput >= *budget.Spec.MaxInputTokens {
			return true, fmt.Sprintf("input tokens %d >= limit %d", usedInput, *budget.Spec.MaxInputTokens)
		}
	}
	if budget.Spec.MaxOutputTokens != nil {
		var usedOutput int64
		if used.OutputTokens != nil {
			usedOutput = *used.OutputTokens
		}
		if usedOutput >= *budget.Spec.MaxOutputTokens {
			return true, fmt.Sprintf("output tokens %d >= limit %d", usedOutput, *budget.Spec.MaxOutputTokens)
		}
	}
	return false, ""
}

// setBudgetBlockedPhase sets the task to Waiting phase with a BudgetBlocked condition.
func (r *TaskReconciler) setBudgetBlockedPhase(ctx context.Context, task *kelos.Task, budgetName, reason string) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		task.Status.Phase = kelos.TaskPhaseWaiting
		task.Status.Message = fmt.Sprintf("Budget %q exceeded: %s", budgetName, reason)
		meta.SetStatusCondition(&task.Status.Conditions, metav1.Condition{
			Type:               "BudgetBlocked",
			Status:             metav1.ConditionTrue,
			Reason:             "BudgetExceeded",
			Message:            fmt.Sprintf("Blocked by TaskBudget %q: %s", budgetName, reason),
			ObservedGeneration: task.Generation,
			LastTransitionTime: metav1.Now(),
		})
		return r.Status().Update(ctx, task)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to update Task status to budget-blocked")
	}
}

// clearBudgetBlockedCondition removes the BudgetBlocked condition if present.
func (r *TaskReconciler) clearBudgetBlockedCondition(ctx context.Context, task *kelos.Task) {
	hasCond := false
	for _, c := range task.Status.Conditions {
		if c.Type == "BudgetBlocked" && c.Status == metav1.ConditionTrue {
			hasCond = true
			break
		}
	}
	if !hasCond {
		return
	}

	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		meta.RemoveStatusCondition(&task.Status.Conditions, "BudgetBlocked")
		return r.Status().Update(ctx, task)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to clear BudgetBlocked condition")
	}
}

// updateBudgetStatus best-effort updates the TaskBudget status with current
// period boundaries and accumulated usage.
func (r *TaskReconciler) updateBudgetStatus(ctx context.Context, budget *kelos.TaskBudget, periodStart, periodEnd time.Time, used *kelos.TaskUsage) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(budget), budget); getErr != nil {
			return getErr
		}
		budget.Status.ObservedGeneration = budget.Generation
		start := metav1.NewTime(periodStart)
		end := metav1.NewTime(periodEnd)
		budget.Status.CurrentPeriodStart = &start
		budget.Status.CurrentPeriodEnd = &end
		budget.Status.Used = used
		return r.Status().Update(ctx, budget)
	})
	if updateErr != nil {
		logger.V(1).Info("Unable to update TaskBudget status", "budget", budget.Name, "error", updateErr)
	}
}

// setBudgetDegradedCondition sets a Degraded condition on the TaskBudget so
// operators can observe configuration or operational errors.
func (r *TaskReconciler) setBudgetDegradedCondition(ctx context.Context, budget *kelos.TaskBudget, reason, message string) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(budget), budget); getErr != nil {
			return getErr
		}
		meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
			Type:               "Degraded",
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: budget.Generation,
			LastTransitionTime: metav1.Now(),
		})
		return r.Status().Update(ctx, budget)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to set Degraded condition on TaskBudget", "budget", budget.Name)
	}
}

// clearBudgetDegradedCondition removes the Degraded condition from a TaskBudget
// after a successful evaluation, so operators see current state.
func (r *TaskReconciler) clearBudgetDegradedCondition(ctx context.Context, budget *kelos.TaskBudget) {
	hasCond := false
	for _, c := range budget.Status.Conditions {
		if c.Type == "Degraded" && c.Status == metav1.ConditionTrue {
			hasCond = true
			break
		}
	}
	if !hasCond {
		return
	}

	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(budget), budget); getErr != nil {
			return getErr
		}
		meta.RemoveStatusCondition(&budget.Status.Conditions, "Degraded")
		return r.Status().Update(ctx, budget)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to clear Degraded condition on TaskBudget", "budget", budget.Name)
	}
}

// now returns the current time, using NowFunc if set for testability.
func (r *TaskReconciler) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}
