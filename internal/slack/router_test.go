package slack

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/router"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

func testRouterScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func slackTaskRouter() *kelos.TaskRouter {
	return &kelos.TaskRouter{
		ObjectMeta: metav1.ObjectMeta{Name: "support", Namespace: "default", UID: "router-uid"},
		Spec: kelos.TaskRouterSpec{
			When:   kelos.RouterWhen{Slack: &kelos.Slack{Channels: []string{"C456"}}},
			Prompt: "Route it",
			Decision: kelos.RouterDecision{
				Worker: &kelos.WorkerSpec{
					Type:        "claude-code",
					Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
				},
				TimeoutSeconds: ptr.To(int32(120)),
			},
			Routes: []kelos.RouterRoute{{
				Name:        "docs",
				Description: "Documentation questions",
				TargetRef:   kelos.RouterTargetRef{Kind: kelos.RouterTargetKindTaskSpawner, Name: "docs-agent"},
			}},
		},
	}
}

func slackRouterHandler(t *testing.T, objects ...*kelos.TaskRouter) (*SlackHandler, func() kelos.TaskList) {
	t.Helper()
	scheme := testRouterScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, object := range objects {
		builder = builder.WithObjects(object.DeepCopy())
	}
	cl := builder.Build()

	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}
	handler := &SlackHandler{client: cl, log: logr.Discard(), taskBuilder: tb, botUserID: "UBOT"}

	return handler, func() kelos.TaskList {
		var tasks kelos.TaskList
		if err := cl.List(context.Background(), &tasks); err != nil {
			t.Fatalf("List tasks: %v", err)
		}
		return tasks
	}
}

func slackRouterMessage() *SlackMessageData {
	return &SlackMessageData{
		UserID:    "U123",
		ChannelID: "C456",
		Text:      "<@UBOT> the docs are stale",
		Body:      "<@UBOT> the docs are stale",
		Timestamp: "1111111111.111111",
	}
}

func TestRouteToRoutersCreatesDecisionTask(t *testing.T) {
	handler, listTasks := slackRouterHandler(t, slackTaskRouter())

	handler.routeToRouters(context.Background(), slackRouterMessage())

	tasks := listTasks()
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1 decision Task", len(tasks.Items))
	}
	task := tasks.Items[0]

	if got := task.Labels[router.LabelTaskRouter]; got != "support" {
		t.Errorf("router label = %q, want support", got)
	}
	if task.Annotations[router.AnnotationItemID] != "1111111111.111111" {
		t.Errorf("item id annotation = %q", task.Annotations[router.AnnotationItemID])
	}
	if !strings.HasPrefix(task.Annotations[router.AnnotationDispatchSuffix], "slack-") {
		t.Errorf("dispatch suffix = %q, want a slack- prefix", task.Annotations[router.AnnotationDispatchSuffix])
	}
	if !strings.Contains(task.Spec.Prompt, "docs: Documentation questions") {
		t.Errorf("prompt is missing the route menu:\n%s", task.Spec.Prompt)
	}
	if !strings.Contains(task.Spec.Prompt, "the docs are stale") {
		t.Errorf("prompt is missing the request:\n%s", task.Spec.Prompt)
	}
	if task.Spec.Worker == nil || task.Spec.Worker.Type != "claude-code" {
		t.Errorf("decision worker = %+v, want the router's decision worker", task.Spec.Worker)
	}
	if task.Spec.Worker.PodOverrides == nil || task.Spec.Worker.PodOverrides.ActiveDeadlineSeconds == nil {
		t.Fatal("decision timeout was not applied to the pod")
	}
	if got := *task.Spec.Worker.PodOverrides.ActiveDeadlineSeconds; got != 120 {
		t.Errorf("activeDeadlineSeconds = %d, want 120", got)
	}

	for key, want := range map[string]string{
		reporting.AnnotationSlackChannel:  "C456",
		reporting.AnnotationSlackThreadTS: "1111111111.111111",
		reporting.AnnotationSlackUserID:   "U123",
	} {
		if got := task.Annotations[key]; got != want {
			t.Errorf("annotation %s = %q, want %q", key, got, want)
		}
	}
	// A decision never posts while it runs: it carries the address so the
	// dispatched Task can inherit it, and the router adds the reporting label
	// afterwards only when the decision answered instead of dispatching.
	if got := task.Annotations[reporting.AnnotationSlackReporting]; got != "" {
		t.Errorf("slack reporting annotation = %q, want the decision to stay silent", got)
	}
	if _, ok := task.Labels[reporting.LabelSlackReporting]; ok {
		t.Error("slack reporting label set, want the decision to stay silent")
	}
	if len(task.OwnerReferences) != 1 || task.OwnerReferences[0].Kind != "TaskRouter" {
		t.Errorf("owner references = %+v, want the TaskRouter", task.OwnerReferences)
	}
}

func TestRouteToRoutersDeduplicatesRedelivery(t *testing.T) {
	handler, listTasks := slackRouterHandler(t, slackTaskRouter())

	handler.routeToRouters(context.Background(), slackRouterMessage())
	handler.routeToRouters(context.Background(), slackRouterMessage())

	if tasks := listTasks(); len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1 after a redelivery", len(tasks.Items))
	}
}

func TestRouteToRoutersSkipsSuspendedRouter(t *testing.T) {
	taskRouter := slackTaskRouter()
	taskRouter.Spec.Suspend = ptr.To(true)
	handler, listTasks := slackRouterHandler(t, taskRouter)

	handler.routeToRouters(context.Background(), slackRouterMessage())

	if tasks := listTasks(); len(tasks.Items) != 0 {
		t.Fatalf("task count = %d, want none for a suspended router", len(tasks.Items))
	}
}

func TestRouteToRoutersSkipsNonMatchingChannel(t *testing.T) {
	handler, listTasks := slackRouterHandler(t, slackTaskRouter())

	msg := slackRouterMessage()
	msg.ChannelID = "C999"
	handler.routeToRouters(context.Background(), msg)

	if tasks := listTasks(); len(tasks.Items) != 0 {
		t.Fatalf("task count = %d, want none outside the router's channels", len(tasks.Items))
	}
}

func TestRouteToRoutersHonorsMaxConcurrency(t *testing.T) {
	taskRouter := slackTaskRouter()
	taskRouter.Spec.MaxConcurrency = ptr.To(int32(1))
	handler, listTasks := slackRouterHandler(t, taskRouter)

	handler.routeToRouters(context.Background(), slackRouterMessage())

	second := slackRouterMessage()
	second.Timestamp = "2222222222.222222"
	handler.routeToRouters(context.Background(), second)

	if tasks := listTasks(); len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1 while a decision is in flight", len(tasks.Items))
	}
}

// TestRouteMessageSkipsOnDemandSpawner covers the spawner side of the same
// message path: a spawner that has opted out of its own source is reachable
// only by explicit dispatch, even when its Slack filters match.
func TestRouteMessageSkipsOnDemandSpawner(t *testing.T) {
	scheme := testRouterScheme(t)
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "docs-agent", Namespace: "default", UID: "spawner-uid"},
		Spec: kelos.TaskSpawnerSpec{
			TriggerMode: kelos.TriggerModeOnDemand,
			When:        kelos.When{Slack: &kelos.Slack{}},
			TaskTemplate: kelos.TaskTemplate{
				Type:           "claude-code",
				Credentials:    &kelos.Credentials{Type: kelos.CredentialTypeNone},
				PromptTemplate: "{{.Body}}",
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(spawner).Build()
	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}
	handler := &SlackHandler{client: cl, log: logr.Discard(), taskBuilder: tb, botUserID: "UBOT"}

	handler.routeMessage(context.Background(), slackRouterMessage())

	var tasks kelos.TaskList
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(tasks.Items) != 0 {
		t.Fatalf("task count = %d, want none for an OnDemand spawner", len(tasks.Items))
	}
}

// Even a Reply router's decision is created silent: whether it speaks depends on
// what it decides, which the ingress cannot know yet.
func TestRouteToRoutersCreatesAReplyRouterDecisionSilent(t *testing.T) {
	taskRouter := slackTaskRouter()
	taskRouter.Spec.Fallback = &kelos.RouterFallback{Action: kelos.RouterFallbackActionReply}
	handler, listTasks := slackRouterHandler(t, taskRouter)

	handler.routeToRouters(context.Background(), slackRouterMessage())

	tasks := listTasks()
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks.Items))
	}
	task := tasks.Items[0]
	if got := task.Annotations[reporting.AnnotationSlackReporting]; got != "" {
		t.Errorf("slack reporting annotation = %q, want it deferred to the outcome", got)
	}
	if _, ok := task.Labels[reporting.LabelSlackReporting]; ok {
		t.Error("slack reporting label set at creation, want it deferred to the outcome")
	}
	// The address is still carried, or neither the reply nor the dispatched
	// Task would know where to answer.
	if got := task.Annotations[reporting.AnnotationSlackThreadTS]; got != "1111111111.111111" {
		t.Errorf("thread timestamp = %q, want the originating thread", got)
	}
}

func TestRouteToRoutersSkipsThreadTimestampForSlashCommands(t *testing.T) {
	handler, listTasks := slackRouterHandler(t, slackTaskRouter())

	msg := slackRouterMessage()
	msg.IsSlashCommand = true
	msg.SlashCommandID = "C456:/ask:trigger-1"
	handler.routeToRouters(context.Background(), msg)

	tasks := listTasks()
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks.Items))
	}
	// A slash command has no thread to answer in, matching how a Slack-sourced
	// TaskSpawner treats one.
	if _, ok := tasks.Items[0].Annotations[reporting.AnnotationSlackThreadTS]; ok {
		t.Error("thread timestamp set for a slash command, want it skipped")
	}
}

func TestRouteToRoutersAppliesTheDefaultDecisionTimeout(t *testing.T) {
	taskRouter := slackTaskRouter()
	taskRouter.Spec.Decision.TimeoutSeconds = nil
	handler, listTasks := slackRouterHandler(t, taskRouter)

	handler.routeToRouters(context.Background(), slackRouterMessage())

	tasks := listTasks()
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks.Items))
	}
	overrides := tasks.Items[0].Spec.Worker.PodOverrides
	if overrides == nil || overrides.ActiveDeadlineSeconds == nil {
		t.Fatal("a decision with no configured timeout still needs a deadline")
	}
	if got := *overrides.ActiveDeadlineSeconds; got != int64(router.DefaultDecisionTimeoutSeconds) {
		t.Errorf("activeDeadlineSeconds = %d, want the %d second default", got, router.DefaultDecisionTimeoutSeconds)
	}
}

func TestRouteToRoutersKeepsAnExplicitPodDeadline(t *testing.T) {
	taskRouter := slackTaskRouter()
	taskRouter.Spec.Decision.Worker.PodOverrides = &kelos.PodOverrides{ActiveDeadlineSeconds: ptr.To(int64(45))}
	handler, listTasks := slackRouterHandler(t, taskRouter)

	handler.routeToRouters(context.Background(), slackRouterMessage())

	tasks := listTasks()
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks.Items))
	}
	if got := *tasks.Items[0].Spec.Worker.PodOverrides.ActiveDeadlineSeconds; got != 45 {
		t.Errorf("activeDeadlineSeconds = %d, want the pod override to win over the router timeout", got)
	}
}

func TestRouteToRoutersLeavesAPooledDecisionUnbounded(t *testing.T) {
	taskRouter := slackTaskRouter()
	taskRouter.Spec.Decision = kelos.RouterDecision{
		WorkerPoolRef:  &kelos.WorkerPoolReference{Name: "router-pool"},
		TimeoutSeconds: ptr.To(int32(120)),
	}
	handler, listTasks := slackRouterHandler(t, taskRouter)

	handler.routeToRouters(context.Background(), slackRouterMessage())

	tasks := listTasks()
	if len(tasks.Items) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks.Items))
	}
	// A pooled decision runs on a pod the pool owns, so there is nothing here
	// to carry the deadline. The field documents that it does not apply.
	if tasks.Items[0].Spec.Worker != nil {
		t.Errorf("worker = %+v, want the pool reference alone", tasks.Items[0].Spec.Worker)
	}
	if tasks.Items[0].Spec.WorkerPoolRef == nil {
		t.Error("pooled decision lost its workerPoolRef")
	}
}
