package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// budgetBlockedMaxRequeue is the maximum requeue delay for budget-blocked tasks.
const budgetBlockedMaxRequeue = 5 * time.Minute

const taskBudgetLabelSnapshotAnnotation = "kelos.dev/taskbudget-labels"

// budgetEnforcer evaluates TaskBudgets and maintains TaskRecords. It is shared
// by the TaskReconciler (Job-backed tasks) and the WorkerPoolReconciler
// (worker-pool tasks) so budget admission and accounting apply uniformly
// regardless of how a Task executes.
type budgetEnforcer struct {
	client.Client
	// now returns the current time; overridable for deterministic tests.
	now func() time.Time
}

// checkBudgetAdmission checks all matching TaskBudgets before job creation.
// Returns (true, _, nil) if admitted, (false, result, nil) if blocked,
// or (false, _, err) on error.
func (e *budgetEnforcer) checkBudgetAdmission(ctx context.Context, task *kelos.Task) (bool, ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var budgetList kelos.TaskBudgetList
	if err := e.List(ctx, &budgetList, client.InNamespace(task.Namespace)); err != nil {
		return false, ctrl.Result{}, err
	}

	if len(budgetList.Items) == 0 {
		// No budgets in namespace — clear any stale BudgetBlocked condition
		e.clearBudgetBlockedCondition(ctx, task)
		return true, ctrl.Result{}, nil
	}

	matchedBudget := false
	for i := range budgetList.Items {
		budget := &budgetList.Items[i]

		selector, err := metav1.LabelSelectorAsSelector(&budget.Spec.TaskSelector)
		if err != nil {
			logger.Error(err, "TaskBudget has invalid selector, blocking task admission", "budget", budget.Name)
			e.setBudgetDegradedCondition(ctx, budget, "InvalidSelector", err.Error())
			return false, ctrl.Result{}, fmt.Errorf("compiling selector for budget %s: %w", budget.Name, err)
		}

		if !selector.Matches(labels.Set(task.Labels)) {
			continue
		}
		matchedBudget = true

		// Budget matches this task — from here on, any operational error must
		// block admission (fail closed).

		periodStart, periodEnd, err := computePeriodBoundaries(budget.Spec.Period, e.now())
		if err != nil {
			logger.Error(err, "TaskBudget has invalid period configuration, blocking task admission", "budget", budget.Name)
			e.setBudgetDegradedCondition(ctx, budget, "InvalidPeriod", err.Error())
			return false, ctrl.Result{}, fmt.Errorf("computing period for budget %s: %w", budget.Name, err)
		}

		// List TaskRecords matching this budget's selector within the current period
		used, err := e.sumPeriodUsage(ctx, task.Namespace, selector, periodStart, periodEnd)
		if err != nil {
			logger.Error(err, "Unable to sum period usage, blocking task", "budget", budget.Name)
			e.setBudgetDegradedCondition(ctx, budget, "ListError", err.Error())
			return false, ctrl.Result{}, fmt.Errorf("summing period usage for budget %s: %w", budget.Name, err)
		}

		// Budget evaluated successfully — clear any stale Degraded condition
		e.clearBudgetDegradedCondition(ctx, budget)

		// Check limits
		exceeded, reason := checkLimitsExceeded(budget, used)
		if exceeded {
			logger.Info("Budget exceeded, blocking task", "budget", budget.Name, "task", task.Name, "reason", reason)

			// Set Waiting phase with BudgetBlocked condition
			e.setBudgetBlockedPhase(ctx, task, budget.Name, reason)

			// Best-effort update TaskBudget status
			e.updateBudgetStatus(ctx, budget, periodStart, periodEnd, used)

			// Requeue after min(5 minutes, time until period end)
			requeueAfter := periodEnd.Sub(e.now())
			if requeueAfter > budgetBlockedMaxRequeue {
				requeueAfter = budgetBlockedMaxRequeue
			}
			if requeueAfter <= 0 {
				requeueAfter = time.Second
			}

			return false, ctrl.Result{RequeueAfter: requeueAfter}, nil
		}

		// Best-effort update TaskBudget status even when not exceeded
		e.updateBudgetStatus(ctx, budget, periodStart, periodEnd, used)
	}

	// All matching budgets are within limits — clear any stale BudgetBlocked condition
	if matchedBudget {
		if err := e.ensureBudgetLabelSnapshot(ctx, task); err != nil {
			return false, ctrl.Result{}, fmt.Errorf("saving budget label snapshot for task %s: %w", task.Name, err)
		}
	}
	e.clearBudgetBlockedCondition(ctx, task)
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
func (e *budgetEnforcer) sumPeriodUsage(ctx context.Context, namespace string, selector labels.Selector, periodStart, periodEnd time.Time) (*kelos.TaskUsage, error) {
	var recordList kelos.TaskRecordList
	if err := e.List(ctx, &recordList,
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
// It skips the status write when the task is already in the desired blocked state,
// so that watch-triggered reconciles do not churn on a stable budget block.
func (e *budgetEnforcer) setBudgetBlockedPhase(ctx context.Context, task *kelos.Task, budgetName, reason string) {
	logger := log.FromContext(ctx)
	wantMessage := fmt.Sprintf("Budget %q exceeded: %s", budgetName, reason)
	wantCondMessage := fmt.Sprintf("Blocked by TaskBudget %q: %s", budgetName, reason)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := e.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		if budgetBlockUnchanged(task, wantMessage, wantCondMessage) {
			return nil
		}
		task.Status.Phase = kelos.TaskPhaseWaiting
		task.Status.Message = wantMessage
		meta.SetStatusCondition(&task.Status.Conditions, metav1.Condition{
			Type:               "BudgetBlocked",
			Status:             metav1.ConditionTrue,
			Reason:             "BudgetExceeded",
			Message:            wantCondMessage,
			ObservedGeneration: task.Generation,
			LastTransitionTime: metav1.Now(),
		})
		return e.Status().Update(ctx, task)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to update Task status to budget-blocked")
	}
}

// budgetBlockUnchanged reports whether the task is already in the desired
// budget-blocked state, so the status write can be skipped.
func budgetBlockUnchanged(task *kelos.Task, wantMessage, wantCondMessage string) bool {
	if task.Status.Phase != kelos.TaskPhaseWaiting || task.Status.Message != wantMessage {
		return false
	}
	cond := meta.FindStatusCondition(task.Status.Conditions, "BudgetBlocked")
	return cond != nil &&
		cond.Status == metav1.ConditionTrue &&
		cond.Reason == "BudgetExceeded" &&
		cond.Message == wantCondMessage &&
		cond.ObservedGeneration == task.Generation
}

// clearBudgetBlockedCondition removes the BudgetBlocked condition if present.
func (e *budgetEnforcer) clearBudgetBlockedCondition(ctx context.Context, task *kelos.Task) {
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
		if getErr := e.Get(ctx, client.ObjectKeyFromObject(task), task); getErr != nil {
			return getErr
		}
		meta.RemoveStatusCondition(&task.Status.Conditions, "BudgetBlocked")
		return e.Status().Update(ctx, task)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to clear BudgetBlocked condition")
	}
}

func (e *budgetEnforcer) ensureBudgetLabelSnapshot(ctx context.Context, task *kelos.Task) error {
	if task.Annotations != nil {
		if snapshot, ok := task.Annotations[taskBudgetLabelSnapshotAnnotation]; ok {
			_, err := decodeTaskBudgetLabelSnapshot(snapshot)
			return err
		}
	}

	snapshot, err := encodeTaskBudgetLabelSnapshot(task.Labels)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest kelos.Task
		if err := e.Get(ctx, client.ObjectKeyFromObject(task), &latest); err != nil {
			return err
		}
		if latest.Annotations != nil {
			if snapshot, ok := latest.Annotations[taskBudgetLabelSnapshotAnnotation]; ok {
				if _, err := decodeTaskBudgetLabelSnapshot(snapshot); err != nil {
					return err
				}
				task.Annotations = latest.Annotations
				return nil
			}
		}

		base := latest.DeepCopy()
		if latest.Annotations == nil {
			latest.Annotations = map[string]string{}
		}
		latest.Annotations[taskBudgetLabelSnapshotAnnotation] = snapshot
		if err := e.Patch(ctx, &latest, client.MergeFrom(base)); err != nil {
			return err
		}
		task.Annotations = latest.Annotations
		return nil
	})
}

func encodeTaskBudgetLabelSnapshot(taskLabels map[string]string) (string, error) {
	data, err := json.Marshal(copyStringMap(taskLabels))
	if err != nil {
		return "", fmt.Errorf("encoding budget label snapshot: %w", err)
	}
	return string(data), nil
}

func taskRecordLabels(task *kelos.Task) (map[string]string, error) {
	if task.Annotations != nil {
		if snapshot, ok := task.Annotations[taskBudgetLabelSnapshotAnnotation]; ok {
			taskLabels, err := decodeTaskBudgetLabelSnapshot(snapshot)
			if err != nil {
				return nil, err
			}
			return copyStringMap(taskLabels), nil
		}
	}
	return copyStringMap(task.Labels), nil
}

func decodeTaskBudgetLabelSnapshot(snapshot string) (map[string]string, error) {
	var taskLabels map[string]string
	if err := json.Unmarshal([]byte(snapshot), &taskLabels); err != nil {
		return nil, fmt.Errorf("decoding budget label snapshot: %w", err)
	}
	return taskLabels, nil
}

func copyStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// updateBudgetStatus best-effort updates the TaskBudget status with current
// period boundaries and accumulated usage. It skips the write when the status is
// already current, so watch-driven reconciles do not churn on stable state.
func (e *budgetEnforcer) updateBudgetStatus(ctx context.Context, budget *kelos.TaskBudget, periodStart, periodEnd time.Time, used *kelos.TaskUsage) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := e.Get(ctx, client.ObjectKeyFromObject(budget), budget); getErr != nil {
			return getErr
		}
		if budgetStatusCurrent(budget, periodStart, periodEnd, used) {
			return nil
		}
		budget.Status.ObservedGeneration = budget.Generation
		start := metav1.NewTime(periodStart)
		end := metav1.NewTime(periodEnd)
		budget.Status.CurrentPeriodStart = &start
		budget.Status.CurrentPeriodEnd = &end
		budget.Status.Used = used
		return e.Status().Update(ctx, budget)
	})
	if updateErr != nil {
		logger.V(1).Info("Unable to update TaskBudget status", "budget", budget.Name, "error", updateErr)
	}
}

// budgetStatusCurrent reports whether the budget status already reflects the
// given period boundaries and usage, so the status write can be skipped.
func budgetStatusCurrent(budget *kelos.TaskBudget, periodStart, periodEnd time.Time, used *kelos.TaskUsage) bool {
	return budget.Status.ObservedGeneration == budget.Generation &&
		metav1TimeEqual(budget.Status.CurrentPeriodStart, periodStart) &&
		metav1TimeEqual(budget.Status.CurrentPeriodEnd, periodEnd) &&
		usageEqual(budget.Status.Used, used)
}

// metav1TimeEqual reports whether a *metav1.Time equals a time.Time instant.
func metav1TimeEqual(a *metav1.Time, b time.Time) bool {
	return a != nil && a.Time.Equal(b)
}

// usageEqual reports whether two TaskUsage values are semantically equal.
// CostUSD is compared with resource.Quantity.Cmp so equivalent quantities
// (e.g. "1000m" and "1") are treated as equal.
func usageEqual(a, b *kelos.TaskUsage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if (a.CostUSD == nil) != (b.CostUSD == nil) {
		return false
	}
	if a.CostUSD != nil && a.CostUSD.Cmp(*b.CostUSD) != 0 {
		return false
	}
	return int64PtrEqual(a.InputTokens, b.InputTokens) &&
		int64PtrEqual(a.OutputTokens, b.OutputTokens)
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// setBudgetDegradedCondition sets a Degraded condition on the TaskBudget so
// operators can observe configuration or operational errors.
func (e *budgetEnforcer) setBudgetDegradedCondition(ctx context.Context, budget *kelos.TaskBudget, reason, message string) {
	logger := log.FromContext(ctx)
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if getErr := e.Get(ctx, client.ObjectKeyFromObject(budget), budget); getErr != nil {
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
		return e.Status().Update(ctx, budget)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to set Degraded condition on TaskBudget", "budget", budget.Name)
	}
}

// clearBudgetDegradedCondition removes the Degraded condition from a TaskBudget
// after a successful evaluation, so operators see current state.
func (e *budgetEnforcer) clearBudgetDegradedCondition(ctx context.Context, budget *kelos.TaskBudget) {
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
		if getErr := e.Get(ctx, client.ObjectKeyFromObject(budget), budget); getErr != nil {
			return getErr
		}
		meta.RemoveStatusCondition(&budget.Status.Conditions, "Degraded")
		return e.Status().Update(ctx, budget)
	})
	if updateErr != nil {
		logger.Error(updateErr, "Unable to clear Degraded condition on TaskBudget", "budget", budget.Name)
	}
}

// createTaskRecord creates an immutable TaskRecord for a completed Task.
// It is idempotent: an AlreadyExists error is treated as success so the call
// can be retried safely on subsequent reconciles. Non-AlreadyExists errors are
// returned to trigger a requeue.
func (e *budgetEnforcer) createTaskRecord(ctx context.Context, task *kelos.Task) error {
	logger := log.FromContext(ctx)

	if task.Status.Usage == nil {
		return nil
	}
	if task.Status.CompletionTime == nil {
		return fmt.Errorf("task %s has usage but no completionTime", task.Name)
	}

	recordLabels, err := taskRecordLabels(task)
	if err != nil {
		return err
	}
	workerType, workerModel, err := e.taskRecordWorkerMetadata(ctx, task)
	if err != nil {
		return err
	}

	ttl := defaultTaskRecordTTL
	record := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      string(task.UID),
			Namespace: task.Namespace,
			Labels:    recordLabels,
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef: kelos.TaskReference{
				Name: task.Name,
				UID:  task.UID,
			},
			Type:                      workerType,
			Model:                     workerModel,
			Phase:                     task.Status.Phase,
			StartTime:                 task.Status.StartTime,
			CompletionTime:            task.Status.CompletionTime,
			Usage:                     task.Status.Usage.DeepCopy(),
			TTLSecondsAfterCompletion: &ttl,
		},
	}

	if err := e.Create(ctx, record); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		logger.Error(err, "Unable to create TaskRecord", "task", task.Name)
		return err
	}
	return nil
}

func (e *budgetEnforcer) taskRecordWorkerMetadata(ctx context.Context, task *kelos.Task) (string, string, error) {
	if task.Spec.WorkerPoolRef == nil {
		return resolveTaskType(task), resolveTaskModel(task), nil
	}

	var pool kelos.WorkerPool
	if err := e.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Spec.WorkerPoolRef.Name}, &pool); err != nil {
		return "", "", fmt.Errorf("getting WorkerPool %s for task %s: %w", task.Spec.WorkerPoolRef.Name, task.Name, err)
	}
	return pool.Spec.Worker.Type, pool.Spec.Worker.Model, nil
}
