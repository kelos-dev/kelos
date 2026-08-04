package slack

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/slack-go/slack/slackevents"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
)

const (
	// resultLabelSettleWindow is how recently a message must have been posted for
	// an unresolved reaction to be treated as a possible race with the correlation
	// label write rather than a reaction on an unrelated message.
	resultLabelSettleWindow = 2 * time.Minute

	// resultLabelResolveRetries and resultLabelResolveBackoff bound the retry for
	// that case. The reporting cycle posts the message and writes the labels in the
	// same pass, so the gap being covered is a write plus cache propagation.
	resultLabelResolveRetries = 3
	resultLabelResolveBackoff = 500 * time.Millisecond
)

// handleReactionAdded records a score when an external actor reacts to a message
// carrying an agent's result with an emoji the owning spawner maps to a verdict.
func (h *SlackHandler) handleReactionAdded(ctx context.Context, evt *slackevents.ReactionAddedEvent) {
	h.handleReaction(ctx, evt.Item.Channel, evt.Item.Timestamp, evt.User, evt.Reaction, evt.EventTimestamp, false)
}

// handleReactionRemoved retracts a score when the actor takes their reaction
// back. Retraction deletes the TaskScore the addition created, so an actor who
// reacts and then un-reacts leaves no score behind.
func (h *SlackHandler) handleReactionRemoved(ctx context.Context, evt *slackevents.ReactionRemovedEvent) {
	h.handleReaction(ctx, evt.Item.Channel, evt.Item.Timestamp, evt.User, evt.Reaction, evt.EventTimestamp, true)
}

func (h *SlackHandler) handleReaction(ctx context.Context, channel, itemTS, user, reaction, eventTS string, retract bool) {
	log := h.log.WithValues("channel", channel, "messageTS", itemTS, "reaction", reaction)

	// The bot reacting to its own output is not external feedback.
	if user == h.botUserID {
		return
	}
	if channel == "" || itemTS == "" {
		return
	}

	// Recording needs a spawner to supply the mapping, so skip all resolution
	// when nothing scores. Retraction is deliberately not gated this way: the
	// event is transient, so dropping it leaves the score counted forever, and
	// disabling scoring on the last spawner is a routine config edit rather than
	// a teardown. A retraction therefore resolves and deletes on its own.
	var scoringSpawners []*kelos.TaskSpawner
	if !retract {
		spawners, err := h.getMatchingSpawners(ctx)
		if err != nil {
			log.Error(err, "Failed to list TaskSpawners for reaction scoring")
			return
		}
		for _, spawner := range spawners {
			if scoring.SlackReactionEnabled(spawner.Spec.When.Slack.Scoring) {
				scoringSpawners = append(scoringSpawners, spawner)
			}
		}
		if len(scoringSpawners) == 0 {
			return
		}
	}

	identity, err := h.resolveReactedResult(ctx, log, channel, itemTS)
	if err != nil {
		log.Error(err, "Failed to resolve Slack reaction to a Task")
		return
	}
	if identity == nil {
		// A reaction on a message that is not an agent result. Expected.
		log.V(1).Info("Slack reaction does not target an agent result")
		return
	}

	ev := scoring.Event{
		Task: *identity,
		Source: kelos.ScoreSource{
			Type:   kelos.ScoreSourceSlackReaction,
			Actor:  user,
			Signal: scoring.NormalizeReaction(reaction),
			URI:    scoring.SlackURI(channel, itemTS),
		},
		ObservedAt: parseSlackEventTime(eventTS),
	}

	// Retraction consults neither the verdict mapping nor any spawner: the score's
	// name depends on neither. A reaction can therefore be taken back after its
	// emoji was removed from the mapping, after the owning spawner was deleted, or
	// after scoring was disabled cluster-wide — scores outlive all three, and any
	// of them would otherwise leave the summary counting a withdrawn verdict.
	if retract {
		if err := scoring.Retract(ctx, h.client, ev); err != nil {
			log.Error(err, "Failed to retract task score", "task", identity.Name)
			return
		}
		log.Info("Retracted task score from Slack reaction", "task", identity.Name, "user", user)
		return
	}

	// Score against the spawner that created the Task, so one spawner's emoji
	// mapping cannot score another's results.
	spawner := spawnerByName(scoringSpawners, identity.Namespace, identity.SpawnerName)
	if spawner == nil {
		log.V(1).Info("No scoring TaskSpawner owns the reacted Task",
			"task", identity.Name, "spawner", identity.SpawnerName)
		return
	}

	verdict, ok := scoring.VerdictForSlackReaction(spawner.Spec.When.Slack.Scoring, reaction)
	if !ok {
		log.V(1).Info("Reaction is not mapped to a verdict", "spawner", spawner.Name)
		return
	}
	ev.Verdict = verdict

	if err := scoring.Record(ctx, h.client, ev); err != nil {
		log.Error(err, "Failed to record task score", "task", identity.Name)
		return
	}
	log.Info("Recorded task score from Slack reaction",
		"task", identity.Name, "spawner", spawner.Name, "verdict", verdict, "user", user)
}

// resolveReactedResult resolves the reacted message to a Task, retrying briefly
// when the message is new enough that the correlation labels may not have landed.
//
// The labels are written immediately after the result message is posted, but the
// write and its propagation to the read cache both take time, so a reaction on a
// just-posted message can resolve to nothing and be dropped. Retrying every
// unresolved reaction would mean retrying nearly every reaction in the workspace,
// since an unrelated message is indistinguishable from a not-yet-labelled one.
//
// The reacted message's own timestamp separates the two cases: labels for an older
// message are long since persisted, so a miss there really is an unrelated message,
// while a miss on a message posted seconds ago is plausibly the race. Only the
// latter retries.
func (h *SlackHandler) resolveReactedResult(ctx context.Context, log logr.Logger, channel, itemTS string) (*scoring.TaskIdentity, error) {
	identity, err := scoring.ResolveSlackResult(ctx, h.client, channel, itemTS)
	if err != nil || identity != nil {
		return identity, err
	}
	if !messagePostedWithin(itemTS, h.now(), resultLabelSettleWindow) {
		return nil, nil
	}

	for attempt := 1; attempt <= resultLabelResolveRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-h.after(resultLabelResolveBackoff):
		}

		identity, err = scoring.ResolveSlackResult(ctx, h.client, channel, itemTS)
		if err != nil || identity != nil {
			if identity != nil {
				log.V(1).Info("Resolved reacted result after retry", "attempt", attempt)
			}
			return identity, err
		}
	}
	return nil, nil
}

// messagePostedWithin reports whether a Slack message timestamp is no older than
// window. An unparseable timestamp is treated as not recent, so a malformed value
// cannot trigger retries.
func messagePostedWithin(itemTS string, now time.Time, window time.Duration) bool {
	posted := parseSlackEventTime(itemTS)
	if posted == nil {
		return false
	}
	age := now.Sub(posted.Time)
	return age >= -window && age <= window
}

// spawnerByName returns the spawner with the given namespace and name, or nil.
func spawnerByName(spawners []*kelos.TaskSpawner, namespace, name string) *kelos.TaskSpawner {
	if name == "" {
		return nil
	}
	for _, spawner := range spawners {
		if spawner.Namespace == namespace && spawner.Name == name {
			return spawner
		}
	}
	return nil
}

// parseSlackEventTime converts a Slack event_ts ("1712345678.123456") into a
// metav1.Time.
//
// It returns nil for an unparseable value rather than substituting the current
// time, so a missing observation time is visibly absent instead of silently
// wrong. The result is truncated to seconds because metav1.Time serializes as
// RFC3339 and would drop the fraction on write anyway; truncating here keeps the
// in-memory value equal to the stored one.
func parseSlackEventTime(eventTS string) *metav1.Time {
	secondsPart, fractionPart, _ := strings.Cut(eventTS, ".")
	seconds, err := strconv.ParseInt(secondsPart, 10, 64)
	if err != nil {
		return nil
	}
	// A present but non-numeric fraction means the value is not a Slack
	// timestamp, so reject it rather than silently reading only the seconds.
	if fractionPart != "" {
		if _, err := strconv.ParseUint(fractionPart, 10, 64); err != nil {
			return nil
		}
	}
	t := metav1.NewTime(time.Unix(seconds, 0).UTC())
	return &t
}
