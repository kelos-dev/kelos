package reporting

import (
	"context"
	"encoding/base64"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// SlackTurnReporter reports AgentTurn lifecycle changes to the originating
// Slack thread.
type SlackTurnReporter struct {
	Client   client.Client
	Reporter SlackMessenger
}

// ReportTurnStatus posts the accepted and terminal Slack messages for an
// AgentTurn. It stores Slack timestamps in status, so status is the source of
// truth for de-duplication.
func (tr *SlackTurnReporter) ReportTurnStatus(ctx context.Context, turn *kelosv1alpha1.AgentTurn) error {
	if tr == nil || tr.Reporter == nil {
		return nil
	}
	annotations := turn.Annotations
	if annotations == nil || annotations[AnnotationSlackReporting] != "enabled" {
		return nil
	}
	channel := annotations[AnnotationSlackChannel]
	threadTS := annotations[AnnotationSlackThreadTS]
	if channel == "" || threadTS == "" {
		return nil
	}

	desiredPhase, terminal := turnSlackPhase(turn.Status.Phase)
	if desiredPhase == "" {
		return nil
	}
	if !terminal && turn.Status.SlackProgressMessageTS != "" {
		return nil
	}
	if terminal && turn.Status.SlackAgentMessageTS != "" {
		return nil
	}

	results := map[string]string{}
	if turn.Status.ResultText != "" {
		results["response"] = base64.StdEncoding.EncodeToString([]byte(turn.Status.ResultText))
	}
	msgs := FormatSlackTransitionMessage(desiredPhase, turn.Name, turn.Status.Message, results)

	if terminal {
		return tr.reportTerminalTurn(ctx, turn, channel, threadTS, msgs)
	}
	return tr.reportAcceptedTurn(ctx, turn, channel, threadTS, msgs[0])
}

func turnSlackPhase(phase kelosv1alpha1.AgentTurnPhase) (string, bool) {
	switch phase {
	case "", kelosv1alpha1.AgentTurnPhaseQueued, kelosv1alpha1.AgentTurnPhaseRunning:
		return "accepted", false
	case kelosv1alpha1.AgentTurnPhaseSucceeded:
		return "succeeded", true
	case kelosv1alpha1.AgentTurnPhaseFailed, kelosv1alpha1.AgentTurnPhaseCanceled:
		return "failed", true
	default:
		return "", false
	}
}

func (tr *SlackTurnReporter) reportAcceptedTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn, channel, threadTS string, msg SlackMessage) error {
	log := ctrl.Log.WithName("slack-turn-reporter")
	log.Info("Posting Slack accepted reply for AgentTurn", "turn", turn.Name, "channel", channel)
	ts, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg)
	if err != nil {
		return fmt.Errorf("posting Slack accepted reply for AgentTurn %s: %w", turn.Name, err)
	}
	return tr.patchTurnStatus(ctx, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.SlackProgressMessageTS = ts
	})
}

func (tr *SlackTurnReporter) reportTerminalTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn, channel, threadTS string, msgs []SlackMessage) error {
	log := ctrl.Log.WithName("slack-turn-reporter")

	firstTS := turn.Status.SlackProgressMessageTS
	if firstTS != "" {
		log.Info("Updating Slack accepted reply with AgentTurn terminal result", "turn", turn.Name, "channel", channel)
		if err := tr.Reporter.UpdateMessage(ctx, channel, firstTS, msgs[0]); err != nil {
			log.Error(err, "Failed to update accepted reply with AgentTurn terminal result, posting new reply", "turn", turn.Name)
			firstTS = ""
		}
	}

	if firstTS == "" {
		log.Info("Posting Slack terminal reply for AgentTurn", "turn", turn.Name, "channel", channel)
		ts, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msgs[0])
		if err != nil {
			return fmt.Errorf("posting Slack terminal reply for AgentTurn %s: %w", turn.Name, err)
		}
		firstTS = ts
	}

	for _, msg := range msgs[1:] {
		if _, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg); err != nil {
			log.Error(err, "Failed to post AgentTurn continuation message", "turn", turn.Name)
		}
	}

	if err := tr.patchTurnStatus(ctx, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.SlackAgentMessageTS = firstTS
	}); err != nil {
		return err
	}

	if turn.Spec.SessionRef.Name != "" {
		if err := tr.patchSessionStatus(ctx, turn.Namespace, turn.Spec.SessionRef.Name, func(s *kelosv1alpha1.AgentSession) {
			s.Status.LastAgentMessageTS = firstTS
		}); err != nil {
			return err
		}
	}

	return nil
}

func (tr *SlackTurnReporter) patchTurnStatus(ctx context.Context, namespace, name string, mutate func(*kelosv1alpha1.AgentTurn)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.AgentTurn
		if err := tr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &current); err != nil {
			return err
		}
		mutate(&current)
		return tr.Client.Status().Update(ctx, &current)
	})
}

func (tr *SlackTurnReporter) patchSessionStatus(ctx context.Context, namespace, name string, mutate func(*kelosv1alpha1.AgentSession)) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.AgentSession
		if err := tr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &current); err != nil {
			return err
		}
		mutate(&current)
		return tr.Client.Status().Update(ctx, &current)
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
