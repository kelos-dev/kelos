package reporting

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
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
	Routes   map[string]SlackRoute

	mu           sync.Mutex
	lastActivity map[types.UID]string
	deferredSent map[types.UID]struct{}
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
		if annotations == nil || annotations[AnnotationSlackReporting] != SlackReportingDeferred {
			return nil
		}
	}
	reportingMode := annotations[AnnotationSlackReporting]
	threadTS := annotations[AnnotationSlackThreadTS]

	desiredPhase, terminal := turnSlackPhase(turn.Status.Phase)
	if desiredPhase == "" {
		return nil
	}
	if terminal && turn.Status.SlackAgentMessageTS != "" {
		return nil
	}
	if threadTS == "" && isSessionSummarySlackLayout(annotations) && turn.Spec.SessionRef.Name != "" {
		return tr.reportSessionSummaryTurn(ctx, turn, desiredPhase, terminal)
	}
	channel := tr.resolveSlackChannel(turn)
	if channel == "" {
		return nil
	}
	if threadTS == "" {
		if reportingMode != SlackReportingDeferred {
			return nil
		}
		if terminal {
			if suppressDeferredSlackTurn(turn) {
				return nil
			}
			return tr.reportDeferredTerminalTurn(ctx, turn, channel, desiredPhase)
		}
		return nil
	}
	if !terminal && turn.Status.SlackProgressMessageTS != "" {
		return tr.reportRunningActivity(ctx, turn, channel)
	}
	results := turnSlackResults(turn)
	msgs := FormatSlackTransitionMessage(desiredPhase, turn.Name, turn.Status.Message, results)

	if terminal {
		if isStableSummarySlackLayout(annotations) {
			return tr.reportStableSummaryTerminalTurn(ctx, turn, channel, threadTS, desiredPhase, results)
		}
		return tr.reportTerminalTurn(ctx, turn, channel, threadTS, msgs)
	}
	return tr.reportAcceptedTurn(ctx, turn, channel, threadTS, msgs[0])
}

func (tr *SlackTurnReporter) resolveSlackChannel(turn *kelosv1alpha1.AgentTurn) string {
	annotations := turn.Annotations
	if annotations == nil {
		return ""
	}
	if channel := annotations[AnnotationSlackChannel]; channel != "" {
		return channel
	}
	destination := annotations[AnnotationSlackDestination]
	if destination == "" {
		return ""
	}
	return tr.Routes[destination].Channel
}

func turnSlackResults(turn *kelosv1alpha1.AgentTurn) map[string]string {
	results := map[string]string{}
	if turn.Status.ResultText != "" {
		results["response"] = base64.StdEncoding.EncodeToString([]byte(turn.Status.ResultText))
	}
	return results
}

func suppressDeferredSlackTurn(turn *kelosv1alpha1.AgentTurn) bool {
	return strings.HasPrefix(strings.TrimSpace(turn.Status.ResultText), "NO_SLACK:")
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

func (tr *SlackTurnReporter) reportRunningActivity(ctx context.Context, turn *kelosv1alpha1.AgentTurn, channel string) error {
	activity := turn.Status.Activity
	if activity == "" {
		return nil
	}
	tr.mu.Lock()
	if tr.lastActivity == nil {
		tr.lastActivity = make(map[types.UID]string)
	}
	if tr.lastActivity[turn.UID] == activity {
		tr.mu.Unlock()
		return nil
	}
	tr.lastActivity[turn.UID] = activity
	tr.mu.Unlock()

	var msg SlackMessage
	if isStableSummarySlackLayout(turn.Annotations) {
		msg = FormatStableSummaryProgressMessage(stableSummaryFromAnnotations(turn.Annotations), activity, turn.Name)
	} else {
		msgs := FormatSlackTransitionMessage("accepted", turn.Name, turn.Status.Message, nil)
		msg = appendActivityContext(msgs[0], activity)
	}
	if len(msg.Blocks) == 0 {
		return nil
	}
	log := ctrl.Log.WithName("slack-turn-reporter")
	log.Info("Updating Slack accepted reply with AgentTurn activity", "turn", turn.Name, "channel", channel)
	if err := tr.Reporter.UpdateMessage(ctx, channel, turn.Status.SlackProgressMessageTS, msg); err != nil {
		log.V(1).Info("Failed to update AgentTurn activity", "turn", turn.Name, "error", err)
		tr.clearActivity(turn.UID)
	}
	return nil
}

func (tr *SlackTurnReporter) reportSessionSummaryTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn, desiredPhase string, terminal bool) error {
	if terminal {
		return tr.reportSessionSummaryTerminalTurn(ctx, turn, desiredPhase)
	}
	return tr.reportSessionSummaryProgressTurn(ctx, turn)
}

func (tr *SlackTurnReporter) reportSessionSummaryProgressTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn) error {
	activity := strings.TrimSpace(turn.Status.Activity)
	if activity == "" {
		return nil
	}
	tr.mu.Lock()
	if tr.lastActivity == nil {
		tr.lastActivity = make(map[types.UID]string)
	}
	if tr.lastActivity[turn.UID] == activity {
		tr.mu.Unlock()
		return nil
	}
	tr.lastActivity[turn.UID] = activity
	tr.mu.Unlock()

	session, err := tr.getSession(ctx, turn.Namespace, turn.Spec.SessionRef.Name)
	if err != nil {
		tr.clearActivity(turn.UID)
		return err
	}
	summary := sessionSlackSummary(session)
	if summary == "" {
		summary = initialSessionSummary(session)
	}
	latest := compactSessionLatest(activity)
	channel, rootTS, err := tr.upsertSessionSummaryRoot(ctx, session, turn, summary, latest)
	if err != nil {
		tr.clearActivity(turn.UID)
		return err
	}
	if channel == "" || rootTS == "" {
		return nil
	}
	if turn.Status.SlackProgressMessageTS == rootTS {
		return nil
	}
	return tr.patchTurnStatus(ctx, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.SlackProgressMessageTS = rootTS
	})
}

func (tr *SlackTurnReporter) reportSessionSummaryTerminalTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn, desiredPhase string) error {
	if suppressDeferredSlackTurn(turn) {
		return nil
	}
	if tr.hasDeferredTerminalPost(turn.UID) {
		return nil
	}

	session, err := tr.getSession(ctx, turn.Namespace, turn.Spec.SessionRef.Name)
	if err != nil {
		return err
	}
	results := turnSlackResults(turn)
	details := hasSlackTerminalDetails(desiredPhase, turn.Status.Message, results)
	summary := appendSessionSummary(sessionSlackSummary(session), terminalSessionSummary(turn, desiredPhase))
	if summary == "" {
		summary = initialSessionSummary(session)
	}
	latest := terminalSessionLatest(turn, desiredPhase, details)
	channel, rootTS, err := tr.upsertSessionSummaryRoot(ctx, session, turn, summary, latest)
	if err != nil {
		return err
	}
	if channel == "" || rootTS == "" {
		return nil
	}

	log := ctrl.Log.WithName("slack-turn-reporter")
	if details {
		for _, msg := range FormatSlackTransitionMessage(desiredPhase, turn.Name, turn.Status.Message, results) {
			if _, err := tr.Reporter.PostThreadReply(ctx, channel, rootTS, msg); err != nil {
				log.Error(err, "Failed to post AgentTurn terminal details", "turn", turn.Name)
			}
		}
	}
	tr.markDeferredTerminalPost(turn.UID)
	if err := tr.patchTurnStatus(ctx, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.SlackProgressMessageTS = rootTS
		t.Status.SlackAgentMessageTS = rootTS
	}); err != nil {
		return err
	}
	tr.clearActivity(turn.UID)
	return nil
}

func (tr *SlackTurnReporter) getSession(ctx context.Context, namespace, name string) (*kelosv1alpha1.AgentSession, error) {
	var session kelosv1alpha1.AgentSession
	if err := tr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (tr *SlackTurnReporter) upsertSessionSummaryRoot(ctx context.Context, session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn, summary, latest string) (string, string, error) {
	channel, destination := tr.resolveSessionSlackChannel(session, turn)
	if channel == "" {
		return "", "", nil
	}
	rootTS := ""
	if session.Status.Slack != nil {
		rootTS = session.Status.Slack.RootTS
	}
	msg := FormatSessionSummaryRootMessage(sessionSlackTitle(session), summary, latest, turn.Name)
	log := ctrl.Log.WithName("slack-turn-reporter")
	if rootTS == "" {
		log.Info("Posting session Slack root message for AgentTurn", "turn", turn.Name, "channel", channel)
		ts, err := tr.Reporter.PostMessage(ctx, channel, msg)
		if err != nil {
			return "", "", fmt.Errorf("posting session Slack root message for AgentTurn %s: %w", turn.Name, err)
		}
		rootTS = ts
	} else {
		log.Info("Updating session Slack root message for AgentTurn", "turn", turn.Name, "channel", channel)
		if err := tr.Reporter.UpdateMessage(ctx, channel, rootTS, msg); err != nil {
			return "", "", fmt.Errorf("updating session Slack root message for AgentTurn %s: %w", turn.Name, err)
		}
	}

	if err := tr.patchSessionStatus(ctx, session.Namespace, session.Name, func(s *kelosv1alpha1.AgentSession) {
		if s.Status.Slack == nil {
			s.Status.Slack = &kelosv1alpha1.AgentSessionSlackStatus{}
		}
		s.Status.Slack.ChannelID = channel
		s.Status.Slack.RootTS = rootTS
		s.Status.Slack.Destination = destination
		s.Status.Slack.Layout = SlackLayoutSessionSummaryRoot
		s.Status.Slack.Summary = summary
		s.Status.Slack.Latest = latest
		s.Status.Slack.LastPostedTurn = turn.Name
		s.Status.Slack.LastPostedSequence++
		s.Status.LastAgentMessageTS = rootTS
	}); err != nil {
		return "", "", err
	}
	if session.Status.Slack == nil {
		session.Status.Slack = &kelosv1alpha1.AgentSessionSlackStatus{}
	}
	session.Status.Slack.ChannelID = channel
	session.Status.Slack.RootTS = rootTS
	session.Status.Slack.Destination = destination
	session.Status.Slack.Layout = SlackLayoutSessionSummaryRoot
	session.Status.Slack.Summary = summary
	session.Status.Slack.Latest = latest
	session.Status.Slack.LastPostedTurn = turn.Name
	session.Status.Slack.LastPostedSequence++
	session.Status.LastAgentMessageTS = rootTS
	return channel, rootTS, nil
}

func (tr *SlackTurnReporter) resolveSessionSlackChannel(session *kelosv1alpha1.AgentSession, turn *kelosv1alpha1.AgentTurn) (string, string) {
	if session.Status.Slack != nil && session.Status.Slack.ChannelID != "" {
		return session.Status.Slack.ChannelID, session.Status.Slack.Destination
	}
	if session.Spec.Source.ChannelID != "" {
		return session.Spec.Source.ChannelID, ""
	}
	annotations := turn.Annotations
	if annotations == nil {
		return "", ""
	}
	if channel := annotations[AnnotationSlackChannel]; channel != "" {
		return channel, annotations[AnnotationSlackDestination]
	}
	destination := annotations[AnnotationSlackDestination]
	if destination == "" {
		return "", ""
	}
	return tr.Routes[destination].Channel, destination
}

func sessionSlackTitle(session *kelosv1alpha1.AgentSession) string {
	title := strings.TrimSpace(session.Spec.Source.DisplayName)
	if title == "" {
		title = strings.TrimSpace(session.Spec.TaskSpawnerRef.Name)
	}
	title = strings.TrimPrefix(title, "cron:")
	if title == "" {
		return "Cody session"
	}
	return title
}

func sessionSlackSummary(session *kelosv1alpha1.AgentSession) string {
	if session.Status.Slack == nil {
		return ""
	}
	return strings.TrimSpace(session.Status.Slack.Summary)
}

func initialSessionSummary(_ *kelosv1alpha1.AgentSession) string {
	return "Session started."
}

func appendSessionSummary(existing, addition string) string {
	addition = compactSessionLine(addition, 360)
	existing = strings.TrimSpace(existing)
	if addition == "" {
		return existing
	}
	line := "- " + strings.TrimPrefix(addition, "- ")
	lines := compactSessionSummaryLines(existing)
	normalizedAddition := normalizeSessionSummaryLine(line)
	for _, existingLine := range lines {
		if normalizeSessionSummaryLine(existingLine) == normalizedAddition {
			return strings.Join(lines, "\n")
		}
	}
	lines = append(lines, line)
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	summary, _ := compactSlackText(strings.Join(lines, "\n"), sessionSummaryRootLimit)
	return summary
}

func compactSessionSummaryLines(summary string) []string {
	var lines []string
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func normalizeSessionSummaryLine(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, "- "))
}

func terminalSessionSummary(turn *kelosv1alpha1.AgentTurn, desiredPhase string) string {
	text := strings.TrimSpace(turn.Status.ResultText)
	if text == "" && desiredPhase == "failed" {
		text = strings.TrimSpace(turn.Status.Message)
	}
	if text == "" {
		text = phaseFallbackText[desiredPhase]
	}
	return text
}

func terminalSessionLatest(turn *kelosv1alpha1.AgentTurn, desiredPhase string, details bool) string {
	text := terminalSessionSummary(turn, desiredPhase)
	latest := compactSessionLine(text, 700)
	if details {
		if latest != "" {
			latest += "\n\n"
		}
		latest += "Full details are posted in this message thread."
	}
	return latest
}

func compactSessionLatest(latest string) string {
	return compactSessionLine(latest, 700)
}

func compactSessionLine(text string, limit int) string {
	text = strings.TrimSpace(text)
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			parts = append(parts, line)
		}
	}
	text = strings.Join(parts, " ")
	text, _ = compactSlackText(text, limit)
	return text
}

func (tr *SlackTurnReporter) reportDeferredTerminalTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn, channel, desiredPhase string) error {
	if tr.hasDeferredTerminalPost(turn.UID) {
		return nil
	}

	results := turnSlackResults(turn)
	msgs := FormatSlackTransitionMessage(desiredPhase, turn.Name, turn.Status.Message, results)
	if isStableSummarySlackLayout(turn.Annotations) {
		stableSummary := stableSummaryFromAnnotations(turn.Annotations)
		if turn.Annotations[AnnotationSlackStableSummary] == "" && turn.Status.ResultText != "" {
			stableSummary = compactStableSummary(turn.Status.ResultText)
		}
		msgs[0] = FormatStableSummaryFinalMessage(stableSummary, desiredPhase, turn.Name, turn.Status.Message, results)
	}

	log := ctrl.Log.WithName("slack-turn-reporter")
	log.Info("Posting deferred Slack terminal root message for AgentTurn", "turn", turn.Name, "channel", channel)
	rootTS, err := tr.Reporter.PostMessage(ctx, channel, msgs[0])
	if err != nil {
		return fmt.Errorf("posting deferred Slack terminal root message for AgentTurn %s: %w", turn.Name, err)
	}
	tr.markDeferredTerminalPost(turn.UID)
	if err := tr.patchTurnStatus(ctx, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.SlackProgressMessageTS = rootTS
		t.Status.SlackAgentMessageTS = rootTS
	}); err != nil {
		return err
	}
	for _, msg := range msgs[1:] {
		if _, err := tr.Reporter.PostThreadReply(ctx, channel, rootTS, msg); err != nil {
			log.Error(err, "Failed to post AgentTurn continuation message", "turn", turn.Name)
		}
	}
	if turn.Spec.SessionRef.Name != "" {
		if err := tr.patchSessionStatus(ctx, turn.Namespace, turn.Spec.SessionRef.Name, func(s *kelosv1alpha1.AgentSession) {
			s.Status.LastAgentMessageTS = rootTS
		}); err != nil {
			return err
		}
	}
	return nil
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
	tr.clearActivity(turn.UID)

	if turn.Spec.SessionRef.Name != "" {
		if err := tr.patchSessionStatus(ctx, turn.Namespace, turn.Spec.SessionRef.Name, func(s *kelosv1alpha1.AgentSession) {
			s.Status.LastAgentMessageTS = firstTS
		}); err != nil {
			return err
		}
	}

	return nil
}

func (tr *SlackTurnReporter) reportStableSummaryTerminalTurn(ctx context.Context, turn *kelosv1alpha1.AgentTurn, channel, threadTS, desiredPhase string, results map[string]string) error {
	log := ctrl.Log.WithName("slack-turn-reporter")

	messageTS := turn.Status.SlackProgressMessageTS
	if messageTS == "" {
		messageTS = turn.Annotations[AnnotationSlackMessageTS]
	}
	if messageTS == "" {
		messageTS = threadTS
	}
	if threadTS == "" {
		threadTS = messageTS
	}
	if messageTS == "" || threadTS == "" {
		return nil
	}

	stableSummary := stableSummaryFromAnnotations(turn.Annotations)
	if turn.Annotations[AnnotationSlackStableSummary] == "" && turn.Status.ResultText != "" {
		stableSummary = compactStableSummary(turn.Status.ResultText)
	}
	rootMsg := FormatStableSummaryFinalMessage(stableSummary, desiredPhase, turn.Name, turn.Status.Message, results)
	log.Info("Updating stable-summary Slack root message with AgentTurn final result", "turn", turn.Name, "channel", channel, "phase", desiredPhase)
	if err := tr.Reporter.UpdateMessage(ctx, channel, messageTS, rootMsg); err != nil {
		log.Error(err, "Failed to update stable-summary root with AgentTurn terminal result, posting details reply", "turn", turn.Name)
		return tr.reportTerminalTurn(ctx, turn, channel, threadTS, FormatSlackTransitionMessage(desiredPhase, turn.Name, turn.Status.Message, results))
	}

	if hasSlackTerminalDetails(desiredPhase, turn.Status.Message, results) {
		for _, msg := range FormatSlackTransitionMessage(desiredPhase, turn.Name, turn.Status.Message, results) {
			if _, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg); err != nil {
				log.Error(err, "Failed to post AgentTurn terminal details", "turn", turn.Name)
			}
		}
	}

	if err := tr.patchTurnStatus(ctx, turn.Namespace, turn.Name, func(t *kelosv1alpha1.AgentTurn) {
		t.Status.SlackAgentMessageTS = messageTS
	}); err != nil {
		return err
	}
	tr.clearActivity(turn.UID)

	if turn.Spec.SessionRef.Name != "" {
		if err := tr.patchSessionStatus(ctx, turn.Namespace, turn.Spec.SessionRef.Name, func(s *kelosv1alpha1.AgentSession) {
			s.Status.LastAgentMessageTS = messageTS
		}); err != nil {
			return err
		}
	}

	return nil
}

func (tr *SlackTurnReporter) clearActivity(uid types.UID) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.lastActivity, uid)
}

func (tr *SlackTurnReporter) SweepActivityCache(activeUIDs map[types.UID]bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for uid := range tr.lastActivity {
		if !activeUIDs[uid] {
			delete(tr.lastActivity, uid)
		}
	}
	for uid := range tr.deferredSent {
		if !activeUIDs[uid] {
			delete(tr.deferredSent, uid)
		}
	}
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

func (tr *SlackTurnReporter) hasDeferredTerminalPost(uid types.UID) bool {
	if uid == "" {
		return false
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	_, ok := tr.deferredSent[uid]
	return ok
}

func (tr *SlackTurnReporter) markDeferredTerminalPost(uid types.UID) {
	if uid == "" {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.deferredSent == nil {
		tr.deferredSent = make(map[types.UID]struct{})
	}
	tr.deferredSent[uid] = struct{}{}
}
