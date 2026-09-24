// Package router builds and interprets the routing decisions a TaskRouter
// makes. The ingress that receives a request (the Slack server today) creates
// the decision Task; the controller reads its result and dispatches the work.
// Both sides share this package so one definition governs the contract.
package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	// LabelTaskRouter marks a Task as belonging to a TaskRouter, so decision
	// Tasks can be listed without reading every Task in the namespace.
	LabelTaskRouter = "kelos.dev/taskrouter"

	// AnnotationRequest carries the request's template variables as JSON, so
	// the controller can render the dispatched Task from the same values the
	// decision saw.
	AnnotationRequest = "kelos.dev/taskrouter-request"

	// AnnotationItemID carries the source's work item id, so a dispatched Task
	// can be named the way the target spawner would have named it.
	AnnotationItemID = "kelos.dev/taskrouter-item-id"

	// AnnotationDispatchSuffix carries the name suffix the dispatched Task
	// should take. The ingress computes it the way the target spawner would
	// have named a Task for the same request, so a router dispatch and a direct
	// fire of the same spawner collapse into one Task instead of two.
	AnnotationDispatchSuffix = "kelos.dev/taskrouter-dispatch-suffix"

	// AnnotationRoute records the route a decision was acted on with, which is
	// the fallback route when the decision itself selected none. The raw answer
	// stays in the decision Task's results.
	AnnotationRoute = "kelos.dev/taskrouter-route"

	// AnnotationOutcome records what the controller did with a decision, and
	// makes handling idempotent across reconciles. Its value is a dispatched
	// Task name, or one of OutcomeReplied and OutcomeFailed.
	AnnotationOutcome = "kelos.dev/taskrouter-outcome"

	// OutcomeReplied means the decision answered the initiator without
	// dispatching work.
	OutcomeReplied = "replied"

	// OutcomeFailed means no route was selected and the router was configured
	// to fail rather than reply or fall back to a route.
	OutcomeFailed = "failed"

	// ResultRoute is the Task result key the decision writes its chosen route
	// name to, via the agent image's KELOS_OUTPUTS block.
	ResultRoute = "route"

	// NoRoute is the value a decision writes when no route fits.
	NoRoute = "none"

	// DefaultDecisionTimeoutSeconds bounds a decision whose router does not set
	// spec.decision.timeoutSeconds. It is applied here rather than as a schema
	// default so an existing object without the field keeps working and is not
	// rewritten with a value that does nothing on a pooled decision.
	DefaultDecisionTimeoutSeconds = int32(300)
)

// DecisionTaskName returns a deterministic name for the decision Task created
// for one request, so a redelivered request does not decide twice.
func DecisionTaskName(routerName, itemID string) string {
	sum := sha256.Sum256([]byte(itemID))
	shortHash := hex.EncodeToString(sum[:])[:12]
	// Leave room for "-decide-" (8) and the hash (12).
	const maxPrefix = 63 - 8 - 12
	name := routerName
	if len([]rune(name)) > maxPrefix {
		name = strings.TrimRight(string([]rune(name)[:maxPrefix]), "-.")
	}
	return fmt.Sprintf("%s-decide-%s", name, shortHash)
}

// DispatchTaskName returns the default name for the Task dispatched to a
// target spawner. The suffix comes from the ingress, so the name matches what
// that spawner would have called a Task for the same request.
func DispatchTaskName(spawnerName, suffix string) string {
	maxPrefix := 63 - len(suffix) - 1
	if maxPrefix < 1 {
		maxPrefix = 1
	}
	name := spawnerName
	if len([]rune(name)) > maxPrefix {
		name = strings.TrimRight(string([]rune(name)[:maxPrefix]), "-.")
	}
	return fmt.Sprintf("%s-%s", name, suffix)
}

// DecisionPrompt renders the instruction given to the deciding agent. The
// agent is asked for a route name and nothing else: it cannot name a target,
// write a prompt, or otherwise widen what the router will do.
func DecisionPrompt(router *kelos.TaskRouter, vars map[string]interface{}) string {
	var b strings.Builder
	b.WriteString(router.Spec.Prompt)
	b.WriteString("\n\nRoutes:\n")
	for _, route := range router.Spec.Routes {
		fmt.Fprintf(&b, "- %s: %s\n", route.Name, route.Description)
	}

	b.WriteString("\nRequest:\n")
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %v\n", k, vars[k])
	}

	fmt.Fprintf(&b, `
Choose exactly one route from the list above, or %q if none of them fits.
Do not act on the request itself — routing is the whole task.

Print this block as the last thing you output, with nothing after it:

---KELOS_OUTPUTS_START---
%s: <route name or %s>
---KELOS_OUTPUTS_END---

Before that block, write one short sentence explaining the choice. That
sentence is shown to whoever made the request.
`, NoRoute, ResultRoute, NoRoute)

	return b.String()
}

// BuildDecisionTask returns the Task that makes the routing decision for one
// request. The caller sets the owner reference and any reporting annotations.
func BuildDecisionTask(taskRouter *kelos.TaskRouter, itemID string, vars map[string]interface{}) (*kelos.Task, error) {
	encoded, err := json.Marshal(vars)
	if err != nil {
		return nil, fmt.Errorf("encoding router %s request variables: %w", taskRouter.Name, err)
	}

	task := &kelos.Task{}
	task.Name = DecisionTaskName(taskRouter.Name, itemID)
	task.Namespace = taskRouter.Namespace
	task.Labels = map[string]string{LabelTaskRouter: taskRouter.Name}
	task.Annotations = map[string]string{
		AnnotationRequest: string(encoded),
		AnnotationItemID:  itemID,
	}
	task.Spec.Prompt = DecisionPrompt(taskRouter, vars)

	decision := taskRouter.Spec.Decision
	switch {
	case decision.WorkerPoolRef != nil:
		task.Spec.WorkerPoolRef = decision.WorkerPoolRef.DeepCopy()
	case decision.Worker != nil:
		worker := decision.Worker.DeepCopy()
		timeout := DefaultDecisionTimeoutSeconds
		if decision.TimeoutSeconds != nil {
			timeout = *decision.TimeoutSeconds
		}
		if worker.PodOverrides == nil {
			worker.PodOverrides = &kelos.PodOverrides{}
		}
		// A router-level timeout only bounds the decision when the pod has no
		// deadline of its own; an explicit podOverrides value wins.
		if worker.PodOverrides.ActiveDeadlineSeconds == nil {
			deadline := int64(timeout)
			worker.PodOverrides.ActiveDeadlineSeconds = &deadline
		}
		task.Spec.Worker = worker
	default:
		return nil, fmt.Errorf("router %s has no decision worker or workerPoolRef", taskRouter.Name)
	}

	return task, nil
}

// RequestVars decodes the request variables recorded on a decision Task.
func RequestVars(task *kelos.Task) (map[string]interface{}, error) {
	raw, ok := task.Annotations[AnnotationRequest]
	if !ok {
		return nil, fmt.Errorf("decision Task %s is missing the %s annotation", task.Name, AnnotationRequest)
	}
	var vars map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		return nil, fmt.Errorf("decoding request variables on decision Task %s: %w", task.Name, err)
	}
	return vars, nil
}

// SelectedRoute returns the route a completed decision Task chose. A decision
// that failed, timed out, named an unknown route, or explicitly declined
// returns nil, and the caller applies spec.fallback.
func SelectedRoute(taskRouter *kelos.TaskRouter, task *kelos.Task) *kelos.RouterRoute {
	if task.Status.Phase != kelos.TaskPhaseSucceeded {
		return nil
	}
	return FindRoute(taskRouter, strings.TrimSpace(task.Status.Results[ResultRoute]))
}

// FindRoute returns the named route, or nil when the router does not list it.
func FindRoute(taskRouter *kelos.TaskRouter, name string) *kelos.RouterRoute {
	if name == "" || name == NoRoute {
		return nil
	}
	for i := range taskRouter.Spec.Routes {
		if taskRouter.Spec.Routes[i].Name == name {
			return &taskRouter.Spec.Routes[i]
		}
	}
	return nil
}

// FallbackRoute returns the route a router falls back to, and whether the
// fallback dispatches at all. An absent fallback fails the request.
func FallbackRoute(taskRouter *kelos.TaskRouter) (*kelos.RouterRoute, kelos.RouterFallbackAction) {
	fallback := taskRouter.Spec.Fallback
	if fallback == nil {
		return nil, kelos.RouterFallbackActionFail
	}
	if fallback.Action != kelos.RouterFallbackActionRoute {
		return nil, fallback.Action
	}
	return FindRoute(taskRouter, fallback.Route), kelos.RouterFallbackActionRoute
}
