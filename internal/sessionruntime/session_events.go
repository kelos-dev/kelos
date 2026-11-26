package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// sessionEvents routes provider activity for the lifetime of the conversation.
// A provider may start another turn without a user submission.
type sessionEvents struct {
	mu               sync.Mutex
	server           *Server
	ctx              context.Context
	turn             *sessionTurn
	deferred         []Event
	suggestionTurnID string
	closed           bool
}

type sessionTurn struct {
	sink             *turnSink
	ctx              context.Context
	cancel           context.CancelCauseFunc
	done             chan struct{}
	kind             sessionCommandKind
	completion       *Event
	stopCancellation func() bool
}

func (s *sessionEvents) begin(ctx context.Context, request turnRequest) (*sessionTurn, turnRequest) {
	for {
		s.server.interruptMu.Lock()
		s.mu.Lock()
		if s.closed || ctx.Err() != nil || s.server.providerStopping.Load() {
			s.mu.Unlock()
			s.server.interruptMu.Unlock()
			return nil, request
		}
		if s.turn == nil {
			if request.accepted != nil {
				var exists bool
				request, exists = s.server.takePendingTurn(request)
				if !exists {
					s.mu.Unlock()
					s.server.interruptMu.Unlock()
					return nil, request
				}
			}
			if request.command.kind == "" {
				request.command = sessionCommand{kind: sessionCommandMessage, text: request.text}
			}
			turn := s.beginLocked(ctx, request, false)
			s.mu.Unlock()
			s.server.interruptMu.Unlock()
			return turn, request
		}
		done := s.turn.done
		s.mu.Unlock()
		s.server.interruptMu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, request
		case <-s.server.providerStop:
			return nil, request
		}
	}
}

func (s *sessionEvents) beginLocked(ctx context.Context, request turnRequest, autonomous bool) *sessionTurn {
	ctx, cancel := context.WithCancelCause(ctx)
	turn := &sessionTurn{sink: &turnSink{server: s.server, turnID: request.id}, ctx: ctx, cancel: cancel, done: make(chan struct{}), kind: request.command.kind}
	s.turn = turn
	s.suggestionTurnID = ""
	if autonomous {
		turn.stopCancellation = context.AfterFunc(ctx, func() { s.finish(turn, context.Cause(ctx)) })
	}
	s.server.activeMu.Lock()
	s.server.activeTurn = request.id
	s.server.activeKind = request.command.kind
	s.server.activeTurnCancel = cancel
	s.server.activeTurnDone = turn.done
	s.server.activeMu.Unlock()
	s.server.requestSessionStatusPublish()
	turn.sink.Emit(Event{Type: EventTurnStarted, Status: "running"})
	return turn
}

func (s *sessionEvents) Emit(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(event)
}

func (s *sessionEvents) emitLocked(event Event) {
	if s.closed {
		return
	}
	if event.Type == EventRuntimeStatus && event.Runtime != nil {
		s.server.updateProviderRuntimeStatus(*event.Runtime)
		return
	}
	// Keep provider activity in order while a local command or the current
	// turn's final workspace snapshot still occupies the client's active turn.
	if s.turn != nil && (s.turn.kind == sessionCommandShell || s.turn.completion != nil) {
		s.deferred = append(s.deferred, event)
		return
	}
	if event.Type == EventPromptSuggestion {
		if s.server.suggestionsEnabled() && s.turn == nil && s.suggestionTurnID != "" {
			event.TurnID = s.suggestionTurnID
			s.server.journal.broadcastLive(event)
		}
		return
	}
	if event.Type == EventTurnStarted {
		if s.turn == nil {
			s.server.submitMu.Lock()
			s.server.outstanding++
			s.server.submitMu.Unlock()
			s.beginLocked(s.ctx, turnRequest{id: fmt.Sprintf("turn-%d", s.server.nextTurnID.Add(1)), command: sessionCommand{kind: sessionCommandMessage}}, true)
		}
		return
	}
	if event.Type == EventTurnCompleted {
		if s.turn != nil {
			turnID := s.turn.sink.turnID
			s.finishLocked(s.turn, event.Status, event.Text)
			if s.server.suggestionsEnabled() && event.providerTurnID != "" && s.suggestionTurnID == turnID && s.server.journal.hasSubscribers() {
				if provider, ok := s.server.provider.(promptSuggestionProvider); ok {
					go s.suggestPrompt(provider, event.providerTurnID, turnID)
				}
			}
		}
		return
	}
	if s.turn != nil {
		s.turn.sink.Emit(event)
	} else if event.Type == EventGoalUpdated {
		_ = s.server.journal.Append(event)
	}
}

type promptSuggestionProvider interface {
	suggestPrompt(turnID string) string
}

func (s *sessionEvents) suggestPrompt(provider promptSuggestionProvider, providerTurnID, turnID string) {
	text := provider.suggestPrompt(providerTurnID)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && s.server.suggestionsEnabled() && s.suggestionTurnID == turnID {
		s.server.journal.broadcastLive(Event{Type: EventPromptSuggestion, TurnID: turnID, Text: text})
	}
}

func (s *sessionEvents) finish(turn *sessionTurn, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turn {
		return
	}
	status, message := "completed", ""
	if errors.Is(err, ErrTurnInterrupted) || errors.Is(context.Cause(turn.ctx), ErrTurnInterrupted) {
		status = "interrupted"
	} else if err != nil {
		status, message = "failed", err.Error()
	}
	s.finishLocked(turn, status, message)
}

func (s *sessionEvents) finishLocked(turn *sessionTurn, status, message string) {
	if turn.completion == nil && turn.ctx.Err() == nil && turn.kind != sessionCommandShell && (s.server.config.AgentType == "claude-code" || s.server.config.AgentType == "opencode") {
		turn.completion = &Event{Type: EventTurnCompleted, Status: status, Text: message}
		go func() {
			diff := s.server.readWorkspaceDiff(turn.ctx, s.server.config.WorkingDir)
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.turn != turn {
				return
			}
			if diff != "" && turn.ctx.Err() == nil {
				turn.sink.Emit(Event{Type: EventFileDiff, Diff: diff})
			}
			s.completeLocked(turn, status, message)
		}()
		return
	}
	s.completeLocked(turn, status, message)
}

func (s *sessionEvents) completeLocked(turn *sessionTurn, status, message string) {
	interrupted := errors.Is(context.Cause(turn.ctx), ErrTurnInterrupted)
	if interrupted {
		status, message = "interrupted", ""
	}
	if turn.ctx.Err() == nil || interrupted {
		if message != "" {
			turn.sink.Emit(Event{Type: EventError, Text: message, Status: "failed"})
		}
		turn.sink.Emit(Event{Type: EventTurnCompleted, Status: status})
		if status == "completed" && turn.kind != sessionCommandShell {
			s.suggestionTurnID = turn.sink.turnID
		}
	}
	turn.sink.stop()
	if turn.stopCancellation != nil {
		turn.stopCancellation()
	}
	turn.cancel(nil)
	s.turn = nil
	s.server.activeMu.Lock()
	s.server.activeTurn = ""
	s.server.activeKind = ""
	s.server.activeTurnCancel = nil
	s.server.activeTurnDone = nil
	s.server.activeMu.Unlock()
	s.server.markTurnCompleted(turn.sink.turnID)
	s.server.requestSessionStatusPublish()
	s.server.requestWorkspaceStatusRefresh(true)
	deferred := s.deferred
	s.deferred = nil
	for _, event := range deferred {
		s.emitLocked(event)
	}
	s.server.finishTurn()
	close(turn.done)
}

func (s *sessionEvents) RequestInput(ctx context.Context, request InputRequest) (map[string][]string, error) {
	var turn *sessionTurn
	for {
		s.mu.Lock()
		turn = s.turn
		waiting := turn != nil && (turn.kind == sessionCommandShell || turn.completion != nil)
		s.mu.Unlock()
		if turn == nil {
			return nil, ErrInputCancelled
		}
		if !waiting {
			break
		}
		select {
		case <-turn.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	inputCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(turn.ctx, cancel)
	defer stop()
	defer cancel()
	return turn.sink.RequestInput(inputCtx, request)
}

func (s *sessionEvents) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.turn != nil {
		s.completeLocked(s.turn, "interrupted", "")
	}
	s.closed = true
}

func (s *sessionEvents) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failedTurn := false
	for s.turn != nil {
		turn := s.turn
		if turn.completion != nil {
			s.completeLocked(turn, turn.completion.Status, turn.completion.Text)
		} else {
			s.completeLocked(turn, "failed", err.Error())
			failedTurn = true
		}
	}
	if !failedTurn {
		_ = s.server.journal.Append(Event{Type: EventError, Text: err.Error(), Status: "failed"})
	}
}
