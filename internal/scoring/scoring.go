// Package scoring turns signals from external actors into TaskScore records.
//
// Every integration (Slack reactions today, GitHub reviews later) normalizes its
// own vocabulary into a ScoreVerdict at this boundary and never leaks
// source-specific meaning to consumers. Adding a source means mapping its signal
// to a verdict and calling Record or Retract — the correlation, naming, and
// idempotency rules are shared here so sources cannot drift apart.
package scoring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// Label keys record the identity of the external artifact carrying a Task's
// result. They are labels rather than annotations because inbound scoring
// resolves an artifact back to its Task by selecting on them, and List accepts a
// label selector while annotations can only be filtered by reading every object.
//
// They are written on the Task when the result is delivered and copied onto the
// TaskRecord, which outlives the Task's TTL. Scores can arrive days later.
const (
	// LabelSlackResultChannel is the Slack channel ID the result was posted in.
	LabelSlackResultChannel = "kelos.dev/slack-result-channel"

	// LabelSlackResultTS is the Slack message timestamp of the message carrying
	// the result. Slack timestamps ("1712345678.123456") are valid label values.
	LabelSlackResultTS = "kelos.dev/slack-result-ts"

	// LabelTaskUID is set on TaskScores and names the UID of the scored Task. It
	// makes the scores for one Task selectable, which is how they are aggregated
	// without listing every score in the namespace.
	LabelTaskUID = "kelos.dev/task-uid"
)

// TaskIdentity is the durable identity of a scored Task. It is resolved from a
// live Task when one still exists and from its TaskRecord afterwards.
type TaskIdentity struct {
	Namespace   string
	Name        string
	UID         types.UID
	SpawnerName string
}

// Event is one normalized scoring observation, ready to be recorded.
type Event struct {
	Task       TaskIdentity
	Verdict    kelos.ScoreVerdict
	Source     kelos.ScoreSource
	ObservedAt *metav1.Time
}

// ResolveSlackResult finds the Task whose result was delivered as the Slack
// message identified by channel and ts.
//
// Live Tasks are checked first, then TaskRecords, so scoring keeps working after
// the Task's TTL removes it. It returns nil without error when nothing matches:
// a reaction on an unrelated message is the common case, not a failure.
func ResolveSlackResult(ctx context.Context, c client.Client, channel, ts string) (*TaskIdentity, error) {
	if channel == "" || ts == "" {
		return nil, nil
	}
	selector := client.MatchingLabels{
		LabelSlackResultChannel: channel,
		LabelSlackResultTS:      ts,
	}
	uri := SlackURI(channel, ts)

	var taskList kelos.TaskList
	if err := c.List(ctx, &taskList, selector); err != nil {
		return nil, fmt.Errorf("listing Tasks for Slack result %s: %w", uri, err)
	}
	if len(taskList.Items) > 1 {
		return nil, fmt.Errorf("Slack result %s matches %d Tasks, expected at most 1", uri, len(taskList.Items))
	}
	if len(taskList.Items) == 1 {
		task := &taskList.Items[0]
		return &TaskIdentity{
			Namespace:   task.Namespace,
			Name:        task.Name,
			UID:         task.UID,
			SpawnerName: task.Labels[taskbuilder.SpawnerLabel],
		}, nil
	}

	var recordList kelos.TaskRecordList
	if err := c.List(ctx, &recordList, selector); err != nil {
		return nil, fmt.Errorf("listing TaskRecords for Slack result %s: %w", uri, err)
	}
	if len(recordList.Items) > 1 {
		return nil, fmt.Errorf("Slack result %s matches %d TaskRecords, expected at most 1", uri, len(recordList.Items))
	}
	if len(recordList.Items) == 1 {
		record := &recordList.Items[0]
		return &TaskIdentity{
			Namespace:   record.Namespace,
			Name:        record.Spec.TaskRef.Name,
			UID:         record.Spec.TaskRef.UID,
			SpawnerName: record.Labels[taskbuilder.SpawnerLabel],
		}, nil
	}

	return nil, nil
}

// LabelMergePatch builds a merge patch body that adds the given labels without
// touching any other label on the object.
//
// A merge patch carries no resourceVersion, so a caller cannot conflict with a
// concurrent writer of different labels on the same object. The body is built by
// hand rather than marshalled so there is no error path to handle for what is a
// fixed shape over validated label strings.
func LabelMergePatch(labels map[string]string) []byte {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(`{"metadata":{"labels":{`)
	for i, key := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q:%q", key, labels[key])
	}
	b.WriteString(`}}}`)
	return []byte(b.String())
}

// SlackURI formats the opaque source locator for a Slack message.
func SlackURI(channel, ts string) string {
	return fmt.Sprintf("slack://%s/%s", channel, ts)
}

// ScoreName derives the TaskScore object name for a scoring event.
//
// The name is a pure function of the Task, source type, actor, and signal, which
// makes the write path idempotent under at-least-once event delivery and lets a
// retraction delete exactly the object its corresponding addition created.
// Notably it does not depend on the verdict, so a score can still be retracted
// after the configuration that produced its verdict has changed.
//
// Actor and signal are hashed rather than embedded because neither is
// constrained to the DNS label alphabet.
func ScoreName(taskUID types.UID, sourceType kelos.ScoreSourceType, actor, signal string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s", sourceType, actor, signal)))
	return fmt.Sprintf("%s-%s", taskUID, hex.EncodeToString(sum[:])[:10])
}

// Record creates the TaskScore for an event. A repeated delivery of the same
// event is a no-op and is not counted, so at-least-once event delivery does not
// inflate the aggregate.
func Record(ctx context.Context, c client.Client, ev Event) error {
	score := &kelos.TaskScore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ScoreName(ev.Task.UID, ev.Source.Type, ev.Source.Actor, ev.Source.Signal),
			Namespace: ev.Task.Namespace,
			Labels:    map[string]string{LabelTaskUID: string(ev.Task.UID)},
		},
		Spec: kelos.TaskScoreSpec{
			TaskRef: kelos.TaskReference{
				Name: ev.Task.Name,
				UID:  ev.Task.UID,
			},
			Verdict:    ev.Verdict,
			Source:     ev.Source,
			ObservedAt: ev.ObservedAt,
		},
	}
	if ev.Task.SpawnerName != "" {
		score.Labels[taskbuilder.SpawnerLabel] = ev.Task.SpawnerName
	}

	switch err := c.Create(ctx, score); {
	case err == nil:
		ev.observe(scoreRecordedTotal)
		return nil
	case apierrors.IsAlreadyExists(err):
		return nil
	default:
		return fmt.Errorf("creating TaskScore %s for task %s: %w", score.Name, ev.Task.Name, err)
	}
}

// Retract deletes the TaskScore an event created, for sources where an actor can
// take a score back (removing a Slack reaction). Deleting an absent score is a
// no-op.
//
// ev.Verdict need not be set: the object name does not depend on the verdict, so
// retraction works even when the configuration that produced the original verdict
// has since changed.
//
// The delete is unconditional rather than guarded by a read. Reads go through a
// cache, and a reaction added and removed inside the propagation window would
// otherwise look absent and strand the score with nothing to converge it. The
// read is only for the metric's verdict label, so its failure is tolerated and the
// label is left empty rather than skipping the delete.
func Retract(ctx context.Context, c client.Client, ev Event) error {
	name := ScoreName(ev.Task.UID, ev.Source.Type, ev.Source.Actor, ev.Source.Signal)

	retracted := ev
	var stored kelos.TaskScore
	if err := c.Get(ctx, client.ObjectKey{Namespace: ev.Task.Namespace, Name: name}, &stored); err == nil {
		retracted.Verdict = stored.Spec.Verdict
	}

	score := &kelos.TaskScore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ev.Task.Namespace},
	}
	switch err := c.Delete(ctx, score); {
	case err == nil:
		retracted.observe(scoreRetractedTotal)
		return nil
	case apierrors.IsNotFound(err):
		return nil
	default:
		return fmt.Errorf("deleting TaskScore %s for task %s: %w", name, ev.Task.Name, err)
	}
}

func (ev Event) observe(counter *prometheus.CounterVec) {
	counter.WithLabelValues(
		ev.Task.Namespace,
		ev.Task.SpawnerName,
		string(ev.Verdict),
		string(ev.Source.Type),
	).Inc()
}
