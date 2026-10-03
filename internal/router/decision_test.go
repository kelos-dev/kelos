package router

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func testRouter() *kelos.TaskRouter {
	return &kelos.TaskRouter{
		ObjectMeta: metav1.ObjectMeta{Name: "support", Namespace: "default"},
		Spec: kelos.TaskRouterSpec{
			When:   kelos.RouterWhen{Slack: &kelos.Slack{}},
			Prompt: "Route it",
			Decision: kelos.RouterDecision{
				Worker: &kelos.WorkerSpec{
					Type:        "claude-code",
					Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
				},
			},
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
		},
	}
}

func testVars() map[string]interface{} {
	return map[string]interface{}{"ID": "1712.0001", "Title": "Docs are stale", "Kind": "SlackMessage"}
}

func decisionDeadline(t *testing.T, task *kelos.Task) *int64 {
	t.Helper()
	if task.Spec.Worker == nil || task.Spec.Worker.PodOverrides == nil {
		return nil
	}
	return task.Spec.Worker.PodOverrides.ActiveDeadlineSeconds
}

// TestBuildDecisionTaskAppliesDefaultTimeout pins the 300 second bound as code
// behavior. It is deliberately not a schema default: a schema default would be
// written into every stored object, including pooled decisions where it does
// nothing, and would appear on objects created before the field existed.
func TestBuildDecisionTaskAppliesDefaultTimeout(t *testing.T) {
	task, err := BuildDecisionTask(testRouter(), "1712.0001", testVars())
	if err != nil {
		t.Fatalf("BuildDecisionTask: %v", err)
	}

	deadline := decisionDeadline(t, task)
	if deadline == nil {
		t.Fatal("no activeDeadlineSeconds applied, want the default bound")
	}
	if *deadline != int64(DefaultDecisionTimeoutSeconds) {
		t.Errorf("activeDeadlineSeconds = %d, want %d", *deadline, DefaultDecisionTimeoutSeconds)
	}
}

func TestBuildDecisionTaskPrefersConfiguredTimeout(t *testing.T) {
	taskRouter := testRouter()
	taskRouter.Spec.Decision.TimeoutSeconds = ptr.To(int32(120))

	task, err := BuildDecisionTask(taskRouter, "1712.0001", testVars())
	if err != nil {
		t.Fatalf("BuildDecisionTask: %v", err)
	}

	if deadline := decisionDeadline(t, task); deadline == nil || *deadline != 120 {
		t.Errorf("activeDeadlineSeconds = %v, want 120", deadline)
	}
}

func TestBuildDecisionTaskLeavesAnExplicitPodDeadlineAlone(t *testing.T) {
	taskRouter := testRouter()
	taskRouter.Spec.Decision.TimeoutSeconds = ptr.To(int32(120))
	taskRouter.Spec.Decision.Worker.PodOverrides = &kelos.PodOverrides{
		ActiveDeadlineSeconds: ptr.To(int64(45)),
	}

	task, err := BuildDecisionTask(taskRouter, "1712.0001", testVars())
	if err != nil {
		t.Fatalf("BuildDecisionTask: %v", err)
	}

	if deadline := decisionDeadline(t, task); deadline == nil || *deadline != 45 {
		t.Errorf("activeDeadlineSeconds = %v, want the pod's own 45", deadline)
	}
}

func TestBuildDecisionTaskDoesNotMutateTheRouter(t *testing.T) {
	taskRouter := testRouter()
	if _, err := BuildDecisionTask(taskRouter, "1712.0001", testVars()); err != nil {
		t.Fatalf("BuildDecisionTask: %v", err)
	}

	if taskRouter.Spec.Decision.Worker.PodOverrides != nil {
		t.Error("the router's own worker gained podOverrides, want the spec left untouched")
	}
}

// A pooled decision runs on a pod the pool owns, so the router applies no
// deadline to it. Validation rejects the combination before it reaches here,
// which this constructs directly to pin the builder's own behavior.
func TestBuildDecisionTaskPooledDecisionTakesNoDeadline(t *testing.T) {
	taskRouter := testRouter()
	taskRouter.Spec.Decision = kelos.RouterDecision{
		WorkerPoolRef:  &kelos.WorkerPoolReference{Name: "router-pool"},
		TimeoutSeconds: ptr.To(int32(120)),
	}

	task, err := BuildDecisionTask(taskRouter, "1712.0001", testVars())
	if err != nil {
		t.Fatalf("BuildDecisionTask: %v", err)
	}

	if task.Spec.WorkerPoolRef == nil || task.Spec.WorkerPoolRef.Name != "router-pool" {
		t.Errorf("workerPoolRef = %+v, want router-pool", task.Spec.WorkerPoolRef)
	}
	if task.Spec.Worker != nil {
		t.Errorf("worker = %+v, want none alongside a pool reference", task.Spec.Worker)
	}
}

func TestBuildDecisionTaskRequiresAnExecutionSource(t *testing.T) {
	taskRouter := testRouter()
	taskRouter.Spec.Decision = kelos.RouterDecision{}

	if _, err := BuildDecisionTask(taskRouter, "1712.0001", testVars()); err == nil {
		t.Fatal("expected an error when the decision has neither worker nor workerPoolRef")
	}
}

func TestDecisionPromptCarriesRoutesAndRequest(t *testing.T) {
	prompt := DecisionPrompt(testRouter(), testVars())

	for _, want := range []string{
		"Route it",
		"- docs: Documentation questions",
		"- code: Code changes",
		"Title: Docs are stale",
		"---KELOS_OUTPUTS_START---",
		"route: <route name or none>",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// The route menu is the whole vocabulary: a target name must never reach the
// deciding agent, or a prompt injection could aim at something unlisted.
func TestDecisionPromptOmitsTargetNames(t *testing.T) {
	prompt := DecisionPrompt(testRouter(), testVars())

	for _, absent := range []string{"docs-agent", "code-agent", "TaskSpawner"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("prompt leaks the dispatch target %q:\n%s", absent, prompt)
		}
	}
}

func TestSelectedRoute(t *testing.T) {
	tests := []struct {
		name   string
		phase  kelos.TaskPhase
		result string
		want   string
	}{
		{name: "listed route", phase: kelos.TaskPhaseSucceeded, result: "docs", want: "docs"},
		{name: "surrounding whitespace", phase: kelos.TaskPhaseSucceeded, result: " docs\n", want: "docs"},
		{name: "declined", phase: kelos.TaskPhaseSucceeded, result: NoRoute},
		{name: "unlisted name", phase: kelos.TaskPhaseSucceeded, result: "deploy-to-prod"},
		{name: "no answer", phase: kelos.TaskPhaseSucceeded},
		{name: "failed decision", phase: kelos.TaskPhaseFailed, result: "docs"},
		{name: "still running", phase: kelos.TaskPhaseRunning, result: "docs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{}
			task.Status.Phase = tt.phase
			if tt.result != "" {
				task.Status.Results = map[string]string{ResultRoute: tt.result}
			}

			route := SelectedRoute(testRouter(), task)
			switch {
			case tt.want == "" && route != nil:
				t.Errorf("SelectedRoute() = %q, want no route", route.Name)
			case tt.want != "" && route == nil:
				t.Errorf("SelectedRoute() = nil, want %q", tt.want)
			case tt.want != "" && route.Name != tt.want:
				t.Errorf("SelectedRoute() = %q, want %q", route.Name, tt.want)
			}
		})
	}
}

func TestFallbackRoute(t *testing.T) {
	tests := []struct {
		name       string
		fallback   *kelos.RouterFallback
		wantAction kelos.RouterFallbackAction
		wantRoute  string
	}{
		{name: "unset fails", fallback: nil, wantAction: kelos.RouterFallbackActionFail},
		{
			name:       "reply",
			fallback:   &kelos.RouterFallback{Action: kelos.RouterFallbackActionReply},
			wantAction: kelos.RouterFallbackActionReply,
		},
		{
			name:       "named route",
			fallback:   &kelos.RouterFallback{Action: kelos.RouterFallbackActionRoute, Route: "code"},
			wantAction: kelos.RouterFallbackActionRoute,
			wantRoute:  "code",
		},
		{
			name:       "route that is no longer listed",
			fallback:   &kelos.RouterFallback{Action: kelos.RouterFallbackActionRoute, Route: "removed"},
			wantAction: kelos.RouterFallbackActionRoute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRouter := testRouter()
			taskRouter.Spec.Fallback = tt.fallback

			route, action := FallbackRoute(taskRouter)
			if action != tt.wantAction {
				t.Errorf("action = %q, want %q", action, tt.wantAction)
			}
			switch {
			case tt.wantRoute == "" && route != nil:
				t.Errorf("route = %q, want none", route.Name)
			case tt.wantRoute != "" && (route == nil || route.Name != tt.wantRoute):
				t.Errorf("route = %+v, want %q", route, tt.wantRoute)
			}
		})
	}
}

func TestDecisionTaskNameIsDeterministicAndBounded(t *testing.T) {
	first := DecisionTaskName("support", "1712.0001")
	if first != DecisionTaskName("support", "1712.0001") {
		t.Error("DecisionTaskName is not deterministic, so a redelivery would decide twice")
	}
	if same := DecisionTaskName("support", "1712.0002"); same == first {
		t.Error("different requests produced the same decision Task name")
	}

	long := DecisionTaskName(strings.Repeat("a", 120), "1712.0001")
	if len(long) > 63 {
		t.Errorf("name length = %d, want at most 63", len(long))
	}
}

func TestDispatchTaskNameIsBounded(t *testing.T) {
	name := DispatchTaskName(strings.Repeat("a", 120), "slack-abcdef123456")
	if len(name) > 63 {
		t.Errorf("name length = %d, want at most 63", len(name))
	}
	if !strings.HasSuffix(name, "-slack-abcdef123456") {
		t.Errorf("name = %q, want the ingress suffix preserved", name)
	}
}

func TestRequestVarsRoundTrip(t *testing.T) {
	task, err := BuildDecisionTask(testRouter(), "1712.0001", testVars())
	if err != nil {
		t.Fatalf("BuildDecisionTask: %v", err)
	}

	vars, err := RequestVars(task)
	if err != nil {
		t.Fatalf("RequestVars: %v", err)
	}
	if got := vars["Title"]; got != "Docs are stale" {
		t.Errorf("Title = %v, want the request value", got)
	}

	delete(task.Annotations, AnnotationRequest)
	if _, err := RequestVars(task); err == nil {
		t.Error("expected an error when the request annotation is missing")
	}
}
