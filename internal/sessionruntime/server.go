package sessionruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultSocketPath                     = "/tmp/kelos-session/runtime.sock"
	DefaultStateDir                       = "/workspace/.kelos/session"
	DefaultWorkingDir                     = "/workspace/repo"
	journalFileName                       = "events.jsonl"
	initializedFile                       = "initialized"
	defaultWorkspaceStatusPublishInterval = 30 * time.Second
	defaultWorkspaceStatusRetryInterval   = 2 * time.Second
)

// Config configures the resident Session runtime.
type Config struct {
	SocketPath             string
	StateDir               string
	WorkingDir             string
	AgentType              string
	Model                  string
	Effort                 string
	PluginDir              string
	Environment            []string
	PublishWorkspaceStatus WorkspaceStatusPublisher
}

type turnRequest struct {
	id       string
	text     string
	accepted chan struct{}
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
	config   Config
	journal  *Journal
	provider Provider

	submitMu      sync.Mutex
	appendMessage func(Event) error
	turns         chan turnRequest
	nextTurnID    atomic.Int64
	activeMu      sync.Mutex
	activeTurn    string

	inputMu                        sync.Mutex
	pendingInputs                  map[string]*pendingInput
	nextInputID                    atomic.Int64
	refreshWorkspaceStatus         func(context.Context) error
	publishWorkspaceStatus         func(context.Context) error
	workspaceStatusRefreshes       chan struct{}
	workspaceStatusPublishInterval time.Duration
	workspaceStatusRetryInterval   time.Duration
}

// NewServer constructs a Session server around injected provider and journal implementations.
func NewServer(config Config, journal *Journal, provider Provider) *Server {
	return &Server{
		config:                         config,
		journal:                        journal,
		provider:                       provider,
		appendMessage:                  journal.Append,
		turns:                          make(chan turnRequest, 32),
		pendingInputs:                  map[string]*pendingInput{},
		workspaceStatusRefreshes:       make(chan struct{}, 1),
		workspaceStatusPublishInterval: defaultWorkspaceStatusPublishInterval,
		workspaceStatusRetryInterval:   defaultWorkspaceStatusRetryInterval,
	}
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
	publishWorkspaceStatus := func(ctx context.Context) error {
		status, err := readWorkspaceStatus(ctx, realWorkspaceStatusRunner{}, config.StateDir, config.WorkingDir)
		if err != nil {
			return err
		}
		return config.PublishWorkspaceStatus(ctx, status)
	}
	server.refreshWorkspaceStatus = func(ctx context.Context) error {
		status, refreshErr := refreshWorkspaceStatus(ctx, config.StateDir, config.WorkingDir, config.Environment)
		if config.PublishWorkspaceStatus == nil {
			return refreshErr
		}
		return errors.Join(refreshErr, config.PublishWorkspaceStatus(ctx, status))
	}
	if config.PublishWorkspaceStatus != nil {
		server.publishWorkspaceStatus = publishWorkspaceStatus
	}
	server.nextTurnID.Store(recovery.nextTurnID)
	server.nextInputID.Store(recovery.nextInputID)
	return server.Serve(ctx)
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
	if s.publishWorkspaceStatus != nil {
		go s.runWorkspaceStatusPublishes(serveCtx)
	}
	s.requestWorkspaceStatusRefresh()
	go s.runTurns(serveCtx)
	go func() {
		select {
		case <-serveCtx.Done():
		case <-providerDone:
		case <-s.journal.Failed():
		}
		_ = listener.Close()
	}()

	log.Printf("Session runtime ready socket=%s provider=%s", s.config.SocketPath, s.config.AgentType)
	defer func() {
		_ = s.provider.Close()
		s.journal.Close()
		_ = os.Remove(s.config.SocketPath)
	}()
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

func (s *Server) runTurns(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case turn := <-s.turns:
			select {
			case <-turn.accepted:
			case <-ctx.Done():
				return
			}
			s.runTurn(ctx, turn)
		}
	}
}

func (s *Server) requestWorkspaceStatusRefresh() {
	if s.refreshWorkspaceStatus == nil {
		return
	}
	select {
	case s.workspaceStatusRefreshes <- struct{}{}:
	default:
	}
}

func (s *Server) runWorkspaceStatusRefreshes(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.workspaceStatusRefreshes:
			if err := s.refreshWorkspaceStatus(ctx); err != nil {
				log.Printf("Unable to refresh Session workspace status error=%v", err)
			}
		}
	}
}

func (s *Server) runWorkspaceStatusPublishes(ctx context.Context) {
	timer := time.NewTimer(s.workspaceStatusRetryInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			next := s.workspaceStatusPublishInterval
			if err := s.publishWorkspaceStatus(ctx); err != nil {
				log.Printf("Unable to publish Session workspace status error=%v", err)
				next = s.workspaceStatusRetryInterval
			} else {
				s.requestWorkspaceStatusRefresh()
			}
			timer.Reset(next)
		}
	}
}

func (s *Server) runTurn(ctx context.Context, turn turnRequest) {
	defer s.requestWorkspaceStatusRefresh()
	s.activeMu.Lock()
	s.activeTurn = turn.id
	s.activeMu.Unlock()
	defer func() {
		s.activeMu.Lock()
		if s.activeTurn == turn.id {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
	}()

	if err := s.journal.Append(Event{Type: EventTurnStarted, TurnID: turn.id, Status: "running"}); err != nil {
		return
	}
	sink := &turnSink{server: s, turnID: turn.id}
	err := s.provider.RunTurn(ctx, turn.text, sink)
	if s.config.AgentType == "claude-code" || s.config.AgentType == "opencode" {
		if diff := workspaceDiff(ctx, s.config.WorkingDir); diff != "" {
			sink.Emit(Event{Type: EventFileDiff, Diff: diff})
		}
	}
	if errors.Is(err, ErrTurnInterrupted) {
		sink.Emit(Event{Type: EventTurnCompleted, Status: "interrupted"})
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		sink.Emit(Event{Type: EventError, Text: err.Error(), Status: "failed"})
		sink.Emit(Event{Type: EventTurnCompleted, Status: "failed"})
		return
	}
	sink.Emit(Event{Type: EventTurnCompleted, Status: "completed"})
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

func (s *Server) submitMessage(text, requestID string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("message must not be empty")
	}
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if err := s.journal.Err(); err != nil {
		return fmt.Errorf("recording Session message: %w", err)
	}
	turn := turnRequest{
		id:       fmt.Sprintf("turn-%d", s.nextTurnID.Add(1)),
		text:     text,
		accepted: make(chan struct{}),
	}
	select {
	case s.turns <- turn:
		if err := s.appendMessage(Event{Type: EventUserMessage, RequestID: requestID, TurnID: turn.id, Text: turn.text}); err != nil {
			return fmt.Errorf("recording Session message: %w", err)
		}
		close(turn.accepted)
		return nil
	default:
		return errors.New("Session message queue is full")
	}
}

type journalRecovery struct {
	nextTurnID  int64
	nextInputID int64
}

func recoverJournal(journal *Journal) (journalRecovery, error) {
	events := journal.Snapshot()
	if len(events) == 0 {
		return journalRecovery{}, nil
	}
	turns := map[string]bool{}
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
		if event.TurnID != "" && event.Type != EventTurnCompleted {
			if _, exists := turns[event.TurnID]; !exists {
				turnOrder = append(turnOrder, event.TurnID)
			}
			turns[event.TurnID] = true
		}
		switch event.Type {
		case EventTurnCompleted:
			delete(turns, event.TurnID)
		case EventInputRequested:
			if event.InputID != "" {
				if _, exists := inputs[event.InputID]; !exists {
					inputOrder = append(inputOrder, event.InputID)
				}
				inputs[event.InputID] = event.TurnID
			}
		case EventInputResolved:
			delete(inputs, event.InputID)
		}
	}
	message := "Session runtime restarted"
	if len(turns) > 0 || len(inputs) > 0 {
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
	for _, turnID := range turnOrder {
		if turns[turnID] {
			if err := journal.Append(Event{Type: EventTurnCompleted, TurnID: turnID, Status: "interrupted"}); err != nil {
				return recovery, fmt.Errorf("recording recovered Session turn: %w", err)
			}
		}
	}
	return recovery, nil
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

func (s *Server) interruptTurn(ctx context.Context, requestID string) error {
	s.activeMu.Lock()
	turnID := s.activeTurn
	s.activeMu.Unlock()
	if turnID == "" {
		return ErrNoActiveTurn
	}
	if err := s.journal.Append(Event{Type: EventTurnInterrupting, RequestID: requestID, TurnID: turnID, Status: "interrupting"}); err != nil {
		return fmt.Errorf("recording Session interruption: %w", err)
	}
	if err := s.provider.Interrupt(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan Event, 512)
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
	subscribe := func(since int64, journalID string, includeHistoryBounds bool) {
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
		subscriptionCancel = stop
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
			Type:         EventHistoryStart,
			FirstEventID: bounds.FirstEventID,
			LastEventID:  bounds.LastEventID,
			JournalID:    bounds.JournalID,
			Reset:        bounds.Reset,
		}) {
			return
		}
		for _, event := range retained {
			if !enqueue(event) {
				return
			}
		}
		if !enqueue(Event{Type: EventHistoryEnd}) {
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
					select {
					case out <- event:
					case <-overflow:
						disconnect()
						return
					case <-connectionCtx.Done():
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
			subscribe(request.Since, request.JournalID, request.HistoryBounds)
		case "message":
			subscribe(0, "", false)
			if err := s.submitMessage(request.Text, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
		case "input":
			subscribe(0, "", false)
			if err := s.resolveInput(request.InputID, request.Answers, request.Cancel, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
		case "interrupt":
			subscribe(0, "", false)
			if err := s.interruptTurn(connectionCtx, request.RequestID); err != nil {
				out <- Event{Type: EventError, RequestID: request.RequestID, Text: err.Error(), Status: "rejected"}
			}
		default:
			out <- Event{Type: EventError, RequestID: request.RequestID, Text: fmt.Sprintf("unsupported client request type %q", request.Type), Status: "rejected"}
		}
	}
}

type turnSink struct {
	server *Server
	turnID string
}

func (s *turnSink) Emit(event Event) {
	if event.TurnID == "" {
		event.TurnID = s.turnID
	}
	_ = s.server.journal.Append(event)
}

func (s *turnSink) RequestInput(ctx context.Context, request InputRequest) (map[string][]string, error) {
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
	defer func() {
		s.server.inputMu.Lock()
		delete(s.server.pendingInputs, request.ID)
		s.server.inputMu.Unlock()
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
