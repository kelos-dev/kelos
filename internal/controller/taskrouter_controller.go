package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/router"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// TaskRouterConditionRoutesResolved reports whether every route target exists.
const TaskRouterConditionRoutesResolved = "RoutesResolved"

// TaskRouterConditionDispatchSucceeded reports whether the most recent attempt
// to dispatch a decision succeeded. It is kept separate from RoutesResolved so
// a Task create conflict or a template failure does not present itself as a
// missing route target, and does not overwrite one.
const TaskRouterConditionDispatchSucceeded = "DispatchSucceeded"

// reportingAnnotationsToCopy are carried from a decision Task to the Task it
// dispatches, so the existing reporter answers in the thread or comment the
// request came from.
var reportingAnnotationsToCopy = []string{
	reporting.AnnotationSlackChannel,
	reporting.AnnotationSlackThreadTS,
	reporting.AnnotationSlackUserID,
	reporting.AnnotationGitHubReporting,
	reporting.AnnotationSourceKind,
	reporting.AnnotationSourceOwner,
	reporting.AnnotationSourceRepo,
	reporting.AnnotationSourceNumber,
	reporting.AnnotationSourceSHA,
}

// TaskRouterReconciler dispatches the work a TaskRouter's decisions select.
type TaskRouterReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	TaskBuilder *taskbuilder.TaskBuilder

	// Now is the clock the decision sweep reads. Tests substitute it; when nil
	// it is time.Now.
	Now func() time.Time
}

// now returns the reconciler's clock.
func (r *TaskRouterReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=kelos.dev,resources=taskrouters,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskrouters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskspawners,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=create;get;list;watch;update;patch

// Reconcile acts on completed routing decisions and aggregates router status.
func (r *TaskRouterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var taskRouter kelos.TaskRouter
	if err := r.Get(ctx, req.NamespacedName, &taskRouter); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	decisions, err := r.listDecisionTasks(ctx, &taskRouter)
	if err != nil {
		return ctrl.Result{}, err
	}

	var dispatchErr error
	for _, decision := range decisions {
		if decision.Annotations[router.AnnotationOutcome] != "" {
			continue
		}
		if !isTerminalTaskPhase(decision.Status.Phase) {
			continue
		}
		if err := r.handleDecision(ctx, &taskRouter, decision); err != nil {
			// One unresolvable route must not stall the others; remember the
			// error and keep going, then requeue.
			logger.Error(err, "Failed to act on routing decision", "taskRouter", taskRouter.Name, "decision", decision.Name)
			dispatchErr = err
			continue
		}
	}

	// Sweeping after the handling pass is what makes the TTL safe: a decision is
	// only ever collected once an outcome has been recorded for it.
	retained, requeueAfter, err := r.sweepHandledDecisions(ctx, &taskRouter, decisions)
	if err != nil {
		return ctrl.Result{}, err
	}
	decisions = retained

	resolution, err := r.resolveRoutes(ctx, &taskRouter)
	if err != nil {
		return ctrl.Result{}, err
	}
	if routesErr := resolution.Err(); routesErr != nil {
		logger.Info("TaskRouter has unresolvable routes", "taskRouter", taskRouter.Name, "reason", routesErr.Error())
	}

	if err := r.updateStatus(ctx, req.NamespacedName, &taskRouter, decisions, resolution, dispatchErr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, dispatchErr
}

// sweepHandledDecisions deletes decision Tasks the router has acted on and that
// have outlived spec.decision.ttlSecondsAfterHandled. It returns the decisions
// that remain, and how long until the next one expires so the sweep runs again
// without waiting for another request.
func (r *TaskRouterReconciler) sweepHandledDecisions(ctx context.Context, taskRouter *kelos.TaskRouter, decisions []*kelos.Task) ([]*kelos.Task, time.Duration, error) {
	logger := log.FromContext(ctx)

	ttl := taskRouter.Spec.Decision.TTLSecondsAfterHandled
	if ttl == nil {
		return decisions, 0, nil
	}
	window := time.Duration(*ttl) * time.Second
	now := r.now()

	retained := make([]*kelos.Task, 0, len(decisions))
	var nextExpiry time.Duration
	for _, decision := range decisions {
		// An unhandled decision is never collected, however old: the request it
		// carries has not been answered yet.
		if decision.Annotations[router.AnnotationOutcome] == "" {
			retained = append(retained, decision)
			continue
		}
		// A handled decision always has a completion time, except for one
		// handled while still running, which nothing produces today. Keep it
		// rather than guess an age for it.
		if decision.Status.CompletionTime == nil {
			retained = append(retained, decision)
			continue
		}

		if remaining := decision.Status.CompletionTime.Add(window).Sub(now); remaining > 0 {
			retained = append(retained, decision)
			if nextExpiry == 0 || remaining < nextExpiry {
				nextExpiry = remaining
			}
			continue
		}

		if err := r.Delete(ctx, decision); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, 0, fmt.Errorf("deleting expired decision Task %s for TaskRouter %s: %w", decision.Name, taskRouter.Name, err)
		}
		logger.Info("Collected expired routing decision", "taskRouter", taskRouter.Name, "decision", decision.Name)
	}

	return retained, nextExpiry, nil
}

// routeResolution summarizes which of a router's routes can be dispatched to.
type routeResolution struct {
	// Unresolvable describes each route that cannot be dispatched to, in the
	// order the routes are listed.
	Unresolvable []string
	// Total is how many routes the router lists.
	Total int
}

// AllUnresolvable reports whether no route can be dispatched to, which is the
// only case where the router cannot serve any request at all.
func (r routeResolution) AllUnresolvable() bool {
	return r.Total > 0 && len(r.Unresolvable) == r.Total
}

// Err returns the reason routes could not be resolved, or nil when every route
// resolves.
func (r routeResolution) Err() error {
	if len(r.Unresolvable) == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d routes cannot be dispatched to: %s", len(r.Unresolvable), r.Total, strings.Join(r.Unresolvable, "; "))
}

// resolveRoutes checks every route target, so a router that points at a missing
// TaskSpawner says so before the first request rather than after. Every bad
// route is reported, not just the first, so one reconcile names them all.
func (r *TaskRouterReconciler) resolveRoutes(ctx context.Context, taskRouter *kelos.TaskRouter) (routeResolution, error) {
	resolution := routeResolution{Total: len(taskRouter.Spec.Routes)}
	for _, route := range taskRouter.Spec.Routes {
		if route.TargetRef.Kind != kelos.RouterTargetKindTaskSpawner {
			resolution.Unresolvable = append(resolution.Unresolvable,
				fmt.Sprintf("route %s targets unsupported kind %s", route.Name, route.TargetRef.Kind))
			continue
		}
		key := client.ObjectKey{Namespace: taskRouter.Namespace, Name: route.TargetRef.Name}
		var spawner kelos.TaskSpawner
		if err := r.Get(ctx, key, &spawner); err != nil {
			if !apierrors.IsNotFound(err) {
				// A read failure is not a verdict on the route, so it stops the
				// reconcile rather than being reported as an unresolvable route.
				return resolution, fmt.Errorf("resolving route %q target for TaskRouter %s: %w", route.Name, taskRouter.Name, err)
			}
			resolution.Unresolvable = append(resolution.Unresolvable,
				fmt.Sprintf("route %s targets TaskSpawner %s, which does not exist", route.Name, route.TargetRef.Name))
		}
	}
	return resolution, nil
}

// listDecisionTasks returns the decision Tasks this router owns, oldest first.
func (r *TaskRouterReconciler) listDecisionTasks(ctx context.Context, taskRouter *kelos.TaskRouter) ([]*kelos.Task, error) {
	var tasks kelos.TaskList
	if err := r.List(ctx, &tasks,
		client.InNamespace(taskRouter.Namespace),
		client.MatchingLabels{router.LabelTaskRouter: taskRouter.Name},
	); err != nil {
		return nil, fmt.Errorf("listing decision Tasks for TaskRouter %s: %w", taskRouter.Name, err)
	}

	owned := make([]*kelos.Task, 0, len(tasks.Items))
	for i := range tasks.Items {
		task := &tasks.Items[i]
		// The label can match a Task left behind by a since-recreated router of
		// the same name; only Tasks this router owns are its decisions.
		if !metav1.IsControlledBy(task, taskRouter) {
			continue
		}
		owned = append(owned, task)
	}
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].CreationTimestamp.Before(&owned[j].CreationTimestamp)
	})
	return owned, nil
}

// handleDecision dispatches, replies, or fails one completed decision, and
// records what it did on the decision Task so a later reconcile does not repeat it.
func (r *TaskRouterReconciler) handleDecision(ctx context.Context, taskRouter *kelos.TaskRouter, decision *kelos.Task) error {
	logger := log.FromContext(ctx)

	route := router.SelectedRoute(taskRouter, decision)
	outcome := ""
	if route == nil {
		fallbackRoute, action := router.FallbackRoute(taskRouter)
		switch action {
		case kelos.RouterFallbackActionRoute:
			if fallbackRoute == nil {
				// Validation keeps fallback.route pointing at a listed route, so
				// this means the routes changed under a decision already in flight.
				return fmt.Errorf("TaskRouter %s fallback route %q is not listed in routes", taskRouter.Name, taskRouter.Spec.Fallback.Route)
			}
			route = fallbackRoute
		case kelos.RouterFallbackActionReply:
			outcome = router.OutcomeReplied
		default:
			outcome = router.OutcomeFailed
		}
	}

	effectiveRoute := ""
	if route != nil {
		dispatched, err := r.dispatch(ctx, taskRouter, decision, route)
		if err != nil {
			return err
		}
		outcome = dispatched
		effectiveRoute = route.Name
		logger.Info("Dispatched routed request", "taskRouter", taskRouter.Name, "route", route.Name, "task", dispatched)
	}

	return r.recordOutcome(ctx, decision, outcome, effectiveRoute)
}

// dispatch creates the Task the selected route's TaskSpawner would have created
// for this request, and returns its name.
func (r *TaskRouterReconciler) dispatch(ctx context.Context, taskRouter *kelos.TaskRouter, decision *kelos.Task, route *kelos.RouterRoute) (string, error) {
	if route.TargetRef.Kind != kelos.RouterTargetKindTaskSpawner {
		return "", fmt.Errorf("TaskRouter %s route %q targets unsupported kind %q", taskRouter.Name, route.Name, route.TargetRef.Kind)
	}

	var spawner kelos.TaskSpawner
	key := client.ObjectKey{Namespace: taskRouter.Namespace, Name: route.TargetRef.Name}
	if err := r.Get(ctx, key, &spawner); err != nil {
		return "", fmt.Errorf("resolving route %q target TaskSpawner %s for TaskRouter %s: %w", route.Name, route.TargetRef.Name, taskRouter.Name, err)
	}

	vars, err := router.RequestVars(decision)
	if err != nil {
		return "", err
	}

	suffix := decision.Annotations[router.AnnotationDispatchSuffix]
	if suffix == "" {
		return "", fmt.Errorf("decision Task %s is missing the %s annotation", decision.Name, router.AnnotationDispatchSuffix)
	}
	defaultName := router.DispatchTaskName(spawner.Name, suffix)
	taskName, err := taskbuilder.ResolveTaskName(defaultName, &spawner.Spec.TaskTemplate, vars)
	if err != nil {
		return "", fmt.Errorf("resolving dispatched Task name for TaskRouter %s route %q: %w", taskRouter.Name, route.Name, err)
	}

	gvks, _, err := r.Scheme.ObjectKinds(&spawner)
	if err != nil || len(gvks) == 0 {
		return "", fmt.Errorf("resolving TaskSpawner %s GVK: %w", spawner.Name, err)
	}
	gvk := gvks[0]

	task, err := r.TaskBuilder.BuildTask(taskName, spawner.Namespace, &spawner.Spec.TaskTemplate, vars, &taskbuilder.SpawnerRef{
		Name:       spawner.Name,
		UID:        string(spawner.UID),
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
	})
	if err != nil {
		return "", fmt.Errorf("building dispatched Task for TaskRouter %s route %q: %w", taskRouter.Name, route.Name, err)
	}
	if err := r.TaskBuilder.AssignSpawnerCredential(&spawner, task); err != nil {
		return "", fmt.Errorf("assigning TaskSpawner %s credential for TaskRouter %s: %w", spawner.Name, taskRouter.Name, err)
	}

	copyReportingMetadata(decision, task)

	if err := r.Create(ctx, task); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The target spawner may have fired directly on the same request.
			// One Task for one request is the intent, so treat the existing
			// Task as this dispatch when the spawner owns it.
			existing := &kelos.Task{}
			if getErr := r.Get(ctx, client.ObjectKeyFromObject(task), existing); getErr != nil {
				return "", fmt.Errorf("reading existing Task %s after create conflict: %w", task.Name, getErr)
			}
			if taskbuilder.TaskBelongsToSpawner(existing, spawner.Name, spawner.UID) {
				return task.Name, nil
			}
			return "", fmt.Errorf("dispatched Task name %s collides with a Task not owned by TaskSpawner %s", task.Name, spawner.Name)
		}
		return "", fmt.Errorf("creating dispatched Task %s for TaskRouter %s: %w", task.Name, taskRouter.Name, err)
	}
	return task.Name, nil
}

// copyReportingMetadata carries the initiator's address from a decision Task
// onto the Task it dispatches, and enables reporting there. The decision Task
// only reports when the router is configured to reply, so enabling it here is
// what makes the dispatched work answer in the originating thread.
func copyReportingMetadata(decision, task *kelos.Task) {
	setAnnotation := func(key, value string) {
		if task.Annotations == nil {
			task.Annotations = map[string]string{}
		}
		task.Annotations[key] = value
	}

	for _, key := range reportingAnnotationsToCopy {
		if value, ok := decision.Annotations[key]; ok {
			setAnnotation(key, value)
		}
	}

	if decision.Annotations[reporting.AnnotationSlackChannel] != "" {
		setAnnotation(reporting.AnnotationSlackReporting, "enabled")
		// The reporting cycle lists on this label, and only a real message
		// timestamp gives it a thread to answer in. Slash commands have none,
		// matching how a Slack-sourced TaskSpawner treats them.
		if decision.Annotations[reporting.AnnotationSlackThreadTS] != "" {
			if task.Labels == nil {
				task.Labels = map[string]string{}
			}
			task.Labels[reporting.LabelSlackReporting] = "enabled"
		}
	}
}

// recordOutcome marks a decision Task as handled, and mirrors the outcome onto
// the caller's copy so status is computed from what was just persisted.
func (r *TaskRouterReconciler) recordOutcome(ctx context.Context, decision *kelos.Task, outcome, route string) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest kelos.Task
		if err := r.Get(ctx, client.ObjectKeyFromObject(decision), &latest); err != nil {
			return err
		}
		if latest.Annotations[router.AnnotationOutcome] != "" {
			return nil
		}
		if latest.Annotations == nil {
			latest.Annotations = map[string]string{}
		}
		latest.Annotations[router.AnnotationOutcome] = outcome
		if route != "" {
			latest.Annotations[router.AnnotationRoute] = route
		}
		if outcome == router.OutcomeReplied {
			enableDecisionReporting(&latest)
		}
		return r.Update(ctx, &latest)
	})
	if err != nil {
		return err
	}
	if decision.Annotations == nil {
		decision.Annotations = map[string]string{}
	}
	decision.Annotations[router.AnnotationOutcome] = outcome
	if route != "" {
		decision.Annotations[router.AnnotationRoute] = route
	}
	if outcome == router.OutcomeReplied {
		enableDecisionReporting(decision)
	}
	return nil
}

// enableDecisionReporting lets the reporter post a decision Task's own output.
// It is applied only to a decision that answered the initiator instead of
// dispatching, because that output is then the whole answer. The reporting
// cycle lists on the label, so a decision without it never posts at all — not
// even an acknowledgement while it runs.
func enableDecisionReporting(decision *kelos.Task) {
	if decision.Annotations[reporting.AnnotationSlackChannel] == "" {
		return
	}
	decision.Annotations[reporting.AnnotationSlackReporting] = "enabled"
	// A slash command carries no thread to answer in, and the reporter skips a
	// Task without one, so the label would only make it list work it discards.
	if decision.Annotations[reporting.AnnotationSlackThreadTS] == "" {
		return
	}
	if decision.Labels == nil {
		decision.Labels = map[string]string{}
	}
	decision.Labels[reporting.LabelSlackReporting] = "enabled"
}

// updateStatus recomputes router status from the decision Tasks on the API server.
func (r *TaskRouterReconciler) updateStatus(ctx context.Context, key client.ObjectKey, taskRouter *kelos.TaskRouter, decisions []*kelos.Task, resolution routeResolution, dispatchErr error) error {
	status := kelos.TaskRouterStatus{
		ObservedGeneration: taskRouter.Generation,
		Phase:              kelos.TaskRouterPhaseRunning,
		RecordedDecisions:  int32(len(decisions)),
	}
	if taskRouter.Spec.Suspend != nil && *taskRouter.Spec.Suspend {
		status.Phase = kelos.TaskRouterPhaseSuspended
	}

	for _, decision := range decisions {
		outcome := decision.Annotations[router.AnnotationOutcome]
		if outcome == "" || outcome == router.OutcomeReplied || outcome == router.OutcomeFailed {
			continue
		}
		status.DispatchedDecisions++
	}

	if last := latestHandledDecision(decisions); last != nil {
		status.LastDecision = &kelos.RouterLastDecision{
			Route: last.Annotations[router.AnnotationRoute],
			Time:  last.Status.CompletionTime,
		}
		if outcome := last.Annotations[router.AnnotationOutcome]; outcome != router.OutcomeReplied && outcome != router.OutcomeFailed {
			status.LastDecision.DispatchedTask = outcome
		}
	}

	resolved := metav1.Condition{
		Type:               TaskRouterConditionRoutesResolved,
		Status:             metav1.ConditionTrue,
		Reason:             "RoutesResolved",
		Message:            "Every route target resolved",
		ObservedGeneration: taskRouter.Generation,
	}
	if routesErr := resolution.Err(); routesErr != nil {
		resolved.Status = metav1.ConditionFalse
		resolved.Reason = "RouteUnresolved"
		resolved.Message = routesErr.Error()
		// A router with one bad route out of several still serves every other
		// route, and the ingress keeps feeding it, so only a router that can
		// dispatch nowhere is Failed. The condition carries the rest.
		if resolution.AllUnresolvable() {
			status.Phase = kelos.TaskRouterPhaseFailed
		}
	}

	dispatched := metav1.Condition{
		Type:               TaskRouterConditionDispatchSucceeded,
		Status:             metav1.ConditionTrue,
		Reason:             "DispatchSucceeded",
		Message:            "No dispatch has failed",
		ObservedGeneration: taskRouter.Generation,
	}
	if dispatchErr != nil {
		status.Phase = kelos.TaskRouterPhaseFailed
		dispatched.Status = metav1.ConditionFalse
		dispatched.Reason = "DispatchFailed"
		dispatched.Message = dispatchErr.Error()
	}
	if taskRouter.Spec.Suspend != nil && *taskRouter.Spec.Suspend {
		// Suspension is what the operator asked for, so it outranks a router
		// that also has a route it cannot resolve.
		status.Phase = kelos.TaskRouterPhaseSuspended
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest kelos.TaskRouter
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		conditions := latest.Status.Conditions
		latest.Status = status
		latest.Status.Conditions = conditions
		apiMeta.SetStatusCondition(&latest.Status.Conditions, resolved)
		apiMeta.SetStatusCondition(&latest.Status.Conditions, dispatched)
		return r.Status().Update(ctx, &latest)
	})
}

// latestHandledDecision returns the most recently completed handled decision.
func latestHandledDecision(decisions []*kelos.Task) *kelos.Task {
	var last *kelos.Task
	for _, decision := range decisions {
		if decision.Annotations[router.AnnotationOutcome] == "" || decision.Status.CompletionTime == nil {
			continue
		}
		if last == nil || last.Status.CompletionTime.Before(decision.Status.CompletionTime) {
			last = decision
		}
	}
	return last
}

// SetupWithManager registers the controller with the manager.
func (r *TaskRouterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.TaskRouter{}).
		Owns(&kelos.Task{}).
		Complete(r)
}
