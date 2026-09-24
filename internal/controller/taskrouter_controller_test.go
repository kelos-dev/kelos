package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/router"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

const testRouterUID = types.UID("router-uid")

func testTaskRouter(fallback *kelos.RouterFallback) *kelos.TaskRouter {
	return &kelos.TaskRouter{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "support",
			Namespace:  "default",
			UID:        testRouterUID,
			Generation: 1,
		},
		Spec: kelos.TaskRouterSpec{
			When: kelos.RouterWhen{Slack: &kelos.Slack{Channels: []string{"C0123456789"}}},
			Decision: kelos.RouterDecision{
				Worker: &kelos.WorkerSpec{
					Type:        "claude-code",
					Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
				},
			},
			Prompt: "Route it",
			Routes: []kelos.RouterRoute{
				{
					Name:        "docs",
					Description: "Documentation questions",
					TargetRef:   kelos.RouterTargetRef{Kind: kelos.RouterTargetKindTaskSpawner, Name: "docs-agent"},
				},
				{
					Name:        "code",
					Description: "Code changes",
					TargetRef:   kelos.RouterTargetRef{Kind: kelos.RouterTargetKindTaskSpawner, Name: "code-agent"},
				},
			},
			Fallback: fallback,
		},
	}
}

func testTargetSpawner(name string) *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec: kelos.TaskSpawnerSpec{
			TriggerMode: kelos.TriggerModeOnDemand,
			TaskTemplate: kelos.TaskTemplate{
				Worker: &kelos.WorkerSpec{
					Type:        "claude-code",
					Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
				},
				PromptTemplate: "Handle {{.Title}}",
			},
		},
	}
}

// testDecisionTask returns a completed decision Task owned by the router,
// carrying the request and the initiator's Slack reporting metadata.
func testDecisionTask(name, route string, phase kelos.TaskPhase) *kelos.Task {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{router.LabelTaskRouter: "support"},
			Annotations: map[string]string{
				router.AnnotationRequest:          `{"ID":"1712.0001","Title":"Docs are stale","Kind":"SlackMessage"}`,
				router.AnnotationItemID:           "1712.0001",
				router.AnnotationDispatchSuffix:   "slack-abcdef123456",
				reporting.AnnotationSlackChannel:  "C0123456789",
				reporting.AnnotationSlackThreadTS: "1712.0001",
				reporting.AnnotationSlackUserID:   "U0123456789",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: kelos.GroupVersion.String(),
				Kind:       "TaskRouter",
				Name:       "support",
				UID:        testRouterUID,
				Controller: ptr.To(true),
			}},
		},
		Spec: kelos.TaskSpec{
			Prompt: "Route it",
			Worker: &kelos.WorkerSpec{Type: "claude-code", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}},
		},
	}
	task.Status.Phase = phase
	task.Status.CompletionTime = &metav1.Time{Time: metav1.Now().Time}
	if route != "" {
		task.Status.Results = map[string]string{router.ResultRoute: route}
	}
	return task
}

func testTaskRouterReconciler(t *testing.T, objects ...client.Object) (*TaskRouterReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskRouter{}, &kelos.Task{}).
		WithObjects(objects...).
		Build()
	builder, err := taskbuilder.NewTaskBuilder(k8sClient)
	if err != nil {
		t.Fatal(err)
	}
	return &TaskRouterReconciler{Client: k8sClient, Scheme: scheme, TaskBuilder: builder}, k8sClient
}

func reconcileTaskRouter(t *testing.T, reconciler *TaskRouterReconciler, taskRouter *kelos.TaskRouter) {
	t.Helper()
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(taskRouter)}); err != nil {
		t.Fatal(err)
	}
}

func getTask(t *testing.T, k8sClient client.Client, name string) *kelos.Task {
	t.Helper()
	var task kelos.Task
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &task); err != nil {
		t.Fatal(err)
	}
	return &task
}

func TestTaskRouterDispatchesToSelectedRoute(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	decision := testDecisionTask("support-decide-1", "docs", kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	dispatched := getTask(t, k8sClient, "docs-agent-slack-abcdef123456")

	if dispatched.Spec.Prompt != "Handle Docs are stale" {
		t.Errorf("dispatched prompt = %q, want the target spawner's template rendered with the request", dispatched.Spec.Prompt)
	}
	if got := dispatched.Labels["kelos.dev/taskspawner"]; got != "docs-agent" {
		t.Errorf("dispatched taskspawner label = %q, want docs-agent", got)
	}
	for key, want := range map[string]string{
		reporting.AnnotationSlackReporting: "enabled",
		reporting.AnnotationSlackChannel:   "C0123456789",
		reporting.AnnotationSlackThreadTS:  "1712.0001",
		reporting.AnnotationSlackUserID:    "U0123456789",
	} {
		if got := dispatched.Annotations[key]; got != want {
			t.Errorf("dispatched annotation %s = %q, want %q", key, got, want)
		}
	}
	if got := dispatched.Labels[reporting.LabelSlackReporting]; got != "enabled" {
		t.Errorf("dispatched slack reporting label = %q, want enabled", got)
	}

	handled := getTask(t, k8sClient, "support-decide-1")
	if got := handled.Annotations[router.AnnotationOutcome]; got != "docs-agent-slack-abcdef123456" {
		t.Errorf("decision outcome = %q, want the dispatched Task name", got)
	}
	// Read the persisted decision: a routed request is narrated by the work it
	// dispatched, so the decision Task must never become reportable.
	if got := handled.Annotations[reporting.AnnotationSlackReporting]; got != "" {
		t.Errorf("persisted decision slack reporting = %q, want it to stay silent", got)
	}
	if _, ok := handled.Labels[reporting.LabelSlackReporting]; ok {
		t.Error("persisted decision carries the reporting label, want it to stay silent")
	}

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.TaskRouterPhaseRunning {
		t.Errorf("phase = %q, want Running", updated.Status.Phase)
	}
	if updated.Status.RecordedDecisions != 1 || updated.Status.DispatchedDecisions != 1 {
		t.Errorf("decision counts = %d/%d, want 1/1", updated.Status.RecordedDecisions, updated.Status.DispatchedDecisions)
	}
	if updated.Status.LastDecision == nil || updated.Status.LastDecision.Route != "docs" {
		t.Errorf("lastDecision = %+v, want route docs", updated.Status.LastDecision)
	}
	if updated.Status.LastDecision.DispatchedTask != "docs-agent-slack-abcdef123456" {
		t.Errorf("lastDecision dispatchedTask = %q", updated.Status.LastDecision.DispatchedTask)
	}
}

func TestTaskRouterDispatchesOnlyOnce(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	decision := testDecisionTask("support-decide-1", "docs", kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)
	// A second pass must not create a second Task for the same request.
	reconcileTaskRouter(t, reconciler, taskRouter)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 2 {
		t.Fatalf("task count = %d, want 2 (one decision, one dispatch)", len(tasks.Items))
	}
}

func TestTaskRouterFallsBackToNamedRoute(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionRoute, Route: "code"})
	decision := testDecisionTask("support-decide-1", router.NoRoute, kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	dispatched := getTask(t, k8sClient, "code-agent-slack-abcdef123456")
	if dispatched.Spec.Prompt != "Handle Docs are stale" {
		t.Errorf("fallback dispatched prompt = %q", dispatched.Spec.Prompt)
	}

	// Status reports the route actually used, not the "none" the decision gave.
	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.LastDecision == nil || updated.Status.LastDecision.Route != "code" {
		t.Errorf("lastDecision = %+v, want the fallback route code", updated.Status.LastDecision)
	}
}

// One bad route out of several is reported on the condition, but the router
// keeps routing: the ingress still feeds it, and a request selecting a working
// route still dispatches, so Failed would misdescribe it.
func TestTaskRouterKeepsRunningWithOneUnresolvableRoute(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	// Only one of the two route targets exists, and no request has arrived.
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"))

	reconcileTaskRouter(t, reconciler, taskRouter)

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.TaskRouterPhaseRunning {
		t.Errorf("phase = %q, want Running while another route still resolves", updated.Status.Phase)
	}
	condition := apiMeta.FindStatusCondition(updated.Status.Conditions, TaskRouterConditionRoutesResolved)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("RoutesResolved = %+v, want False", condition)
	}
	if !strings.Contains(condition.Message, "code-agent") {
		t.Errorf("condition message = %q, want it to name the missing target", condition.Message)
	}
}

func TestTaskRouterFailsWhenNoRouteResolves(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter)

	reconcileTaskRouter(t, reconciler, taskRouter)

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.TaskRouterPhaseFailed {
		t.Errorf("phase = %q, want Failed when nothing resolves", updated.Status.Phase)
	}
	condition := apiMeta.FindStatusCondition(updated.Status.Conditions, TaskRouterConditionRoutesResolved)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("RoutesResolved = %+v, want False", condition)
	}
	// Both bad routes are named in one pass, not just the first.
	for _, want := range []string{"docs-agent", "code-agent"} {
		if !strings.Contains(condition.Message, want) {
			t.Errorf("condition message = %q, want it to name %s", condition.Message, want)
		}
	}
}

func TestTaskRouterResolvedRoutesReportTrue(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"))

	reconcileTaskRouter(t, reconciler, taskRouter)

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	condition := apiMeta.FindStatusCondition(updated.Status.Conditions, TaskRouterConditionRoutesResolved)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("RoutesResolved = %+v, want True", condition)
	}
	if updated.Status.Phase != kelos.TaskRouterPhaseRunning {
		t.Errorf("phase = %q, want Running", updated.Status.Phase)
	}
}

func TestTaskRouterFallsBackForAnUnknownRoute(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionRoute, Route: "code"})
	decision := testDecisionTask("support-decide-1", "not-a-route", kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("code-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	getTask(t, k8sClient, "code-agent-slack-abcdef123456")
}

func TestTaskRouterFallsBackForAFailedDecision(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionRoute, Route: "code"})
	decision := testDecisionTask("support-decide-1", "docs", kelos.TaskPhaseFailed)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("code-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	getTask(t, k8sClient, "code-agent-slack-abcdef123456")
}

func TestTaskRouterReplyDispatchesNothing(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionReply})
	decision := testDecisionTask("support-decide-1", router.NoRoute, kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want only the decision Task", len(tasks.Items))
	}
	if got := getTask(t, k8sClient, "support-decide-1").Annotations[router.AnnotationOutcome]; got != router.OutcomeReplied {
		t.Errorf("outcome = %q, want %q", got, router.OutcomeReplied)
	}
}

// A Reply router that routes successfully must still stay silent: the gate is
// what the decision did, not how the router is configured.
func TestTaskRouterReplyRouterStaysSilentWhenItDispatches(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionReply})
	decision := testDecisionTask("support-decide-1", "docs", kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	handled := getTask(t, k8sClient, "support-decide-1")
	if got := handled.Annotations[reporting.AnnotationSlackReporting]; got != "" {
		t.Errorf("slack reporting = %q, want silence when the decision dispatched", got)
	}
	if _, ok := handled.Labels[reporting.LabelSlackReporting]; ok {
		t.Error("reporting label set on a decision that dispatched")
	}

	// The dispatched Task is the one that answers.
	dispatched := getTask(t, k8sClient, "docs-agent-slack-abcdef123456")
	if got := dispatched.Annotations[reporting.AnnotationSlackReporting]; got != "enabled" {
		t.Errorf("dispatched slack reporting = %q, want enabled", got)
	}
}

func TestTaskRouterReplyMakesTheDecisionReportable(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionReply})
	decision := testDecisionTask("support-decide-1", router.NoRoute, kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	handled := getTask(t, k8sClient, "support-decide-1")
	if got := handled.Annotations[reporting.AnnotationSlackReporting]; got != "enabled" {
		t.Errorf("slack reporting = %q, want enabled so the reply reaches the thread", got)
	}
	if got := handled.Labels[reporting.LabelSlackReporting]; got != "enabled" {
		t.Errorf("reporting label = %q, want enabled so the reporting cycle lists it", got)
	}
}

// A slash command has no thread to answer in, and the reporter discards such a
// Task, so labelling it would only make the cycle list work it drops.
func TestTaskRouterReplyToASlashCommandIsNotLabelled(t *testing.T) {
	taskRouter := testTaskRouter(&kelos.RouterFallback{Action: kelos.RouterFallbackActionReply})
	decision := testDecisionTask("support-decide-1", router.NoRoute, kelos.TaskPhaseSucceeded)
	delete(decision.Annotations, reporting.AnnotationSlackThreadTS)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	handled := getTask(t, k8sClient, "support-decide-1")
	if _, ok := handled.Labels[reporting.LabelSlackReporting]; ok {
		t.Error("reporting label set for a decision with no thread to answer in")
	}
}

func TestTaskRouterFailsWithoutAFallback(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	decision := testDecisionTask("support-decide-1", router.NoRoute, kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	if got := getTask(t, k8sClient, "support-decide-1").Annotations[router.AnnotationOutcome]; got != router.OutcomeFailed {
		t.Errorf("outcome = %q, want %q", got, router.OutcomeFailed)
	}

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.DispatchedDecisions != 0 {
		t.Errorf("dispatchedDecisions = %d, want 0", updated.Status.DispatchedDecisions)
	}
}

func TestTaskRouterIgnoresRunningDecisions(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	decision := testDecisionTask("support-decide-1", "", kelos.TaskPhaseRunning)
	decision.Status.CompletionTime = nil
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want only the in-flight decision", len(tasks.Items))
	}
}

func TestTaskRouterReportsAnUnresolvableTarget(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	decision := testDecisionTask("support-decide-1", "docs", kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, decision)

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(taskRouter)}); err == nil {
		t.Fatal("expected an error when the route target does not exist")
	}

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.TaskRouterPhaseFailed {
		t.Errorf("phase = %q, want Failed", updated.Status.Phase)
	}
	if got := getTask(t, k8sClient, "support-decide-1").Annotations[router.AnnotationOutcome]; got != "" {
		t.Errorf("outcome = %q, want the decision left unhandled for a retry", got)
	}
}

func TestTaskRouterSuspendedReportsSuspended(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	taskRouter.Spec.Suspend = ptr.To(true)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"))

	reconcileTaskRouter(t, reconciler, taskRouter)

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.TaskRouterPhaseSuspended {
		t.Errorf("phase = %q, want Suspended", updated.Status.Phase)
	}
}

func TestTaskRouterRejectsAnUnsupportedTargetKind(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	taskRouter.Spec.Routes[0].TargetRef.Kind = "TaskPipeline"
	decision := testDecisionTask("support-decide-1", "docs", kelos.TaskPhaseSucceeded)
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs"), decision)

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(taskRouter)}); err == nil {
		t.Fatal("expected an error for a target kind the controller cannot dispatch to")
	}

	var tasks kelos.TaskList
	if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want nothing dispatched", len(tasks.Items))
	}
}

// testRouterWithTTL returns a router that collects handled decisions after ttl.
func testRouterWithTTL(ttl int32) *kelos.TaskRouter {
	taskRouter := testTaskRouter(nil)
	taskRouter.Spec.Decision.TTLSecondsAfterHandled = ptr.To(ttl)
	return taskRouter
}

// agedDecision returns a decision Task that completed completedAgo in the past.
func agedDecision(name, route string, completedAgo time.Duration, outcome string) *kelos.Task {
	decision := testDecisionTask(name, route, kelos.TaskPhaseSucceeded)
	decision.Status.CompletionTime = &metav1.Time{Time: time.Now().Add(-completedAgo)}
	if outcome != "" {
		decision.Annotations[router.AnnotationOutcome] = outcome
		decision.Annotations[router.AnnotationRoute] = route
	}
	return decision
}

func taskExists(t *testing.T, k8sClient client.Client, name string) bool {
	t.Helper()
	var task kelos.Task
	err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &task)
	if err == nil {
		return true
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("getting Task %s: %v", name, err)
	}
	return false
}

func TestTaskRouterCollectsHandledDecisionsPastTheTTL(t *testing.T) {
	taskRouter := testRouterWithTTL(3600)
	expired := agedDecision("support-decide-old", "docs", 2*time.Hour, "docs-agent-slack-old")
	fresh := agedDecision("support-decide-new", "docs", time.Minute, "docs-agent-slack-new")
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), expired, fresh)

	reconcileTaskRouter(t, reconciler, taskRouter)

	if taskExists(t, k8sClient, "support-decide-old") {
		t.Error("a handled decision past its TTL was retained")
	}
	if !taskExists(t, k8sClient, "support-decide-new") {
		t.Error("a handled decision within its TTL was collected")
	}

	var updated kelos.TaskRouter
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(taskRouter), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.RecordedDecisions != 1 {
		t.Errorf("recordedDecisions = %d, want 1 after the sweep", updated.Status.RecordedDecisions)
	}
}

// The TTL must never drop a request the router has not answered, however long
// the controller was down.
func TestTaskRouterKeepsUnhandledDecisionsPastTheTTL(t *testing.T) {
	taskRouter := testRouterWithTTL(1)
	unhandled := agedDecision("support-decide-1", "", 30*24*time.Hour, "")
	unhandled.Status.Phase = kelos.TaskPhaseRunning
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), unhandled)

	reconcileTaskRouter(t, reconciler, taskRouter)

	if !taskExists(t, k8sClient, "support-decide-1") {
		t.Fatal("an unhandled decision was collected, dropping the request it carries")
	}
}

// A decision that completed long ago — a controller outage, say — is dispatched
// first and only then collected, in the same pass. The work outlives it.
func TestTaskRouterDispatchesBeforeCollectingAStaleDecision(t *testing.T) {
	taskRouter := testRouterWithTTL(60)
	decision := agedDecision("support-decide-1", "docs", time.Hour, "")
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), decision)

	reconcileTaskRouter(t, reconciler, taskRouter)

	if !taskExists(t, k8sClient, "docs-agent-slack-abcdef123456") {
		t.Fatal("the stale decision was collected without dispatching its work")
	}
	if taskExists(t, k8sClient, "support-decide-1") {
		t.Error("a decision past its TTL survived the sweep that followed handling it")
	}
}

func TestTaskRouterRetainsDecisionsWithoutATTL(t *testing.T) {
	taskRouter := testTaskRouter(nil)
	taskRouter.Spec.Decision.TTLSecondsAfterHandled = nil
	old := agedDecision("support-decide-1", "docs", 30*24*time.Hour, "docs-agent-slack-old")
	reconciler, k8sClient := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), old)

	reconcileTaskRouter(t, reconciler, taskRouter)

	if !taskExists(t, k8sClient, "support-decide-1") {
		t.Error("a decision was collected although no TTL is configured")
	}
}

func TestTaskRouterRequeuesForTheNextExpiry(t *testing.T) {
	taskRouter := testRouterWithTTL(3600)
	fresh := agedDecision("support-decide-1", "docs", time.Minute, "docs-agent-slack-new")
	reconciler, _ := testTaskRouterReconciler(t, taskRouter, testTargetSpawner("docs-agent"), testTargetSpawner("code-agent"), fresh)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(taskRouter)})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatal("no requeue scheduled, so the sweep would wait for the next request")
	}
	if result.RequeueAfter > time.Hour {
		t.Errorf("requeueAfter = %s, want no later than the TTL", result.RequeueAfter)
	}
}
