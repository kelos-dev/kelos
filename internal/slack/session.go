package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
	"github.com/kelos-dev/kelos/internal/sessionturn"
)

const (
	// DefaultMaxQueuedTurns is the default bound on how many unstarted messages
	// one Slack thread may hold. Beyond it the thread is told the request was
	// dropped rather than silently accumulating work nobody is waiting for any
	// more.
	DefaultMaxQueuedTurns = 8

	// defaultReadyTimeout bounds the wait for a newly created Session to start
	// its Pod and report Ready.
	defaultReadyTimeout = 10 * time.Minute

	// slackPostTimeout bounds one Slack write made by the bridge.
	slackPostTimeout = 10 * time.Second

	// AnnotationSlackThreadContextSent marks a Session that has accepted a turn
	// carrying its Slack thread's context. Until it is set, every turn resends
	// that context, so a first turn lost to a cold start or a dropped stream
	// does not leave the Session without the conversation it belongs to.
	AnnotationSlackThreadContextSent = "kelos.dev/slack-thread-context-sent"

	// shutdownNoticeTimeout bounds the Slack writes made while the bridge is
	// draining, so a slow Slack API cannot hold up the whole process exit.
	shutdownNoticeTimeout = 15 * time.Second

	// shutdownNoticeConcurrency bounds how many shutdown notices are in flight
	// at once, so a large drain does not arrive as a burst Slack rate-limits.
	shutdownNoticeConcurrency = 8

	// maxPromptAuthorLength bounds the author name copied into a prompt, since
	// it is free text from the author's own Slack profile.
	maxPromptAuthorLength = 80

	// shutdownNotice tells a thread that its turn did not survive a restart.
	shutdownNotice = "The Slack server restarted before this turn finished. Send another message to continue — the conversation still has its history."

	// maxHandledMessages bounds the record of already-accepted Slack messages
	// kept for redelivery detection.
	maxHandledMessages = 4096

	// placeholderEditAttempts is how many times the placeholder edit carrying a
	// turn's reply is tried before the reply is posted as a new message instead.
	placeholderEditAttempts = 2
	// placeholderEditRetryDelay separates those attempts.
	placeholderEditRetryDelay = time.Second
)

// TurnDriver submits one prompt to a Session runtime and returns its reply.
type TurnDriver interface {
	Submit(ctx context.Context, namespace, podName, prompt string) (sessionturn.Result, error)
}

// ThreadPoster posts and edits Slack thread replies. *reporting.SlackReporter
// implements it.
type ThreadPoster interface {
	PostThreadReply(ctx context.Context, channel, threadTS string, msg reporting.SlackMessage) (string, error)
	UpdateMessage(ctx context.Context, channel, messageTS string, msg reporting.SlackMessage) error
}

// SessionBridge maps a Slack thread to a persistent Session and drives one
// conversation turn per message in that thread.
//
// Every thread gets its own serial queue: a message that arrives while the
// thread's previous turn is still running waits for it rather than opening a
// second stream into the same Session. Different threads run concurrently.
type SessionBridge struct {
	client client.Client
	driver TurnDriver
	poster ThreadPoster
	log    logr.Logger

	// maxQueuedTurns bounds how many unstarted messages one thread may hold.
	maxQueuedTurns int
	// readyTimeout bounds the wait for a Session to become Ready.
	readyTimeout time.Duration
	// editRetryDelay separates the attempts at editing the placeholder.
	editRetryDelay time.Duration
	// postTimeout bounds one Slack write.
	postTimeout time.Duration
	// waitReady resolves a Ready Session to its Pod name. Tests replace it.
	waitReady func(ctx context.Context, cl client.Client, key client.ObjectKey) (string, error)
	// acknowledgeResume records that a client connected after an idle resume.
	acknowledgeResume func(ctx context.Context, cl client.Client, key client.ObjectKey) error

	mu      sync.Mutex
	threads map[string]*threadQueue
	// inflight records the placeholder posted for each thread whose turn is
	// running, so a shutdown can resolve it instead of leaving it mid-sentence.
	inflight map[string]*inflightTurn
	// draining stops new work once the bridge has begun shutting down.
	draining bool
	// handled records the Slack messages already accepted so a Socket Mode
	// redelivery does not run the same request twice. It lives in the process
	// only: a restart forgets it, which is acceptable because Slack redelivers
	// a message within seconds of the original.
	handled map[string]struct{}
	// handledOrder is insertion order for handled, so the oldest entries can be
	// evicted once the record reaches maxHandledMessages.
	handledOrder []string
}

// threadQueue holds the messages waiting on one Slack thread's Session.
type threadQueue struct {
	jobs    []*turnJob
	running bool
}

// inflightTurn tracks one dequeued turn so that either the turn itself or a
// shutdown — whichever claims it first — has the last word in the thread, and
// never both.
//
// It is registered when the turn is dequeued rather than when its placeholder
// is posted. A turn registered only at the placeholder would be invisible to a
// shutdown for the whole time it spends resolving its Session, and the message
// would be dropped without telling the thread.
type inflightTurn struct {
	channelID string
	threadTS  string

	mu sync.Mutex
	// placeholderTS is the reply to edit, empty until one has been posted.
	placeholderTS string
	// resolved records that someone has committed to writing the final message.
	resolved bool
}

// wasResolved reports whether anyone has spoken for this turn.
func (t *inflightTurn) wasResolved() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resolved
}

// claim takes responsibility for the thread's final message, reporting the
// placeholder to edit and whether the caller won. A caller that loses must stay
// silent, because the winner has already spoken for this turn.
func (t *inflightTurn) claim() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved {
		return "", false
	}
	t.resolved = true
	return t.placeholderTS, true
}

// postPlaceholder posts the turn's placeholder while holding the turn's lock,
// so a concurrent claim either observes the placeholder or knows none is
// coming. It returns an empty timestamp when the turn was already claimed, in
// which case no placeholder is posted and none is left orphaned.
func (t *inflightTurn) postPlaceholder(post func() string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved {
		return ""
	}
	t.placeholderTS = post()
	return t.placeholderTS
}

// turnJob is one Slack message waiting to be driven as a conversation turn.
type turnJob struct {
	ctx     context.Context
	spawner *kelos.SessionSpawner
	msg     *SlackMessageData
}

// SessionBridgeOptions carries the operator-tunable limits of a SessionBridge.
type SessionBridgeOptions struct {
	// MaxQueuedTurns bounds how many unstarted messages one Slack thread may
	// hold. Zero selects DefaultMaxQueuedTurns.
	MaxQueuedTurns int
}

// NewSessionBridge creates a bridge that drives turns through driver and
// reports them through poster.
func NewSessionBridge(cl client.Client, driver TurnDriver, poster ThreadPoster, log logr.Logger, options SessionBridgeOptions) *SessionBridge {
	maxQueuedTurns := options.MaxQueuedTurns
	if maxQueuedTurns <= 0 {
		maxQueuedTurns = DefaultMaxQueuedTurns
	}
	return &SessionBridge{
		client:         cl,
		driver:         driver,
		poster:         poster,
		log:            log,
		maxQueuedTurns: maxQueuedTurns,
		readyTimeout:   defaultReadyTimeout,
		editRetryDelay: placeholderEditRetryDelay,
		postTimeout:    slackPostTimeout,
		waitReady: func(ctx context.Context, cl client.Client, key client.ObjectKey) (string, error) {
			return sessionturn.WaitReady(ctx, cl, key, 0)
		},
		acknowledgeResume: sessionturn.AcknowledgeResume,
		threads:           map[string]*threadQueue{},
		inflight:          map[string]*inflightTurn{},
		handled:           map[string]struct{}{},
	}
}

// Enqueue accepts a Slack message for the given SessionSpawner and returns
// once it is queued. The turn itself runs in the background, because a turn
// takes minutes and the Socket Mode listener must stay responsive.
func (b *SessionBridge) Enqueue(ctx context.Context, spawner *kelos.SessionSpawner, msg *SlackMessageData) {
	key := ThreadKey(msg)
	if key == "" {
		b.log.V(1).Info("Skipping Slack message with no thread identity", "channel", msg.ChannelID)
		return
	}
	queueKey := spawner.Namespace + "/" + spawner.Name + "/" + key
	job := &turnJob{ctx: ctx, spawner: spawner, msg: msg}

	messageKey := queueKey + "/" + msg.Timestamp

	b.mu.Lock()
	if b.draining {
		b.mu.Unlock()
		b.log.Info("Slack server is shutting down, dropping message",
			"spawner", spawner.Name, "channel", msg.ChannelID, "thread", key)
		return
	}
	if _, seen := b.handled[messageKey]; seen {
		b.mu.Unlock()
		b.log.V(1).Info("Skipping a Slack message already accepted for this thread",
			"spawner", spawner.Name, "channel", msg.ChannelID, "thread", key, "timestamp", msg.Timestamp)
		return
	}
	queue := b.threads[queueKey]
	if queue == nil {
		queue = &threadQueue{}
		b.threads[queueKey] = queue
	}
	if len(queue.jobs) >= b.maxQueuedTurns {
		b.mu.Unlock()
		b.log.Info("Slack thread turn queue is full, dropping message",
			"spawner", spawner.Name, "channel", msg.ChannelID, "thread", key)
		b.postDropped(ctx, msg)
		return
	}
	queue.jobs = append(queue.jobs, job)
	b.rememberHandled(messageKey)
	start := !queue.running
	queue.running = true
	b.mu.Unlock()

	if start {
		go b.drain(queueKey)
	}
}

// rememberHandled records an accepted message and evicts the oldest record
// once the bound is reached. The caller must hold b.mu.
func (b *SessionBridge) rememberHandled(messageKey string) {
	b.handled[messageKey] = struct{}{}
	b.handledOrder = append(b.handledOrder, messageKey)
	if len(b.handledOrder) > maxHandledMessages {
		delete(b.handled, b.handledOrder[0])
		b.handledOrder = b.handledOrder[1:]
	}
}

// drain runs the queued turns for one thread, in arrival order, until none are
// left. The queue entry is removed under the same lock that guards appends, so
// a message arriving as the queue empties always starts a new drain.
func (b *SessionBridge) drain(queueKey string) {
	for {
		b.mu.Lock()
		queue := b.threads[queueKey]
		if queue == nil || len(queue.jobs) == 0 {
			delete(b.threads, queueKey)
			b.mu.Unlock()
			return
		}
		job := queue.jobs[0]
		queue.jobs = queue.jobs[1:]
		// Registering here, under the same lock that pops the job, closes the
		// window in which a turn belongs to neither the queue nor the in-flight
		// set and a shutdown would miss it entirely.
		turn := &inflightTurn{channelID: job.msg.ChannelID, threadTS: ThreadTimestamp(job.msg)}
		b.inflight[queueKey] = turn
		b.mu.Unlock()

		b.runTurn(job, turn)
		if !turn.wasResolved() {
			// The turn declined to claim and left itself for drainOnShutdown,
			// which is the goroutine the manager waits for. Deregistering it
			// would hide it from that drain — and so would taking the next job,
			// because that would overwrite this turn's in-flight record. Stop
			// here and let the drain speak for the whole thread.
			return
		}
		b.endInflight(queueKey)
	}
}

// runTurn resolves the thread's Session, drives one turn and reports the reply
// into the thread. Every failure path reports into the thread as well, so a
// turn that fails resolves its placeholder rather than leaving it mid-sentence.
// A graceful shutdown resolves it too, through drainOnShutdown; only a killed
// process leaves a placeholder unresolved.
func (b *SessionBridge) runTurn(job *turnJob, turn *inflightTurn) {
	msg := job.msg
	threadTS := ThreadTimestamp(msg)
	sessionName := SessionName(job.spawner.Name, job.spawner.UID, msg.ChannelID, threadTS)
	log := b.log.WithValues(
		"sessionSpawner", job.spawner.Name,
		"namespace", job.spawner.Namespace,
		"session", sessionName,
		"channel", msg.ChannelID,
		"thread", threadTS,
	)

	session, created, err := b.ensureSession(job.ctx, job.spawner, msg, sessionName)
	if err != nil {
		log.Error(err, "Failed to resolve Session for Slack thread")
		b.finish(job.ctx, msg, turn, reporting.FormatSessionReply("", err.Error(), "", sessionName))
		return
	}
	if created {
		log.Info("Created Session for Slack thread")
	}

	turn.postPlaceholder(func() string { return b.postWorking(job.ctx, msg, sessionName) })
	key := client.ObjectKeyFromObject(session)

	readyCtx, cancelReady := context.WithTimeout(job.ctx, b.readyTimeout)
	podName, err := b.waitReady(readyCtx, b.client, key)
	cancelReady()
	if err != nil {
		log.Error(err, "Session did not become ready")
		b.finish(job.ctx, msg, turn, reporting.FormatSessionReply("", err.Error(), "", sessionName))
		return
	}
	if err := b.acknowledgeResume(job.ctx, b.client, key); err != nil {
		// The Session is running; failing to record the acknowledgement only
		// shortens its protection from being suspended again.
		log.Error(err, "Failed to acknowledge Session idle resume")
	}

	// The thread's context goes with every turn until one is known to have
	// reached the Session. Deciding this from `created` would skip it forever
	// once a first turn failed after the Session existed but before it ran.
	contextSent := session.Annotations[AnnotationSlackThreadContextSent] == "true"
	prompt := turnPrompt(msg, !contextSent)
	log.Info("Driving Session turn for Slack message", "pod", podName, "user", msg.UserID)
	result, err := b.driver.Submit(job.ctx, session.Namespace, podName, prompt)
	if err != nil {
		log.Error(err, "Session turn failed")
		b.finish(job.ctx, msg, turn, reporting.FormatSessionReply(result.Text, err.Error(), declinedQuestionsNote(result), sessionName))
		return
	}
	if !contextSent {
		if err := b.markThreadContextSent(job.ctx, key); err != nil {
			// Worst case the next turn resends the thread context, which the
			// Session already has. That is cheap next to losing it entirely.
			log.Error(err, "Failed to record that the Slack thread context reached the Session")
		}
	}

	// result.Message can hold an error the turn recovered from — Codex records
	// one for a retried request — so it is only worth reporting when the turn
	// did not actually complete.
	errorText := ""
	if !result.Succeeded() {
		errorText = result.Message
		if errorText == "" {
			errorText = fmt.Sprintf("The turn ended as %s", result.Status)
		}
	}
	log.Info("Session turn finished", "turn", result.TurnID, "status", result.Status,
		"declinedQuestions", result.DeclinedQuestions)
	b.finish(job.ctx, msg, turn, reporting.FormatSessionReply(result.Text, errorText, declinedQuestionsNote(result), sessionName))
}

// declinedQuestionsNote explains a turn in which the agent asked something a
// Slack thread could not answer, so a reply that starts from an assumption does
// not look arbitrary.
func declinedQuestionsNote(result sessionturn.Result) string {
	switch result.DeclinedQuestions {
	case 0:
		return ""
	case 1:
		return "The agent asked a question. A thread cannot answer one mid-turn, so it was declined and the agent continued."
	default:
		return fmt.Sprintf("The agent asked %d questions. A thread cannot answer one mid-turn, so they were declined and the agent continued.", result.DeclinedQuestions)
	}
}

// finish writes the thread's final message unless a shutdown already claimed
// the turn and said its piece.
//
// A turn runs on the Socket Mode listener's context, which the manager cancels
// at the same moment as the bridge's own. Claiming the turn with that context
// already cancelled would consume the claim and then fail every Slack write,
// leaving the thread on its placeholder. Declining to claim hands the turn to
// drainOnShutdown, which writes on a context detached from the cancellation.
func (b *SessionBridge) finish(ctx context.Context, msg *SlackMessageData, turn *inflightTurn, messages []reporting.SlackMessage) {
	if ctx.Err() != nil {
		b.log.Info("Leaving an interrupted turn for the shutdown drain", "channel", msg.ChannelID)
		return
	}
	placeholderTS, won := turn.claim()
	if !won {
		return
	}
	b.report(ctx, msg, placeholderTS, messages)
}

// markThreadContextSent records that a turn carrying the thread's context has
// reached the Session, so later turns can send only the new message.
func (b *SessionBridge) markThreadContextSent(ctx context.Context, key client.ObjectKey) error {
	var session kelos.Session
	if err := b.client.Get(ctx, key, &session); err != nil {
		return fmt.Errorf("getting Session %s: %w", key.Name, err)
	}
	if session.Annotations[AnnotationSlackThreadContextSent] == "true" {
		return nil
	}
	patched := session.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = map[string]string{}
	}
	patched.Annotations[AnnotationSlackThreadContextSent] = "true"
	if err := b.client.Patch(ctx, patched, client.MergeFrom(&session)); err != nil {
		return fmt.Errorf("annotating Session %s: %w", key.Name, err)
	}
	return nil
}

// ensureSession returns the Session backing this thread, creating it from the
// spawner template the first time the thread is seen. It reports whether this
// call created it.
func (b *SessionBridge) ensureSession(ctx context.Context, spawner *kelos.SessionSpawner, msg *SlackMessageData, sessionName string) (*kelos.Session, bool, error) {
	key := client.ObjectKey{Namespace: spawner.Namespace, Name: sessionName}
	var existing kelos.Session
	if err := b.client.Get(ctx, key, &existing); err == nil {
		// SessionName hashes the spawner's name and UID, so a Session under this
		// name should always be this spawner's. This guards the remaining ways
		// it might not be — a hash collision, or an object created by hand —
		// because driving a turn in a Session this spawner does not own would
		// reach the wrong runtime.
		if owner := existing.Labels[sessionbuilder.LabelSessionSpawner]; owner != string(spawner.UID) {
			return nil, false, fmt.Errorf("Session %s belongs to another SessionSpawner and cannot be reused for this thread", sessionName)
		}
		return &existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("getting Session %s: %w", sessionName, err)
	}

	gvks, _, err := b.client.Scheme().ObjectKinds(spawner)
	if err != nil {
		return nil, false, fmt.Errorf("getting SessionSpawner %s GVK: %w", spawner.Name, err)
	}
	if len(gvks) == 0 {
		return nil, false, fmt.Errorf("getting SessionSpawner %s GVK: no registered kind", spawner.Name)
	}

	session, err := sessionbuilder.Build(
		sessionName,
		spawner.Namespace,
		&spawner.Spec.SessionTemplate,
		ExtractSlackWorkItem(msg),
		sessionbuilder.SpawnerRef{
			Name:       spawner.Name,
			UID:        spawner.UID,
			APIVersion: gvks[0].GroupVersion().String(),
			Kind:       gvks[0].Kind,
		},
	)
	if err != nil {
		return nil, false, fmt.Errorf("building Session %s: %w", sessionName, err)
	}
	if err := sessionbuilder.AssignSpawnerCredential(spawner, session); err != nil {
		return nil, false, fmt.Errorf("assigning SessionSpawner credential to Session %s: %w", sessionName, err)
	}
	if session.Annotations == nil {
		session.Annotations = map[string]string{}
	}
	session.Annotations[reporting.AnnotationSlackChannel] = msg.ChannelID
	session.Annotations[reporting.AnnotationSlackThreadTS] = ThreadTimestamp(msg)
	session.Annotations[reporting.AnnotationSlackUserID] = msg.UserID

	if err := b.client.Create(ctx, session); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// An earlier message in this thread already created it. Re-reading
			// it would go through the same cache the Get above missed, so keep
			// the built object: only its name and namespace are used from here.
			return session, false, nil
		}
		return nil, false, fmt.Errorf("creating Session %s: %w", sessionName, err)
	}
	return session, true, nil
}

// postWorking posts the placeholder reply and returns its timestamp. An empty
// result means the placeholder could not be posted, and the final reply is
// posted as a new message instead.
func (b *SessionBridge) postWorking(ctx context.Context, msg *SlackMessageData, sessionName string) string {
	postCtx, cancel := context.WithTimeout(ctx, b.postTimeout)
	defer cancel()
	ts, err := b.poster.PostThreadReply(postCtx, msg.ChannelID, ThreadTimestamp(msg), reporting.SessionWorkingMessage(sessionName))
	if err != nil {
		b.log.Error(err, "Failed to post Session placeholder reply", "channel", msg.ChannelID)
		return ""
	}
	return ts
}

// postDropped tells the thread that a message was not accepted.
func (b *SessionBridge) postDropped(ctx context.Context, msg *SlackMessageData) {
	postCtx, cancel := context.WithTimeout(ctx, b.postTimeout)
	defer cancel()
	dropped := reporting.SlackMessage{Text: "Too many messages are already queued in this thread. Wait for the current work to finish, then try again."}
	if _, err := b.poster.PostThreadReply(postCtx, msg.ChannelID, ThreadTimestamp(msg), dropped); err != nil {
		b.log.Error(err, "Failed to post queue-full notice", "channel", msg.ChannelID)
	}
}

// report delivers the turn result, replacing the placeholder with the first
// message and posting any further parts as new thread replies.
func (b *SessionBridge) report(ctx context.Context, msg *SlackMessageData, placeholderTS string, messages []reporting.SlackMessage) {
	threadTS := ThreadTimestamp(msg)
	for index, message := range messages {
		if index == 0 && placeholderTS != "" {
			err := b.editPlaceholder(ctx, msg.ChannelID, placeholderTS, message)
			if err == nil {
				continue
			}
			b.log.Error(err, "Failed to edit Session placeholder, posting the reply instead",
				"channel", msg.ChannelID)
		}
		if err := b.withTimeout(ctx, func(postCtx context.Context) error {
			_, err := b.poster.PostThreadReply(postCtx, msg.ChannelID, threadTS, message)
			return err
		}); err != nil {
			b.log.Error(err, "Failed to deliver Session reply", "channel", msg.ChannelID, "part", index+1)
			return
		}
	}
}

// editPlaceholder replaces the placeholder with the turn's reply, retrying a
// failed edit before the caller falls back to posting a new message.
//
// The retry exists because a lost response is indistinguishable from a rejected
// edit: falling back immediately would post a duplicate whenever the edit
// actually applied. Editing is idempotent, so a retry heals that case. If every
// attempt fails the caller still posts, because a thread that silently loses the
// answer to a ten-minute run is worse than one that shows it twice.
func (b *SessionBridge) editPlaceholder(ctx context.Context, channelID, placeholderTS string, message reporting.SlackMessage) error {
	var err error
	for attempt := range placeholderEditAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(b.editRetryDelay):
			}
		}
		err = b.withTimeout(ctx, func(postCtx context.Context) error {
			return b.poster.UpdateMessage(postCtx, channelID, placeholderTS, message)
		})
		if err == nil {
			return nil
		}
	}
	return err
}

// withTimeout runs one Slack write under the bridge's write timeout.
func (b *SessionBridge) withTimeout(ctx context.Context, write func(context.Context) error) error {
	postCtx, cancel := context.WithTimeout(ctx, b.postTimeout)
	defer cancel()
	return write(postCtx)
}

// ThreadTimestamp returns the timestamp identifying the message's thread: the
// parent timestamp for a reply, or the message's own timestamp for a message
// that starts one.
func ThreadTimestamp(msg *SlackMessageData) string {
	if msg.ThreadTS != "" {
		return msg.ThreadTS
	}
	return msg.Timestamp
}

// ThreadKey identifies the Slack thread a message belongs to. Slash commands
// have no thread, so they have no key and cannot drive a Session.
func ThreadKey(msg *SlackMessageData) string {
	if msg.IsSlashCommand || msg.ChannelID == "" {
		return ""
	}
	threadTS := ThreadTimestamp(msg)
	if threadTS == "" {
		return ""
	}
	return msg.ChannelID + ":" + threadTS
}

// SessionName returns the deterministic Session name for one Slack thread. The
// inputs are hashed rather than embedded, because a Slack timestamp contains a
// dot and would not survive as a resource name.
//
// The spawner's name and UID are part of the hash, not just the truncated name
// prefix. Without them, two spawners whose names share their first 44
// characters would resolve the same thread to one Session, and a spawner
// deleted and recreated under the same name would inherit Sessions it does not
// own until they are garbage collected.
func SessionName(spawnerName string, spawnerUID types.UID, channelID, threadTS string) string {
	return spawnResourceName(spawnerName, strings.Join([]string{
		spawnerName, string(spawnerUID), channelID, threadTS,
	}, ":"))
}

// spawnResourceName builds a resource name of the form
// "<spawner>-slack-<hash>", truncating the spawner name so the result fits the
// 63-character limit.
func spawnResourceName(spawnerName, hashInput string) string {
	sum := sha256.Sum256([]byte(hashInput))
	shortHash := hex.EncodeToString(sum[:])[:12]
	// Truncate the spawner name to leave room for "-slack-" (7) + hash (12).
	const maxPrefix = 63 - 7 - 12
	name := spawnerName
	if len([]rune(name)) > maxPrefix {
		name = strings.TrimRight(string([]rune(name)[:maxPrefix]), "-.")
	}
	return fmt.Sprintf("%s-slack-%s", name, shortHash)
}

// turnPrompt renders the prompt for one Slack message. The message that opens
// a thread's Session carries the thread context as it stands, so the Session
// starts knowing what the thread has already said. Later messages carry only
// the new text, because the Session retains the rest.
//
// Every prompt opens with a literal, and that is load-bearing rather than
// cosmetic. The Session runtime parses a submitted prompt for its own commands
// before the agent sees it: text beginning with "!" runs as a shell command
// inside the Session Pod, with the Session's credentials in the environment,
// and text beginning with "/goal" drives a Codex goal. Those are meant for an
// authenticated console operator.
//
// Every part of a Slack message its author controls — the text, and the display
// name that names them — must therefore appear after that literal, never at the
// start of the prompt.
func turnPrompt(msg *SlackMessageData, firstTurn bool) string {
	if firstTurn {
		if msg.HasThreadContext {
			return "Relayed from a Slack thread. The conversation so far:\n\n" + msg.Body
		}
		return fmt.Sprintf("Relayed from Slack. Author: %s\n\n%s", promptAuthor(msg), msg.Body)
	}
	text := strings.TrimSpace(stripLeadingMentions(msg.Text))
	if attachments := strings.TrimSpace(msg.AttachmentText); attachments != "" {
		// enrichMessage folds attachment text into Body, but Body carries the
		// whole thread on a reply, so a later turn has to pick it up here or
		// forwarded messages and unfurls never reach the Session.
		if text != "" {
			text += "\n" + attachments
		} else {
			text = attachments
		}
	}
	if text == "" {
		// Never fall back to Body here: on a reply it holds the whole formatted
		// thread, including messages that did not match, so a bare mention
		// would resend the conversation the Session already has.
		text = strings.TrimSpace(msg.Text)
	}
	if text == "" {
		text = "(no message text)"
	}
	return fmt.Sprintf("Relayed from the Slack thread. Author: %s\n\n%s", promptAuthor(msg), text)
}

// promptAuthor names the message author for a prompt envelope. The name comes
// from the author's Slack profile, which they edit freely, so it is flattened
// to one bounded line and never placed at the start of a prompt — the envelope
// keeps its own literal opening for that.
func promptAuthor(msg *SlackMessageData) string {
	name := msg.UserName
	if name == "" {
		name = msg.UserID
	}
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "unknown"
	}
	if len([]rune(name)) > maxPromptAuthorLength {
		name = string([]rune(name)[:maxPromptAuthorLength]) + "…"
	}
	return name
}

// endInflight forgets a placeholder whose turn has been reported.
func (b *SessionBridge) endInflight(queueKey string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.inflight, queueKey)
}

// Start makes the bridge a manager.Runnable so the manager waits for it during
// shutdown. It blocks until ctx is cancelled, then resolves every placeholder
// left behind and tells every queued thread its message was dropped.
//
// Without this, a rollout leaves each in-flight thread showing "Working on your
// request..." forever: the turn's Slack writes use the listener's context,
// which is already cancelled by the time the turn fails.
func (b *SessionBridge) Start(ctx context.Context) error {
	<-ctx.Done()
	b.drainOnShutdown(context.WithoutCancel(ctx))
	return nil
}

// NeedLeaderElection reports that only the replica holding the Socket Mode
// connection runs the bridge, matching the listener that feeds it.
func (b *SessionBridge) NeedLeaderElection() bool { return true }

// drainOnShutdown notifies the threads a restart interrupted. It runs with a
// context detached from the one that was just cancelled, because every notice
// it sends is a Slack write.
func (b *SessionBridge) drainOnShutdown(ctx context.Context) {
	b.mu.Lock()
	b.draining = true
	inflight := make([]*inflightTurn, 0, len(b.inflight))
	for _, turn := range b.inflight {
		inflight = append(inflight, turn)
	}
	b.inflight = map[string]*inflightTurn{}
	queued := make([]*turnJob, 0)
	for _, queue := range b.threads {
		queued = append(queued, queue.jobs...)
		queue.jobs = nil
	}
	b.mu.Unlock()

	if len(inflight) == 0 && len(queued) == 0 {
		return
	}
	b.log.Info("Notifying Slack threads interrupted by shutdown",
		"running", len(inflight), "queued", len(queued))

	ctx, cancel := context.WithTimeout(ctx, shutdownNoticeTimeout)
	defer cancel()

	notice := reporting.SlackMessage{Text: shutdownNotice}
	// Each notice gets its own deadline and runs alongside the others: sending
	// them in sequence under one budget lets a single stalled Slack call use it
	// all up and leave every later thread unnotified.
	var wait sync.WaitGroup
	slots := make(chan struct{}, shutdownNoticeConcurrency)
	send := func(write func(context.Context) error, description, channelID string) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			noticeCtx, cancelNotice := context.WithTimeout(ctx, b.postTimeout)
			defer cancelNotice()
			if err := write(noticeCtx); err != nil {
				b.log.Error(err, description, "channel", channelID)
			}
		}()
	}

	for _, turn := range inflight {
		placeholderTS, won := turn.claim()
		if !won {
			// The turn reported its own result while the snapshot was taken.
			continue
		}
		channelID, threadTS := turn.channelID, turn.threadTS
		if placeholderTS == "" {
			// The turn was dequeued but had not posted a placeholder yet, so
			// there is nothing to edit and the thread has seen nothing at all.
			send(func(noticeCtx context.Context) error {
				_, err := b.poster.PostThreadReply(noticeCtx, channelID, threadTS, notice)
				return err
			}, "Failed to notify an unstarted thread during shutdown", channelID)
			continue
		}
		send(func(noticeCtx context.Context) error {
			return b.poster.UpdateMessage(noticeCtx, channelID, placeholderTS, notice)
		}, "Failed to resolve a placeholder during shutdown", channelID)
	}
	for _, job := range queued {
		channelID, threadTS := job.msg.ChannelID, ThreadTimestamp(job.msg)
		send(func(noticeCtx context.Context) error {
			_, err := b.poster.PostThreadReply(noticeCtx, channelID, threadTS, notice)
			return err
		}, "Failed to notify a queued thread during shutdown", channelID)
	}
	wait.Wait()
}
