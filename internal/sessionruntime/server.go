package sessionruntime

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionupdate"
	clientv1alpha2 "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/typed/api/v1alpha2"
)

const (
	DefaultSocketPath                           = "/tmp/kelos-session/runtime.sock"
	DefaultStateDir                             = "/workspace/.kelos/session"
	DefaultWorkingDir                           = "/workspace/repo"
	journalFileName                             = "events.jsonl"
	activityPublishedFile                       = "activity-published"
	initializedFile                             = "initialized"
	defaultInterruptTimeout                     = 10 * time.Second
	shellCommandWaitDelay                       = 250 * time.Millisecond
	defaultSessionStatusPublishInterval         = 30 * time.Second
	defaultSessionStatusRetryInterval           = 2 * time.Second
	defaultWorkspaceStatusRefreshInterval       = 5 * time.Minute
	defaultActiveWorkspaceStatusRefreshInterval = 30 * time.Second
	defaultWorkspaceStatusRetryInterval         = time.Minute
	defaultWorkspaceStatusMaxRetryInterval      = 15 * time.Minute
)

// Config configures the resident Session runtime.
type Config struct {
	SocketPath           string
	StateDir             string
	WorkingDir           string
	AgentType            string
	Model                string
	Effort               string
	PluginDir            string
	InitialPrompt        string
	Environment          []string
	PublishSessionStatus SessionStatusPublisher
	SessionName          string
	PodUID               types.UID
	SessionClient        clientv1alpha2.SessionInterface
}

type turnRequest struct {
	id          string
	text        string
	revision    int64
	attachments []Attachment
	command     sessionCommand
	accepted    chan struct{}
}

type sessionStatusPublishRequest struct {
	active          bool
	waitingForInput bool
	// settledTurnID is the completed-turn high-water mark captured when this
	// request was enqueued. It becomes the persisted activity mark only after an
	// idle (active == false) publication of this request succeeds, so a stale
	// idle publication in flight cannot record a turn that started after it.
	settledTurnID int64
}

type pendingInput struct {
	questions map[string]struct{}
	answers   map[string][]string
	result    chan pendingInputResult
	resolved  bool
}

type pendingInputResult struct {
	answers   map[string][]string
	cancelled bool
}

// Server owns one provider process, event stream, and local client socket.
type Server struct {
	config            Config
	journal           *Journal
	provider          Provider
	attachmentStore   *AttachmentStore
	providerCloseOnce sync.Once
	providerCloseErr  error

	submitMu      sync.Mutex
	appendMessage func(Event) error
	turns         chan turnRequest
	pendingTurn   *turnRequest
	nextTurnID    atomic.Int64
	outstanding   int
	// completedTurnID is the highest turn ID that has finished running (completed,
	// failed, or interrupted). A status publication snapshots it at enqueue time so
	// that only turns already complete when the publication was created can be
	// marked settled once it succeeds.
	completedTurnID atomic.Int64
	// settledTurnID is the highest turn ID whose Active=False (idle) status has been
	// durably published, persisted to activityMarkerPath so it survives a container
	// restart. activityMarkerPath is empty when no StateDir is configured (for
	// example, in tests), which disables persistence.
	settledTurnID      atomic.Int64
	activityMarkerPath string
	updateRequest      *sessionupdate.Request
	idleDrainRequest   *sessionupdate.Request
	updateReport       chan struct{}
	interruptMu        sync.Mutex
	activeMu           sync.Mutex
	activeTurn         string
	activeKind         sessionCommandKind
	activeTurnCancel   context.CancelCauseFunc
	activeTurnDone     chan struct{}
	providerStopping   atomic.Bool
	providerStopOnce   sync.Once
	providerStop       chan struct{}
	recoveredGoalTurn  *turnRequest
	interruptTimeout   time.Duration
	pendingInputCount  atomic.Int64

	runtimeStatusMu             sync.RWMutex
	runtimeStatus               RuntimeStatus
	runtimeStatusSubscribers    map[int]chan RuntimeStatus
	nextRuntimeStatusSubscriber int

	inputMu                              sync.Mutex
	pendingInputs                        map[string]*pendingInput
	nextInputID                          atomic.Int64
	refreshWorkspaceStatus               func(context.Context, bool) (WorkspaceStatus, error)
	publishSessionStatus                 func(context.Context, bool, bool) error
	workspaceStatusMu                    sync.Mutex
	workspaceStatusForceDiscovery        bool
	workspaceStatusRefreshes             chan struct{}
	workspaceStatusRefreshInterval       time.Duration
	activeWorkspaceStatusRefreshInterval time.Duration
	workspaceStatusRetryInterval         time.Duration
	workspaceStatusMaxRetryInterval      time.Duration
	sessionStatusMu                      sync.Mutex
	sessionStatusPublishQueue            []sessionStatusPublishRequest
	sessionStatusPublishWakeups          chan struct{}
	sessionStatusPublishInterval         time.Duration
	sessionStatusRetryInterval           time.Duration
}

// NewServer constructs a Session runtime server around injected provider and journal implementations.
func NewServer(config Config, journal *Journal, provider Provider) *Server {
	server := &Server{
		config:                               config,
		journal:                              journal,
		provider:                             provider,
		appendMessage:                        journal.Append,
		turns:                                make(chan turnRequest, 1),
		updateReport:                         make(chan struct{}, 1),
		runtimeStatus:                        newRuntimeStatus(config),
		runtimeStatusSubscribers:             map[int]chan RuntimeStatus{},
		pendingInputs:                        map[string]*pendingInput{},
		workspaceStatusRefreshes:             make(chan struct{}, 1),
		sessionStatusPublishWakeups:          make(chan struct{}, 1),
		providerStop:                         make(chan struct{}),
		interruptTimeout:                     defaultInterruptTimeout,
		workspaceStatusRefreshInterval:       defaultWorkspaceStatusRefreshInterval,
		activeWorkspaceStatusRefreshInterval: defaultActiveWorkspaceStatusRefreshInterval,
		workspaceStatusRetryInterval:         defaultWorkspaceStatusRetryInterval,
		workspaceStatusMaxRetryInterval:      defaultWorkspaceStatusMaxRetryInterval,
		sessionStatusPublishInterval:         defaultSessionStatusPublishInterval,
		sessionStatusRetryInterval:           defaultSessionStatusRetryInterval,
	}
	if config.StateDir != "" {
		server.activityMarkerPath = filepath.Join(config.StateDir, activityPublishedFile)
		server.attachmentStore, _ = NewAttachmentStore(config.StateDir)
	}
	if provider, ok := provider.(runtimeStatusProvider); ok {
		server.updateProviderRuntimeStatus(provider.runtimeStatusSnapshot())
	}
	return server
}

// Run prepares the agent image and serves the Session until ctx is cancelled.
func Run(ctx context.Context, config Config) error {
	if err := os.MkdirAll(config.WorkingDir, 0750); err != nil {
		return fmt.Errorf("creating Session working directory: %w", err)
	}
	if err := os.MkdirAll(config.StateDir, 0700); err != nil {
		return fmt.Errorf("creating Session state directory: %w", err)
	}
	initialized, err := sessionInitialized(config.StateDir)
	if err != nil {
		return err
	}
	if err := runAgentSetup(ctx, config.WorkingDir, config.Environment); err != nil {
		return err
	}
	if !initialized {
		if err := os.WriteFile(filepath.Join(config.StateDir, initializedFile), []byte("initialized\n"), 0600); err != nil {
			return fmt.Errorf("recording initialized Session workspace: %w", err)
		}
	}

	journal, err := OpenJournal(filepath.Join(config.StateDir, journalFileName))
	if err != nil {
		return err
	}
	provider, err := NewProvider(ctx, ProviderConfig{
		AgentType:   config.AgentType,
		WorkingDir:  config.WorkingDir,
		StateDir:    config.StateDir,
		Model:       config.Model,
		Effort:      config.Effort,
		PluginDir:   config.PluginDir,
		Environment: config.Environment,
	})
	if err != nil {
		journal.Close()
		return err
	}

	recovery, err := recoverJournal(journal)
	if err != nil {
		_ = provider.Close()
		journal.Close()
		return err
	}
	server := NewServer(config, journal, provider)
	publishSessionStatus := func(ctx context.Context, active, waitingForInput bool) error {
		model := server.runtimeStatusSnapshot().Model
		return publishObservedSessionStatus(ctx, config.PublishSessionStatus, active, waitingForInput, model, func(ctx context.Context) (WorkspaceStatus, error) {
			return readWorkspaceStatus(ctx, realWorkspaceStatusRunner{}, config.StateDir, config.WorkingDir)
		})
	}
	server.refreshWorkspaceStatus = func(ctx context.Context, forceDiscovery bool) (WorkspaceStatus, error) {
		status, err := refreshWorkspaceStatus(ctx, config.StateDir, config.WorkingDir, config.Environment, forceDiscovery)
		server.updateWorkspaceRuntimeStatus(status)
		return status, err
	}
	if config.PublishSessionStatus != nil {
		server.publishSessionStatus = publishSessionStatus
	}
	server.nextTurnID.Store(recovery.nextTurnID)
	server.nextInputID.Store(recovery.nextInputID)
	server.completedTurnID.Store(recovery.completedTurnID)
	if err := server.restoreTurn(recovery.pendingTurn); err != nil {
		_ = provider.Close()
		journal.Close()
		return err
	}
	if goalManager, ok := provider.(goalProvider); ok && goalManager.ActiveGoal() != nil {
		turn := turnRequest{
			id:       fmt.Sprintf("turn-%d", server.nextTurnID.Add(1)),
			text:     "/goal resume",
			revision: 1,
			command:  sessionCommand{kind: sessionCommandGoal, goal: goalCommand{action: goalCommandResume}},
		}
		server.recoveredGoalTurn = &turn
		server.outstanding++
	}
	if server.activityMarkerPath != "" {
		settledTurnID, err := loadSettledTurnID(server.activityMarkerPath)
		if err != nil {
			_ = provider.Close()
			journal.Close()
			return err
		}
		server.settledTurnID.Store(settledTurnID)
	}
	// Republish activity when the journal holds locally accepted turns that were
	// never durably settled to a published idle status. This covers pending or
	// interrupted turns and turns that completed while their ordered Active status
	// publications were still retrying: a container restart drops the in-memory
	// publish queue, so without this a surviving idle-drain request could be
	// acknowledged Drained against a stale Active=False status.
	if server.publishSessionStatus != nil && recovery.hasUnsettledActivity(server.settledTurnID.Load()) {
		server.seedRecoveredActivityPublish()
	}
	return server.Serve(ctx)
}

func publishObservedSessionStatus(ctx context.Context, publisher SessionStatusPublisher, active, waitingForInput bool, model string, readStatus func(context.Context) (WorkspaceStatus, error)) error {
	workspaceStatus, readErr := readStatus(ctx)
	status := ObservedSessionStatus{Active: active, WaitingForInput: waitingForInput, Model: model}
	if readErr == nil {
		status.WorkspaceStatus = &workspaceStatus
	} else {
		// Workspace inspection is best-effort. The returned error gates retries and
		// keeps the status-publish queue entry pending, which in turn withholds the
		// idle-drain Drained report; a persistently unreadable workspace-status file
		// or failing git inspection must not wedge the drain once the Active
		// condition itself is durably published. Publish activity without the
		// workspace status and leave the workspace fields to a later publication.
		log.Printf("Unable to inspect Session workspace status; publishing activity without it error=%v", readErr)
	}
	return publisher(ctx, status)
}

func sessionInitialized(stateDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(stateDir, initializedFile))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("checking initialized Session workspace: %w", err)
}

func runAgentSetup(ctx context.Context, workingDir string, environment []string) error {
	command := exec.CommandContext(ctx, "/kelos_entrypoint.sh")
	command.Dir = workingDir
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = sessionSetupEnvironment(environment)
	if err := command.Run(); err != nil {
		return fmt.Errorf("preparing Session agent environment: %w", err)
	}
	return nil
}

func sessionSetupEnvironment(environment []string) []string {
	return replaceProcessEnv(environment, "KELOS_SESSION_SETUP_ONLY", "1")
}

func replaceProcessEnv(current []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(current)+1)
	for _, entry := range current {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

// Serve listens for local clients and keeps provider turns alive independently of them.
func (s *Server) Serve(ctx context.Context) error {
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	defer func() {
		_ = s.closeProvider()
		s.journal.Close()
		_ = os.Remove(s.config.SocketPath)
	}()
	if err := s.initializeSessionUpdate(serveCtx); err != nil {
		return err
	}
	if s.config.SessionClient != nil {
		go s.runSessionUpdateWatch(serveCtx)
		go s.runSessionUpdateReporter(serveCtx)
	}
	go s.runTurns(serveCtx)
	if err := s.deliverInitialPrompt(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.config.SocketPath), 0700); err != nil {
		return fmt.Errorf("creating Session socket directory: %w", err)
	}
	if err := os.Remove(s.config.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale Session socket: %w", err)
	}
	listener, err := net.Listen("unix", s.config.SocketPath)
	if err != nil {
		return fmt.Errorf("listening on Session socket: %w", err)
	}
	if err := os.Chmod(s.config.SocketPath, 0600); err != nil {
		listener.Close()
		return fmt.Errorf("securing Session socket: %w", err)
	}

	providerDone := s.provider.Done()
	go s.runWorkspaceStatusRefreshes(serveCtx)
	if s.publishSessionStatus != nil {
		go s.runSessionStatusPublishes(serveCtx)
	}
	s.requestWorkspaceStatusRefresh(true)
	s.requestSessionStatusPublish()
	go func() {
		select {
		case <-serveCtx.Done():
		case <-providerDone:
		case <-s.journal.Failed():
		}
		_ = listener.Close()
	}()

	log.Printf("Session runtime ready socket=%s provider=%s", s.config.SocketPath, s.config.AgentType)
	for {
		connection, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return nil
			}
			if journalErr := s.journal.Err(); journalErr != nil {
				return journalErr
			}
			select {
			case <-providerDone:
				return errors.New("Session provider stopped")
			default:
			}
			return fmt.Errorf("accepting Session client: %w", err)
		}
		go s.handleConnection(serveCtx, connection)
	}
}

func (s *Server) deliverInitialPrompt() error {
	if strings.TrimSpace(s.config.InitialPrompt) == "" || len(s.journal.Snapshot()) > 0 {
		return nil
	}
	if err := s.submitMessage(s.config.InitialPrompt, "initial-prompt"); err != nil {
		return fmt.Errorf("submitting initial Session prompt: %w", err)
	}
	return nil
}

func (s *Server) runTurns(ctx context.Context) {
	if s.recoveredGoalTurn != nil {
		turn := s.recoveredGoalTurn
		s.recoveredGoalTurn = nil
		select {
		case <-ctx.Done():
			return
		case <-s.providerStop:
			return
		default:
			s.runTurn(ctx, *turn)
			s.interruptMu.Lock()
			s.interruptMu.Unlock()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.providerStop:
			return
		case turn := <-s.turns:
			select {
			case <-turn.accepted:
			case <-ctx.Done():
				return
			case <-s.providerStop:
				// Leave accepted but unstarted work pending for journal recovery.
				s.turns <- turn
				return
			}
			if s.providerStopping.Load() {
				// Leave accepted but unstarted work pending for journal recovery.
				s.turns <- turn
				return
			}
			pending, exists := s.takePendingTurn(turn)
			if !exists {
				continue
			}
			s.runTurn(ctx, pending)
			// Keep the pending turn out of reach of an interrupt call that is
			// still settling after this turn returned.
			s.interruptMu.Lock()
			s.interruptMu.Unlock()
		}
	}
}

func (s *Server) requestWorkspaceStatusRefresh(forceDiscovery bool) {
	if s.refreshWorkspaceStatus == nil {
		return
	}
	s.workspaceStatusMu.Lock()
	if forceDiscovery {
		s.workspaceStatusForceDiscovery = true
	}
	select {
	case s.workspaceStatusRefreshes <- struct{}{}:
	default:
	}
	s.workspaceStatusMu.Unlock()
}

func (s *Server) runWorkspaceStatusRefreshes(ctx context.Context) {
	if s.refreshWorkspaceStatus == nil {
		return
	}
	timer := time.NewTimer(s.workspaceStatusRefreshInterval)
	defer timer.Stop()
	retryDelay := s.workspaceStatusRetryInterval
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.workspaceStatusRefreshes:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.workspaceStatusMu.Lock()
		forceDiscovery := s.workspaceStatusForceDiscovery
		s.workspaceStatusForceDiscovery = false
		select {
		case <-s.workspaceStatusRefreshes:
		default:
		}
		s.workspaceStatusMu.Unlock()

		status, err := s.refreshWorkspaceStatus(ctx, forceDiscovery)
		s.requestSessionStatusPublishAfterWorkspaceRefresh()
		next := s.workspaceStatusRefreshDelay(status)
		if err != nil {
			log.Printf("Unable to refresh Session workspace status error=%v", err)
			next = retryDelay
			retryDelay = min(retryDelay*2, s.workspaceStatusMaxRetryInterval)
		} else {
			retryDelay = s.workspaceStatusRetryInterval
		}
		timer.Reset(next)
	}
}

func (s *Server) workspaceStatusRefreshDelay(status WorkspaceStatus) time.Duration {
	if status.PullRequest == nil {
		return s.workspaceStatusRefreshInterval
	}
	if status.PullRequest.State == kelos.SessionPullRequestStateQueued ||
		(status.PullRequest.Checks != nil && status.PullRequest.Checks.State == kelos.SessionPullRequestChecksStatePending) {
		return s.activeWorkspaceStatusRefreshInterval
	}
	return s.workspaceStatusRefreshInterval
}

func (s *Server) requestSessionStatusPublish() {
	s.queueSessionStatusPublish(false)
}

func (s *Server) requestSessionStatusPublishAfterWorkspaceRefresh() {
	s.queueSessionStatusPublish(true)
}

func (s *Server) requestPeriodicSessionStatusPublish() {
	s.queueSessionStatusPublish(true)
}

func (s *Server) queueSessionStatusPublish(force bool) {
	if s.publishSessionStatus == nil {
		return
	}
	s.activeMu.Lock()
	active := s.activeTurn != ""
	waitingForInput := s.pendingInputCount.Load() > 0
	settledTurnID := s.completedTurnID.Load()
	s.sessionStatusMu.Lock()
	queued := force || len(s.sessionStatusPublishQueue) == 0 ||
		s.sessionStatusPublishQueue[len(s.sessionStatusPublishQueue)-1].active != active ||
		s.sessionStatusPublishQueue[len(s.sessionStatusPublishQueue)-1].waitingForInput != waitingForInput
	if queued {
		s.sessionStatusPublishQueue = append(s.sessionStatusPublishQueue, sessionStatusPublishRequest{
			active:          active,
			waitingForInput: waitingForInput,
			settledTurnID:   settledTurnID,
		})
	}
	s.sessionStatusMu.Unlock()
	s.activeMu.Unlock()
	if !queued {
		return
	}
	select {
	case s.sessionStatusPublishWakeups <- struct{}{}:
	default:
	}
}

func (s *Server) nextSessionStatusPublish() (sessionStatusPublishRequest, bool) {
	s.sessionStatusMu.Lock()
	defer s.sessionStatusMu.Unlock()
	if len(s.sessionStatusPublishQueue) == 0 {
		return sessionStatusPublishRequest{}, false
	}
	return s.sessionStatusPublishQueue[0], true
}

func (s *Server) completeSessionStatusPublish() {
	s.sessionStatusMu.Lock()
	defer s.sessionStatusMu.Unlock()
	s.sessionStatusPublishQueue = s.sessionStatusPublishQueue[1:]
}

// seedRecoveredActivityPublish queues an initial Active=True status publish for a
// runtime that recovered locally accepted activity not yet durably settled to the
// Session status. Publishing recovered activity advances the Session's
// lastActivityTime, and because the queued publish keeps hasPendingStatusPublishes
// true until it is durably sent, an idle-drain request cannot be acknowledged
// Drained before that activity reaches the Session status. Without it, a container
// restart that preserves the Pod UID and a pending idle-drain request could report
// Drained against a stale Active=False status and let the controller delete the
// Session and its workspace despite the recovered activity.
func (s *Server) seedRecoveredActivityPublish() {
	s.sessionStatusMu.Lock()
	defer s.sessionStatusMu.Unlock()
	s.sessionStatusPublishQueue = append(s.sessionStatusPublishQueue, sessionStatusPublishRequest{active: true})
}

// recordSettledActivity persists how far locally accepted activity has been
// durably reflected in the Session status. When an Active=False (idle) status is
// published with no turn in flight, every turn submitted so far is complete and
// durably settled, so the completed-turn high-water mark that the request carried
// at enqueue time becomes the activity publication high-water mark. Using the
// request's snapshot rather than the live counter is essential: a stale idle or
// periodic publication may only succeed after a later turn has started and
// finished, and recording the live counter would mark that later turn settled
// even though its own Active publications are still queued. On a container
// restart, a journal whose activity extends beyond this mark (an interrupted or
// completed-but-unpublished turn) triggers a conservative republish before an
// idle drain may report Drained. The mark is only advanced after the write
// succeeds, so a failed persist leaves the runtime to re-publish rather than lose
// progress.
// markTurnCompleted advances the completed-turn high-water mark to the given
// turn, which a subsequent idle status publication snapshots as the point up to
// which activity is durably settled once it succeeds.
func (s *Server) markTurnCompleted(turnID string) {
	id := numericEventID(turnID, "turn-")
	for {
		current := s.completedTurnID.Load()
		if id <= current {
			return
		}
		if s.completedTurnID.CompareAndSwap(current, id) {
			return
		}
	}
}

func (s *Server) recordSettledActivity(request sessionStatusPublishRequest) {
	if request.active || s.activityMarkerPath == "" {
		return
	}
	if request.settledTurnID <= s.settledTurnID.Load() {
		return
	}
	if err := writeSettledTurnID(s.activityMarkerPath, request.settledTurnID); err != nil {
		log.Printf("Unable to persist Session activity publication progress error=%v", err)
		return
	}
	s.settledTurnID.Store(request.settledTurnID)
}

// loadSettledTurnID reads the persisted activity publication high-water mark,
// returning 0 when the marker file does not yet exist.
func loadSettledTurnID(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading Session activity publication progress: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("parsing Session activity publication progress %q: %w", text, err)
	}
	return id, nil
}

// writeSettledTurnID atomically persists the activity publication high-water mark.
func writeSettledTurnID(path string, id int64) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-activity-*")
	if err != nil {
		return fmt.Errorf("creating Session activity publication progress: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("securing Session activity publication progress: %w", err)
	}
	if _, err := fmt.Fprintf(temporary, "%d\n", id); err != nil {
		return fmt.Errorf("writing Session activity publication progress: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing Session activity publication progress: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing Session activity publication progress: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replacing Session activity publication progress: %w", err)
	}
	removeTemporary = false
	return nil
}

func (s *Server) runSessionStatusPublishes(ctx context.Context) {
	timer := time.NewTimer(s.sessionStatusPublishInterval)
	defer timer.Stop()
	var (
		request sessionStatusPublishRequest
		pending bool
	)
	for {
		if !pending {
			if next, ok := s.nextSessionStatusPublish(); ok {
				request = next
				pending = true
			} else {
				select {
				case <-ctx.Done():
					return
				case <-s.sessionStatusPublishWakeups:
					continue
				case <-timer.C:
					s.requestPeriodicSessionStatusPublish()
					continue
				}
			}
		}
		next := s.sessionStatusPublishInterval
		err := s.publishSessionStatus(ctx, request.active, request.waitingForInput)
		if err != nil {
			log.Printf("Unable to publish Session runtime status error=%v", err)
			next = s.sessionStatusRetryInterval
		} else {
			s.completeSessionStatusPublish()
			s.recordSettledActivity(request)
			// A drain report may have been withheld until in-flight activity was
			// durably published; re-evaluate now that the queue has advanced.
			if s.drainReportPending() {
				s.signalSessionUpdateReport()
			}
			pending = false
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(next)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}
}

func (s *Server) runTurn(ctx context.Context, turn turnRequest) {
	defer s.requestWorkspaceStatusRefresh(true)
	turnCtx, cancelTurn := context.WithCancelCause(ctx)
	turnDone := make(chan struct{})
	command := turn.command
	if command.kind == "" {
		command = sessionCommand{kind: sessionCommandMessage, text: turn.text}
	}
	s.activeMu.Lock()
	s.activeTurn = turn.id
	s.activeKind = command.kind
	s.activeTurnCancel = cancelTurn
	s.activeTurnDone = turnDone
	s.activeMu.Unlock()
	s.requestSessionStatusPublish()
	defer func() {
		cancelTurn(nil)
		s.activeMu.Lock()
		if s.activeTurn == turn.id {
			s.activeTurn = ""
			s.activeKind = ""
			s.activeTurnCancel = nil
			s.activeTurnDone = nil
		}
		s.activeMu.Unlock()
		// Advance the completed-turn high-water mark before requesting the idle
		// publication so that publication carries a snapshot reflecting this turn.
		s.markTurnCompleted(turn.id)
		s.requestSessionStatusPublish()
		s.finishTurn()
		close(turnDone)
	}()

	if err := s.journal.Append(Event{Type: EventTurnStarted, TurnID: turn.id, Status: "running"}); err != nil {
		return
	}
	sink := &turnSink{server: s, turnID: turn.id}
	if command.kind == sessionCommandShell {
		record, runErr := s.runShellCommand(turnCtx, turn.id, command.text, sink)
		sink.stop()
		interrupted := errors.Is(runErr, ErrTurnInterrupted) || errors.Is(context.Cause(turnCtx), ErrTurnInterrupted)
		if runErr != nil && turnCtx.Err() != nil && !interrupted {
			return
		}
		if provider, ok := s.provider.(shellCommandContextProvider); ok {
			recordCtx := turnCtx
			if interrupted {
				recordCtx = ctx
			}
			if err := provider.recordShellCommand(recordCtx, record); err != nil {
				_ = s.journal.Append(Event{Type: EventError, TurnID: turn.id, Text: err.Error(), Status: "failed"})
				_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "failed"})
				return
			}
		}
		if interrupted {
			_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "interrupted"})
			return
		}
		_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "completed"})
		return
	}
	if command.kind == sessionCommandGoal {
		provider, ok := s.provider.(goalProvider)
		if !ok {
			_ = s.journal.Append(Event{Type: EventError, TurnID: turn.id, Text: "/goal is available only in Codex Sessions", Status: "failed"})
			_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "failed"})
			return
		}
		runErr := provider.RunGoal(turnCtx, command.goal, sink)
		sink.stop()
		if errors.Is(runErr, ErrTurnInterrupted) || errors.Is(context.Cause(turnCtx), ErrTurnInterrupted) {
			_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "interrupted"})
			return
		}
		if runErr != nil {
			if turnCtx.Err() != nil {
				return
			}
			_ = s.journal.Append(Event{Type: EventError, TurnID: turn.id, Text: runErr.Error(), Status: "failed"})
			_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "failed"})
			return
		}
		_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "completed"})
		return
	}
	resolvedAttachments, err := s.resolveAttachments(turn.attachments)
	if err != nil {
		_ = s.journal.Append(Event{Type: EventError, TurnID: turn.id, Text: err.Error(), Status: "failed"})
		_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "failed"})
		return
	}
	result := make(chan error, 1)
	go func() {
		result <- s.provider.RunTurn(turnCtx, TurnInput{Text: turn.text, Attachments: resolvedAttachments}, sink)
	}()
	var runErr error
	select {
	case runErr = <-result:
	case <-turnCtx.Done():
		runErr = context.Cause(turnCtx)
	}
	if s.config.AgentType == "claude-code" || s.config.AgentType == "opencode" {
		if diff := workspaceDiff(turnCtx, s.config.WorkingDir); diff != "" {
			sink.Emit(Event{Type: EventFileDiff, Diff: diff})
		}
	}
	sink.stop()
	if errors.Is(runErr, ErrTurnInterrupted) || errors.Is(context.Cause(turnCtx), ErrTurnInterrupted) {
		_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "interrupted"})
		return
	}
	if runErr != nil {
		if turnCtx.Err() != nil {
			return
		}
		_ = s.journal.Append(Event{Type: EventError, TurnID: turn.id, Text: runErr.Error(), Status: "failed"})
		_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "failed"})
		return
	}
	_ = s.journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "completed"})
}

func (s *Server) runShellCommand(ctx context.Context, turnID, script string, sink EventSink) (shellCommandRecord, error) {
	toolID := turnID + "-shell"
	sink.Emit(Event{Type: EventToolStarted, ToolID: toolID, ToolName: script, Status: "running"})
	command := exec.CommandContext(ctx, "/bin/sh", "-lc", script)
	command.Dir = s.config.WorkingDir
	command.Env = s.config.Environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = shellCommandWaitDelay
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	outputReader, outputWriter := io.Pipe()
	command.Stdout = outputWriter
	command.Stderr = outputWriter
	started := time.Now()
	if err := command.Start(); err != nil {
		_ = outputReader.Close()
		_ = outputWriter.Close()
		sink.Emit(Event{Type: EventToolCompleted, ToolID: toolID, ToolName: script, Output: err.Error(), Status: "failed"})
		return shellCommandRecord{command: script, exitCode: -1, duration: time.Since(started), output: err.Error()}, nil
	}

	output := newBoundedToolOutput(maxToolOutputBytes)
	readDone := make(chan error, 1)
	go func() {
		defer outputReader.Close()
		buffer := make([]byte, 32*1024)
		streamed := 0
		for {
			count, err := outputReader.Read(buffer)
			if count > 0 {
				chunk := string(buffer[:count])
				output.WriteString(chunk)
				if streamed < maxToolOutputBytes {
					remaining := maxToolOutputBytes - streamed
					if len(chunk) > remaining {
						chunk = chunk[:remaining]
					}
					streamed += len(chunk)
					sink.Emit(Event{Type: EventToolDelta, ToolID: toolID, ToolName: script, Output: chunk, Status: "running"})
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					readDone <- nil
				} else {
					readDone <- err
				}
				return
			}
		}
	}()
	waitErr := command.Wait()
	duration := time.Since(started)
	_ = outputWriter.Close()
	readErr := <-readDone
	if readErr != nil && waitErr == nil {
		waitErr = readErr
	}
	status := "completed"
	if ctx.Err() != nil {
		status = "interrupted"
	} else if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		status = "failed"
		if output.String() == "" {
			output.WriteString(waitErr.Error())
		}
	}
	exitCode := command.ProcessState.ExitCode()
	if ctx.Err() != nil {
		exitCode = -1
	}
	record := shellCommandRecord{command: script, exitCode: exitCode, duration: duration, output: output.String()}
	sink.Emit(Event{Type: EventToolCompleted, ToolID: toolID, ToolName: script, Output: record.output, Status: status})
	if ctx.Err() != nil {
		return record, context.Cause(ctx)
	}
	return record, nil
}

func workspaceDiff(ctx context.Context, workingDir string) string {
	command := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--no-color")
	command.Dir = workingDir
	output, err := command.Output()
	if err != nil || len(output) == 0 {
		return ""
	}
	const maxDiffBytes = 1024 * 1024
	if len(output) > maxDiffBytes {
		return string(output[:maxDiffBytes]) + "\n\n[diff truncated]"
	}
	return string(output)
}

func (s *Server) submitMessage(text, requestID string, attachmentIDs ...string) error {
	return s.submitParsedMessage(text, requestID, sessionCommand{kind: sessionCommandMessage, text: text}, attachmentIDs...)
}

func (s *Server) submitParsedMessage(text, requestID string, command sessionCommand, attachmentIDs ...string) error {
	if strings.TrimSpace(text) == "" && len(attachmentIDs) == 0 {
		return errors.New("message must not be empty")
	}
	resolvedAttachments, err := s.resolveAttachmentIDs(attachmentIDs)
	if err != nil {
		return err
	}
	attachments := make([]Attachment, len(resolvedAttachments))
	for index := range resolvedAttachments {
		attachments[index] = resolvedAttachments[index].Attachment
	}
	if command.kind != sessionCommandMessage && len(attachments) > 0 {
		return errors.New("Session commands do not accept attachments")
	}
	if command.kind == sessionCommandGoal {
		if _, ok := s.provider.(goalProvider); !ok {
			return errors.New("/goal is available only in Codex Sessions")
		}
	}
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if s.idleDrainRequest != nil {
		return errors.New("Session is idle and is being reclaimed")
	}
	if s.updateRequest != nil {
		return errors.New("Session runtime is draining for an update; retry after it reconnects")
	}
	if s.providerStopping.Load() {
		return errors.New("Session provider is restarting; retry after it reconnects")
	}
	if err := s.journal.Err(); err != nil {
		return fmt.Errorf("recording Session message: %w", err)
	}
	if s.pendingTurn != nil {
		turn := *s.pendingTurn
		pendingKind := turn.command.kind
		if pendingKind == "" {
			pendingKind = sessionCommandMessage
		}
		if command.kind != sessionCommandMessage || pendingKind != sessionCommandMessage {
			return errors.New("Session already has a pending command or message")
		}
		turn.text = mergePendingMessageText(turn.text, text)
		turn.attachments = append(turn.attachments, attachments...)
		turn.revision = max(1, turn.revision) + 1
		if err := s.appendMessage(Event{
			Type:           EventUserMessageUpdated,
			RequestID:      requestID,
			TurnID:         turn.id,
			Text:           turn.text,
			Revision:       turn.revision,
			SessionCommand: false,
			Attachments:    turn.attachments,
		}); err != nil {
			return fmt.Errorf("recording pending Session message: %w", err)
		}
		s.pendingTurn = &turn
		return nil
	}
	turn := turnRequest{
		id:          fmt.Sprintf("turn-%d", s.nextTurnID.Add(1)),
		text:        text,
		revision:    1,
		attachments: attachments,
		command:     command,
		accepted:    make(chan struct{}),
	}
	select {
	case s.turns <- turn:
		s.pendingTurn = &turn
		s.outstanding++
		if err := s.appendMessage(Event{Type: EventUserMessage, RequestID: requestID, TurnID: turn.id, Text: turn.text, Revision: turn.revision, SessionCommand: command.kind != sessionCommandMessage, Attachments: turn.attachments}); err != nil {
			s.pendingTurn = nil
			s.outstanding--
			return fmt.Errorf("recording Session message: %w", err)
		}
		close(turn.accepted)
		return nil
	default:
		return errors.New("Session already has a pending message")
	}
}

func (s *Server) submitClientMessage(ctx context.Context, text, requestID string, attachmentIDs ...string) error {
	command, err := parseSessionCommand(text)
	if err != nil {
		return err
	}
	if command.kind != sessionCommandMessage && len(attachmentIDs) > 0 {
		return errors.New("Session commands do not accept attachments")
	}
	if command.kind == sessionCommandGoal {
		if _, ok := s.provider.(goalProvider); !ok {
			return errors.New("/goal is available only in Codex Sessions")
		}
	}
	if command.kind == sessionCommandGoal {
		s.activeMu.Lock()
		turnID := s.activeTurn
		activeKind := s.activeKind
		s.activeMu.Unlock()
		if turnID != "" && activeKind == sessionCommandGoal {
			provider, ok := s.provider.(goalProvider)
			if !ok {
				return errors.New("/goal is available only in Codex Sessions")
			}
			if err := s.journal.Append(Event{Type: EventUserMessage, RequestID: requestID, Text: text, SessionCommand: true}); err != nil {
				return fmt.Errorf("recording Session goal command: %w", err)
			}
			return provider.ControlGoal(ctx, command.goal, &turnSink{server: s})
		}
	}
	return s.submitParsedMessage(text, requestID, command, attachmentIDs...)
}

func mergePendingMessageText(current, addition string) string {
	if strings.TrimSpace(current) == "" {
		return addition
	}
	if strings.TrimSpace(addition) == "" {
		return current
	}
	return current + "\n\n" + addition
}

func (s *Server) editMessage(turnID, text string, expectedRevision int64, requestID string) error {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if s.pendingTurn == nil || s.pendingTurn.id != turnID {
		return fmt.Errorf("Session turn %q is no longer pending", turnID)
	}
	turn := *s.pendingTurn
	if expectedRevision <= 0 {
		return fmt.Errorf("editing Session turn %q: expectedRevision must be positive", turnID)
	}
	if turn.revision == 0 {
		turn.revision = 1
	}
	if expectedRevision != turn.revision {
		return fmt.Errorf("editing Session turn %q: revision is %d, not %d", turnID, turn.revision, expectedRevision)
	}
	if strings.TrimSpace(text) == "" && len(turn.attachments) == 0 {
		return fmt.Errorf("editing Session turn %q: message must not be empty", turnID)
	}
	command, err := parseSessionCommand(text)
	if err != nil {
		return err
	}
	if command.kind != sessionCommandMessage && len(turn.attachments) > 0 {
		return errors.New("Session commands do not accept attachments")
	}
	if command.kind == sessionCommandGoal {
		if _, ok := s.provider.(goalProvider); !ok {
			return errors.New("/goal is available only in Codex Sessions")
		}
	}
	turn.text = text
	turn.command = command
	turn.revision++
	if err := s.appendMessage(Event{
		Type:           EventUserMessageUpdated,
		RequestID:      requestID,
		TurnID:         turn.id,
		Text:           turn.text,
		Revision:       turn.revision,
		SessionCommand: command.kind != sessionCommandMessage,
		Attachments:    turn.attachments,
	}); err != nil {
		return fmt.Errorf("recording edited Session turn %q: %w", turnID, err)
	}
	s.pendingTurn = &turn
	return nil
}

func (s *Server) removePendingMessage(turnID string, expectedRevision int64, requestID string) error {
	s.submitMu.Lock()
	if s.pendingTurn == nil || s.pendingTurn.id != turnID {
		s.submitMu.Unlock()
		return fmt.Errorf("Session turn %q is no longer pending", turnID)
	}
	turn := *s.pendingTurn
	if expectedRevision <= 0 {
		s.submitMu.Unlock()
		return fmt.Errorf("removing Session turn %q: expectedRevision must be positive", turnID)
	}
	if turn.revision == 0 {
		turn.revision = 1
	}
	if expectedRevision != turn.revision {
		s.submitMu.Unlock()
		return fmt.Errorf("removing Session turn %q: revision is %d, not %d", turnID, turn.revision, expectedRevision)
	}
	if err := s.appendMessage(Event{
		Type:      EventUserMessageRemoved,
		RequestID: requestID,
		TurnID:    turn.id,
		Revision:  turn.revision + 1,
		Status:    "removed",
	}); err != nil {
		s.submitMu.Unlock()
		return fmt.Errorf("recording removed Session turn %q: %w", turnID, err)
	}
	s.pendingTurn = nil
	select {
	case buffered := <-s.turns:
		if buffered.id != turn.id {
			s.turns <- buffered
		}
	default:
	}
	if s.outstanding > 0 {
		s.outstanding--
	}
	shouldReport := (s.updateRequest != nil || s.idleDrainRequest != nil) && s.outstanding == 0
	s.submitMu.Unlock()

	s.markTurnCompleted(turn.id)
	s.requestSessionStatusPublish()
	if shouldReport {
		s.signalSessionUpdateReport()
	}
	return nil
}

func (s *Server) takePendingTurn(turn turnRequest) (turnRequest, bool) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if s.pendingTurn == nil || s.pendingTurn.id != turn.id {
		return turnRequest{}, false
	}
	pending := *s.pendingTurn
	s.pendingTurn = nil
	return pending, true
}

func (s *Server) resolveAttachmentIDs(ids []string) ([]ResolvedAttachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.attachmentStore == nil {
		return nil, errors.New("Session attachment storage is unavailable")
	}
	return s.attachmentStore.Resolve(ids)
}

func (s *Server) resolveAttachments(attachments []Attachment) ([]ResolvedAttachment, error) {
	ids := make([]string, len(attachments))
	for index := range attachments {
		ids[index] = attachments[index].ID
	}
	return s.resolveAttachmentIDs(ids)
}

type journalRecovery struct {
	nextTurnID      int64
	nextInputID     int64
	completedTurnID int64
	pendingTurn     *turnRequest
}

type journalTurnRecovery struct {
	id          string
	text        string
	revision    int64
	attachments []Attachment
	command     bool
	started     bool
	completed   bool
}

// hasUnsettledActivity reports whether the journal holds locally accepted turns
// beyond the given durably-settled high-water mark. nextTurnID is the highest
// turn ID observed in the journal, so any pending, interrupted, or completed turn
// whose idle status was never durably published leaves nextTurnID above
// settledTurnID.
func (r journalRecovery) hasUnsettledActivity(settledTurnID int64) bool {
	return r.nextTurnID > settledTurnID
}

func recoverJournal(journal *Journal) (journalRecovery, error) {
	events := journal.snapshotForRecovery()
	if len(events) == 0 {
		return journalRecovery{}, nil
	}
	turns := map[string]*journalTurnRecovery{}
	turnOrder := make([]string, 0)
	inputs := map[string]string{}
	inputOrder := make([]string, 0)
	recovery := journalRecovery{}
	for _, event := range events {
		if value := numericEventID(event.TurnID, "turn-"); value > recovery.nextTurnID {
			recovery.nextTurnID = value
		}
		if value := numericEventID(event.InputID, "input-"); value > recovery.nextInputID {
			recovery.nextInputID = value
		}
		var turn *journalTurnRecovery
		if event.TurnID != "" {
			if _, exists := turns[event.TurnID]; !exists {
				turnOrder = append(turnOrder, event.TurnID)
				turns[event.TurnID] = &journalTurnRecovery{id: event.TurnID}
			}
			turn = turns[event.TurnID]
		}
		switch event.Type {
		case EventUserMessage, EventUserMessageUpdated:
			if turn != nil {
				turn.text = event.Text
				turn.attachments = event.Attachments
				turn.command = event.SessionCommand
				turn.revision = event.Revision
				if turn.revision == 0 {
					turn.revision = 1
				}
			}
		case EventUserMessageRemoved, EventTurnCompleted:
			if turn != nil {
				turn.completed = true
			}
			if value := numericEventID(event.TurnID, "turn-"); value > recovery.completedTurnID {
				recovery.completedTurnID = value
			}
		case EventInputRequested:
			if turn != nil {
				turn.started = true
			}
			if event.InputID != "" {
				if _, exists := inputs[event.InputID]; !exists {
					inputOrder = append(inputOrder, event.InputID)
				}
				inputs[event.InputID] = event.TurnID
			}
		case EventInputResolved:
			delete(inputs, event.InputID)
		default:
			if turn != nil {
				turn.started = true
			}
		}
	}
	message := "Session runtime restarted"
	interruptedWork := len(inputs) > 0
	for _, turn := range turns {
		interruptedWork = interruptedWork || (!turn.completed && turn.started)
	}
	if interruptedWork {
		message += "; unfinished work was interrupted"
	}
	if err := journal.Append(Event{Type: EventRuntimeRecovered, Text: message, Status: "recovered"}); err != nil {
		return recovery, fmt.Errorf("recording Session recovery: %w", err)
	}
	for _, inputID := range inputOrder {
		if turnID, pending := inputs[inputID]; pending {
			if err := journal.Append(Event{Type: EventInputResolved, TurnID: turnID, InputID: inputID, Status: "cancelled"}); err != nil {
				return recovery, fmt.Errorf("recording recovered Session input: %w", err)
			}
		}
	}
	unstartedTurns := make([]turnRequest, 0, 1)
	for _, turnID := range turnOrder {
		turn := turns[turnID]
		if turn.completed {
			continue
		}
		if !turn.started {
			accepted := make(chan struct{})
			close(accepted)
			command := sessionCommand{kind: sessionCommandMessage, text: turn.text}
			if turn.command {
				var err error
				command, err = parseSessionCommand(turn.text)
				if err != nil {
					return recovery, fmt.Errorf("recovering Session command %q: %w", turn.id, err)
				}
			}
			unstartedTurns = append(unstartedTurns, turnRequest{id: turn.id, text: turn.text, revision: turn.revision, attachments: turn.attachments, command: command, accepted: accepted})
			continue
		}
		if err := journal.Append(Event{Type: EventTurnCompleted, TurnID: turnID, Status: "interrupted"}); err != nil {
			return recovery, fmt.Errorf("recording recovered Session turn: %w", err)
		}
		if value := numericEventID(turnID, "turn-"); value > recovery.completedTurnID {
			recovery.completedTurnID = value
		}
	}
	if len(unstartedTurns) > 0 {
		sort.SliceStable(unstartedTurns, func(i, j int) bool {
			left := numericEventID(unstartedTurns[i].id, "turn-")
			right := numericEventID(unstartedTurns[j].id, "turn-")
			if left > 0 && right > 0 {
				return left < right
			}
			if (left > 0) != (right > 0) {
				return left > 0
			}
			return unstartedTurns[i].id < unstartedTurns[j].id
		})
		pending := unstartedTurns[0]
		if len(unstartedTurns) > 1 {
			for _, turn := range unstartedTurns[1:] {
				pending.text = mergePendingMessageText(pending.text, turn.text)
				pending.attachments = append(pending.attachments, turn.attachments...)
				if err := journal.Append(Event{Type: EventTurnCompleted, TurnID: turn.id, Status: "merged"}); err != nil {
					return recovery, fmt.Errorf("recording merged Session turn: %w", err)
				}
				if value := numericEventID(turn.id, "turn-"); value > recovery.completedTurnID {
					recovery.completedTurnID = value
				}
			}
			pending.revision = max(1, pending.revision) + 1
			if err := journal.Append(Event{
				Type:           EventUserMessageUpdated,
				TurnID:         pending.id,
				Text:           pending.text,
				Revision:       pending.revision,
				SessionCommand: pending.command.kind != sessionCommandMessage,
				Attachments:    pending.attachments,
			}); err != nil {
				return recovery, fmt.Errorf("recording pending Session message: %w", err)
			}
		}
		recovery.pendingTurn = &pending
	}
	return recovery, nil
}

func (s *Server) restoreTurn(turn *turnRequest) error {
	if turn == nil {
		return nil
	}
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	s.turns <- *turn
	s.pendingTurn = turn
	s.outstanding++
	return nil
}

func numericEventID(value, prefix string) int64 {
	if !strings.HasPrefix(value, prefix) {
		return 0
	}
	number, err := strconv.ParseInt(strings.TrimPrefix(value, prefix), 10, 64)
	if err != nil || number < 0 {
		return 0
	}
	return number
}

func (s *Server) interruptTurn(runtimeCtx context.Context, requestID string) error {
	s.activeMu.Lock()
	turnID := s.activeTurn
	turnCancel := s.activeTurnCancel
	turnDone := s.activeTurnDone
	s.activeMu.Unlock()
	if turnID == "" {
		return ErrNoActiveTurn
	}
	s.interruptMu.Lock()
	defer s.interruptMu.Unlock()
	s.activeMu.Lock()
	activeTurn := s.activeTurn
	activeKind := s.activeKind
	s.activeMu.Unlock()
	if activeTurn != turnID {
		return nil
	}
	if err := s.journal.Append(Event{Type: EventTurnInterrupting, RequestID: requestID, TurnID: turnID, Status: "interrupting"}); err != nil {
		return fmt.Errorf("recording Session interruption: %w", err)
	}
	interruptCtx, cancel := context.WithTimeout(runtimeCtx, s.interruptTimeout)
	defer cancel()
	if activeKind == sessionCommandShell {
		if turnCancel == nil || turnDone == nil {
			return errors.New("Session active shell command cannot be stopped")
		}
		turnCancel(ErrTurnInterrupted)
		select {
		case <-turnDone:
			return nil
		case <-interruptCtx.Done():
			return interruptCtx.Err()
		}
	}
	interruptResult := make(chan error, 1)
	go func() {
		interruptResult <- s.provider.Interrupt(interruptCtx)
	}()
	select {
	case err := <-interruptResult:
		if err != nil && !errors.Is(err, ErrNoActiveTurn) {
			if runtimeCtx.Err() != nil {
				return runtimeCtx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Session provider interruption timed out turnID=%s", turnID)
			} else {
				log.Printf("Session provider interruption failed; stopping provider turnID=%s error=%v", turnID, err)
			}
			return s.stopInterruptedTurn(turnID, turnCancel, turnDone)
		}
	case <-interruptCtx.Done():
		if runtimeCtx.Err() != nil {
			return runtimeCtx.Err()
		}
		log.Printf("Session provider interruption timed out turnID=%s", turnID)
		return s.stopInterruptedTurn(turnID, turnCancel, turnDone)
	}
	select {
	case <-turnDone:
		return nil
	case <-interruptCtx.Done():
		if runtimeCtx.Err() != nil {
			return runtimeCtx.Err()
		}
		log.Printf("Session provider interruption timed out turnID=%s", turnID)
		return s.stopInterruptedTurn(turnID, turnCancel, turnDone)
	}
}

func (s *Server) stopInterruptedTurn(turnID string, cancel context.CancelCauseFunc, done <-chan struct{}) error {
	if cancel == nil || done == nil {
		return errors.New("Session active turn cannot be stopped")
	}
	s.activeMu.Lock()
	if s.activeTurn != "" && s.activeTurn != turnID {
		s.activeMu.Unlock()
		return nil
	}
	s.activeMu.Unlock()
	s.submitMu.Lock()
	s.providerStopping.Store(true)
	s.providerStopOnce.Do(func() { close(s.providerStop) })
	s.submitMu.Unlock()
	cancel(ErrTurnInterrupted)
	if err := s.closeProvider(); err != nil {
		log.Printf("Unable to restart Session provider after forced interruption error=%v", err)
	}
	<-done
	return nil
}

func (s *Server) closeProvider() error {
	s.providerCloseOnce.Do(func() {
		s.providerCloseErr = s.provider.Close()
	})
	return s.providerCloseErr
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan Event, 512)
	var writeMu sync.Mutex
	writerDone := make(chan error, 1)
	go func() {
		encoder := json.NewEncoder(connection)
		for {
			select {
			case <-connectionCtx.Done():
				writerDone <- nil
				return
			case event := <-out:
				if err := encoder.Encode(event); err != nil {
					writerDone <- err
					cancel()
					return
				}
			}
		}
	}()

	var subscriptionCancel func()
	defer func() {
		if subscriptionCancel != nil {
			subscriptionCancel()
		}
	}()
	subscribe := func(since int64, journalID string, includeHistoryBounds bool, historyItems, historyBytes int) {
		if subscriptionCancel != nil {
			return
		}
		var bounds HistoryBounds
		var retained []Event
		var stream <-chan Event
		var overflow <-chan struct{}
		var stop func()
		if includeHistoryBounds {
			bounds, retained, stream, overflow, stop = s.journal.SubscribeWithBounds(since, journalID)
		} else {
			retained, stream, overflow, stop = s.journal.Subscribe(since)
		}
		historyCursor := ""
		var historyState *HistoryState
		if includeHistoryBounds && historyItems > 0 && historyBytes > 0 && (since == 0 || bounds.Reset) {
			historyItems = min(historyItems, maxHistoryItemLimit)
			historyBytes = min(historyBytes, maxHistoryByteLimit)
			items, state, essential := projectHistory(retained)
			var beforeEventID int64
			retained, beforeEventID = historyItemsPage(items, 0, historyItems, historyBytes)
			retained = append(retained, essential...)
			historyState = &state
			if beforeEventID > 0 {
				historyCursor = encodeHistoryCursor(sessionHistoryCursor{
					JournalID:     bounds.JournalID,
					BeforeEventID: beforeEventID,
					ItemLimit:     historyItems,
					ByteLimit:     historyBytes,
				})
			}
		}
		statusStream, stopStatus := s.subscribeRuntimeStatus()
		subscriptionCancel = func() {
			stop()
			stopStatus()
		}
		disconnect := func() {
			cancel()
			_ = connection.Close()
		}
		enqueue := func(event Event) bool {
			select {
			case out <- event:
				return true
			case <-overflow:
				disconnect()
				return false
			case <-connectionCtx.Done():
				return false
			}
		}
		if includeHistoryBounds && !enqueue(Event{
			Type:           EventHistoryStart,
			FirstEventID:   bounds.FirstEventID,
			LastEventID:    bounds.LastEventID,
			JournalID:      bounds.JournalID,
			Reset:          bounds.Reset,
			HistoryLimited: historyCursor != "",
			HistoryCursor:  historyCursor,
		}) {
			return
		}
		for _, event := range retained {
			if !enqueue(event) {
				return
			}
		}
		if status := s.runtimeStatusSnapshot(); !status.empty() {
			if !enqueue(Event{Type: EventRuntimeStatus, Runtime: &status}) {
				return
			}
		}
		if !enqueue(Event{Type: EventHistoryEnd, HistoryState: historyState}) {
			return
		}
		go func() {
			for {
				select {
				case <-connectionCtx.Done():
					return
				case <-overflow:
					disconnect()
					return
				case event, ok := <-stream:
					if !ok {
						disconnect()
						return
					}
					writeMu.Lock()
					sent := sendLiveEvent(connectionCtx, out, overflow, event, disconnect)
					writeMu.Unlock()
					if !sent {
						return
					}
				case status, ok := <-statusStream:
					if !ok {
						return
					}
					writeMu.Lock()
					sent := sendLiveEvent(connectionCtx, out, overflow, Event{Type: EventRuntimeStatus, Runtime: &status}, disconnect)
					writeMu.Unlock()
					if !sent {
						return
					}
				}
			}
		}()
	}

	decoder := json.NewDecoder(bufio.NewReader(connection))
	for {
		var request ClientRequest
		if err := decoder.Decode(&request); err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("Session client read failed error=%v", err)
			}
			cancel()
			<-writerDone
			return
		}
		switch request.Type {
		case "subscribe":
			subscribe(request.Since, request.JournalID, request.HistoryBounds, request.HistoryItems, request.HistoryBytes)
		case "history":
			start, retained, err := s.loadHistoryPage(request.RequestID, request.HistoryCursor)
			if err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
				continue
			}
			writeMu.Lock()
			sent := sendHistoryPage(connectionCtx, out, start, retained)
			writeMu.Unlock()
			if !sent {
				return
			}
		case "prompts":
			event, err := s.loadPrompts(request.RequestID, request.HistoryCursor)
			if err != nil {
				event = Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
			writeMu.Lock()
			select {
			case out <- event:
			case <-connectionCtx.Done():
				writeMu.Unlock()
				return
			}
			writeMu.Unlock()
		case "message":
			subscribe(0, "", false, 0, 0)
			if err := s.submitClientMessage(ctx, request.Text, request.RequestID, request.AttachmentIDs...); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
		case "message.edit":
			subscribe(0, "", false, 0, 0)
			if err := s.editMessage(request.TurnID, request.Text, request.ExpectedRevision, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, TurnID: request.TurnID, Text: err.Error(), Status: "rejected"}
			}
		case "message.remove":
			subscribe(0, "", false, 0, 0)
			if err := s.removePendingMessage(request.TurnID, request.ExpectedRevision, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, TurnID: request.TurnID, Text: err.Error(), Status: "rejected"}
			}
		case "input":
			subscribe(0, "", false, 0, 0)
			if err := s.resolveInput(request.InputID, request.Answers, request.Cancel, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
		case "interrupt":
			subscribe(0, "", false, 0, 0)
			if err := s.interruptTurn(ctx, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
		default:
			out <- Event{Type: EventError, RequestID: request.RequestID, Text: fmt.Sprintf("unsupported client request type %q", request.Type), Status: "rejected"}
		}
	}
}

type sessionHistoryCursor struct {
	JournalID     string `json:"journalId"`
	BeforeEventID int64  `json:"beforeEventId"`
	ItemLimit     int    `json:"itemLimit"`
	ByteLimit     int    `json:"byteLimit"`
}

func encodeHistoryCursor(cursor sessionHistoryCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeHistoryCursor(value string) (sessionHistoryCursor, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return sessionHistoryCursor{}, errors.New("invalid Session history cursor")
	}
	var cursor sessionHistoryCursor
	if err := json.Unmarshal(encoded, &cursor); err != nil ||
		cursor.JournalID == "" || cursor.BeforeEventID <= 0 ||
		cursor.ItemLimit <= 0 || cursor.ItemLimit > maxHistoryItemLimit ||
		cursor.ByteLimit <= 0 || cursor.ByteLimit > maxHistoryByteLimit {
		return sessionHistoryCursor{}, errors.New("invalid Session history cursor")
	}
	return cursor, nil
}

func (s *Server) loadHistoryPage(requestID, value string) (Event, []Event, error) {
	cursor, err := decodeHistoryCursor(value)
	if err != nil {
		return Event{}, nil, err
	}
	bounds, events := s.journal.SnapshotWithBounds()
	if bounds.JournalID != cursor.JournalID {
		return Event{}, nil, errors.New("Session history cursor expired; reconnect to reload history")
	}
	items, _, _ := projectHistory(events)
	retained, beforeEventID := historyItemsPage(items, cursor.BeforeEventID, cursor.ItemLimit, cursor.ByteLimit)
	nextCursor := ""
	if beforeEventID > 0 {
		cursor.BeforeEventID = beforeEventID
		nextCursor = encodeHistoryCursor(cursor)
	}
	return Event{
		Type:           EventHistoryStart,
		RequestID:      requestID,
		JournalID:      bounds.JournalID,
		HistoryPage:    true,
		HistoryLimited: nextCursor != "",
		HistoryCursor:  nextCursor,
	}, retained, nil
}

func sendHistoryPage(ctx context.Context, out chan<- Event, start Event, events []Event) bool {
	send := func(event Event) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(start) {
		return false
	}
	for _, event := range events {
		if !send(event) {
			return false
		}
	}
	return send(Event{Type: EventHistoryEnd, RequestID: start.RequestID, HistoryPage: true})
}

func sendLiveEvent(ctx context.Context, out chan<- Event, overflow <-chan struct{}, event Event, disconnect func()) bool {
	select {
	case out <- event:
		return true
	case <-overflow:
		disconnect()
		return false
	case <-ctx.Done():
		return false
	}
}

type turnSink struct {
	server  *Server
	turnID  string
	mu      sync.Mutex
	stopped bool
}

func (s *turnSink) Emit(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if event.Type == EventRuntimeStatus && event.Runtime != nil {
		s.server.updateProviderRuntimeStatus(*event.Runtime)
		return
	}
	if event.TurnID == "" {
		event.TurnID = s.turnID
	}
	if event.Type == EventToolCompleted {
		event.Output = truncateToolOutput(event.Output)
	}
	_ = s.server.journal.Append(event)
}

func (s *turnSink) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

func (s *turnSink) RequestInput(ctx context.Context, request InputRequest) (map[string][]string, error) {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return nil, ErrInputCancelled
	}
	if request.ID == "" {
		request.ID = fmt.Sprintf("input-%d", s.server.nextInputID.Add(1))
	}
	if len(request.Questions) == 0 {
		return nil, errors.New("input request must contain at least one question")
	}
	questions := make(map[string]struct{}, len(request.Questions))
	for _, question := range request.Questions {
		if strings.TrimSpace(question.ID) == "" {
			return nil, errors.New("input question ID must not be empty")
		}
		if _, exists := questions[question.ID]; exists {
			return nil, fmt.Errorf("input question ID %q is duplicated", question.ID)
		}
		questions[question.ID] = struct{}{}
	}
	pending := &pendingInput{
		questions: questions,
		answers:   map[string][]string{},
		result:    make(chan pendingInputResult, 1),
	}
	s.server.inputMu.Lock()
	if _, exists := s.server.pendingInputs[request.ID]; exists {
		s.server.inputMu.Unlock()
		return nil, fmt.Errorf("input request %q is already pending", request.ID)
	}
	s.server.pendingInputs[request.ID] = pending
	s.server.inputMu.Unlock()
	s.server.pendingInputCount.Add(1)
	s.server.requestSessionStatusPublish()
	defer func() {
		s.server.inputMu.Lock()
		delete(s.server.pendingInputs, request.ID)
		s.server.inputMu.Unlock()
		s.server.pendingInputCount.Add(-1)
		s.server.requestSessionStatusPublish()
	}()

	s.Emit(Event{
		Type:      EventInputRequested,
		InputID:   request.ID,
		Questions: request.Questions,
		Status:    "pending",
	})
	select {
	case result := <-pending.result:
		if result.cancelled {
			return nil, ErrInputCancelled
		}
		return result.answers, nil
	case <-ctx.Done():
		s.server.inputMu.Lock()
		cancelled := !pending.resolved
		pending.resolved = true
		s.server.inputMu.Unlock()
		if cancelled {
			_ = s.server.journal.Append(Event{Type: EventInputResolved, InputID: request.ID, Status: "cancelled"})
		}
		return nil, ctx.Err()
	}
}

func (s *Server) resolveInput(id string, answers map[string][]string, cancel bool, requestID string) error {
	s.inputMu.Lock()
	pending := s.pendingInputs[id]
	if pending == nil {
		s.inputMu.Unlock()
		return fmt.Errorf("input request %q is not pending", id)
	}
	if pending.resolved {
		s.inputMu.Unlock()
		return fmt.Errorf("input request %q was already resolved", id)
	}
	if cancel {
		if err := s.journal.Append(Event{Type: EventInputResolved, RequestID: requestID, InputID: id, Status: "cancelled"}); err != nil {
			s.inputMu.Unlock()
			return fmt.Errorf("recording Session input cancellation: %w", err)
		}
		pending.resolved = true
		s.inputMu.Unlock()
		pending.result <- pendingInputResult{cancelled: true}
		return nil
	}
	if len(answers) == 0 {
		s.inputMu.Unlock()
		return errors.New("input response must contain at least one answer")
	}
	updated := make(map[string][]string, len(pending.answers)+len(answers))
	for questionID, values := range pending.answers {
		updated[questionID] = append([]string(nil), values...)
	}
	for questionID, values := range answers {
		if _, exists := pending.questions[questionID]; !exists {
			s.inputMu.Unlock()
			return fmt.Errorf("input question %q is not pending", questionID)
		}
		if len(values) == 0 {
			s.inputMu.Unlock()
			return fmt.Errorf("input question %q must contain an answer", questionID)
		}
		updated[questionID] = append([]string(nil), values...)
	}
	if len(updated) < len(pending.questions) {
		if err := s.journal.Append(Event{Type: EventRequestAccepted, RequestID: requestID, InputID: id, Status: "accepted"}); err != nil {
			s.inputMu.Unlock()
			return fmt.Errorf("recording Session input response: %w", err)
		}
		pending.answers = updated
		s.inputMu.Unlock()
		return nil
	}
	if err := s.journal.Append(Event{Type: EventInputResolved, RequestID: requestID, InputID: id, Status: "answered"}); err != nil {
		s.inputMu.Unlock()
		return fmt.Errorf("recording Session input response: %w", err)
	}
	pending.answers = updated
	pending.resolved = true
	resolved := make(map[string][]string, len(pending.answers))
	for questionID, values := range pending.answers {
		resolved[questionID] = append([]string(nil), values...)
	}
	s.inputMu.Unlock()
	pending.result <- pendingInputResult{answers: resolved}
	return nil
}
