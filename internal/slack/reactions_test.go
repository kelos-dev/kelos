package slack

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/slack-go/slack/slackevents"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

const (
	testBotUserID  = "UBOT"
	testChannel    = "C123"
	testResultTS   = "1712345678.123456"
	testActorID    = "U777"
	testSpawner    = "reviewer"
	testTaskName   = "reviewer-slack-abc"
	testTaskUID    = "task-uid-1"
	testNamespace  = "agents"
	testEventTS    = "1712345699.654321"
	testTaskRecord = testTaskUID
)

func newReactionScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	return scheme
}

func scoringSpawner(reactions []kelos.SlackReactionScore) *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSpawner,
			Namespace: testNamespace,
			UID:       "spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				Slack: &kelos.Slack{
					Scoring: &kelos.SlackScoring{Reactions: reactions},
				},
			},
		},
	}
}

func resultTask() *kelos.Task {
	return &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testTaskName,
			Namespace: testNamespace,
			UID:       testTaskUID,
			Labels: map[string]string{
				scoring.LabelSlackResultChannel: testChannel,
				scoring.LabelSlackResultTS:      testResultTS,
				taskbuilder.SpawnerLabel:        testSpawner,
			},
		},
	}
}

func newReactionHandler(t *testing.T, objects ...client.Object) (*SlackHandler, client.Client) {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(newReactionScheme(t)).WithObjects(objects...).Build()
	return &SlackHandler{
		client:    cl,
		log:       logr.Discard(),
		botUserID: testBotUserID,
	}, cl
}

func listScores(t *testing.T, cl client.Client) []kelos.TaskScore {
	t.Helper()
	var scores kelos.TaskScoreList
	if err := cl.List(context.Background(), &scores); err != nil {
		t.Fatalf("listing TaskScores: %v", err)
	}
	return scores.Items
}

func reactionAdded(reaction, user string) *slackevents.ReactionAddedEvent {
	return &slackevents.ReactionAddedEvent{
		User:     user,
		Reaction: reaction,
		Item: slackevents.Item{
			Channel:   testChannel,
			Timestamp: testResultTS,
		},
		EventTimestamp: testEventTS,
	}
}

func TestHandleReactionAddedRecordsScore(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{
		{Name: "+1", Verdict: kelos.ScoreVerdictPositive},
		{Name: "-1", Verdict: kelos.ScoreVerdictNegative},
	})
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))

	scores := listScores(t, cl)
	if len(scores) != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", len(scores))
	}
	score := scores[0]

	if score.Namespace != testNamespace {
		t.Errorf("namespace = %q, want %q", score.Namespace, testNamespace)
	}
	if score.Spec.Verdict != kelos.ScoreVerdictPositive {
		t.Errorf("verdict = %q, want Positive", score.Spec.Verdict)
	}
	if score.Spec.TaskRef.Name != testTaskName || score.Spec.TaskRef.UID != testTaskUID {
		t.Errorf("taskRef = %+v, want %s/%s", score.Spec.TaskRef, testTaskName, testTaskUID)
	}
	if score.Labels[taskbuilder.SpawnerLabel] != testSpawner {
		t.Errorf("%s label = %q, want %q", taskbuilder.SpawnerLabel, score.Labels[taskbuilder.SpawnerLabel], testSpawner)
	}
	if score.Spec.Source.Type != kelos.ScoreSourceSlackReaction {
		t.Errorf("source type = %q, want SlackReaction", score.Spec.Source.Type)
	}
	if score.Spec.Source.Actor != testActorID {
		t.Errorf("source actor = %q, want %q", score.Spec.Source.Actor, testActorID)
	}
	if score.Spec.Source.Signal != "+1" {
		t.Errorf("source signal = %q, want +1", score.Spec.Source.Signal)
	}
	if want := "slack://" + testChannel + "/" + testResultTS; score.Spec.Source.URI != want {
		t.Errorf("source uri = %q, want %q", score.Spec.Source.URI, want)
	}
	if score.Spec.ObservedAt == nil {
		t.Fatal("observedAt = nil, want the Slack event timestamp")
	}
	if got := score.Spec.ObservedAt.Unix(); got != 1712345699 {
		t.Errorf("observedAt = %d, want 1712345699", got)
	}
}

func TestHandleReactionAddedRecordsNegativeVerdict(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{
		{Name: "+1", Verdict: kelos.ScoreVerdictPositive},
		{Name: "-1", Verdict: kelos.ScoreVerdictNegative},
	})
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("-1", testActorID))

	scores := listScores(t, cl)
	if len(scores) != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", len(scores))
	}
	if scores[0].Spec.Verdict != kelos.ScoreVerdictNegative {
		t.Errorf("verdict = %q, want Negative", scores[0].Spec.Verdict)
	}
}

// Scores must still land after the Task's TTL has removed it, resolved through
// the TaskRecord instead.
func TestHandleReactionAddedResolvesThroughTaskRecord(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}})
	record := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testTaskRecord,
			Namespace: testNamespace,
			Labels: map[string]string{
				scoring.LabelSlackResultChannel: testChannel,
				scoring.LabelSlackResultTS:      testResultTS,
				taskbuilder.SpawnerLabel:        testSpawner,
			},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef: kelos.TaskReference{Name: testTaskName, UID: testTaskUID},
		},
	}
	h, cl := newReactionHandler(t, spawner, record)

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))

	scores := listScores(t, cl)
	if len(scores) != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", len(scores))
	}
	if scores[0].Spec.TaskRef.UID != testTaskUID {
		t.Errorf("taskRef UID = %q, want %q", scores[0].Spec.TaskRef.UID, testTaskUID)
	}
}

func TestHandleReactionRemovedRetractsScore(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}})
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))
	if got := len(listScores(t, cl)); got != 1 {
		t.Fatalf("recorded %d TaskScores before retraction, want 1", got)
	}

	h.handleReactionRemoved(context.Background(), &slackevents.ReactionRemovedEvent{
		User:     testActorID,
		Reaction: "+1",
		Item: slackevents.Item{
			Channel:   testChannel,
			Timestamp: testResultTS,
		},
		EventTimestamp: testEventTS,
	})

	if got := len(listScores(t, cl)); got != 0 {
		t.Errorf("%d TaskScores remain after retraction, want 0", got)
	}
}

func TestHandleReactionIgnored(t *testing.T) {
	tests := []struct {
		name      string
		spawner   *kelos.TaskSpawner
		objects   []client.Object
		reaction  string
		user      string
		channel   string
		timestamp string
	}{
		{
			name:     "reaction from the bot itself is not external feedback",
			spawner:  scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}),
			objects:  []client.Object{resultTask()},
			reaction: "+1",
			user:     testBotUserID,
		},
		{
			name:     "unmapped reaction",
			spawner:  scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}),
			objects:  []client.Object{resultTask()},
			reaction: "eyes",
			user:     testActorID,
		},
		{
			name:     "spawner has no scoring configured",
			spawner:  &kelos.TaskSpawner{ObjectMeta: metav1.ObjectMeta{Name: testSpawner, Namespace: testNamespace}, Spec: kelos.TaskSpawnerSpec{When: kelos.When{Slack: &kelos.Slack{}}}},
			objects:  []client.Object{resultTask()},
			reaction: "+1",
			user:     testActorID,
		},
		{
			name:     "reaction on a message that is not an agent result",
			spawner:  scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}),
			objects:  nil,
			reaction: "+1",
			user:     testActorID,
		},
		{
			name:    "result Task belongs to a spawner that does not score",
			spawner: scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}),
			objects: []client.Object{&kelos.Task{ObjectMeta: metav1.ObjectMeta{
				Name:      "other-task",
				Namespace: testNamespace,
				UID:       "other-uid",
				Labels: map[string]string{
					scoring.LabelSlackResultChannel: testChannel,
					scoring.LabelSlackResultTS:      testResultTS,
					taskbuilder.SpawnerLabel:        "some-other-spawner",
				},
			}}},
			reaction: "+1",
			user:     testActorID,
		},
		{
			name:     "reaction with no channel",
			spawner:  scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}),
			objects:  []client.Object{resultTask()},
			reaction: "+1",
			user:     testActorID,
			channel:  "-",
		},
		{
			name:      "reaction with no message timestamp",
			spawner:   scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}),
			objects:   []client.Object{resultTask()},
			reaction:  "+1",
			user:      testActorID,
			timestamp: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := append([]client.Object{tt.spawner}, tt.objects...)
			h, cl := newReactionHandler(t, objects...)

			evt := reactionAdded(tt.reaction, tt.user)
			if tt.channel == "-" {
				evt.Item.Channel = ""
			}
			if tt.timestamp == "-" {
				evt.Item.Timestamp = ""
			}

			h.handleReactionAdded(context.Background(), evt)

			if got := len(listScores(t, cl)); got != 0 {
				t.Errorf("recorded %d TaskScores, want 0", got)
			}
		})
	}
}

// A reaction on one spawner's result must be scored with that spawner's emoji
// mapping, not another spawner's.
func TestHandleReactionUsesOwningSpawnerMapping(t *testing.T) {
	owning := scoringSpawner([]kelos.SlackReactionScore{{Name: "tada", Verdict: kelos.ScoreVerdictNegative}})
	other := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "other-spawner", Namespace: testNamespace},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{Slack: &kelos.Slack{
				Scoring: &kelos.SlackScoring{
					Reactions: []kelos.SlackReactionScore{{Name: "tada", Verdict: kelos.ScoreVerdictPositive}},
				},
			}},
		},
	}
	h, cl := newReactionHandler(t, owning, other, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("tada", testActorID))

	scores := listScores(t, cl)
	if len(scores) != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", len(scores))
	}
	if scores[0].Spec.Verdict != kelos.ScoreVerdictNegative {
		t.Errorf("verdict = %q, want Negative from the owning spawner's mapping", scores[0].Spec.Verdict)
	}
}

func TestParseSlackEventTime(t *testing.T) {
	tests := []struct {
		name     string
		eventTS  string
		wantNil  bool
		wantUnix int64
		wantNano int
	}{
		{name: "microsecond fraction is truncated to seconds", eventTS: "1712345678.123456", wantUnix: 1712345678},
		{name: "no fraction", eventTS: "1712345678", wantUnix: 1712345678},
		{name: "trailing dot", eventTS: "1712345678.", wantUnix: 1712345678},
		{name: "empty", eventTS: "", wantNil: true},
		{name: "not a number", eventTS: "not-a-timestamp", wantNil: true},
		{name: "non-numeric fraction", eventTS: "1712345678.abc", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSlackEventTime(tt.eventTS)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("parseSlackEventTime(%q) = %v, want nil", tt.eventTS, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseSlackEventTime(%q) = nil, want a time", tt.eventTS)
			}
			if got.Unix() != tt.wantUnix {
				t.Errorf("parseSlackEventTime(%q).Unix() = %d, want %d", tt.eventTS, got.Unix(), tt.wantUnix)
			}
			if got.Nanosecond() != tt.wantNano {
				t.Errorf("parseSlackEventTime(%q).Nanosecond() = %d, want %d", tt.eventTS, got.Nanosecond(), tt.wantNano)
			}
		})
	}
}

// An operator who moves an emoji out of the mapping must not strand scores
// already recorded with it: removing the reaction still has to delete the score,
// or the summary counts a withdrawn verdict forever.
func TestHandleReactionRemovedRetractsAfterMappingChange(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "tada", Verdict: kelos.ScoreVerdictPositive}})
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("tada", testActorID))
	if got := len(listScores(t, cl)); got != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", got)
	}

	// The operator drops "tada" from the mapping and keeps only "+1".
	var updated kelos.TaskSpawner
	key := client.ObjectKey{Namespace: testNamespace, Name: testSpawner}
	if err := cl.Get(context.Background(), key, &updated); err != nil {
		t.Fatalf("getting spawner: %v", err)
	}
	updated.Spec.When.Slack.Scoring.Reactions = []kelos.SlackReactionScore{
		{Name: "+1", Verdict: kelos.ScoreVerdictPositive},
	}
	if err := cl.Update(context.Background(), &updated); err != nil {
		t.Fatalf("updating spawner mapping: %v", err)
	}

	h.handleReactionRemoved(context.Background(), &slackevents.ReactionRemovedEvent{
		User:     testActorID,
		Reaction: "tada",
		Item: slackevents.Item{
			Channel:   testChannel,
			Timestamp: testResultTS,
		},
		EventTimestamp: testEventTS,
	})

	if got := len(listScores(t, cl)); got != 0 {
		t.Errorf("%d TaskScores remain after retracting an unmapped reaction, want 0", got)
	}
}

// Scores outlive the spawner that produced them, so retraction must not depend on
// the spawner still existing — otherwise deleting a spawner freezes every score
// its Tasks collected.
func TestHandleReactionRemovedRetractsAfterSpawnerDeleted(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}})
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))
	if got := len(listScores(t, cl)); got != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", got)
	}

	// The only scoring spawner in the cluster is deleted; its Tasks, records, and
	// scores survive. Nothing is left to supply a verdict mapping, which must not
	// prevent the actor from taking their reaction back.
	if err := cl.Delete(context.Background(), spawner); err != nil {
		t.Fatalf("deleting owning spawner: %v", err)
	}

	h.handleReactionRemoved(context.Background(), &slackevents.ReactionRemovedEvent{
		User:     testActorID,
		Reaction: "+1",
		Item: slackevents.Item{
			Channel:   testChannel,
			Timestamp: testResultTS,
		},
		EventTimestamp: testEventTS,
	})

	if got := len(listScores(t, cl)); got != 0 {
		t.Errorf("%d TaskScores remain after the owning spawner was deleted, want 0", got)
	}
}

// Turning scoring off is a routine config edit, not a teardown. The retraction
// event is transient, so if it is dropped while scoring is disabled the score is
// counted forever with nothing to converge it.
func TestHandleReactionRemovedRetractsAfterScoringDisabled(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}})
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))
	if got := len(listScores(t, cl)); got != 1 {
		t.Fatalf("recorded %d TaskScores, want 1", got)
	}

	var updated kelos.TaskSpawner
	key := client.ObjectKey{Namespace: testNamespace, Name: testSpawner}
	if err := cl.Get(context.Background(), key, &updated); err != nil {
		t.Fatalf("getting spawner: %v", err)
	}
	updated.Spec.When.Slack.Scoring = nil
	if err := cl.Update(context.Background(), &updated); err != nil {
		t.Fatalf("disabling scoring: %v", err)
	}

	h.handleReactionRemoved(context.Background(), &slackevents.ReactionRemovedEvent{
		User:     testActorID,
		Reaction: "+1",
		Item: slackevents.Item{
			Channel:   testChannel,
			Timestamp: testResultTS,
		},
		EventTimestamp: testEventTS,
	})

	if got := len(listScores(t, cl)); got != 0 {
		t.Errorf("%d TaskScores remain after scoring was disabled, want 0", got)
	}
}

// With no scoring configured anywhere, an added reaction must still be a no-op.
func TestHandleReactionAddedIgnoredWhenNothingScores(t *testing.T) {
	spawner := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: testSpawner, Namespace: testNamespace},
		Spec:       kelos.TaskSpawnerSpec{When: kelos.When{Slack: &kelos.Slack{}}},
	}
	h, cl := newReactionHandler(t, spawner, resultTask())

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))

	if got := len(listScores(t, cl)); got != 0 {
		t.Errorf("recorded %d TaskScores, want 0", got)
	}
}

// A reaction on a just-posted result message can arrive before the correlation
// labels are readable. Losing it silently is the failure this retry exists to
// prevent, so the labels are made visible only after the first attempt misses.
func TestHandleReactionAddedRetriesWhileLabelsSettle(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}})
	unlabelled := resultTask()
	delete(unlabelled.Labels, scoring.LabelSlackResultChannel)
	delete(unlabelled.Labels, scoring.LabelSlackResultTS)

	h, cl := newReactionHandler(t, spawner, unlabelled)

	// The reacted message is "now", so the miss is treated as a possible race.
	posted, err := strconv.ParseInt(strings.SplitN(testResultTS, ".", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parsing test timestamp: %v", err)
	}
	h.nowFunc = func() time.Time { return time.Unix(posted, 0) }

	// Each simulated backoff lands the labels that the reporter would have written.
	h.afterFunc = func(time.Duration) <-chan time.Time {
		var task kelos.Task
		key := client.ObjectKey{Namespace: testNamespace, Name: testTaskName}
		if err := cl.Get(context.Background(), key, &task); err == nil {
			if task.Labels == nil {
				task.Labels = map[string]string{}
			}
			task.Labels[scoring.LabelSlackResultChannel] = testChannel
			task.Labels[scoring.LabelSlackResultTS] = testResultTS
			_ = cl.Update(context.Background(), &task)
		}
		fired := make(chan time.Time, 1)
		fired <- time.Unix(posted, 0)
		return fired
	}

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))

	scores := listScores(t, cl)
	if len(scores) != 1 {
		t.Fatalf("recorded %d TaskScores, want the reaction to land after a retry", len(scores))
	}
	if scores[0].Spec.Verdict != kelos.ScoreVerdictPositive {
		t.Errorf("verdict = %q, want Positive", scores[0].Spec.Verdict)
	}
}

// An older message that does not resolve is an ordinary reaction on something
// unrelated, so it must not spend retries — otherwise nearly every reaction in the
// workspace would retry.
func TestHandleReactionAddedDoesNotRetryOldMessage(t *testing.T) {
	spawner := scoringSpawner([]kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}})
	h, cl := newReactionHandler(t, spawner)

	posted, err := strconv.ParseInt(strings.SplitN(testResultTS, ".", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parsing test timestamp: %v", err)
	}
	h.nowFunc = func() time.Time { return time.Unix(posted, 0).Add(time.Hour) }

	retries := 0
	h.afterFunc = func(time.Duration) <-chan time.Time {
		retries++
		fired := make(chan time.Time, 1)
		fired <- time.Now()
		return fired
	}

	h.handleReactionAdded(context.Background(), reactionAdded("+1", testActorID))

	if retries != 0 {
		t.Errorf("retried %d times for an hour-old message, want 0", retries)
	}
	if got := len(listScores(t, cl)); got != 0 {
		t.Errorf("recorded %d TaskScores, want 0", got)
	}
}

func TestMessagePostedWithin(t *testing.T) {
	base := time.Unix(1712345678, 0)
	tests := []struct {
		name   string
		itemTS string
		now    time.Time
		want   bool
	}{
		{name: "same second", itemTS: "1712345678.123456", now: base, want: true},
		{name: "just inside the window", itemTS: "1712345678.123456", now: base.Add(90 * time.Second), want: true},
		{name: "outside the window", itemTS: "1712345678.123456", now: base.Add(10 * time.Minute), want: false},
		{name: "clock skew puts it slightly ahead", itemTS: "1712345678.123456", now: base.Add(-30 * time.Second), want: true},
		{name: "unparseable is never recent", itemTS: "not-a-timestamp", now: base, want: false},
		{name: "empty is never recent", itemTS: "", now: base, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messagePostedWithin(tt.itemTS, tt.now, resultLabelSettleWindow); got != tt.want {
				t.Errorf("messagePostedWithin(%q) = %v, want %v", tt.itemTS, got, tt.want)
			}
		})
	}
}
