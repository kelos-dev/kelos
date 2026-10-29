// Package sessionturn drives a single conversation turn on a running Session
// from outside the browser. The Session runtime exposes no request/response
// API for a turn: a client writes newline-delimited sessionruntime.ClientRequest
// values to the runtime socket and reads back a stream of sessionruntime.Event
// values. This package speaks that protocol and reaches the socket through the
// same pods/exec bridge the console server uses.
package sessionturn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

// Turn status values reported by the Session runtime for a completed turn.
const (
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
	// StatusRemoved means the queued prompt was withdrawn before it ran, so the
	// runtime will never report a completion for it.
	StatusRemoved = "removed"
)

// scanBufferLimit bounds one JSON event line. The console server applies the
// same limit to the stream it forwards to browsers.
const scanBufferLimit = 8 * 1024 * 1024

// eventChannelSize buffers decoded events so a slow consumer cannot stall the
// exec stream reader.
const eventChannelSize = 256

// ErrStreamClosed reports that the runtime stream ended before the turn did.
// The turn itself may still be running inside the Session.
var ErrStreamClosed = errors.New("Session runtime stream closed before the turn completed")

// Result describes the outcome of one driven turn.
type Result struct {
	// TurnID is the runtime-assigned identifier for the turn.
	TurnID string
	// Status is the turn.completed status: completed, failed or interrupted.
	Status string
	// Text is every assistant message emitted during the turn, joined by blank lines.
	Text string
	// Message is the text of the last error event recorded against the turn.
	Message string
	// DeclinedQuestions counts the provider questions the driver cancelled
	// because a Slack thread has no way to answer one mid-turn.
	DeclinedQuestions int
}

// Succeeded reports whether the turn ran to completion without a runtime error.
func (r Result) Succeeded() bool {
	return r.Status == StatusCompleted
}

// RunTurn submits prompt on an established runtime stream and collects the
// assistant reply. requests receives client requests; events carries the
// runtime's event stream. It returns once the runtime reports turn.completed
// for the submitted turn.
//
// A turn already running in the Session does not block submission: the runtime
// queues the new turn behind it, and RunTurn ignores the events of every turn
// but its own. A prompt that arrives while another prompt is still queued is
// merged into that queued turn by the runtime, and RunTurn then reports that
// turn's result.
func RunTurn(ctx context.Context, requests io.Writer, events io.Reader, prompt string) (Result, error) {
	if strings.TrimSpace(prompt) == "" {
		return Result{}, errors.New("Session turn prompt must not be empty")
	}

	stream, streamErr := readEvents(ctx, events)
	encoder := json.NewEncoder(requests)

	// Subscribe before submitting so the runtime has a client attached and the
	// history replay is bounded. Everything up to history.end is replay.
	subscribe := sessionruntime.ClientRequest{
		Type:          "subscribe",
		HistoryBounds: true,
		HistoryItems:  1,
		HistoryBytes:  1024,
	}
	if err := encoder.Encode(subscribe); err != nil {
		return Result{}, fmt.Errorf("subscribing to Session runtime: %w", err)
	}
	if err := awaitHistoryEnd(ctx, stream, streamErr); err != nil {
		return Result{}, err
	}

	requestID := uuid.NewString()
	message := sessionruntime.ClientRequest{
		Type:      "message",
		RequestID: requestID,
		Text:      prompt,
	}
	if err := encoder.Encode(message); err != nil {
		return Result{}, fmt.Errorf("submitting Session message: %w", err)
	}

	result, progress, err := collectTurn(ctx, stream, streamErr, encoder, requestID)
	if err != nil && ctx.Err() != nil {
		// An abandoned turn keeps running inside the Session, holding the
		// runtime against every later message in the thread. The caller keeps
		// the stream open a moment longer so it can be withdrawn.
		if abandonErr := abandonTurn(encoder, progress); abandonErr != nil {
			return result, errors.Join(err, abandonErr)
		}
	}
	return result, err
}

// turnProgress is what the driver knows about its own turn when it gives up.
type turnProgress struct {
	// turnID is empty until the runtime accepts the submission.
	turnID string
	// revision is the accepted revision of a turn still waiting to run.
	revision int64
	// started records that the runtime began running this turn.
	started bool
	// shared records that another client's prompt is part of this turn, either
	// because this prompt merged into one already waiting or because a later
	// prompt merged into this one.
	shared bool
}

// abandonTurn withdraws a turn the driver is no longer waiting for.
//
// Neither way of withdrawing one is precise enough to use blindly. An interrupt
// request carries no turn ID, so the runtime stops whichever turn is active:
// sending one for a turn that never started would stop somebody else's.
// Removing a pending turn deletes the whole turn, and the runtime merges
// prompts into one that is already waiting, so removing a shared turn deletes
// another client's text with it.
//
// So the driver withdraws only a turn that is entirely its own: a started one
// is interrupted, a still-queued one is removed, and a turn that is shared, or
// that the runtime never accepted, is left alone. A shared turn is not really
// abandoned anyway — another client is still waiting on it.
func abandonTurn(encoder *json.Encoder, progress turnProgress) error {
	switch {
	case progress.turnID == "", progress.shared:
		return nil
	case progress.started:
		if err := encoder.Encode(sessionruntime.ClientRequest{
			Type:      "interrupt",
			RequestID: uuid.NewString(),
		}); err != nil {
			return fmt.Errorf("interrupting the abandoned Session turn: %w", err)
		}
	default:
		if err := encoder.Encode(sessionruntime.ClientRequest{
			Type:             "message.remove",
			RequestID:        uuid.NewString(),
			TurnID:           progress.turnID,
			ExpectedRevision: progress.revision,
		}); err != nil {
			return fmt.Errorf("withdrawing the abandoned Session turn: %w", err)
		}
	}
	return nil
}

// awaitHistoryEnd drains the replayed history that follows a subscribe request.
func awaitHistoryEnd(ctx context.Context, stream <-chan sessionruntime.Event, streamErr <-chan error) error {
	for {
		event, err := nextEvent(ctx, stream, streamErr)
		if err != nil {
			return err
		}
		switch event.Type {
		case sessionruntime.EventHistoryEnd:
			return nil
		case sessionruntime.EventError:
			// An error emitted before any submission rejects the subscription itself.
			return fmt.Errorf("Session runtime rejected the subscription: %s", event.Text)
		}
	}
}

// collectTurn follows the stream until the turn opened by requestID completes.
func collectTurn(ctx context.Context, stream <-chan sessionruntime.Event, streamErr <-chan error, encoder *json.Encoder, requestID string) (Result, turnProgress, error) {
	var (
		result   Result
		progress turnProgress
		replies  []string
		segment  strings.Builder
		turnID   string
		accepted bool
	)
	// The runtime streams an assistant reply as deltas and, for some providers,
	// closes it with a complete assistant.message. OpenCode only ever emits
	// deltas, so a driver that collected messages alone would report an empty
	// reply for every OpenCode turn. Mirror the runtime's own history
	// projection: deltas accumulate, a non-empty assistant.message replaces
	// what they accumulated, and any other event closes the segment.
	flushSegment := func() {
		if text := strings.TrimSpace(segment.String()); text != "" {
			replies = append(replies, text)
		}
		segment.Reset()
	}
	for {
		event, err := nextEvent(ctx, stream, streamErr)
		if err != nil {
			return result, progress, err
		}

		if !accepted {
			// The runtime echoes the submission back as a user message carrying
			// the request ID, which is the only way to learn the turn ID. A
			// prompt merged into an already queued turn arrives as an update.
			switch event.Type {
			case sessionruntime.EventUserMessage, sessionruntime.EventUserMessageUpdated:
				if event.RequestID == requestID && event.TurnID != "" {
					turnID = event.TurnID
					result.TurnID = turnID
					progress.turnID = turnID
					progress.revision = max(event.Revision, 1)
					// An update rather than a new message means this prompt was
					// merged into a turn that was already waiting, so the turn
					// carries someone else's text too.
					progress.shared = event.Type == sessionruntime.EventUserMessageUpdated
					accepted = true
				}
			case sessionruntime.EventError:
				if event.RequestID == requestID {
					return result, progress, fmt.Errorf("Session runtime rejected the message: %s", event.Text)
				}
			}
			continue
		}

		if event.TurnID != turnID {
			continue
		}
		if event.Type != sessionruntime.EventAssistantDelta && event.Type != sessionruntime.EventAssistantMessage {
			flushSegment()
		}
		switch event.Type {
		case sessionruntime.EventUserMessageUpdated:
			// A later prompt merged into this turn while it waited to run.
			progress.revision = max(event.Revision, progress.revision)
			if event.RequestID != requestID {
				progress.shared = true
			}
		case sessionruntime.EventTurnStarted:
			progress.started = true
		case sessionruntime.EventAssistantDelta:
			segment.WriteString(event.Text)
		case sessionruntime.EventAssistantMessage:
			if event.Text != "" {
				segment.Reset()
				segment.WriteString(event.Text)
			}
			flushSegment()
		case sessionruntime.EventUserMessageRemoved:
			// A console client withdrew this queued prompt, so the runtime will
			// never report a completion for it. Waiting would burn the whole
			// turn timeout with the thread showing work that will never run.
			result.Status = StatusRemoved
			result.Text = strings.Join(replies, "\n\n")
			return result, progress, nil
		case sessionruntime.EventInputRequested:
			// The provider is blocked on a question. A Slack thread cannot
			// answer one mid-turn, and leaving it unanswered stalls the turn
			// until its timeout while holding the runtime, so decline it: the
			// provider is told the question was cancelled and carries on.
			if err := encoder.Encode(sessionruntime.ClientRequest{
				Type:      "input",
				RequestID: uuid.NewString(),
				InputID:   event.InputID,
				Cancel:    true,
			}); err != nil {
				return result, progress, fmt.Errorf("declining a Session question: %w", err)
			}
			result.DeclinedQuestions++
		case sessionruntime.EventError:
			result.Message = event.Text
		case sessionruntime.EventTurnCompleted:
			result.Status = event.Status
			result.Text = strings.Join(replies, "\n\n")
			return result, progress, nil
		}
	}
}

// nextEvent returns the next event, or the reason no further event will arrive.
func nextEvent(ctx context.Context, stream <-chan sessionruntime.Event, streamErr <-chan error) (sessionruntime.Event, error) {
	select {
	case <-ctx.Done():
		return sessionruntime.Event{}, ctx.Err()
	case event, ok := <-stream:
		if !ok {
			select {
			case err := <-streamErr:
				if err != nil {
					return sessionruntime.Event{}, fmt.Errorf("reading Session runtime stream: %w", err)
				}
			default:
			}
			return sessionruntime.Event{}, ErrStreamClosed
		}
		return event, nil
	}
}

// readEvents decodes the newline-delimited event stream in the background so
// the caller can abandon a turn on context cancellation without waiting on a
// blocked read. The returned error channel is buffered and carries at most one
// value, sent before the event channel closes.
func readEvents(ctx context.Context, reader io.Reader) (<-chan sessionruntime.Event, <-chan error) {
	events := make(chan sessionruntime.Event, eventChannelSize)
	failure := make(chan error, 1)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), scanBufferLimit)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var event sessionruntime.Event
			if err := json.Unmarshal(line, &event); err != nil {
				// A line the runtime emits outside the protocol is not fatal;
				// the turn's own events still arrive.
				continue
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			failure <- err
		}
	}()
	return events, failure
}
