package sessionturn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

// fakeRuntime plays the Session runtime side of the protocol: it reads client
// requests and emits the events a real runtime would emit for them.
type fakeRuntime struct {
	// events returns the events to emit for a client request. Returning false
	// stops the stream, which is how a dropped connection is simulated.
	events func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool)

	mu       sync.Mutex
	requests []sessionruntime.ClientRequest
}

// start wires a runtime to a pair of pipes and returns the client's ends.
func (f *fakeRuntime) start(t *testing.T) (io.Writer, io.Reader) {
	t.Helper()
	requestReader, requestWriter := io.Pipe()
	eventReader, eventWriter := io.Pipe()
	go func() {
		defer eventWriter.Close()
		decoder := json.NewDecoder(bufio.NewReader(requestReader))
		encoder := json.NewEncoder(eventWriter)
		for {
			var request sessionruntime.ClientRequest
			if err := decoder.Decode(&request); err != nil {
				return
			}
			f.mu.Lock()
			f.requests = append(f.requests, request)
			f.mu.Unlock()
			events, keepOpen := f.events(request)
			for _, event := range events {
				if err := encoder.Encode(event); err != nil {
					return
				}
			}
			if !keepOpen {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = requestWriter.Close()
		_ = eventReader.Close()
	})
	return requestWriter, eventReader
}

func (f *fakeRuntime) recorded() []sessionruntime.ClientRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessionruntime.ClientRequest(nil), f.requests...)
}

// replyingRuntime answers a subscribe with history and a message with a full
// turn producing the given assistant replies and final status.
func replyingRuntime(turnID, status string, replies ...string) *fakeRuntime {
	return &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventHistoryStart},
				{Type: sessionruntime.EventUserMessage, TurnID: "turn-old", Text: "earlier message"},
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-old", Text: "earlier reply"},
				{Type: sessionruntime.EventHistoryEnd},
			}, true
		case "message":
			events := []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: turnID, Text: request.Text},
				{Type: sessionruntime.EventTurnStarted, TurnID: turnID, Status: "running"},
			}
			for _, reply := range replies {
				events = append(events,
					sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, TurnID: turnID, Text: "partial"},
					sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, TurnID: turnID, Text: reply},
				)
			}
			events = append(events, sessionruntime.Event{Type: sessionruntime.EventTurnCompleted, TurnID: turnID, Status: status})
			return events, true
		}
		return nil, true
	}}
}

func TestRunTurnCollectsAssistantReply(t *testing.T) {
	runtime := replyingRuntime("turn-7", StatusCompleted, "first half", "second half")
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "what changed?")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.TurnID != "turn-7" {
		t.Errorf("TurnID = %q, want turn-7", result.TurnID)
	}
	if result.Status != StatusCompleted || !result.Succeeded() {
		t.Errorf("Status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.Text != "first half\n\nsecond half" {
		t.Errorf("Text = %q, want the two assistant messages joined", result.Text)
	}

	recorded := runtime.recorded()
	if len(recorded) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(recorded))
	}
	if recorded[0].Type != "subscribe" || !recorded[0].HistoryBounds {
		t.Errorf("first request = %+v, want a bounded subscribe", recorded[0])
	}
	if recorded[1].Type != "message" || recorded[1].Text != "what changed?" {
		t.Errorf("second request = %+v, want the message submission", recorded[1])
	}
	if recorded[1].RequestID == "" {
		t.Error("message request carried no request ID, so the turn could not be identified")
	}
}

func TestRunTurnIgnoresOtherTurns(t *testing.T) {
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				// A turn that was already running when the prompt arrived.
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-1", Text: "not ours"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: StatusCompleted},
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-2", Text: request.Text},
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-2", Text: "ours"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-2", Status: StatusCompleted},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "hello")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Text != "ours" {
		t.Errorf("Text = %q, want only this turn's reply", result.Text)
	}
	if result.TurnID != "turn-2" {
		t.Errorf("TurnID = %q, want turn-2", result.TurnID)
	}
}

func TestRunTurnFollowsMergedTurn(t *testing.T) {
	// A prompt submitted while another prompt is still queued is merged into
	// that queued turn, which the runtime reports as an update.
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessageUpdated, RequestID: request.RequestID, TurnID: "turn-3", Revision: 2},
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-3", Text: "merged reply"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-3", Status: StatusCompleted},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "and also this")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.TurnID != "turn-3" || result.Text != "merged reply" {
		t.Errorf("result = %+v, want the merged turn's reply", result)
	}
}

func TestRunTurnReportsFailedTurn(t *testing.T) {
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-9"},
				{Type: sessionruntime.EventError, TurnID: "turn-9", Text: "provider exploded", Status: "failed"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-9", Status: StatusFailed},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "go")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Succeeded() {
		t.Error("Succeeded() = true for a failed turn")
	}
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", result.Status, StatusFailed)
	}
	if result.Message != "provider exploded" {
		t.Errorf("Message = %q, want the runtime error text", result.Message)
	}
}

func TestRunTurnRejectedSubmission(t *testing.T) {
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventError, RequestID: request.RequestID, Text: "Session already has a pending message", Status: "rejected"},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	_, err := RunTurn(context.Background(), requests, events, "go")
	if err == nil {
		t.Fatal("RunTurn returned no error for a rejected submission")
	}
	if !strings.Contains(err.Error(), "pending message") {
		t.Errorf("error = %v, want it to carry the rejection reason", err)
	}
}

func TestRunTurnStreamClosesMidTurn(t *testing.T) {
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-4"},
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-4", Text: "half an answer"},
			}, false
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	_, err := RunTurn(context.Background(), requests, events, "go")
	if !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("error = %v, want ErrStreamClosed", err)
	}
}

func TestRunTurnHonoursContextCancellation(t *testing.T) {
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		if request.Type == "subscribe" {
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		}
		// A turn that never reports completion.
		return []sessionruntime.Event{
			{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-5"},
		}, true
	}}
	requests, events := runtime.start(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := RunTurn(ctx, requests, events, "go")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRunTurnRejectsEmptyPrompt(t *testing.T) {
	if _, err := RunTurn(context.Background(), io.Discard, strings.NewReader(""), "   "); err == nil {
		t.Fatal("RunTurn accepted an empty prompt")
	}
}

func TestRunTurnSkipsUnparseableLines(t *testing.T) {
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-6"},
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-6", Text: "answer"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-6", Status: StatusCompleted},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)
	// Prefix the runtime stream with a line that is not an event.
	noisy := io.MultiReader(strings.NewReader("not json\n"), events)

	result, err := RunTurn(context.Background(), requests, noisy, "go")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Text != "answer" {
		t.Errorf("Text = %q, want the assistant reply", result.Text)
	}
}

func TestRunTurnCollectsADeltaOnlyReply(t *testing.T) {
	// OpenCode never emits assistant.message, only deltas. A driver that
	// collected messages alone would report an empty reply for every turn.
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1"},
				{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Status: "running"},
				{Type: sessionruntime.EventAssistantDelta, TurnID: "turn-1", Text: "the build "},
				{Type: sessionruntime.EventAssistantDelta, TurnID: "turn-1", Text: "is green"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: StatusCompleted},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "how is the build?")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Text != "the build is green" {
		t.Errorf("Text = %q, want the deltas joined into one reply", result.Text)
	}
}

func TestRunTurnSeparatesDeltaSegments(t *testing.T) {
	// A tool call between two runs of deltas closes the first segment, so the
	// reply reads as two messages rather than one run-on.
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1"},
				{Type: sessionruntime.EventAssistantDelta, TurnID: "turn-1", Text: "checking"},
				{Type: sessionruntime.EventToolStarted, TurnID: "turn-1", ToolName: "Bash"},
				{Type: sessionruntime.EventToolCompleted, TurnID: "turn-1", ToolName: "Bash"},
				{Type: sessionruntime.EventAssistantDelta, TurnID: "turn-1", Text: "all good"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: StatusCompleted},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "check it")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Text != "checking\n\nall good" {
		t.Errorf("Text = %q, want the two segments kept apart", result.Text)
	}
}

func TestRunTurnPrefersTheCompleteAssistantMessage(t *testing.T) {
	// Claude and Codex close a streamed reply with the complete text, which
	// must replace the deltas rather than duplicate them.
	runtime := replyingRuntime("turn-1", StatusCompleted, "the complete answer")
	requests, events := runtime.start(t)

	result, err := RunTurn(context.Background(), requests, events, "go")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Text != "the complete answer" {
		t.Errorf("Text = %q, want only the complete message, not the deltas too", result.Text)
	}
}

func TestRunTurnStopsWhenTheQueuedPromptIsRemoved(t *testing.T) {
	// A console client can withdraw a queued prompt. The runtime then never
	// reports a completion for it, so waiting would burn the whole timeout.
	runtime := &fakeRuntime{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1", Revision: 1},
				{Type: sessionruntime.EventUserMessageRemoved, TurnID: "turn-1", Revision: 2},
			}, true
		}
		return nil, true
	}}
	requests, events := runtime.start(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RunTurn(ctx, requests, events, "go")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if result.Status != StatusRemoved {
		t.Errorf("Status = %q, want %q", result.Status, StatusRemoved)
	}
	if result.Succeeded() {
		t.Error("Succeeded() = true for a withdrawn prompt")
	}
}
