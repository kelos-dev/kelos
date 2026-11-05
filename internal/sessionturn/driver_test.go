package sessionturn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

// scriptedExecutor plays the in-Pod runtime client: it reads client requests
// from stdin and writes the scripted events to stdout.
type scriptedExecutor struct {
	events func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool)
	stderr string
	err    error
	url    *url.URL
}

func (e *scriptedExecutor) Stream(remotecommand.StreamOptions) error {
	return errors.New("unused")
}

func (e *scriptedExecutor) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	if e.stderr != "" && options.Stderr != nil {
		_, _ = io.WriteString(options.Stderr, e.stderr)
	}
	if e.err != nil {
		return e.err
	}
	decoder := json.NewDecoder(options.Stdin)
	encoder := json.NewEncoder(options.Stdout)
	for {
		var request sessionruntime.ClientRequest
		if err := decoder.Decode(&request); err != nil {
			return nil
		}
		events, keepOpen := e.events(request)
		for _, event := range events {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		if !keepOpen {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func newTestDriver(t *testing.T, executor *scriptedExecutor) *Driver {
	t.Helper()
	driver, err := NewDriver(&rest.Config{Host: "http://kubernetes.example"})
	if err != nil {
		t.Fatal(err)
	}
	driver.NewExecutor = func(_ *rest.Config, method string, requestURL *url.URL) (remotecommand.Executor, error) {
		if method != http.MethodPost {
			t.Errorf("exec method = %q, want POST", method)
		}
		executor.url = requestURL
		return executor, nil
	}
	driver.Timeout = 10 * time.Second
	return driver
}

func TestDriverSubmitDrivesATurn(t *testing.T) {
	executor := &scriptedExecutor{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return []sessionruntime.Event{
				{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1"},
				{Type: sessionruntime.EventAssistantMessage, TurnID: "turn-1", Text: "the answer"},
				{Type: sessionruntime.EventTurnCompleted, TurnID: "turn-1", Status: StatusCompleted},
			}, true
		}
		return nil, true
	}}
	driver := newTestDriver(t, executor)

	result, err := driver.Submit(context.Background(), "default", "thread-session-0", "what changed?")
	if err != nil {
		t.Fatalf("Submit returned an error: %v", err)
	}
	if !result.Succeeded() || result.Text != "the answer" {
		t.Errorf("result = %+v, want the completed reply", result)
	}

	if executor.url == nil {
		t.Fatal("Submit did not build an exec request")
	}
	if executor.url.Path != "/api/v1/namespaces/default/pods/thread-session-0/exec" {
		t.Errorf("exec path = %q, want the Session Pod exec subresource", executor.url.Path)
	}
	query := executor.url.Query()
	if got := query["command"]; len(got) != 2 || got[0] != runtimeExecutable || got[1] != "client" {
		t.Errorf("exec command = %v, want the runtime client", got)
	}
	if query.Get("stdin") != "true" || query.Get("stdout") != "true" {
		t.Errorf("exec query = %v, want stdin and stdout attached", query)
	}
	if got := query.Get("container"); got != "kelos-agent" {
		t.Errorf("exec container = %q, want kelos-agent", got)
	}
}

func TestDriverSubmitReportsStderrFromTheStream(t *testing.T) {
	executor := &scriptedExecutor{
		stderr: "cannot exec into a terminating Pod",
		err:    errors.New("exec failed"),
	}
	driver := newTestDriver(t, executor)

	_, err := driver.Submit(context.Background(), "default", "thread-session-0", "go")
	if err == nil {
		t.Fatal("Submit returned no error for a failed exec stream")
	}
	if !strings.Contains(err.Error(), "cannot exec into a terminating Pod") {
		t.Errorf("error = %v, want it to carry the stream's stderr", err)
	}
}

func TestDriverSubmitRejectsEmptyPodName(t *testing.T) {
	driver := newTestDriver(t, &scriptedExecutor{})
	if _, err := driver.Submit(context.Background(), "default", "", "go"); err == nil {
		t.Fatal("Submit accepted an empty Pod name")
	}
}

func TestDriverSubmitTimesOut(t *testing.T) {
	executor := &scriptedExecutor{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		if request.Type == "subscribe" {
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		}
		// A turn that never completes.
		return []sessionruntime.Event{
			{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1"},
		}, true
	}}
	driver := newTestDriver(t, executor)
	driver.Timeout = 100 * time.Millisecond

	_, err := driver.Submit(context.Background(), "default", "thread-session-0", "go")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestDriverSubmitUsesTheConfiguredTimeout(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	executor := &scriptedExecutor{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		if request.Type == "subscribe" {
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		}
		once.Do(func() { close(started) })
		return []sessionruntime.Event{
			{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1"},
		}, true
	}}
	driver := newTestDriver(t, executor)
	driver.Timeout = 50 * time.Millisecond

	begin := time.Now()
	_, err := driver.Submit(context.Background(), "default", "thread-session-0", "go")
	elapsed := time.Since(begin)

	<-started
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	// The default is an hour; a configured timeout must actually replace it.
	if elapsed > 30*time.Second {
		t.Errorf("Submit took %s, want it bounded by the configured timeout", elapsed)
	}
}

func TestDriverTurnTimeout(t *testing.T) {
	tests := []struct {
		name       string
		configured time.Duration
		want       time.Duration
	}{
		{name: "unset falls back to the default", configured: 0, want: DefaultTurnTimeout},
		{name: "negative falls back to the default", configured: -time.Second, want: DefaultTurnTimeout},
		{name: "configured value is used", configured: 5 * time.Minute, want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &Driver{Timeout: tt.configured}
			if got := driver.turnTimeout(); got != tt.want {
				t.Errorf("turnTimeout() = %s, want %s", got, tt.want)
			}
		})
	}
}

// abandonedTurnRequests drives a turn that never completes and returns every
// client request the runtime saw, so the withdrawal can be inspected.
func abandonedTurnRequests(t *testing.T, started bool) []sessionruntime.ClientRequest {
	t.Helper()
	return abandonedTurnRequestsWith(t, func(request sessionruntime.ClientRequest) []sessionruntime.Event {
		events := []sessionruntime.Event{
			{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1", Revision: 1},
		}
		if started {
			events = append(events, sessionruntime.Event{Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Status: "running"})
		}
		// Either way the turn never completes.
		return events
	})
}

// abandonedTurnRequestsWith drives a turn whose acceptance the caller scripts,
// lets it time out, and returns every client request the runtime saw.
func abandonedTurnRequestsWith(t *testing.T, accept func(sessionruntime.ClientRequest) []sessionruntime.Event) []sessionruntime.ClientRequest {
	t.Helper()
	var (
		mu       sync.Mutex
		requests []sessionruntime.ClientRequest
	)
	executor := &scriptedExecutor{events: func(request sessionruntime.ClientRequest) ([]sessionruntime.Event, bool) {
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		switch request.Type {
		case "subscribe":
			return []sessionruntime.Event{{Type: sessionruntime.EventHistoryEnd}}, true
		case "message":
			return accept(request), true
		}
		return nil, true
	}}
	driver := newTestDriver(t, executor)
	driver.Timeout = 100 * time.Millisecond

	if _, err := driver.Submit(context.Background(), "default", "thread-session-0", "go"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]sessionruntime.ClientRequest(nil), requests...)
}

func TestDriverSubmitInterruptsAnAbandonedTurnThatStarted(t *testing.T) {
	// A turn that is running holds the runtime against every later message in
	// the thread, so it has to be interrupted.
	var interrupted bool
	for _, request := range abandonedTurnRequests(t, true) {
		if request.Type == "interrupt" {
			interrupted = true
		}
	}
	if !interrupted {
		t.Error("a started turn was abandoned without interrupting it")
	}
}

func TestDriverSubmitWithdrawsAnAbandonedTurnThatNeverStarted(t *testing.T) {
	// An interrupt carries no turn ID, so the runtime stops whichever turn is
	// active. For a turn still queued that would be somebody else's — a
	// terminal client on the same Session, or the turn this one waits behind.
	var (
		interrupted bool
		removed     sessionruntime.ClientRequest
	)
	for _, request := range abandonedTurnRequests(t, false) {
		switch request.Type {
		case "interrupt":
			interrupted = true
		case "message.remove":
			removed = request
		}
	}
	if interrupted {
		t.Error("a queued turn was abandoned with an interrupt, which would stop whichever turn is running")
	}
	if removed.TurnID != "turn-1" {
		t.Errorf("message.remove turnID = %q, want turn-1", removed.TurnID)
	}
	if removed.ExpectedRevision != 1 {
		t.Errorf("message.remove expectedRevision = %d, want the accepted revision", removed.ExpectedRevision)
	}
}

// withdrawalRequests reports the requests that would take a turn away from the
// runtime, which is what must not happen to a turn shared with another client.
func withdrawalRequests(requests []sessionruntime.ClientRequest) []sessionruntime.ClientRequest {
	var withdrawals []sessionruntime.ClientRequest
	for _, request := range requests {
		if request.Type == "interrupt" || request.Type == "message.remove" {
			withdrawals = append(withdrawals, request)
		}
	}
	return withdrawals
}

func TestDriverSubmitLeavesATurnItMergedIntoAlone(t *testing.T) {
	// This prompt merged into a turn that was already waiting, so the turn
	// carries another client's text. Removing it would delete their prompt.
	requests := abandonedTurnRequestsWith(t, func(request sessionruntime.ClientRequest) []sessionruntime.Event {
		return []sessionruntime.Event{
			{Type: sessionruntime.EventUserMessageUpdated, RequestID: request.RequestID, TurnID: "turn-1", Revision: 2},
		}
	})
	if withdrawals := withdrawalRequests(requests); len(withdrawals) != 0 {
		t.Errorf("withdrew a shared turn with %+v, which would discard another client's prompt", withdrawals)
	}
}

func TestDriverSubmitLeavesATurnAnotherClientMergedIntoAlone(t *testing.T) {
	// This prompt created the turn, but another client merged into it while it
	// waited. Removing it now would take their text with it.
	requests := abandonedTurnRequestsWith(t, func(request sessionruntime.ClientRequest) []sessionruntime.Event {
		return []sessionruntime.Event{
			{Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-1", Revision: 1},
			{Type: sessionruntime.EventUserMessageUpdated, RequestID: "another-client", TurnID: "turn-1", Revision: 2},
		}
	})
	if withdrawals := withdrawalRequests(requests); len(withdrawals) != 0 {
		t.Errorf("withdrew a shared turn with %+v, which would discard another client's prompt", withdrawals)
	}
}
