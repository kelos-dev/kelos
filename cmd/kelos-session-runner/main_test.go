package main

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestWaitForTurn_AppServerErrorWaitsForFailedTurnCompleted(t *testing.T) {
	turn, cl := newTestTurnClient(t)
	app := &appServerClient{
		events: make(chan rpcMessage, 2),
	}
	app.events <- rpcMessage{Method: "error", Params: rawJSON(`{"error":{"message":"Reconnecting... 2/5"}}`)}
	app.events <- rpcMessage{Method: "turn/completed", Params: rawJSON(`{"turn":{"status":"failed"}}`)}

	_, err := waitForTurn(context.Background(), app, "session", "turn-1", cl, turn)
	if err == nil {
		t.Fatal("waitForTurn() error = nil, want stored app-server error")
	}
	if !strings.Contains(err.Error(), "Reconnecting... 2/5") {
		t.Fatalf("waitForTurn() error = %q, want stored app-server error", err.Error())
	}
}

func TestWaitForTurn_AppServerErrorOnCloseUsesStoredMessage(t *testing.T) {
	turn, cl := newTestTurnClient(t)
	app := &appServerClient{
		events: make(chan rpcMessage, 2),
	}
	app.events <- rpcMessage{Method: "error", Params: rawJSON(`{"error":{"message":"upstream disconnected"}}`)}
	close(app.events)

	_, err := waitForTurn(context.Background(), app, "session", "turn-1", cl, turn)
	if err == nil {
		t.Fatal("waitForTurn() error = nil, want app-server exit error")
	}
	if !strings.Contains(err.Error(), "upstream disconnected") {
		t.Fatalf("waitForTurn() error = %q, want stored app-server error", err.Error())
	}
}

func TestWaitForTurn_ThreadStatusActivityDoesNotFailTurn(t *testing.T) {
	turn, cl := newTestTurnClient(t)
	app := &appServerClient{
		events: make(chan rpcMessage, 2),
	}
	app.events <- rpcMessage{Method: "thread/status/changed", Params: rawJSON(`{"status":"active"}`)}
	app.events <- rpcMessage{Method: "turn/completed", Params: rawJSON(`{"turn":{"status":"completed","finalMessage":"done"}}`)}

	got, err := waitForTurn(context.Background(), app, "session", "turn-1", cl, turn)
	if err != nil {
		t.Fatalf("waitForTurn() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("waitForTurn() = %q, want done", got)
	}
	var updated kelosv1alpha1.AgentTurn
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(turn), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Activity != "Codex session status: active" {
		t.Fatalf("Activity = %q, want thread status activity", updated.Status.Activity)
	}
}

func TestClassifyAppServerDiagnostic_StdinClosed(t *testing.T) {
	diag, ok := classifyAppServerDiagnostic("write_stdin failed: stdin is closed for this session; rerun exec_command with tty=true")
	if !ok {
		t.Fatal("classifyAppServerDiagnostic() ok = false, want true")
	}
	if diag.Kind != "interactive_tool_without_pty" {
		t.Fatalf("Kind = %q, want interactive_tool_without_pty", diag.Kind)
	}
	if !strings.Contains(diag.Activity, "tty=true") {
		t.Fatalf("Activity = %q, want tty guidance", diag.Activity)
	}
}

func TestRenderTurnPrompt_IncludesTTYGuidance(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "kelos-system"},
	}
	turn := &kelosv1alpha1.AgentTurn{
		Spec: kelosv1alpha1.AgentTurnSpec{
			Input: kelosv1alpha1.AgentTurnInput{Body: "help"},
			Source: kelosv1alpha1.AgentTurnSource{
				UserID:    "U123",
				MessageTS: "1.2",
			},
		},
	}
	got := renderTurnPrompt(session, turn)
	if !strings.Contains(got, "start the command with a TTY") {
		t.Fatalf("renderTurnPrompt() did not include TTY guidance:\n%s", got)
	}
}

func TestRenderTurnPrompt_FirstTurnUsesRoutePrompt(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "kelos-system"},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				ThreadURL: "https://example.slack.com/thread",
			},
			TaskSpawnerRef: kelosv1alpha1.TaskSpawnerReference{Name: "cody-debug-slack"},
		},
	}
	turn := &kelosv1alpha1.AgentTurn{
		Spec: kelosv1alpha1.AgentTurnSpec{
			Sequence: 1,
			Input: kelosv1alpha1.AgentTurnInput{
				Body: "Someone pinged you in Slack.\n\nSlack message:\n  check qa",
			},
			Source: kelosv1alpha1.AgentTurnSource{
				UserID:    "U123",
				MessageTS: "1.2",
			},
		},
	}
	got := renderTurnPrompt(session, turn)
	for _, want := range []string{
		"You are running Cody through a Kelos Slack AgentSession.",
		"Route prompt:\nSomeone pinged you in Slack.",
		"Slack message:\n  check qa",
		"Reply once in the same Slack thread through the Kelos reporter.",
		"start the command with a TTY",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderTurnPrompt() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Current explicit request:") {
		t.Fatalf("First turn prompt should not use follow-up request wrapper:\n%s", got)
	}
}

func TestRenderTurnPrompt_FollowUpKeepsCurrentRequestWrapper(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "kelos-system"},
		Spec: kelosv1alpha1.AgentSessionSpec{
			TaskSpawnerRef: kelosv1alpha1.TaskSpawnerReference{Name: "cody-debug-slack"},
		},
	}
	turn := &kelosv1alpha1.AgentTurn{
		Spec: kelosv1alpha1.AgentTurnSpec{
			Sequence: 2,
			Input:    kelosv1alpha1.AgentTurnInput{Body: "check logs too"},
			Source: kelosv1alpha1.AgentTurnSource{
				UserID:    "U123",
				MessageTS: "1.3",
			},
		},
	}
	got := renderTurnPrompt(session, turn)
	for _, want := range []string{
		"You are continuing an existing Cody Slack session.",
		"Current explicit request:",
		"check logs too",
		"Conversation since your last terminal answer:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderTurnPrompt() missing %q:\n%s", want, got)
		}
	}
}

func TestRenderTurnPrompt_CronTurnUsesCronContext(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "cron-session", Namespace: "kelos-system"},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				Type:        "Cron",
				Key:         "infra-health/non-prod/qa/2026-06-03",
				DisplayName: "cron:cody-datadog-health-non-prod-qa",
				Schedule:    "*/5 * * * *",
			},
			TaskSpawnerRef: kelosv1alpha1.TaskSpawnerReference{Name: "cody-datadog-health-non-prod-qa"},
		},
	}
	turn := &kelosv1alpha1.AgentTurn{
		Spec: kelosv1alpha1.AgentTurnSpec{
			Sequence: 2,
			Input:    kelosv1alpha1.AgentTurnInput{Body: "check qa infra health"},
			Source: kelosv1alpha1.AgentTurnSource{
				Type:     "CronTick",
				ID:       "20260603-0830",
				Time:     "2026-06-03T08:30:00Z",
				Schedule: "*/5 * * * *",
			},
		},
	}
	got := renderTurnPrompt(session, turn)
	for _, want := range []string{
		"You are continuing Cody through a Kelos cron AgentSession.",
		"Session scope: infra-health/non-prod/qa/2026-06-03",
		"Cron tick: 20260603-0830 at 2026-06-03T08:30:00Z",
		"Cron tick prompt:\ncheck qa infra health",
		"Use the existing Codex App Server thread context",
		"start the command with a TTY",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderTurnPrompt() missing %q:\n%s", want, got)
		}
	}
}

func TestRenderTurnPrompt_AikidoTurnUsesSecurityRemediationContext(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "aikido-session", Namespace: "kelos-system"},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				Type:        "Aikido",
				Key:         "aikido/main/17997122",
				DisplayName: "aikido:Upgrade golang.org/x/crypto",
				Schedule:    "0 7 * * *",
			},
			TaskSpawnerRef: kelosv1alpha1.TaskSpawnerReference{Name: "cody-aikido-security-main"},
		},
	}
	turn := &kelosv1alpha1.AgentTurn{
		Spec: kelosv1alpha1.AgentTurnSpec{
			Sequence: 2,
			Input: kelosv1alpha1.AgentTurnInput{
				Body: "Aikido issue group ID: 17997122\nBranch: main\nAffected packages: golang.org/x/crypto",
			},
			Source: kelosv1alpha1.AgentTurnSource{
				Type:     "AikidoIssueGroup",
				ID:       "aikido-group-17997122-20260608-0300",
				Time:     "2026-06-08T03:00:00Z",
				Schedule: "0 7 * * *",
			},
		},
	}
	got := renderTurnPrompt(session, turn)
	for _, want := range []string{
		"You are continuing Cody through a Kelos Aikido security remediation AgentSession.",
		"Session scope: aikido/main/17997122",
		"Aikido turn: aikido-group-17997122-20260608-0300 at 2026-06-08T03:00:00Z",
		"Aikido issue prompt:\nAikido issue group ID: 17997122",
		"search for existing open remediation PRs",
		"Create or update fixes only against latest main",
		"Do not merge PRs.",
		"start the command with a TTY",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderTurnPrompt() missing %q:\n%s", want, got)
		}
	}
}

func newTestTurnClient(t *testing.T) (*kelosv1alpha1.AgentTurn, client.Client) {
	t.Helper()
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{Name: "turn", Namespace: "kelos-system"},
		Status:     kelosv1alpha1.AgentTurnStatus{Phase: kelosv1alpha1.AgentTurnPhaseRunning},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}).
		WithObjects(turn).
		Build()
	return turn, cl
}

func rawJSON(s string) []byte {
	return []byte(s)
}
