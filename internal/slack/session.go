package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	goslack "github.com/slack-go/slack"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
)

const (
	LabelAgentSession = "kelos.dev/agent-session"
	LabelSlackChannel = "kelos.dev/slack-channel"
	LabelSlackRootTS  = "kelos.dev/slack-root-ts"
	LabelTaskSpawner  = "kelos.dev/taskspawner"
	LabelSource       = "kelos.dev/source"

	defaultSessionIdleTimeout = time.Hour
	defaultMaxQueuedTurns     = int32(5)
	maxTurnTranscriptBytes    = 128 * 1024
)

func slackSessionEnabled(cfg *v1alpha1.Slack) bool {
	return cfg != nil && cfg.Session != nil && cfg.Session.Enabled
}

func effectiveSessionConfig(cfg *v1alpha1.Slack) (time.Duration, int32, v1alpha1.SlackSessionContextWindow) {
	idle := defaultSessionIdleTimeout
	maxQueued := defaultMaxQueuedTurns
	mode := v1alpha1.SlackSessionContextWindowSinceLastAgentMessage
	if cfg != nil && cfg.Session != nil {
		if cfg.Session.IdleTimeout != nil && cfg.Session.IdleTimeout.Duration > 0 {
			idle = cfg.Session.IdleTimeout.Duration
		}
		if cfg.Session.MaxQueuedTurns != nil && *cfg.Session.MaxQueuedTurns > 0 {
			maxQueued = *cfg.Session.MaxQueuedTurns
		}
		if cfg.Session.ContextWindow != "" {
			mode = cfg.Session.ContextWindow
		}
	}
	return idle, maxQueued, mode
}

func sessionName(spawnerName, teamID, channelID, rootTS, namespace string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{teamID, channelID, rootTS, namespace, spawnerName}, "\n")))
	hash := hex.EncodeToString(sum[:])[:12]
	prefix := spawnerName
	const suffixLen = len("-sess-") + 12
	if len(prefix) > 63-suffixLen {
		prefix = strings.TrimRight(prefix[:63-suffixLen], "-.")
	}
	return fmt.Sprintf("%s-sess-%s", prefix, hash)
}

func turnName(sessionName string, sequence int32) string {
	suffix := fmt.Sprintf("-t-%04d", sequence)
	prefix := sessionName
	if len(prefix) > 63-len(suffix) {
		prefix = strings.TrimRight(prefix[:63-len(suffix)], "-.")
	}
	return prefix + suffix
}

func rootThreadTS(msg *SlackMessageData) string {
	if msg.ThreadTS != "" {
		return msg.ThreadTS
	}
	return msg.Timestamp
}

func semanticBody(text string) string {
	return strings.TrimSpace(StripLeadingMentions(text))
}

func (h *SlackHandler) routeSessionFollowUp(ctx context.Context, msg *SlackMessageData) bool {
	if msg.ThreadTS == "" || !HasBotMention(msg.Text, h.botUserID) {
		return false
	}
	sessions, err := h.activeSessionsForThread(ctx, msg.ChannelID, msg.ThreadTS)
	if err != nil {
		h.log.Error(err, "Failed to list AgentSessions for Slack thread", "channel", msg.ChannelID, "threadTS", msg.ThreadTS)
		return false
	}
	if len(sessions) == 0 {
		return false
	}
	if len(sessions) > 1 {
		h.postSessionNotice(ctx, msg.ChannelID, msg.ThreadTS, "Multiple Cody sessions are active in this thread. Please use the original route prefix to disambiguate.")
		return true
	}
	if err := h.createTurnForSession(ctx, &sessions[0], msg); err != nil {
		h.log.Error(err, "Failed to create AgentTurn for Slack follow-up", "session", sessions[0].Name)
		h.postSessionNotice(ctx, msg.ChannelID, msg.ThreadTS, fmt.Sprintf("Could not queue this Cody follow-up: %v", err))
	}
	return true
}

func (h *SlackHandler) activeSessionsForThread(ctx context.Context, channelID, rootTS string) ([]v1alpha1.AgentSession, error) {
	var list v1alpha1.AgentSessionList
	if err := h.client.List(ctx, &list,
		client.MatchingLabels{
			LabelSource:       "slack",
			LabelSlackChannel: channelID,
			LabelSlackRootTS:  rootTS,
		},
	); err != nil {
		return nil, err
	}
	var out []v1alpha1.AgentSession
	for _, session := range list.Items {
		switch session.Status.Phase {
		case v1alpha1.AgentSessionPhaseClosed, v1alpha1.AgentSessionPhaseError:
			continue
		default:
			out = append(out, session)
		}
	}
	return out, nil
}

func (h *SlackHandler) createSessionAndFirstTurn(ctx context.Context, spawner *v1alpha1.TaskSpawner, msg *SlackMessageData) error {
	idleTimeout, maxQueued, contextWindow := effectiveSessionConfig(spawner.Spec.When.Slack)
	rootTS := rootThreadTS(msg)
	name := sessionName(spawner.Name, "", msg.ChannelID, rootTS, spawner.Namespace)

	session := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: spawner.Namespace,
			Labels: map[string]string{
				LabelSource:       "slack",
				LabelTaskSpawner:  spawner.Name,
				LabelSlackChannel: msg.ChannelID,
				LabelSlackRootTS:  rootTS,
			},
		},
		Spec: v1alpha1.AgentSessionSpec{
			Source: v1alpha1.AgentSessionSource{
				Type:      "SlackThread",
				ChannelID: msg.ChannelID,
				RootTS:    rootTS,
				ThreadURL: msg.Permalink,
			},
			TaskSpawnerRef:       v1alpha1.TaskSpawnerReference{Name: spawner.Name},
			TaskTemplateSnapshot: spawner.Spec.TaskTemplate,
			Route: v1alpha1.AgentSessionRoute{
				InitialText: msg.Text,
			},
			IdleTimeout:    metav1.Duration{Duration: idleTimeout},
			ContextWindow:  contextWindow,
			MaxQueuedTurns: maxQueued,
		},
	}
	if err := controllerutil.SetControllerReference(spawner, session, h.client.Scheme()); err != nil {
		return fmt.Errorf("setting AgentSession owner reference: %w", err)
	}
	if err := h.client.Create(ctx, session); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating AgentSession: %w", err)
		}
		if getErr := h.client.Get(ctx, client.ObjectKey{Namespace: spawner.Namespace, Name: name}, session); getErr != nil {
			return fmt.Errorf("fetching existing AgentSession: %w", getErr)
		}
	}
	return h.createTurnForSession(ctx, session, msg)
}

func (h *SlackHandler) createTurnForSession(ctx context.Context, session *v1alpha1.AgentSession, msg *SlackMessageData) error {
	if session.Spec.MaxQueuedTurns > 0 {
		count, err := h.countQueuedOrRunningTurns(ctx, session)
		if err != nil {
			return err
		}
		if count >= session.Spec.MaxQueuedTurns {
			return fmt.Errorf("session already has %d queued or running turns", count)
		}
	}
	nextSeq, err := h.nextTurnSequence(ctx, session)
	if err != nil {
		return err
	}
	contextWindow := session.Spec.ContextWindow
	if contextWindow == "" {
		contextWindow = v1alpha1.SlackSessionContextWindowSinceLastAgentMessage
	}
	turn := &v1alpha1.AgentTurn{
		ObjectMeta: metav1.ObjectMeta{
			Name:      turnName(session.Name, nextSeq),
			Namespace: session.Namespace,
			Labels: map[string]string{
				LabelSource:                   "slack",
				LabelAgentSession:             session.Name,
				LabelTaskSpawner:              session.Spec.TaskSpawnerRef.Name,
				reporting.LabelSlackReporting: "enabled",
			},
			Annotations: map[string]string{
				reporting.AnnotationSlackReporting: "enabled",
				reporting.AnnotationSlackChannel:   msg.ChannelID,
				reporting.AnnotationSlackThreadTS:  rootThreadTS(msg),
				reporting.AnnotationSlackUserID:    msg.UserID,
			},
		},
		Spec: v1alpha1.AgentTurnSpec{
			SessionRef: v1alpha1.AgentSessionReference{Name: session.Name},
			Sequence:   nextSeq,
			Source: v1alpha1.AgentTurnSource{
				Type:      "SlackMessage",
				ChannelID: msg.ChannelID,
				RootTS:    rootThreadTS(msg),
				MessageTS: msg.Timestamp,
				UserID:    msg.UserID,
				BotID:     msg.BotID,
				Permalink: msg.Permalink,
			},
			Input: v1alpha1.AgentTurnInput{
				Text: msg.Text,
				Body: semanticBody(msg.Text),
			},
			Context: v1alpha1.AgentTurnContext{
				Mode:          contextWindow,
				ToTSInclusive: msg.Timestamp,
			},
		},
	}
	if msg.BotID != "" {
		turn.Annotations["kelos.dev/slack-bot-id"] = msg.BotID
	}
	if err := h.populateTurnTranscript(ctx, session, turn); err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(session, turn, h.client.Scheme()); err != nil {
		return fmt.Errorf("setting AgentTurn owner reference: %w", err)
	}
	if err := h.client.Create(ctx, turn); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating AgentTurn: %w", err)
	}
	return nil
}

func (h *SlackHandler) populateTurnTranscript(ctx context.Context, session *v1alpha1.AgentSession, turn *v1alpha1.AgentTurn) error {
	fromTS := session.Status.LastAgentMessageTS
	if fromTS == "" && turn.Spec.Source.MessageTS != session.Spec.Source.RootTS {
		fromTS = session.Spec.Source.RootTS
	}
	turn.Spec.Context.FromTSExclusive = fromTS
	replies, err := FetchThreadReplies(ctx, h.api, session.Spec.Source.ChannelID, session.Spec.Source.RootTS)
	if err != nil {
		return err
	}
	transcript, size, err := BuildSlackDeltaTranscript(replies, h.botUserID, h.botID, fromTS, turn.Spec.Source.MessageTS)
	if err != nil {
		return err
	}
	if size > maxTurnTranscriptBytes {
		return fmt.Errorf("turn transcript is %d bytes, limit is %d", size, maxTurnTranscriptBytes)
	}
	turn.Spec.Context.Transcript = transcript
	turn.Spec.Context.TranscriptBytes = int32(size)
	return nil
}

func (h *SlackHandler) countQueuedOrRunningTurns(ctx context.Context, session *v1alpha1.AgentSession) (int32, error) {
	var list v1alpha1.AgentTurnList
	if err := h.client.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{LabelAgentSession: session.Name}); err != nil {
		return 0, err
	}
	var count int32
	for _, turn := range list.Items {
		switch turn.Status.Phase {
		case "", v1alpha1.AgentTurnPhaseQueued, v1alpha1.AgentTurnPhaseRunning:
			count++
		}
	}
	return count, nil
}

func (h *SlackHandler) nextTurnSequence(ctx context.Context, session *v1alpha1.AgentSession) (int32, error) {
	var list v1alpha1.AgentTurnList
	if err := h.client.List(ctx, &list, client.InNamespace(session.Namespace), client.MatchingLabels{LabelAgentSession: session.Name}); err != nil {
		return 0, err
	}
	var maxSeq int32
	for _, turn := range list.Items {
		if turn.Spec.Sequence > maxSeq {
			maxSeq = turn.Spec.Sequence
		}
	}
	return maxSeq + 1, nil
}

func (h *SlackHandler) postSessionNotice(ctx context.Context, channelID, threadTS, text string) {
	if h.api == nil || channelID == "" || threadTS == "" || strings.TrimSpace(text) == "" {
		return
	}
	postCtx, cancel := context.WithTimeout(ctx, postMessageTimeout)
	defer cancel()
	if _, _, err := h.api.PostMessageContext(postCtx, channelID, goslack.MsgOptionText(text, false), goslack.MsgOptionTS(threadTS)); err != nil {
		h.log.Error(err, "Failed to post Slack session notice", "channel", channelID, "threadTS", threadTS)
	}
}
