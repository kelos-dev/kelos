package reporting

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestSlackTurnReporter_PostsAcceptedAndStoresProgressTS(t *testing.T) {
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-t-0001",
			Namespace: "kelos-system",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123",
				AnnotationSlackThreadTS:  "1710000000.000001",
			},
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: "session"},
		},
		Status: kelosv1alpha1.AgentTurnStatus{Phase: kelosv1alpha1.AgentTurnPhaseQueued},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}).
		WithObjects(turn).
		Build()
	reporter := &fakeSlackReporter{
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			if channel != "C123" || threadTS != "1710000000.000001" {
				t.Fatalf("unexpected Slack target channel=%s threadTS=%s", channel, threadTS)
			}
			return "reply-ts", nil
		},
	}

	tr := &SlackTurnReporter{Client: cl, Reporter: reporter}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}

	var updated kelosv1alpha1.AgentTurn
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(turn), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.SlackProgressMessageTS != "reply-ts" {
		t.Fatalf("SlackProgressMessageTS = %q, want reply-ts", updated.Status.SlackProgressMessageTS)
	}
}

func TestSlackTurnReporter_UpdatesAcceptedMessageAndSessionTimestamp(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: "kelos-system"},
	}
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-t-0001",
			Namespace: "kelos-system",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123",
				AnnotationSlackThreadTS:  "1710000000.000001",
			},
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: session.Name},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:                  kelosv1alpha1.AgentTurnPhaseSucceeded,
			SlackProgressMessageTS: "progress-ts",
			ResultText:             "Final answer",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}, &kelosv1alpha1.AgentSession{}).
		WithObjects(session, turn).
		Build()
	updatedProgress := false
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			if messageTS != "progress-ts" {
				t.Fatalf("updated messageTS = %q, want progress-ts", messageTS)
			}
			if msg.Text == "" {
				t.Fatal("expected non-empty Slack message text")
			}
			updatedProgress = true
			return nil
		},
	}

	tr := &SlackTurnReporter{Client: cl, Reporter: reporter}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}
	if !updatedProgress {
		t.Fatal("expected progress message update")
	}

	var updatedTurn kelosv1alpha1.AgentTurn
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(turn), &updatedTurn); err != nil {
		t.Fatal(err)
	}
	if updatedTurn.Status.SlackAgentMessageTS != "progress-ts" {
		t.Fatalf("SlackAgentMessageTS = %q, want progress-ts", updatedTurn.Status.SlackAgentMessageTS)
	}
	var updatedSession kelosv1alpha1.AgentSession
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Status.LastAgentMessageTS != "progress-ts" {
		t.Fatalf("LastAgentMessageTS = %q, want progress-ts", updatedSession.Status.LastAgentMessageTS)
	}
}

func TestSlackTurnReporter_UpdatesRunningActivity(t *testing.T) {
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-t-0001",
			Namespace: "kelos-system",
			UID:       "turn-uid",
			Annotations: map[string]string{
				AnnotationSlackReporting: "enabled",
				AnnotationSlackChannel:   "C123",
				AnnotationSlackThreadTS:  "1710000000.000001",
			},
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: "session"},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:                  kelosv1alpha1.AgentTurnPhaseRunning,
			SlackProgressMessageTS: "progress-ts",
			Activity:               "Running command: gh pr checks",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}).
		WithObjects(turn).
		Build()
	updateCount := 0
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updateCount++
			if channel != "C123" || messageTS != "progress-ts" {
				t.Fatalf("unexpected Slack update target channel=%s messageTS=%s", channel, messageTS)
			}
			if msg.Text == "" || len(msg.Blocks) == 0 {
				t.Fatal("expected rich Slack activity message")
			}
			return nil
		},
	}

	tr := &SlackTurnReporter{Client: cl, Reporter: reporter}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() second call error = %v", err)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want one deduped activity update", updateCount)
	}
}

func TestSlackTurnReporter_DeferredDestinationCreatesRootMessage(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "cron-session", Namespace: "kelos-system"},
	}
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-session-t-0001",
			Namespace: "kelos-system",
			UID:       "turn-uid",
			Annotations: map[string]string{
				AnnotationSlackReporting:   SlackReportingDeferred,
				AnnotationSlackDestination: "cody-devops",
				AnnotationSlackLayout:      SlackLayoutStableSummaryRoot,
			},
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: "cron-session"},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:      kelosv1alpha1.AgentTurnPhaseSucceeded,
			ResultText: "Found a QA rollout issue and opened a fix PR.",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}, &kelosv1alpha1.AgentSession{}).
		WithObjects(session, turn).
		Build()
	postCount := 0
	reporter := &fakeSlackReporter{
		postMessageFn: func(ctx context.Context, channel string, msg SlackMessage) (string, error) {
			postCount++
			if channel != "C123" {
				t.Fatalf("channel = %q, want C123", channel)
			}
			if msg.Text == "" || len(msg.Blocks) == 0 {
				t.Fatal("expected rich deferred root message")
			}
			return "root-ts", nil
		},
	}

	tr := &SlackTurnReporter{
		Client:   cl,
		Reporter: reporter,
		Routes:   map[string]SlackRoute{"cody-devops": {Channel: "C123"}},
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() second call error = %v", err)
	}
	if postCount != 1 {
		t.Fatalf("postCount = %d, want 1", postCount)
	}

	var updated kelosv1alpha1.AgentTurn
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(turn), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[AnnotationSlackChannel] != "" {
		t.Fatalf("slack channel annotation = %q, want empty", updated.Annotations[AnnotationSlackChannel])
	}
	if updated.Annotations[AnnotationSlackThreadTS] != "" {
		t.Fatalf("slack thread annotation = %q, want empty", updated.Annotations[AnnotationSlackThreadTS])
	}
	if updated.Status.SlackProgressMessageTS != "root-ts" {
		t.Fatalf("SlackProgressMessageTS = %q, want root-ts", updated.Status.SlackProgressMessageTS)
	}
	if updated.Status.SlackAgentMessageTS != "root-ts" {
		t.Fatalf("SlackAgentMessageTS = %q, want root-ts", updated.Status.SlackAgentMessageTS)
	}
	freshReporter := &SlackTurnReporter{
		Client:   cl,
		Reporter: reporter,
		Routes:   map[string]SlackRoute{"cody-devops": {Channel: "C123"}},
	}
	if err := freshReporter.ReportTurnStatus(context.Background(), &updated); err != nil {
		t.Fatalf("fresh ReportTurnStatus() error = %v", err)
	}
	if postCount != 1 {
		t.Fatalf("postCount after fresh reporter = %d, want 1", postCount)
	}
	var updatedSession kelosv1alpha1.AgentSession
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Status.LastAgentMessageTS != "root-ts" {
		t.Fatalf("LastAgentMessageTS = %q, want root-ts", updatedSession.Status.LastAgentMessageTS)
	}
}

func TestSlackTurnReporter_SessionSummaryProgressCreatesSessionRoot(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "cron-session", Namespace: "kelos-system"},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				Type:        "Cron",
				DisplayName: "cron:cody-datadog-health-non-prod-qa",
			},
			TaskSpawnerRef: kelosv1alpha1.TaskSpawnerReference{Name: "cody-datadog-health-non-prod-qa"},
		},
	}
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-session-t-0001",
			Namespace: "kelos-system",
			UID:       "turn-uid",
			Annotations: map[string]string{
				AnnotationSlackReporting:   SlackReportingDeferred,
				AnnotationSlackDestination: "cody-devops",
				AnnotationSlackLayout:      SlackLayoutSessionSummaryRoot,
			},
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: "cron-session"},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:    kelosv1alpha1.AgentTurnPhaseRunning,
			Activity: "Checking Kubernetes events for qa.",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}, &kelosv1alpha1.AgentSession{}).
		WithObjects(session, turn).
		Build()
	postCount := 0
	reporter := &fakeSlackReporter{
		postMessageFn: func(ctx context.Context, channel string, msg SlackMessage) (string, error) {
			postCount++
			if channel != "C123" {
				t.Fatalf("channel = %q, want C123", channel)
			}
			if !strings.Contains(msg.Text, "Summary") || !strings.Contains(msg.Text, "Latest") {
				t.Fatalf("message text = %q, want Summary and Latest", msg.Text)
			}
			if strings.Contains(msg.Text, "Detected issue") || strings.Contains(msg.Text, "Outcome") {
				t.Fatalf("message text = %q, should not use stable-summary labels", msg.Text)
			}
			return "root-ts", nil
		},
	}

	tr := &SlackTurnReporter{
		Client:   cl,
		Reporter: reporter,
		Routes:   map[string]SlackRoute{"cody-devops": {Channel: "C123"}},
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() second call error = %v", err)
	}
	if postCount != 1 {
		t.Fatalf("postCount = %d, want 1", postCount)
	}

	var updatedTurn kelosv1alpha1.AgentTurn
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(turn), &updatedTurn); err != nil {
		t.Fatal(err)
	}
	if updatedTurn.Status.SlackProgressMessageTS != "root-ts" {
		t.Fatalf("SlackProgressMessageTS = %q, want root-ts", updatedTurn.Status.SlackProgressMessageTS)
	}
	var updatedSession kelosv1alpha1.AgentSession
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Status.Slack == nil {
		t.Fatal("expected session Slack status")
	}
	if updatedSession.Status.Slack.RootTS != "root-ts" {
		t.Fatalf("session rootTS = %q, want root-ts", updatedSession.Status.Slack.RootTS)
	}
	if updatedSession.Status.Slack.Latest != "Checking Kubernetes events for qa." {
		t.Fatalf("session latest = %q, want activity", updatedSession.Status.Slack.Latest)
	}
	if updatedSession.Status.Slack.Summary != "Session started." {
		t.Fatalf("session summary = %q, want initial summary", updatedSession.Status.Slack.Summary)
	}
}

func TestSlackTurnReporter_SessionSummaryTerminalUpdatesRootAndPostsDetailsThread(t *testing.T) {
	session := &kelosv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "cron-session", Namespace: "kelos-system"},
		Spec: kelosv1alpha1.AgentSessionSpec{
			Source: kelosv1alpha1.AgentSessionSource{
				Type:        "Cron",
				DisplayName: "cron:cody-kubernetes-health-non-prod-integration",
			},
			TaskSpawnerRef: kelosv1alpha1.TaskSpawnerReference{Name: "cody-kubernetes-health-non-prod-integration"},
		},
		Status: kelosv1alpha1.AgentSessionStatus{
			Slack: &kelosv1alpha1.AgentSessionSlackStatus{
				ChannelID: "C123",
				RootTS:    "root-ts",
				Layout:    SlackLayoutSessionSummaryRoot,
				Summary:   "Session started.",
			},
		},
	}
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-session-t-0002",
			Namespace: "kelos-system",
			UID:       "turn-uid",
			Annotations: map[string]string{
				AnnotationSlackReporting: SlackReportingDeferred,
				AnnotationSlackLayout:    SlackLayoutSessionSummaryRoot,
			},
		},
		Spec: kelosv1alpha1.AgentTurnSpec{
			SessionRef: kelosv1alpha1.AgentSessionReference{Name: "cron-session"},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:      kelosv1alpha1.AgentTurnPhaseSucceeded,
			ResultText: "Found ai-api-worker missing its ServiceAccount and opened a GitOps PR.",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}, &kelosv1alpha1.AgentSession{}).
		WithObjects(session, turn).
		Build()
	updateCount := 0
	replyCount := 0
	reporter := &fakeSlackReporter{
		updateFn: func(ctx context.Context, channel, messageTS string, msg SlackMessage) error {
			updateCount++
			if channel != "C123" || messageTS != "root-ts" {
				t.Fatalf("unexpected Slack update target channel=%s messageTS=%s", channel, messageTS)
			}
			if !strings.Contains(msg.Text, "Summary") || !strings.Contains(msg.Text, "Latest") {
				t.Fatalf("message text = %q, want Summary and Latest", msg.Text)
			}
			return nil
		},
		postFn: func(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error) {
			replyCount++
			if channel != "C123" || threadTS != "root-ts" {
				t.Fatalf("unexpected thread reply target channel=%s threadTS=%s", channel, threadTS)
			}
			if !strings.Contains(msg.Text, "ServiceAccount") {
				t.Fatalf("thread reply text = %q, want terminal details", msg.Text)
			}
			return "details-ts", nil
		},
	}

	tr := &SlackTurnReporter{Client: cl, Reporter: reporter}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}
	var updatedTurn kelosv1alpha1.AgentTurn
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(turn), &updatedTurn); err != nil {
		t.Fatal(err)
	}
	if err := tr.ReportTurnStatus(context.Background(), &updatedTurn); err != nil {
		t.Fatalf("ReportTurnStatus() second call error = %v", err)
	}
	if updateCount != 1 {
		t.Fatalf("updateCount = %d, want 1", updateCount)
	}
	if replyCount != 1 {
		t.Fatalf("replyCount = %d, want 1", replyCount)
	}
	if updatedTurn.Status.SlackAgentMessageTS != "root-ts" {
		t.Fatalf("SlackAgentMessageTS = %q, want root-ts", updatedTurn.Status.SlackAgentMessageTS)
	}
	var updatedSession kelosv1alpha1.AgentSession
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updatedSession); err != nil {
		t.Fatal(err)
	}
	if updatedSession.Status.Slack == nil {
		t.Fatal("expected session Slack status")
	}
	if !strings.Contains(updatedSession.Status.Slack.Summary, "ServiceAccount") {
		t.Fatalf("summary = %q, want terminal summary", updatedSession.Status.Slack.Summary)
	}
	if !strings.Contains(updatedSession.Status.Slack.Latest, "Full details are posted") {
		t.Fatalf("latest = %q, want details note", updatedSession.Status.Slack.Latest)
	}
}

func TestSlackTurnReporter_DeferredDestinationDoesNotRepostWhenStatusPatchFails(t *testing.T) {
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-session-t-0001",
			Namespace: "kelos-system",
			UID:       "turn-uid",
			Annotations: map[string]string{
				AnnotationSlackReporting:   SlackReportingDeferred,
				AnnotationSlackDestination: "cody-devops",
			},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:      kelosv1alpha1.AgentTurnPhaseSucceeded,
			ResultText: "Found a QA rollout issue and opened a fix PR.",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}).
		Build()
	postCount := 0
	reporter := &fakeSlackReporter{
		postMessageFn: func(ctx context.Context, channel string, msg SlackMessage) (string, error) {
			postCount++
			return "root-ts", nil
		},
	}

	tr := &SlackTurnReporter{
		Client:   cl,
		Reporter: reporter,
		Routes:   map[string]SlackRoute{"cody-devops": {Channel: "C123"}},
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err == nil {
		t.Fatal("ReportTurnStatus() error = nil, want status persistence error")
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() second call error = %v", err)
	}
	if postCount != 1 {
		t.Fatalf("postCount = %d, want 1", postCount)
	}
}

func TestSlackTurnReporter_DeferredNoSlackFinalStaysSilent(t *testing.T) {
	turn := &kelosv1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-session-t-0001",
			Namespace: "kelos-system",
			Annotations: map[string]string{
				AnnotationSlackReporting:   SlackReportingDeferred,
				AnnotationSlackDestination: "cody-devops",
			},
		},
		Status: kelosv1alpha1.AgentTurnStatus{
			Phase:      kelosv1alpha1.AgentTurnPhaseSucceeded,
			ResultText: "NO_SLACK: known issue already covered by an open PR",
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithStatusSubresource(&kelosv1alpha1.AgentTurn{}).
		WithObjects(turn).
		Build()
	postCount := 0
	reporter := &fakeSlackReporter{
		postMessageFn: func(ctx context.Context, channel string, msg SlackMessage) (string, error) {
			postCount++
			return "root-ts", nil
		},
	}

	tr := &SlackTurnReporter{
		Client:   cl,
		Reporter: reporter,
		Routes:   map[string]SlackRoute{"cody-devops": {Channel: "C123"}},
	}
	if err := tr.ReportTurnStatus(context.Background(), turn); err != nil {
		t.Fatalf("ReportTurnStatus() error = %v", err)
	}
	if postCount != 0 {
		t.Fatalf("postCount = %d, want 0", postCount)
	}
}
