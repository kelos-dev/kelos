package reporting

import (
	"context"
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
