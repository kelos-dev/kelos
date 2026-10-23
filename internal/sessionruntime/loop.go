package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func cloneLoop(loop *Loop) *Loop {
	if loop == nil {
		return nil
	}
	copy := *loop
	return &copy
}

func (j *Journal) currentLoop() *Loop {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.loopEvent == nil {
		return nil
	}
	return cloneLoop(j.loopEvent.Loop)
}

func (s *Server) initializeLoop() error {
	loop := s.journal.currentLoop()
	if loop == nil {
		return nil
	}
	interval, err := time.ParseDuration(loop.Interval)
	if err != nil || interval < time.Second || strings.TrimSpace(loop.Prompt) == "" ||
		(loop.Status != "active" && loop.Status != "paused") || loop.NextRunAt.IsZero() {
		return errors.New("Session journal contains an invalid loop")
	}
	s.loopActive.Store(loop.Status == "active")
	return nil
}

func (s *Server) controlLoop(command loopCommand, text, requestID string) error {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if (command.action == "create" || command.action == "resume") && (s.updateRequest != nil || s.idleDrainRequest != nil || s.providerStopping.Load()) {
		return errors.New("Session runtime is draining or restarting; retry after it reconnects")
	}
	loop := s.journal.currentLoop()
	status := "empty"
	switch command.action {
	case "show":
	case "create":
		if loop != nil {
			return errors.New("a loop is already set; use /loop clear first")
		}
		loop = &Loop{Prompt: command.prompt, Interval: command.interval.String(), Status: "active", NextRunAt: time.Now().UTC().Add(command.interval)}
	case "pause", "resume":
		if loop == nil {
			return errors.New("no loop is currently set")
		}
		loop.Status = "paused"
		if command.action == "resume" {
			loop.Status = "active"
			interval, _ := time.ParseDuration(loop.Interval)
			loop.NextRunAt = time.Now().UTC().Add(interval)
		}
	case "clear":
		loop, status = nil, "cleared"
	default:
		return errors.New(loopUsage)
	}
	if loop != nil {
		status = loop.Status
	}
	if err := s.journal.Append(Event{Type: EventUserMessage, RequestID: requestID, Text: text, SessionCommand: true}); err != nil {
		return fmt.Errorf("recording Session loop command: %w", err)
	}
	if err := s.journal.Append(Event{Type: EventLoopUpdated, Loop: loop, Status: status}); err != nil {
		return fmt.Errorf("recording Session loop: %w", err)
	}
	s.loopActive.Store(loop != nil && loop.Status == "active")
	s.signalLoop()
	s.requestSessionStatusPublish()
	s.signalSessionUpdateReport()
	return nil
}

func (s *Server) signalLoop() {
	select {
	case s.loopWakeups <- struct{}{}:
	default:
	}
}

func (s *Server) runLoop(ctx context.Context) {
	for ctx.Err() == nil {
		var timer *time.Timer
		var due <-chan time.Time
		if loop := s.journal.currentLoop(); loop != nil && loop.Status == "active" {
			if time.Until(loop.NextRunAt) > 0 {
				timer = time.NewTimer(time.Until(loop.NextRunAt))
				due = timer.C
			} else if err := s.submitLoopPrompt(time.Now().UTC()); err != nil {
				_ = s.journal.Append(Event{Type: EventError, Text: err.Error(), Status: "failed"})
				return
			}
		}
		stopped := false
		select {
		case <-ctx.Done():
			stopped = true
		case <-s.providerStop:
			stopped = true
		case <-s.journal.Failed():
			stopped = true
		case <-s.loopWakeups:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
		if stopped {
			return
		}
	}
}

func (s *Server) submitLoopPrompt(now time.Time) error {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	loop := s.journal.currentLoop()
	if loop == nil || loop.Status != "active" || now.Before(loop.NextRunAt) || s.outstanding > 0 ||
		s.backgroundActive.Load() || s.updateRequest != nil || s.idleDrainRequest != nil || s.providerStopping.Load() {
		return nil
	}
	interval, _ := time.ParseDuration(loop.Interval)
	loop.NextRunAt = now.Add(interval)
	if err := s.journal.Append(Event{Type: EventLoopUpdated, Loop: loop, Status: loop.Status}); err != nil {
		return fmt.Errorf("recording Session loop schedule: %w", err)
	}
	if err := s.enqueueTurnLocked(loop.Prompt, "", sessionCommand{kind: sessionCommandMessage, text: loop.Prompt}, nil); err != nil {
		return fmt.Errorf("submitting Session loop prompt: %w", err)
	}
	s.signalLoop()
	return nil
}
