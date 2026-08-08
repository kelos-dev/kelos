package sessionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionupdate"
	kelosfake "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/fake"
)

type fakeProvider struct {
	mu        sync.Mutex
	prompts   []string
	resume    chan struct{}
	closed    bool
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
}

type inputProvider struct {
	answers   chan map[string][]string
	done      chan struct{}
	closeOnce sync.Once
}

func (p *inputProvider) RunTurn(ctx context.Context, _ string, sink EventSink) error {
	answers, err := sink.RequestInput(ctx, InputRequest{
		ID: "input-test",
		Questions: []InputQuestion{
			{ID: "first", Question: "Choose the first value"},
			{ID: "second", Question: "Choose the second value", MultiSelect: true},
		},
	})
	if err != nil {
		return err
	}
	p.answers <- answers
	sink.Emit(Event{Type: EventAssistantMessage, Text: "answers received"})
	return nil
}

func (p *inputProvider) Interrupt(context.Context) error { return ErrNoActiveTurn }
func (p *inputProvider) Done() <-chan struct{}           { return p.done }
func (p *inputProvider) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return nil
}

type interruptProvider struct {
	started     chan struct{}
	interrupted chan struct{}
	done        chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
	closeOnce   sync.Once
}

type stuckInterruptProvider struct {
	started                chan struct{}
	interruptCalled        chan struct{}
	runStopped             chan struct{}
	done                   chan struct{}
	acknowledge            bool
	ignoreInterruptContext bool
	interruptErr           error
	startOnce              sync.Once
	interruptOnce          sync.Once
	closeOnce              sync.Once
}

type turnCompletionRaceProvider struct {
	mu              sync.Mutex
	runCount        int
	firstStarted    chan struct{}
	secondStarted   chan struct{}
	finishFirst     chan struct{}
	interruptCalled chan struct{}
	finishInterrupt chan struct{}
	done            chan struct{}
	interruptOnce   sync.Once
	closeOnce       sync.Once
}

func (p *turnCompletionRaceProvider) RunTurn(ctx context.Context, _ string, _ EventSink) error {
	p.mu.Lock()
	p.runCount++
	runCount := p.runCount
	p.mu.Unlock()
	switch runCount {
	case 1:
		close(p.firstStarted)
		select {
		case <-p.finishFirst:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case 2:
		close(p.secondStarted)
		<-ctx.Done()
		return ctx.Err()
	default:
		return errors.New("unexpected provider turn")
	}
}

func (p *turnCompletionRaceProvider) Interrupt(ctx context.Context) error {
	p.interruptOnce.Do(func() { close(p.interruptCalled) })
	select {
	case <-p.finishInterrupt:
		return ErrNoActiveTurn
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *turnCompletionRaceProvider) Done() <-chan struct{} { return p.done }
func (p *turnCompletionRaceProvider) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return nil
}

func (p *stuckInterruptProvider) RunTurn(ctx context.Context, _ string, _ EventSink) error {
	p.startOnce.Do(func() { close(p.started) })
	<-p.runStopped
	return ctx.Err()
}

func (p *stuckInterruptProvider) Interrupt(ctx context.Context) error {
	p.interruptOnce.Do(func() { close(p.interruptCalled) })
	if p.interruptErr != nil {
		return p.interruptErr
	}
	if p.acknowledge {
		return nil
	}
	if p.ignoreInterruptContext {
		<-p.done
		return errors.New("provider stopped")
	}
	<-ctx.Done()
	return ctx.Err()
}

func (p *stuckInterruptProvider) Done() <-chan struct{} { return p.done }
func (p *stuckInterruptProvider) Close() error {
	p.closeOnce.Do(func() {
		close(p.runStopped)
		close(p.done)
	})
	return nil
}

func (p *interruptProvider) RunTurn(context.Context, string, EventSink) error {
	p.startOnce.Do(func() { close(p.started) })
	<-p.interrupted
	return ErrTurnInterrupted
}

func (p *interruptProvider) Interrupt(context.Context) error {
	p.stopOnce.Do(func() { close(p.interrupted) })
	return nil
}

func (p *interruptProvider) Done() <-chan struct{} { return p.done }
func (p *interruptProvider) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return nil
}

func (p *fakeProvider) RunTurn(ctx context.Context, prompt string, sink EventSink) error {
	p.mu.Lock()
	p.prompts = append(p.prompts, prompt)
	p.mu.Unlock()
	sink.Emit(Event{Type: EventAssistantDelta, Text: "working"})
	if p.resume != nil {
		select {
		case <-p.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	sink.Emit(Event{Type: EventAssistantDelta, Text: " done"})
	return nil
}

func (p *fakeProvider) Interrupt(context.Context) error {
	return ErrNoActiveTurn
}

func (p *fakeProvider) Done() <-chan struct{} {
	p.doneOnce.Do(func() { p.done = make(chan struct{}) })
	return p.done
}

func (p *fakeProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.closeOnce.Do(func() {
		p.doneOnce.Do(func() { p.done = make(chan struct{}) })
		close(p.done)
	})
	return nil
}

func TestSessionSetupEnvironmentKeepsWorkspaceSetupCommand(t *testing.T) {
	setupCommand := `KELOS_SETUP_COMMAND=["sh","-c","pip install --user some-tool"]`
	environment := sessionSetupEnvironment([]string{setupCommand, "KELOS_SESSION_SETUP_ONLY=0"})
	values := map[string]string{}
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	if values["KELOS_SETUP_COMMAND"] != strings.TrimPrefix(setupCommand, "KELOS_SETUP_COMMAND=") {
		t.Fatalf("KELOS_SETUP_COMMAND = %q", values["KELOS_SETUP_COMMAND"])
	}
	if values["KELOS_SESSION_SETUP_ONLY"] != "1" {
		t.Fatalf("KELOS_SESSION_SETUP_ONLY = %q, want 1", values["KELOS_SESSION_SETUP_ONLY"])
	}
}

func TestRunTurnQueuesWorkspaceStatusRefresh(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	server.refreshWorkspaceStatus = func(ctx context.Context) error {
		close(refreshStarted)
		select {
		case <-releaseRefresh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runWorkspaceStatusRefreshes(ctx)

	turnDone := make(chan struct{})
	go func() {
		server.runTurn(ctx, turnRequest{id: "turn-1", text: "work"})
		close(turnDone)
	}()

	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("runTurn() waited for workspace status refresh")
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("workspace status refresh was not requested")
	}
	close(releaseRefresh)
}

func TestServerQueuesStatusPublicationAfterWorkspaceRefresh(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	refreshed := make(chan struct{})
	server.refreshWorkspaceStatus = func(context.Context) error {
		close(refreshed)
		return nil
	}
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runWorkspaceStatusRefreshes(ctx)

	server.requestSessionStatusPublish()
	select {
	case <-server.sessionStatusPublishWakeups:
	case <-time.After(time.Second):
		t.Fatal("initial Session status publication was not requested")
	}
	server.requestWorkspaceStatusRefresh()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("workspace status was not refreshed")
	}
	select {
	case <-server.sessionStatusPublishWakeups:
	case <-time.After(time.Second):
		t.Fatal("Session status publication was not requested after workspace refresh")
	}
	server.sessionStatusMu.Lock()
	defer server.sessionStatusMu.Unlock()
	want := []sessionStatusPublishRequest{{active: false}, {active: false}}
	if !reflect.DeepEqual(server.sessionStatusPublishQueue, want) {
		t.Fatalf("pending Session status = %v, want two idle publications", server.sessionStatusPublishQueue)
	}
}

func TestPublishObservedSessionStatusPublishesActivityWhenWorkspaceReadFails(t *testing.T) {
	readErr := errors.New("workspace unavailable")
	var got ObservedSessionStatus
	// A failing workspace inspection must not surface as an error: doing so would
	// retain the status-publish queue entry and wedge the idle-drain handshake even
	// though the Active condition was durably published. Only the publisher's own
	// error should gate retries.
	err := publishObservedSessionStatus(
		context.Background(),
		func(_ context.Context, status ObservedSessionStatus) error {
			got = status
			return nil
		},
		true,
		true,
		"gpt-5.6-sol",
		func(context.Context) (WorkspaceStatus, error) {
			return WorkspaceStatus{}, readErr
		},
	)
	if err != nil {
		t.Fatalf("publishObservedSessionStatus() error = %v, want nil so the drain can advance", err)
	}
	if !got.Active || !got.WaitingForInput || got.Model != "gpt-5.6-sol" || got.WorkspaceStatus != nil {
		t.Fatalf("published Session status = %#v, want active model with unobserved workspace", got)
	}
}

func TestPublishObservedSessionStatusReturnsPublisherError(t *testing.T) {
	publishErr := errors.New("api unavailable")
	err := publishObservedSessionStatus(
		context.Background(),
		func(context.Context, ObservedSessionStatus) error { return publishErr },
		true,
		false,
		"gpt-5.6-sol",
		func(context.Context) (WorkspaceStatus, error) { return WorkspaceStatus{}, nil },
	)
	if !errors.Is(err, publishErr) {
		t.Fatalf("publishObservedSessionStatus() error = %v, want %v", err, publishErr)
	}
}

func TestServerRetriesSessionStatusPublicationInOrder(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.sessionStatusRetryInterval = 10 * time.Millisecond
	server.sessionStatusPublishInterval = time.Hour
	attempts := 0
	activity := make(chan bool, 3)
	server.publishSessionStatus = func(_ context.Context, active, _ bool) error {
		attempts++
		activity <- active
		if attempts == 1 {
			return errors.New("not ready")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.runTurn(ctx, turnRequest{id: "turn-1", text: "work"})
	go server.runSessionStatusPublishes(ctx)

	assertActivity(t, activity, true)
	assertActivity(t, activity, true)
	assertActivity(t, activity, false)
}

func TestServerRefreshesWorkspaceStatusAfterPeriodicPublication(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.sessionStatusRetryInterval = time.Hour
	server.sessionStatusPublishInterval = 10 * time.Millisecond
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	server.refreshWorkspaceStatus = func(context.Context) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runSessionStatusPublishes(ctx)

	select {
	case <-server.workspaceStatusRefreshes:
	case <-time.After(time.Second):
		t.Fatal("periodic workspace status publication did not request a refresh")
	}
}

func TestRunTurnPublishesActivityTransitions(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	provider := &fakeProvider{resume: make(chan struct{})}
	server := NewServer(Config{}, journal, provider)
	server.sessionStatusPublishInterval = time.Hour
	activity := make(chan bool, 2)
	server.publishSessionStatus = func(_ context.Context, active, _ bool) error {
		activity <- active
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runSessionStatusPublishes(ctx)

	turnDone := make(chan struct{})
	go func() {
		server.runTurn(ctx, turnRequest{id: "turn-1", text: "work"})
		close(turnDone)
	}()
	assertActivity(t, activity, true)
	close(provider.resume)
	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("runTurn() did not finish")
	}
	assertActivity(t, activity, false)
}

func TestRunTurnPreservesShortActivityTransitions(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.sessionStatusPublishInterval = time.Hour
	activity := make(chan bool, 2)
	server.publishSessionStatus = func(_ context.Context, active, _ bool) error {
		activity <- active
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server.runTurn(ctx, turnRequest{id: "turn-1", text: "work"})
	go server.runSessionStatusPublishes(ctx)

	assertActivity(t, activity, true)
	assertActivity(t, activity, false)
}

func TestRequestInputPublishesWaitingForInputTransitions(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.sessionStatusPublishInterval = time.Hour
	type publishedStatus struct {
		active          bool
		waitingForInput bool
	}
	statuses := make(chan publishedStatus, 2)
	server.publishSessionStatus = func(_ context.Context, active, waitingForInput bool) error {
		statuses <- publishedStatus{active: active, waitingForInput: waitingForInput}
		return nil
	}
	server.activeMu.Lock()
	server.activeTurn = "turn-1"
	server.activeMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runSessionStatusPublishes(ctx)

	type inputResult struct {
		answers map[string][]string
		err     error
	}
	result := make(chan inputResult, 1)
	go func() {
		answers, err := (&turnSink{server: server, turnID: "turn-1"}).RequestInput(ctx, InputRequest{
			ID: "claude-request-1",
			Questions: []InputQuestion{{
				ID:       "question-1",
				Question: "Which database?",
			}},
		})
		result <- inputResult{answers: answers, err: err}
	}()

	assertStatus := func(want publishedStatus) {
		t.Helper()
		select {
		case got := <-statuses:
			if got != want {
				t.Fatalf("published Session status = %#v, want %#v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("Session status %#v was not published", want)
		}
	}
	assertStatus(publishedStatus{active: true, waitingForInput: true})

	if err := server.resolveInput("claude-request-1", map[string][]string{"question-1": {"PostgreSQL"}}, false, "response-1"); err != nil {
		t.Fatal(err)
	}
	assertStatus(publishedStatus{active: true, waitingForInput: false})
	select {
	case got := <-result:
		if got.err != nil || !reflect.DeepEqual(got.answers, map[string][]string{"question-1": {"PostgreSQL"}}) {
			t.Fatalf("input result = %#v, want PostgreSQL answer", got)
		}
	case <-time.After(time.Second):
		t.Fatal("input request did not finish")
	}
}

func assertActivity(t *testing.T, activity <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-activity:
		if got != want {
			t.Fatalf("published activity = %t, want %t", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("activity %t was not published", want)
	}
}

func TestServerSubmitsInitialPromptOnlyWithoutHistory(t *testing.T) {
	t.Run("empty journal", func(t *testing.T) {
		stateDir := shortRuntimeTempDir(t)
		journal := NewJournal()
		provider := &fakeProvider{}
		server := NewServer(Config{
			SocketPath:    filepath.Join(stateDir, "runtime.sock"),
			StateDir:      stateDir,
			WorkingDir:    stateDir,
			AgentType:     "fake",
			InitialPrompt: "Investigate issue #42",
		}, journal, provider)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		serveDone := make(chan error, 1)
		go func() { serveDone <- server.Serve(ctx) }()

		deadline := time.Now().Add(5 * time.Second)
		for {
			provider.mu.Lock()
			prompts := append([]string(nil), provider.prompts...)
			provider.mu.Unlock()
			if len(prompts) > 0 {
				if !reflect.DeepEqual(prompts, []string{"Investigate issue #42"}) {
					t.Fatalf("provider prompts = %v", prompts)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("initial prompt was not submitted")
			}
			select {
			case err := <-serveDone:
				t.Fatalf("Serve() returned before submitting the initial prompt: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}

		var initialMessage *Event
		for _, event := range journal.Snapshot() {
			if event.Type == EventUserMessage {
				copy := event
				initialMessage = &copy
				break
			}
		}
		if initialMessage == nil || initialMessage.RequestID != "initial-prompt" || initialMessage.Text != "Investigate issue #42" {
			t.Fatalf("initial user message = %#v", initialMessage)
		}

		cancel()
		if err := <-serveDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("existing journal", func(t *testing.T) {
		stateDir := shortRuntimeTempDir(t)
		journal := NewJournal()
		if err := journal.Append(Event{Type: EventUserMessage, Text: "Existing conversation"}); err != nil {
			t.Fatal(err)
		}
		provider := &fakeProvider{}
		server := NewServer(Config{
			SocketPath:    filepath.Join(stateDir, "runtime.sock"),
			StateDir:      stateDir,
			WorkingDir:    stateDir,
			AgentType:     "fake",
			InitialPrompt: "Do not submit",
		}, journal, provider)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		serveDone := make(chan error, 1)
		go func() { serveDone <- server.Serve(ctx) }()
		waitForRuntime(t, server.config.SocketPath)
		time.Sleep(50 * time.Millisecond)
		provider.mu.Lock()
		prompts := append([]string(nil), provider.prompts...)
		provider.mu.Unlock()
		if len(prompts) != 0 {
			t.Fatalf("provider prompts = %v, want none", prompts)
		}
		cancel()
		if err := <-serveDone; err != nil {
			t.Fatal(err)
		}
	})
}

func TestServerHealthWaitsForInitialPromptSubmission(t *testing.T) {
	stateDir := shortRuntimeTempDir(t)
	journal := NewJournal()
	provider := &fakeProvider{}
	deliveryStarted := make(chan struct{})
	release := make(chan struct{})
	server := NewServer(Config{
		SocketPath:    filepath.Join(stateDir, "runtime.sock"),
		StateDir:      stateDir,
		WorkingDir:    stateDir,
		AgentType:     "fake",
		InitialPrompt: "Investigate issue #42",
	}, journal, provider)
	appendMessage := server.appendMessage
	server.appendMessage = func(event Event) error {
		close(deliveryStarted)
		<-release
		return appendMessage(event)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()

	select {
	case <-deliveryStarted:
	case <-time.After(time.Second):
		cancel()
		close(release)
		<-serveDone
		t.Fatal("initial prompt submission did not start")
	}
	if err := Health(server.config.SocketPath); err == nil {
		cancel()
		close(release)
		<-serveDone
		t.Fatal("Session runtime reported healthy before initial prompt submission completed")
	}
	close(release)
	waitForRuntime(t, server.config.SocketPath)
	cancel()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestServerClosesResourcesWhenInitialPromptFails(t *testing.T) {
	stateDir := shortRuntimeTempDir(t)
	journal := NewJournal()
	provider := &fakeProvider{}
	server := NewServer(Config{
		SocketPath:    filepath.Join(stateDir, "runtime.sock"),
		StateDir:      stateDir,
		WorkingDir:    stateDir,
		AgentType:     "fake",
		InitialPrompt: "Investigate issue #42",
	}, journal, provider)
	server.appendMessage = func(Event) error { return errors.New("journal write failed") }

	err := server.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "submitting initial Session prompt") {
		t.Fatalf("Serve() error = %v", err)
	}
	provider.mu.Lock()
	providerClosed := provider.closed
	provider.mu.Unlock()
	if !providerClosed {
		t.Fatal("provider was not closed after initial prompt failure")
	}
	if err := journal.Append(Event{Type: EventUserMessage, Text: "after failure"}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("journal.Append() after Serve() = %v, want closed journal error", err)
	}
}

func TestServerSharesConversationAcrossConnections(t *testing.T) {
	stateDir := shortRuntimeTempDir(t)
	journal := NewJournal()
	provider := &fakeProvider{resume: make(chan struct{})}
	config := Config{
		SocketPath: filepath.Join(stateDir, "runtime.sock"),
		StateDir:   stateDir,
		WorkingDir: stateDir,
		AgentType:  "fake",
	}
	server := NewServer(config, journal, provider)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	waitForRuntime(t, config.SocketPath)

	connection, err := net.Dial("unix", config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(connection)
	decoder := json.NewDecoder(connection)
	if err := encoder.Encode(ClientRequest{Type: "subscribe"}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(ClientRequest{Type: "message", RequestID: "request-message", Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	var first []Event
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decoding first connection event: %v", err)
		}
		first = append(first, event)
		if event.Type == EventAssistantDelta && event.Text == "working" {
			break
		}
	}
	_ = connection.Close()
	assertEventTypes(t, first, EventUserMessage, EventTurnStarted, EventAssistantDelta)
	for _, event := range first {
		if event.Type == EventUserMessage && event.RequestID != "request-message" {
			t.Fatalf("user message request ID = %q", event.RequestID)
		}
	}

	second, err := net.Dial("unix", config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := json.NewEncoder(second).Encode(ClientRequest{Type: "subscribe"}); err != nil {
		t.Fatal(err)
	}
	secondDecoder := json.NewDecoder(second)
	var retained []Event
	for {
		var event Event
		if err := secondDecoder.Decode(&event); err != nil {
			t.Fatalf("decoding retained event: %v", err)
		}
		if event.Type == EventHistoryEnd {
			break
		}
		retained = append(retained, event)
	}
	assertEventTypes(t, retained, EventUserMessage, EventTurnStarted, EventAssistantDelta)
	close(provider.resume)
	var resumed []Event
	for {
		var event Event
		if err := secondDecoder.Decode(&event); err != nil {
			t.Fatalf("decoding resumed connection event: %v", err)
		}
		resumed = append(resumed, event)
		if event.Type == EventTurnCompleted {
			break
		}
	}
	assertEventTypes(t, resumed, EventAssistantDelta, EventTurnCompleted)

	provider.mu.Lock()
	if len(provider.prompts) != 1 || provider.prompts[0] != "hello" {
		t.Fatalf("provider prompts = %v, want [hello]", provider.prompts)
	}
	provider.mu.Unlock()

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not stop")
	}
}

func TestServerSharesInputRequestAcrossConnections(t *testing.T) {
	stateDir := shortRuntimeTempDir(t)
	journal := NewJournal()
	provider := &inputProvider{answers: make(chan map[string][]string, 1), done: make(chan struct{})}
	server := NewServer(Config{SocketPath: filepath.Join(stateDir, "runtime.sock")}, journal, provider)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	waitForRuntime(t, server.config.SocketPath)

	first, err := net.Dial("unix", server.config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	firstEncoder := json.NewEncoder(first)
	firstDecoder := json.NewDecoder(first)
	if err := firstEncoder.Encode(ClientRequest{Type: "subscribe"}); err != nil {
		t.Fatal(err)
	}
	if err := firstEncoder.Encode(ClientRequest{Type: "message", Text: "ask me"}); err != nil {
		t.Fatal(err)
	}
	for {
		var event Event
		if err := firstDecoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventInputRequested {
			if event.InputID != "input-test" || len(event.Questions) != 2 {
				t.Fatalf("input request = %#v", event)
			}
			break
		}
	}
	_ = first.Close()

	second, err := net.Dial("unix", server.config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondEncoder := json.NewEncoder(second)
	secondDecoder := json.NewDecoder(second)
	if err := secondEncoder.Encode(ClientRequest{Type: "subscribe"}); err != nil {
		t.Fatal(err)
	}
	var retained []Event
	for {
		var event Event
		if err := secondDecoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventHistoryEnd {
			break
		}
		retained = append(retained, event)
	}
	assertEventTypes(t, retained, EventInputRequested)
	if err := secondEncoder.Encode(ClientRequest{Type: "input", RequestID: "request-input-1", InputID: "input-test", Answers: map[string][]string{"first": {"one"}}}); err != nil {
		t.Fatal(err)
	}
	if err := secondEncoder.Encode(ClientRequest{Type: "input", RequestID: "request-input-2", InputID: "input-test", Answers: map[string][]string{"second": {"two", "three"}}}); err != nil {
		t.Fatal(err)
	}
	var resumed []Event
	for {
		var event Event
		if err := secondDecoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		resumed = append(resumed, event)
		if event.Type == EventTurnCompleted {
			break
		}
	}
	assertEventTypes(t, resumed, EventRequestAccepted, EventInputResolved, EventAssistantMessage, EventTurnCompleted)
	requestIDs := map[string]bool{}
	for _, event := range resumed {
		if event.RequestID != "" {
			requestIDs[event.RequestID] = true
		}
	}
	if !requestIDs["request-input-1"] || !requestIDs["request-input-2"] {
		t.Fatalf("input response request IDs = %v", requestIDs)
	}
	select {
	case answers := <-provider.answers:
		if !reflect.DeepEqual(answers, map[string][]string{"first": {"one"}, "second": {"two", "three"}}) {
			t.Fatalf("provider answers = %#v", answers)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive answers")
	}

	cancel()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestServerInterruptsActiveTurn(t *testing.T) {
	stateDir := shortRuntimeTempDir(t)
	journal := NewJournal()
	provider := &interruptProvider{
		started:     make(chan struct{}),
		interrupted: make(chan struct{}),
		done:        make(chan struct{}),
	}
	server := NewServer(Config{SocketPath: filepath.Join(stateDir, "runtime.sock")}, journal, provider)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	waitForRuntime(t, server.config.SocketPath)

	connection, err := net.Dial("unix", server.config.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	encoder := json.NewEncoder(connection)
	decoder := json.NewDecoder(connection)
	if err := encoder.Encode(ClientRequest{Type: "subscribe"}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(ClientRequest{Type: "message", Text: "work"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider turn did not start")
	}
	if err := encoder.Encode(ClientRequest{Type: "interrupt", RequestID: "request-interrupt"}); err != nil {
		t.Fatal(err)
	}
	var events []Event
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.Type == EventTurnCompleted {
			if event.Status != "interrupted" {
				t.Fatalf("turn completion = %#v", event)
			}
			break
		}
	}
	assertEventTypes(t, events, EventTurnInterrupting, EventTurnCompleted)
	for _, event := range events {
		if event.Type == EventTurnInterrupting && event.RequestID != "request-interrupt" {
			t.Fatalf("interrupt request ID = %q", event.RequestID)
		}
	}

	cancel()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestServerGracefullyInterruptsDrainingTurn(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	provider := &interruptProvider{
		started:     make(chan struct{}),
		interrupted: make(chan struct{}),
		done:        make(chan struct{}),
	}
	defer provider.Close()
	server := NewServer(Config{PodUID: types.UID("pod-uid")}, journal, provider)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		server.runTurns(ctx)
		close(runDone)
	}()
	if err := server.submitMessage("work", "request-work"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider turn did not start")
	}
	podUID := server.config.PodUID
	request := sessionupdate.NewRequest(podUID, "desired-revision")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.RequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := server.interruptTurn(t.Context(), "request-interrupt"); err != nil {
		t.Fatalf("interruptTurn() error = %v", err)
	}
	select {
	case <-provider.interrupted:
	default:
		t.Fatal("draining interruption did not call the provider")
	}
	select {
	case <-provider.done:
		t.Fatal("graceful draining interruption recycled the provider")
	default:
	}
	report, _ := server.sessionDrainReports()
	if report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("runtime update report = %#v, want Drained", report)
	}
	cancel()
	<-runDone
}

func TestServerForcesStuckInterruptAfterTimeout(t *testing.T) {
	for _, test := range []struct {
		name                   string
		acknowledge            bool
		ignoreInterruptContext bool
		interruptErr           error
	}{
		{name: "interrupt request does not return"},
		{name: "interrupt request ignores cancellation", ignoreInterruptContext: true},
		{name: "interrupt request fails", interruptErr: errors.New("interrupt failed")},
		{name: "turn does not finish after interrupt", acknowledge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, provider, turnDone := startStuckInterruptServer(t, test.acknowledge)
			provider.ignoreInterruptContext = test.ignoreInterruptContext
			provider.interruptErr = test.interruptErr
			server.interruptTimeout = 20 * time.Millisecond
			if err := server.interruptTurn(t.Context(), "request-interrupt"); err != nil {
				t.Fatalf("interruptTurn() error = %v", err)
			}
			assertStuckTurnInterrupted(t, server, provider, turnDone)
			select {
			case <-provider.interruptCalled:
			default:
				t.Fatal("provider interrupt was not attempted before timeout recovery")
			}
		})
	}
}

func TestServerCompletesAcceptedInterruptAfterClientDisconnect(t *testing.T) {
	server, provider, turnDone := startStuckInterruptServer(t, false)
	server.interruptTimeout = 20 * time.Millisecond
	serverConnection, clientConnection := net.Pipe()
	connectionDone := make(chan struct{})
	go func() {
		server.handleConnection(t.Context(), serverConnection)
		close(connectionDone)
	}()
	if err := json.NewEncoder(clientConnection).Encode(ClientRequest{Type: "interrupt", RequestID: "request-interrupt"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.interruptCalled:
	case <-time.After(time.Second):
		t.Fatal("provider interrupt was not attempted")
	}
	if err := clientConnection.Close(); err != nil {
		t.Fatal(err)
	}
	assertStuckTurnInterrupted(t, server, provider, turnDone)
	select {
	case <-connectionDone:
	case <-time.After(time.Second):
		t.Fatal("disconnected client handler did not finish")
	}
}

func TestServerCancelsInterruptWhenRuntimeStops(t *testing.T) {
	server, provider, _ := startStuckInterruptServer(t, false)
	defer provider.Close()
	server.interruptTimeout = time.Second
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- server.interruptTurn(runtimeCtx, "request-interrupt")
	}()
	select {
	case <-provider.interruptCalled:
	case <-time.After(time.Second):
		t.Fatal("provider interrupt was not attempted")
	}
	cancelRuntime()
	select {
	case err := <-interruptDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interruptTurn() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime shutdown did not cancel provider interruption")
	}
	select {
	case <-provider.done:
		t.Fatal("runtime cancellation forced provider recovery")
	default:
	}
}

func TestServerDoesNotInterruptNextTurnWhenCompletionRacesWithInterrupt(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	provider := &turnCompletionRaceProvider{
		firstStarted:    make(chan struct{}),
		secondStarted:   make(chan struct{}),
		finishFirst:     make(chan struct{}),
		interruptCalled: make(chan struct{}),
		finishInterrupt: make(chan struct{}),
		done:            make(chan struct{}),
	}
	defer provider.Close()
	server := NewServer(Config{}, journal, provider)
	if err := server.submitMessage("first", "request-first"); err != nil {
		t.Fatal(err)
	}
	if err := server.submitMessage("second", "request-second"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		server.runTurns(ctx)
		close(runDone)
	}()
	select {
	case <-provider.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first provider turn did not start")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- server.interruptTurn(t.Context(), "request-interrupt")
	}()
	select {
	case <-provider.interruptCalled:
	case <-time.After(time.Second):
		t.Fatal("provider interrupt was not attempted")
	}
	close(provider.finishFirst)
	select {
	case <-provider.secondStarted:
		t.Fatal("second turn started while the first turn interruption was still pending")
	case <-time.After(20 * time.Millisecond):
	}
	close(provider.finishInterrupt)
	if err := <-interruptDone; err != nil {
		t.Fatalf("interruptTurn() error = %v", err)
	}
	select {
	case <-provider.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second provider turn did not start after interruption settled")
	}
	cancel()
	<-runDone
}

func TestServerPreservesQueuedTurnAcrossForcedProviderRestart(t *testing.T) {
	server, provider, turnDone := startStuckInterruptServer(t, false)
	server.interruptTimeout = 20 * time.Millisecond
	if err := server.submitMessage("queued work", "request-queued"); err != nil {
		t.Fatal(err)
	}
	podUID := server.config.PodUID
	request := sessionupdate.NewRequest(podUID, "desired-revision")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.RequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := server.interruptTurn(t.Context(), "request-interrupt"); err != nil {
		t.Fatalf("interruptTurn() error = %v", err)
	}
	assertStuckTurnInterrupted(t, server, provider, turnDone)
	if report, _ := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("runtime update report = %#v, want Draining while queued work remains", report)
	}

	recovery, err := recoverJournal(server.journal)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.queuedTurns) != 1 || recovery.queuedTurns[0].id != "turn-2" || recovery.queuedTurns[0].text != "queued work" {
		t.Fatalf("recovered queued turns = %#v", recovery.queuedTurns)
	}
	restartedProvider := &fakeProvider{}
	restarted := NewServer(Config{}, server.journal, restartedProvider)
	if err := restarted.restoreTurns(recovery.queuedTurns); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		restarted.runTurns(ctx)
		close(runDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		restartedProvider.mu.Lock()
		prompts := append([]string(nil), restartedProvider.prompts...)
		restartedProvider.mu.Unlock()
		if reflect.DeepEqual(prompts, []string{"queued work"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider prompts = %v, want queued work", prompts)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-runDone
}

func TestServerStopsAcceptingAndDispatchingTurnsWhileProviderRestarts(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	provider := &fakeProvider{}
	server := NewServer(Config{}, journal, provider)
	accepted := make(chan struct{})
	close(accepted)
	server.turns <- turnRequest{id: "turn-1", text: "queued work", accepted: accepted}
	server.providerStopping.Store(true)

	runDone := make(chan struct{})
	go func() {
		server.runTurns(t.Context())
		close(runDone)
	}()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("turn dispatcher did not stop")
	}
	if len(server.turns) != 1 {
		t.Fatalf("queued turns = %d, want 1", len(server.turns))
	}
	if err := server.submitMessage("late work", "request-late"); err == nil || !strings.Contains(err.Error(), "restarting") {
		t.Fatalf("submitMessage() error = %v, want provider restart rejection", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.prompts) != 0 {
		t.Fatalf("provider prompts = %v, want none", provider.prompts)
	}
}

func startStuckInterruptServer(t *testing.T, acknowledge bool) (*Server, *stuckInterruptProvider, <-chan struct{}) {
	t.Helper()
	journal := NewJournal()
	t.Cleanup(journal.Close)
	provider := &stuckInterruptProvider{
		started:         make(chan struct{}),
		interruptCalled: make(chan struct{}),
		runStopped:      make(chan struct{}),
		done:            make(chan struct{}),
		acknowledge:     acknowledge,
	}
	server := NewServer(Config{PodUID: types.UID("pod-uid")}, journal, provider)
	if err := server.submitMessage("work", "request-work"); err != nil {
		t.Fatal(err)
	}
	turnDone := make(chan struct{})
	go func() {
		server.runTurns(t.Context())
		close(turnDone)
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider turn did not start")
	}
	return server, provider, turnDone
}

func assertStuckTurnInterrupted(t *testing.T, server *Server, provider *stuckInterruptProvider, turnDone <-chan struct{}) {
	t.Helper()
	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("forced interruption did not finish the turn")
	}
	select {
	case <-provider.done:
	default:
		t.Fatal("stuck provider was not closed")
	}
	events := server.journal.Snapshot()
	assertEventTypes(t, events, EventTurnInterrupting, EventTurnCompleted)
	if completion := events[len(events)-1]; completion.Type != EventTurnCompleted || completion.Status != "interrupted" {
		t.Fatalf("turn completion = %#v, want interrupted", completion)
	}
}

func TestServerRecoversActiveTurnAfterShutdown(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	provider := &fakeProvider{resume: make(chan struct{})}
	server := NewServer(Config{}, journal, provider)
	_, events, _, stop := journal.Subscribe(0)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	turnDone := make(chan struct{})
	go func() {
		server.runTurn(ctx, turnRequest{id: "turn-1"})
		close(turnDone)
	}()

	if event := <-events; event.Type != EventTurnStarted {
		t.Fatalf("first event = %#v", event)
	}
	if event := <-events; event.Type != EventAssistantDelta {
		t.Fatalf("second event = %#v", event)
	}
	cancel()
	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("active turn did not stop")
	}

	if _, err := recoverJournal(journal); err != nil {
		t.Fatal(err)
	}
	recovered := journal.Snapshot()
	assertEventTypes(t, recovered, EventTurnStarted, EventAssistantDelta, EventRuntimeRecovered, EventTurnCompleted)
	if completion := recovered[len(recovered)-1]; completion.Status != "interrupted" {
		t.Fatalf("recovered turn completion = %#v", completion)
	}
}

func TestInputRequestCancellationResolvesEvent(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	_, events, _, stop := journal.Subscribe(0)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	requestDone := make(chan error, 1)
	go func() {
		_, err := (&turnSink{server: server, turnID: "turn-1"}).RequestInput(ctx, InputRequest{
			ID:        "input-cancel",
			Questions: []InputQuestion{{ID: "question-1", Question: "Continue?"}},
		})
		requestDone <- err
	}()
	if event := <-events; event.Type != EventInputRequested {
		t.Fatalf("first event = %#v", event)
	}
	cancel()
	if err := <-requestDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("RequestInput() error = %v", err)
	}
	if event := <-events; event.Type != EventInputResolved || event.Status != "cancelled" {
		t.Fatalf("resolved event = %#v", event)
	}
}

func TestSubmitMessageDoesNotStartProviderWhenJournalWriteFails(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if err := journal.file.Close(); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	server := NewServer(Config{}, journal, provider)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		server.runTurns(ctx)
		close(runDone)
	}()
	if err := server.submitMessage("work", "request-message"); err == nil {
		t.Fatal("submitMessage() succeeded after the journal failed")
	}
	cancel()
	<-runDone
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.prompts) != 0 {
		t.Fatalf("provider prompts = %v, want none", provider.prompts)
	}
}

func TestServerSerializesConcurrentMessageAcceptance(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	firstAppending := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondAppending := make(chan struct{})
	appendMessage := server.appendMessage
	server.appendMessage = func(event Event) error {
		switch event.Text {
		case "first":
			close(firstAppending)
			<-releaseFirst
		case "second":
			close(secondAppending)
		}
		return appendMessage(event)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- server.submitMessage("first", "request-first") }()
	select {
	case <-firstAppending:
	case <-time.After(time.Second):
		t.Fatal("first message did not reach the journal")
	}
	secondDone := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondDone <- server.submitMessage("second", "request-second")
	}()
	<-secondStarted
	secondReachedJournal := false
	select {
	case <-secondAppending:
		secondReachedJournal = true
	case <-time.After(time.Second):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if secondReachedJournal {
		t.Fatal("second message reached the journal before the first message was accepted")
	}

	events := journal.Snapshot()
	assertEventTypes(t, events, EventUserMessage, EventUserMessage)
	if events[0].Text != "first" || events[1].Text != "second" {
		t.Fatalf("message order = %q, %q", events[0].Text, events[1].Text)
	}
	firstTurn := <-server.turns
	secondTurn := <-server.turns
	if firstTurn.text != "first" || secondTurn.text != "second" {
		t.Fatalf("turn order = %q, %q", firstTurn.text, secondTurn.text)
	}
}

func TestServerDrainsAcceptedTurnsBeforeRuntimeUpdate(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	podUID := types.UID("pod-uid")
	server := NewServer(Config{PodUID: podUID}, journal, &fakeProvider{})
	if err := server.submitMessage("first", "request-first"); err != nil {
		t.Fatal(err)
	}
	request := sessionupdate.NewRequest(podUID, "desired-revision")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.RequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := server.submitMessage("second", "request-second"); err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("submitMessage() error = %v, want draining rejection", err)
	}
	report, _ := server.sessionDrainReports()
	if report == nil || report.RequestID != request.ID || report.PodUID != podUID || report.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("runtime update report = %#v", report)
	}

	server.finishTurn()
	report, _ = server.sessionDrainReports()
	if report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("runtime update report after finishing turn = %#v", report)
	}
	if err := server.observeSessionUpdate(&kelos.Session{}); err != nil {
		t.Fatal(err)
	}
	if err := server.submitMessage("after update cancelled", "request-after"); err != nil {
		t.Fatalf("submitMessage() after cancelling drain error = %v", err)
	}
}

func TestServerDrainsAcceptedTurnsBeforeIdleReap(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	podUID := types.UID("pod-uid")
	server := NewServer(Config{PodUID: podUID}, journal, &fakeProvider{})
	if err := server.submitMessage("first", "request-first"); err != nil {
		t.Fatal(err)
	}
	request := sessionupdate.NewRequest(podUID, "idle")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.IdleDrainRequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := server.submitMessage("second", "request-second"); err == nil || !strings.Contains(err.Error(), "reclaimed") {
		t.Fatalf("submitMessage() error = %v, want idle reclamation rejection", err)
	}
	_, report := server.sessionDrainReports()
	if report == nil || report.RequestID != request.ID || report.PodUID != podUID || report.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("idle drain report = %#v", report)
	}

	server.finishTurn()
	if _, report = server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("idle drain report after finishing turn = %#v", report)
	}
}

// TestServerWithholdsIdleDrainUntilActivityPublished verifies that the runtime
// does not report Drained while a turn's activity status update is still queued
// for publication, so the controller cannot delete the Session against a stale
// Active=False state.
func TestServerWithholdsIdleDrainUntilActivityPublished(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	podUID := types.UID("pod-uid")
	server := NewServer(Config{PodUID: podUID}, journal, &fakeProvider{})
	// A configured status publisher means activity transitions must be durably
	// published before the runtime reports Drained.
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }

	if err := server.submitMessage("first", "request-first"); err != nil {
		t.Fatal(err)
	}
	request := sessionupdate.NewRequest(podUID, "idle-period")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.IdleDrainRequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}

	// The turn finished, but its Active=False status update is still queued, so
	// the runtime must report Draining rather than Drained.
	server.finishTurn()
	server.sessionStatusPublishQueue = []sessionStatusPublishRequest{{active: false}}
	if _, report := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("idle drain report with a pending status publish = %#v, want Draining", report)
	}

	// Once the queued activity has been durably published, the runtime reports Drained.
	server.sessionStatusPublishQueue = nil
	if _, report := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("idle drain report after publish = %#v, want Drained", report)
	}
}

// TestServerWithholdsIdleDrainAfterRecoveringInterruptedWork verifies that a
// runtime which recovered interrupted in-flight work from its journal seeds an
// activity publish and withholds an idle-drain Drained report until that
// recovered activity is durably published. A container restart preserves the Pod
// UID and any pending idle-drain request while resetting the in-memory
// outstanding-turn count and publish queue, so without this seed the restarted
// runtime could report Drained against a stale Active=False status and let the
// controller delete the Session despite the recovered activity.
func TestServerWithholdsIdleDrainAfterRecoveringInterruptedWork(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	// An in-flight turn that never completed represents work interrupted by a restart.
	if err := journal.Append(Event{Type: EventUserMessage, RequestID: "request-first", TurnID: "turn-1", Text: "work"}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(Event{Type: EventTurnStarted, TurnID: "turn-1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	recovery, err := recoverJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	// The interrupted turn is unsettled activity relative to a zero high-water mark.
	if !recovery.hasUnsettledActivity(0) {
		t.Fatal("recoverJournal() did not report unsettled activity for an in-flight turn")
	}

	podUID := types.UID("pod-uid")
	server := NewServer(Config{PodUID: podUID}, journal, &fakeProvider{})
	// A configured status publisher means recovered activity must be durably
	// published before the runtime reports Drained.
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	server.seedRecoveredActivityPublish()

	request := sessionupdate.NewRequest(podUID, "idle-period")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.IdleDrainRequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}

	// The recovered activity publish is still queued, so the runtime reports Draining
	// even though no turn is in flight (outstanding is zero after the restart).
	if _, report := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("idle drain report before publishing recovered activity = %#v, want Draining", report)
	}

	// Once the recovered activity has been durably published, the runtime reports Drained.
	server.completeSessionStatusPublish()
	if _, report := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("idle drain report after publishing recovered activity = %#v, want Drained", report)
	}
}

// TestServerWithholdsIdleDrainForUnpublishedCompletedTurn verifies the
// completed-turn restart case: a turn can finish and be recorded in the journal
// while its ordered Active status publications are still retrying. If the
// container restarts then, recovery finds no interrupted work, but the completed
// turn's ID still exceeds the durably-settled high-water mark, so the runtime must
// republish activity and withhold Drained until it is published rather than
// acknowledging a drain against the stale Active=False status.
func TestServerWithholdsIdleDrainForUnpublishedCompletedTurn(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	for _, event := range []Event{
		{Type: EventUserMessage, RequestID: "request-first", TurnID: "turn-1", Text: "work"},
		{Type: EventTurnStarted, TurnID: "turn-1", Status: "running"},
		{Type: EventTurnCompleted, TurnID: "turn-1", Status: "completed"},
	} {
		if err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	recovery, err := recoverJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.nextTurnID != 1 {
		t.Fatalf("recovery.nextTurnID = %d, want 1", recovery.nextTurnID)
	}
	// The completed turn was never durably settled (marker is zero), so it counts
	// as unsettled activity even though no work was interrupted.
	if !recovery.hasUnsettledActivity(0) {
		t.Fatal("expected unsettled activity for an unpublished completed turn")
	}
	// Had its idle status been durably published (marker advanced to the turn ID),
	// a restart would not re-seed and reset the idle clock.
	if recovery.hasUnsettledActivity(1) {
		t.Fatal("a settled completed turn must not count as unsettled activity")
	}

	podUID := types.UID("pod-uid")
	server := NewServer(Config{PodUID: podUID}, journal, &fakeProvider{})
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	server.nextTurnID.Store(recovery.nextTurnID)
	server.seedRecoveredActivityPublish()

	request := sessionupdate.NewRequest(podUID, "idle-period")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.IdleDrainRequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}

	if _, report := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDraining {
		t.Fatalf("idle drain report before publishing recovered activity = %#v, want Draining", report)
	}
	server.completeSessionStatusPublish()
	if _, report := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("idle drain report after publishing recovered activity = %#v, want Drained", report)
	}
}

// TestServerRecordsSettledActivityFromRequestSnapshot verifies that the activity
// publication high-water mark advances (and is persisted) only from an idle
// (Active=False) request's own captured snapshot, never from an active request.
func TestServerRecordsSettledActivityFromRequestSnapshot(t *testing.T) {
	dir := t.TempDir()
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{StateDir: dir}, journal, &fakeProvider{})

	// An Active=True publication never settles activity, whatever its snapshot.
	server.recordSettledActivity(sessionStatusPublishRequest{active: true, settledTurnID: 3})
	if got := server.settledTurnID.Load(); got != 0 {
		t.Fatalf("settledTurnID after active publish = %d, want 0", got)
	}

	// An idle publication records the high-water mark carried by that request.
	server.recordSettledActivity(sessionStatusPublishRequest{active: false, settledTurnID: 3})
	if got := server.settledTurnID.Load(); got != 3 {
		t.Fatalf("settledTurnID after idle publish = %d, want 3", got)
	}
	persisted, err := loadSettledTurnID(server.activityMarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != 3 {
		t.Fatalf("persisted settledTurnID = %d, want 3", persisted)
	}

	// A later idle publication carrying a lower snapshot never regresses the mark.
	server.recordSettledActivity(sessionStatusPublishRequest{active: false, settledTurnID: 2})
	if got := server.settledTurnID.Load(); got != 3 {
		t.Fatalf("settledTurnID after stale idle publish = %d, want 3", got)
	}
}

// TestServerDoesNotSettleLaterTurnFromStaleIdlePublication reproduces the race
// where a stale idle or periodic publication succeeds only after a later turn has
// started and finished. Because each request carries the completed-turn snapshot
// from when it was enqueued, completing the stale publication must not settle the
// later turn whose own Active publications are still queued.
func TestServerDoesNotSettleLaterTurnFromStaleIdlePublication(t *testing.T) {
	dir := t.TempDir()
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{StateDir: dir}, journal, &fakeProvider{})

	// Turns 1-5 are complete and a periodic idle publication was enqueued then.
	server.completedTurnID.Store(5)
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	server.requestPeriodicSessionStatusPublish()
	stale, ok := server.nextSessionStatusPublish()
	if !ok || stale.active || stale.settledTurnID != 5 {
		t.Fatalf("stale idle request = %#v, ok=%v, want idle snapshot 5", stale, ok)
	}

	// While that publication is in flight, a later turn starts and finishes.
	server.markTurnCompleted("turn-6")

	// Completing the in-flight stale publication settles only turn 5, not turn 6:
	// turn 6's own Active publications have not been published yet.
	server.recordSettledActivity(stale)
	if got := server.settledTurnID.Load(); got != 5 {
		t.Fatalf("settledTurnID after stale idle publish = %d, want 5", got)
	}
}

func TestSettledTurnIDPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), activityPublishedFile)
	if got, err := loadSettledTurnID(path); err != nil || got != 0 {
		t.Fatalf("loadSettledTurnID() on a missing marker = %d, %v, want 0, nil", got, err)
	}
	if err := writeSettledTurnID(path, 7); err != nil {
		t.Fatal(err)
	}
	if got, err := loadSettledTurnID(path); err != nil || got != 7 {
		t.Fatalf("loadSettledTurnID() = %d, %v, want 7, nil", got, err)
	}
}

// TestServerRuntimeUpdateDrainIgnoresPendingPublishes verifies that a
// runtime-update drain reports Drained as soon as no turn is in flight, without
// waiting for queued activity publications: the Pod is replaced and recovers
// from the journal, so an unpublished transition is not lost, and gating the
// replacement on a wedged status publisher would needlessly stall the update.
func TestServerRuntimeUpdateDrainIgnoresPendingPublishes(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	podUID := types.UID("pod-uid")
	server := NewServer(Config{PodUID: podUID}, journal, &fakeProvider{})
	server.publishSessionStatus = func(context.Context, bool, bool) error { return nil }

	request := sessionupdate.NewRequest(podUID, "desired-revision")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(&kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{sessionupdate.RequestAnnotation: encoded},
	}}); err != nil {
		t.Fatal(err)
	}

	// A status update is still queued, but with no turn in flight the runtime
	// must report Drained so the update can proceed.
	server.sessionStatusPublishQueue = []sessionStatusPublishRequest{{active: false}}
	if report, _ := server.sessionDrainReports(); report == nil || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("runtime update report with a pending status publish = %#v, want Drained", report)
	}
}

func TestServerAcknowledgesSessionRuntimeUpdate(t *testing.T) {
	podUID := types.UID("pod-uid")
	request := sessionupdate.NewRequest(podUID, "desired-revision")
	encoded, err := sessionupdate.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	session := &kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Name:        "chat",
		Namespace:   "default",
		Annotations: map[string]string{sessionupdate.RequestAnnotation: encoded},
	}}
	clientset := kelosfake.NewSimpleClientset(session)
	server := NewServer(Config{
		SessionName:   session.Name,
		PodUID:        podUID,
		SessionClient: clientset.ApiV1alpha2().Sessions(session.Namespace),
	}, NewJournal(), &fakeProvider{})
	defer server.journal.Close()
	if err := server.initializeSessionUpdate(t.Context()); err != nil {
		t.Fatal(err)
	}
	updated, err := clientset.ApiV1alpha2().Sessions(session.Namespace).Get(t.Context(), session.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := sessionupdate.DecodeReport(updated.Annotations[sessionupdate.ReportAnnotation])
	if err != nil {
		t.Fatal(err)
	}
	if report.RequestID != request.ID || report.PodUID != podUID || report.Phase != sessionupdate.PhaseDrained {
		t.Fatalf("runtime update report = %#v", report)
	}

	delete(updated.Annotations, sessionupdate.RequestAnnotation)
	if _, err := clientset.ApiV1alpha2().Sessions(session.Namespace).Update(t.Context(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := server.observeSessionUpdate(updated); err != nil {
		t.Fatal(err)
	}
	if err := server.reportSessionUpdate(t.Context()); err != nil {
		t.Fatal(err)
	}
	updated, err = clientset.ApiV1alpha2().Sessions(session.Namespace).Get(t.Context(), session.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := updated.Annotations[sessionupdate.ReportAnnotation]; exists {
		t.Fatalf("runtime update report was not removed: %q", updated.Annotations[sessionupdate.ReportAnnotation])
	}
}

func TestJournalBoundsRetainedHistory(t *testing.T) {
	journal := newJournal(4)
	defer journal.Close()
	for i := 0; i < 10; i++ {
		journal.Append(Event{Type: EventAssistantDelta, Text: strconv.Itoa(i)})
	}
	retained, _, _, cancel := journal.Subscribe(0)
	defer cancel()
	if len(retained) != 4 || retained[0].Text != "6" || retained[3].Text != "9" {
		t.Fatalf("retained events = %#v", retained)
	}
}

func TestServerResetsHistoryForReplacedJournalWithOverlappingIDs(t *testing.T) {
	previousJournal := newJournal(2)
	defer previousJournal.Close()
	for i := 0; i < 2; i++ {
		if err := previousJournal.Append(Event{Type: EventAssistantDelta, Text: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}

	journal := newJournal(2)
	defer journal.Close()
	for i := 0; i < 3; i++ {
		if err := journal.Append(Event{Type: EventAssistantDelta, Text: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{journal: journal}
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	go server.handleConnection(t.Context(), serverConnection)
	if err := json.NewEncoder(clientConnection).Encode(ClientRequest{
		Type:          "subscribe",
		Since:         2,
		JournalID:     previousJournal.journalID,
		HistoryBounds: true,
	}); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(clientConnection)
	var start Event
	if err := decoder.Decode(&start); err != nil {
		t.Fatal(err)
	}
	if start.Type != EventHistoryStart || start.FirstEventID != 2 || start.LastEventID != 3 || start.JournalID != journal.journalID || !start.Reset {
		t.Fatalf("history start = %#v", start)
	}
	for _, wantID := range []int64{2, 3} {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.ID != wantID {
			t.Fatalf("retained event ID = %d, want %d", event.ID, wantID)
		}
	}
	var end Event
	if err := decoder.Decode(&end); err != nil {
		t.Fatal(err)
	}
	if end.Type != EventHistoryEnd {
		t.Fatalf("final event = %#v, want history end", end)
	}
}

func TestServerPagesProjectedHistoryItems(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	for _, event := range []Event{
		{Type: EventUserMessage, TurnID: "turn-1", Text: "first"},
		{Type: EventTurnStarted, TurnID: "turn-1", Status: "running"},
		{Type: EventAssistantDelta, TurnID: "turn-1", Text: "first "},
		{Type: EventAssistantDelta, TurnID: "turn-1", Text: "answer"},
		{Type: EventAssistantMessage, TurnID: "turn-1", Text: "first answer"},
		{Type: EventTurnCompleted, TurnID: "turn-1", Status: "completed"},
		{Type: EventUserMessage, TurnID: "turn-2", Text: "second"},
		{Type: EventTurnStarted, TurnID: "turn-2", Status: "running"},
		{Type: EventAssistantMessage, TurnID: "turn-2", Text: "second answer"},
		{Type: EventTurnCompleted, TurnID: "turn-2", Status: "completed"},
	} {
		if err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{journal: journal}
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	go server.handleConnection(t.Context(), serverConnection)
	if err := json.NewEncoder(clientConnection).Encode(ClientRequest{
		Type:          "subscribe",
		HistoryBounds: true,
		HistoryItems:  2,
		HistoryBytes:  DefaultHistoryByteLimit,
	}); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(clientConnection)
	var start Event
	if err := decoder.Decode(&start); err != nil {
		t.Fatal(err)
	}
	if start.Type != EventHistoryStart || !start.HistoryLimited || start.HistoryCursor == "" {
		t.Fatalf("history start = %#v, want limited history", start)
	}

	var retained []Event
	var end Event
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventHistoryEnd {
			end = event
			break
		}
		retained = append(retained, event)
	}
	wantIDs := []int64{7, 9}
	if len(retained) != len(wantIDs) {
		t.Fatalf("retained events = %#v, want IDs %v", retained, wantIDs)
	}
	for index, event := range retained {
		if event.ID != wantIDs[index] {
			t.Fatalf("retained event IDs = %#v, want %v", retained, wantIDs)
		}
		if event.TurnID != "" {
			t.Fatalf("projected event retains turn state: %#v", retained)
		}
	}
	assertEventTypes(t, retained, EventUserMessage, EventAssistantMessage)
	if end.HistoryState == nil {
		t.Fatalf("history end = %#v, want state snapshot", end)
	}

	if err := json.NewEncoder(clientConnection).Encode(ClientRequest{
		Type:          "history",
		RequestID:     "history-1",
		HistoryCursor: start.HistoryCursor,
	}); err != nil {
		t.Fatal(err)
	}
	var pageStart Event
	if err := decoder.Decode(&pageStart); err != nil {
		t.Fatal(err)
	}
	if pageStart.Type != EventHistoryStart || !pageStart.HistoryPage || pageStart.RequestID != "history-1" || pageStart.HistoryCursor != "" {
		t.Fatalf("older history start = %#v", pageStart)
	}
	retained = nil
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventHistoryEnd {
			if !event.HistoryPage || event.RequestID != "history-1" {
				t.Fatalf("older history end = %#v", event)
			}
			break
		}
		retained = append(retained, event)
	}
	wantIDs = []int64{1, 5}
	if len(retained) != len(wantIDs) {
		t.Fatalf("older retained events = %#v, want IDs %v", retained, wantIDs)
	}
	for index, event := range retained {
		if event.ID != wantIDs[index] || event.TurnID != "" {
			t.Fatalf("older retained events = %#v, want projected IDs %v", retained, wantIDs)
		}
	}
	assertEventTypes(t, retained, EventUserMessage, EventAssistantMessage)
}

func TestServerPreservesStateOutsideProjectedHistoryPage(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	startedAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	queuedText := strings.Repeat("queued ", maxHistoryMessageBytes)
	for _, event := range []Event{
		{Type: EventFileDiff, Diff: "diff --git a/old.txt b/old.txt\n-old\n+new"},
		{Type: EventUserMessage, TurnID: "turn-1", Text: "active request"},
		{Type: EventTurnStarted, TurnID: "turn-1", Timestamp: &startedAt, Status: "running"},
		{Type: EventAssistantDelta, TurnID: "turn-1", Text: "partial answer"},
		{Type: EventInputRequested, TurnID: "turn-1", InputID: "input-1", Questions: []InputQuestion{{ID: "confirm", Question: "Continue?"}}, Status: "pending"},
		{Type: EventUserMessage, TurnID: "turn-2", Text: queuedText},
	} {
		if err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{journal: journal}
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	go server.handleConnection(t.Context(), serverConnection)
	if err := json.NewEncoder(clientConnection).Encode(ClientRequest{
		Type:          "subscribe",
		HistoryBounds: true,
		HistoryItems:  1,
		HistoryBytes:  DefaultHistoryByteLimit,
	}); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(clientConnection)
	var start Event
	if err := decoder.Decode(&start); err != nil {
		t.Fatal(err)
	}
	if start.Type != EventHistoryStart || !start.HistoryLimited || start.HistoryCursor == "" {
		t.Fatalf("history start = %#v, want limited history", start)
	}
	var retained []Event
	var end Event
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventHistoryEnd {
			end = event
			break
		}
		retained = append(retained, event)
	}
	assertEventTypes(t, retained, EventAssistantMessage, EventInputRequested)
	if retained[0].Text != "partial answer" || retained[0].TurnID != "" {
		t.Fatalf("projected assistant message = %#v", retained[0])
	}
	if retained[1].InputID != "input-1" || retained[1].TurnID != "" || len(retained[1].Questions) != 1 || retained[1].Questions[0].Question != "Continue?" {
		t.Fatalf("pending input = %#v", retained[1])
	}
	if end.HistoryState == nil {
		t.Fatalf("history end = %#v, want state snapshot", end)
	}
	state := end.HistoryState
	if state.ActiveTurnID != "turn-1" || state.ActiveTurnStarted == nil || !state.ActiveTurnStarted.Equal(startedAt) || !state.WaitingForInput {
		t.Fatalf("history state = %#v, want active turn waiting for input", state)
	}
	if len(state.QueuedTurns) != 1 || state.QueuedTurns[0].TurnID != "turn-2" {
		t.Fatalf("queued turns = %#v, want turn-2", state.QueuedTurns)
	}
	if state.FileDiff != "diff --git a/old.txt b/old.txt\n-old\n+new" {
		t.Fatalf("file diff = %q, want state outside projected history page", state.FileDiff)
	}
	if text := state.QueuedTurns[0].Text; len(text) > maxHistoryMessageBytes || !strings.Contains(text, historyTruncationMarker) {
		t.Fatalf("queued message preview has %d bytes", len(text))
	}
}

func TestServerStreamsRuntimeStatusOutsideConversationHistory(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{
		SessionName: "fix-session-tui",
		AgentType:   "codex",
		Model:       "gpt-5.6-sol",
		Effort:      "xhigh",
		WorkingDir:  "/home/agent/workspace/kelos",
		Environment: []string{"HOME=/home/agent"},
	}, journal, &fakeProvider{})
	server.updateWorkspaceRuntimeStatus(WorkspaceStatus{
		Branch: "agent/fix-session-tui",
		PullRequest: &kelos.SessionPullRequest{
			URL: "https://github.com/kelos-dev/kelos/pull/1547",
		},
	})

	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	go server.handleConnection(t.Context(), serverConnection)
	if err := json.NewEncoder(clientConnection).Encode(ClientRequest{Type: "subscribe", HistoryBounds: true}); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(clientConnection)
	var start, statusEvent, end Event
	for _, event := range []*Event{&start, &statusEvent, &end} {
		if err := decoder.Decode(event); err != nil {
			t.Fatal(err)
		}
	}
	if start.Type != EventHistoryStart || statusEvent.Type != EventRuntimeStatus || end.Type != EventHistoryEnd {
		t.Fatalf("subscription events = %#v, %#v, %#v", start, statusEvent, end)
	}
	if statusEvent.Runtime == nil ||
		statusEvent.Runtime.SessionName != "fix-session-tui" ||
		statusEvent.Runtime.Model != "gpt-5.6-sol" ||
		statusEvent.Runtime.Branch != "agent/fix-session-tui" ||
		statusEvent.Runtime.PullRequestNumber != 1547 {
		t.Fatalf("runtime status = %#v", statusEvent.Runtime)
	}
	if events := journal.Snapshot(); len(events) != 0 {
		t.Fatalf("conversation journal contains runtime status: %#v", events)
	}

	server.updateProviderRuntimeStatus(RuntimeStatus{
		Usage:       &RuntimeUsage{InputTokens: 1200, OutputTokens: 300, TotalTokens: 1500, ContextWindow: 200_000},
		WeeklyLimit: &RuntimeRateLimit{UsedPercent: 31},
	})
	var update Event
	if err := decoder.Decode(&update); err != nil {
		t.Fatal(err)
	}
	if update.Type != EventRuntimeStatus ||
		update.Runtime == nil ||
		update.Runtime.Usage == nil ||
		update.Runtime.Usage.InputTokens != 1200 ||
		update.Runtime.WeeklyLimit == nil ||
		update.Runtime.WeeklyLimit.UsedPercent != 31 {
		t.Fatalf("runtime status update = %#v", update)
	}
}

func TestJournalSignalsSubscriberOverflow(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	_, _, overflow, cancel := journal.Subscribe(0)
	defer cancel()
	for i := 0; i < 257; i++ {
		journal.Append(Event{Type: EventAssistantDelta, Text: "event"})
	}
	select {
	case <-overflow:
	default:
		t.Fatal("subscriber overflow was not signaled")
	}
}

func TestSendLiveEventDisconnectsOnOverflow(t *testing.T) {
	out := make(chan Event)
	overflow := make(chan struct{})
	close(overflow)
	disconnected := false

	if sendLiveEvent(t.Context(), out, overflow, Event{Type: EventRuntimeStatus}, func() {
		disconnected = true
	}) {
		t.Fatal("sendLiveEvent() = true, want overflow failure")
	}
	if !disconnected {
		t.Fatal("sendLiveEvent() did not disconnect the overflowing subscriber")
	}
}

func TestServerStopsWhenProviderStops(t *testing.T) {
	stateDir := shortRuntimeTempDir(t)
	journal := NewJournal()
	provider := &fakeProvider{}
	server := NewServer(Config{SocketPath: filepath.Join(stateDir, "runtime.sock")}, journal, provider)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(t.Context()) }()
	waitForRuntime(t, server.config.SocketPath)
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err == nil || !strings.Contains(err.Error(), "provider stopped") {
			t.Fatalf("Serve() error = %v, want provider stopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() stayed ready after the provider stopped")
	}
}

func TestClaudeEventMapping(t *testing.T) {
	provider := &ClaudeProvider{}
	sink := &collectingSink{}
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool-1","name":"Bash","input":{}}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"session-1"}`,
	}
	for _, line := range lines {
		result, err := provider.handleClaudeLine([]byte(line), sink)
		if err != nil {
			t.Fatalf("handleClaudeLine() error = %v", err)
		}
		if result != nil && result.error != "" {
			t.Fatalf("handleClaudeLine() result error = %q", result.error)
		}
	}
	assertEventTypes(t, sink.events, EventAssistantDelta, EventToolStarted, EventToolCompleted)
	if got := sink.events[2].Output; got != "ok" {
		t.Fatalf("Claude tool output = %q, want ok", got)
	}
}

func TestClaudeResultCompletion(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantError string
	}{
		{
			name: "normal completion",
			line: `{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","terminal_reason":"completed","result":"done"}`,
		},
		{
			name: "older result without reasons",
			line: `{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
		},
		{
			name:      "pending tool use",
			line:      `{"type":"result","subtype":"success","is_error":false,"stop_reason":"tool_use","result":"Starting the next tool"}`,
			wantError: "Claude Code run incomplete (stop_reason=tool_use)",
		},
		{
			name:      "aborted tools",
			line:      `{"type":"result","subtype":"success","is_error":false,"stop_reason":"tool_use","terminal_reason":"aborted_tools","result":"Starting the next tool"}`,
			wantError: "Claude Code run incomplete (terminal_reason=aborted_tools, stop_reason=tool_use)",
		},
		{
			name:      "explicit error preserves result text",
			line:      `{"type":"result","subtype":"error_max_turns","is_error":true,"stop_reason":"tool_use","terminal_reason":"max_turns","result":"Reached maximum number of turns"}`,
			wantError: "Reached maximum number of turns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &ClaudeProvider{}
			result, err := provider.handleClaudeLine([]byte(tt.line), &collectingSink{})
			if err != nil {
				t.Fatalf("handleClaudeLine() error = %v", err)
			}
			if result == nil {
				t.Fatal("handleClaudeLine() returned nil result")
			}
			if result.error != tt.wantError {
				t.Fatalf("result error = %q, want %q", result.error, tt.wantError)
			}
		})
	}
}

func TestClaudeInputResponse(t *testing.T) {
	reader, writer := io.Pipe()
	sink := &inputAnswerSink{answers: map[string][]string{"question-1": {"PostgreSQL"}}}
	provider := &ClaudeProvider{
		ctx:        context.Background(),
		stdin:      writer,
		sessionID:  "session-1",
		activeSink: sink,
	}
	done := make(chan struct{})
	go func() {
		provider.handleControlRequest([]byte(`{"type":"control_request","request_id":"request-2","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-2","input":{"questions":[{"question":"Which database?","header":"Database","multiSelect":false,"options":[{"label":"PostgreSQL","description":"Relational database"},{"label":"SQLite","description":"Embedded database"}]}]}}}`))
		close(done)
	}()
	var response struct {
		Response struct {
			Response struct {
				Behavior     string `json:"behavior"`
				UpdatedInput struct {
					Answers map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	<-done
	if response.Response.Response.Behavior != "allow" || response.Response.Response.UpdatedInput.Answers["Which database?"] != "PostgreSQL" {
		t.Fatalf("Claude input response = %#v", response)
	}
	if len(sink.request.Questions) != 1 || sink.request.Questions[0].ID != "question-1" {
		t.Fatalf("Claude input request = %#v", sink.request)
	}
}

func TestWriteClaudeSessionID(t *testing.T) {
	stateDir := t.TempDir()
	if err := writeClaudeSessionID(stateDir, "session-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "claude-session-id"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "session-1\n" {
		t.Fatalf("Claude session ID file = %q, want %q", data, "session-1\\n")
	}
}

func TestClaudeProviderPersistsSessionOnlyAfterCompletedTurn(t *testing.T) {
	binDir := t.TempDir()
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte(`#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"materialized-session"}'
done
`), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := t.TempDir()
	provider, err := NewClaudeProvider(t.Context(), ProviderConfig{
		StateDir:   stateDir,
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	sessionPath := filepath.Join(stateDir, "claude-session-id")
	if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Claude session ID exists before a completed turn: %v", err)
	}
	if err := provider.RunTurn(t.Context(), "hello", &collectingSink{}); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "materialized-session\n" {
		t.Fatalf("Claude session ID file = %q, want %q", data, "materialized-session\\n")
	}
}

func TestClaudeInterruptUsesControlProtocol(t *testing.T) {
	reader, writer := io.Pipe()
	interactionCtx, interactionCancel := context.WithCancel(context.Background())
	provider := &ClaudeProvider{
		stdin:             writer,
		turnDone:          make(chan claudeTurnResult, 1),
		interactionCtx:    interactionCtx,
		interactionCancel: interactionCancel,
		controlPending:    map[string]chan error{},
		done:              make(chan struct{}),
	}
	interruptDone := make(chan error, 1)
	go func() { interruptDone <- provider.Interrupt(t.Context()) }()
	var request struct {
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Request.Subtype != "interrupt" {
		t.Fatalf("Claude control request = %#v", request)
	}
	provider.handleControlResponse([]byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`, request.RequestID)))
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	select {
	case <-interactionCtx.Done():
	default:
		t.Fatal("Claude interaction context was not cancelled")
	}
}

func TestCodexEventMapping(t *testing.T) {
	sink := &collectingSink{}
	done := make(chan codexTurnResult, 1)
	provider := &CodexProvider{activeSink: sink, turnDone: done}
	provider.handleNotification("item/agentMessage/delta", json.RawMessage(`{"delta":"hello"}`))
	provider.handleNotification("item/started", json.RawMessage(`{"item":{"type":"commandExecution","id":"tool-1","command":"make test"}}`))
	provider.handleNotification("item/commandExecution/outputDelta", json.RawMessage(`{"itemId":"tool-1","delta":"ok\n"}`))
	provider.handleNotification("item/completed", json.RawMessage(`{"item":{"type":"commandExecution","id":"tool-1","command":"make test","status":"completed"}}`))
	provider.handleNotification("turn/diff/updated", json.RawMessage(`{"diff":"+updated"}`))
	provider.handleNotification("turn/completed", json.RawMessage(`{"turn":{"status":"completed"}}`))
	assertEventTypes(t, sink.events, EventAssistantDelta, EventToolStarted, EventToolCompleted, EventFileDiff)
	if got := sink.events[2].Output; got != "ok\n" {
		t.Fatalf("Codex tool output = %q, want %q", got, "ok\n")
	}
	select {
	case result := <-done:
		if result.status != "completed" || result.error != "" {
			t.Fatalf("Codex turn result = %#v", result)
		}
	default:
		t.Fatal("Codex turn completion was not delivered")
	}
}

func TestCodexRuntimeStatusMapping(t *testing.T) {
	sink := &collectingSink{}
	provider := &CodexProvider{activeSink: sink}
	provider.handleNotification("thread/tokenUsage/updated", json.RawMessage(`{
		"tokenUsage": {
			"total": {
				"totalTokens": 3000,
				"inputTokens": 2400,
				"outputTokens": 600
			},
			"last": {
				"totalTokens": 2000
			},
			"modelContextWindow": 200000
		}
	}`))
	provider.handleNotification("account/rateLimits/updated", json.RawMessage(`{
		"rateLimits": {
			"primary": {"usedPercent": 10, "windowDurationMins": 300},
			"secondary": {"usedPercent": 31, "windowDurationMins": 10080}
		}
	}`))

	if len(sink.events) != 2 {
		t.Fatalf("runtime status events = %#v", sink.events)
	}
	usage := sink.events[0].Runtime
	if sink.events[0].Type != EventRuntimeStatus ||
		usage == nil ||
		usage.Usage == nil ||
		usage.Usage.InputTokens != 2400 ||
		usage.Usage.OutputTokens != 600 ||
		usage.Usage.TotalTokens != 3000 ||
		usage.Usage.ContextTokens != 2000 ||
		usage.Usage.ContextWindow != 200000 {
		t.Fatalf("Codex token usage event = %#v", sink.events[0])
	}
	limit := sink.events[1].Runtime
	if sink.events[1].Type != EventRuntimeStatus ||
		limit == nil ||
		limit.WeeklyLimit == nil ||
		limit.WeeklyLimit.UsedPercent != 31 {
		t.Fatalf("Codex rate limit event = %#v", sink.events[1])
	}
	if sparse := codexWeeklyRateLimit(json.RawMessage(`{
		"rateLimits": {"secondary": {"usedPercent": 42}}
	}`)); sparse == nil || sparse.UsedPercent != 42 {
		t.Fatalf("sparse Codex weekly rate limit = %#v", sparse)
	}

	provider.model = "gpt-5.6-sol"
	provider.config.Effort = "xhigh"
	server := NewServer(Config{AgentType: "codex"}, NewJournal(), provider)
	t.Cleanup(func() { server.journal.Close() })
	initial := server.runtimeStatusSnapshot()
	if initial.Model != "gpt-5.6-sol" ||
		initial.Effort != "xhigh" ||
		initial.WeeklyLimit == nil ||
		initial.WeeklyLimit.UsedPercent != 31 {
		t.Fatalf("initial server runtime status = %#v", initial)
	}
}

func TestCodexRequestOmitsNilParams(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	provider := &CodexProvider{
		stdin:   writer,
		pending: map[string]chan codexResponse{},
		done:    make(chan struct{}),
	}
	requestDone := make(chan error, 1)
	go func() {
		_, err := provider.request(t.Context(), "account/rateLimits/read", nil)
		requestDone <- err
	}()

	var request map[string]json.RawMessage
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if _, exists := request["params"]; exists {
		t.Fatalf("parameterless Codex request contains params: %s", request["params"])
	}
	var id int64
	if err := json.Unmarshal(request["id"], &id); err != nil {
		t.Fatal(err)
	}
	provider.pendingMu.Lock()
	pending := provider.pending[strconv.FormatInt(id, 10)]
	provider.pendingMu.Unlock()
	pending <- codexResponse{Result: json.RawMessage(`{}`)}
	if err := <-requestDone; err != nil {
		t.Fatalf("Codex request error = %v", err)
	}
}

func TestCodexToolResultOutputMapping(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   string
	}{
		{
			name:   "dynamic tool text",
			params: `{"item":{"type":"dynamicToolCall","id":"tool-1","tool":"exec","status":"completed","contentItems":[{"type":"inputText","text":"command output"}]}}`,
			want:   "command output",
		},
		{
			name:   "MCP text",
			params: `{"item":{"type":"mcpToolCall","id":"tool-1","server":"github","tool":"search","status":"completed","result":{"content":[{"type":"text","text":"search result"}]}}}`,
			want:   "search result",
		},
		{
			name:   "tool error",
			params: `{"item":{"type":"mcpToolCall","id":"tool-1","server":"github","tool":"search","status":"failed","error":{"message":"search failed"}}}`,
			want:   "search failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &collectingSink{}
			provider := &CodexProvider{}

			provider.emitCodexItem("item/completed", json.RawMessage(test.params), sink)

			if len(sink.events) != 1 {
				t.Fatalf("Codex tool events = %#v, want one completion", sink.events)
			}
			if got := sink.events[0].Output; got != test.want {
				t.Fatalf("Codex tool output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCodexMcpElicitationUsesProtocolResponse(t *testing.T) {
	reader, writer := io.Pipe()
	sink := &collectingSink{}
	provider := &CodexProvider{stdin: writer, activeSink: sink}
	done := make(chan struct{})
	go func() {
		provider.handleServerRequest(json.RawMessage(`2`), "mcpServer/elicitation/request", json.RawMessage(`{"message":"Choose a value"}`))
		close(done)
	}()
	var response struct {
		ID     int `json:"id"`
		Result struct {
			Action string `json:"action"`
		} `json:"result"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	<-done
	if response.ID != 2 || response.Result.Action != "decline" {
		t.Fatalf("Codex elicitation response = %#v", response)
	}
	assertEventTypes(t, sink.events, EventError)
}

func TestCodexInputResponse(t *testing.T) {
	reader, writer := io.Pipe()
	sink := &inputAnswerSink{answers: map[string][]string{"database": {"PostgreSQL"}}}
	provider := &CodexProvider{ctx: context.Background(), stdin: writer, activeSink: sink}
	done := make(chan struct{})
	go func() {
		provider.handleServerRequest(json.RawMessage(`3`), "item/tool/requestUserInput", json.RawMessage(`{"questions":[{"id":"database","header":"Database","question":"Which database?","options":[{"label":"PostgreSQL","description":"Relational database"}]}]}`))
		close(done)
	}()
	var response struct {
		ID     int `json:"id"`
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	<-done
	if response.ID != 3 || !reflect.DeepEqual(response.Result.Answers["database"].Answers, []string{"PostgreSQL"}) {
		t.Fatalf("Codex input response = %#v", response)
	}
	if len(sink.request.Questions) != 1 || sink.request.Questions[0].ID != "database" {
		t.Fatalf("Codex input request = %#v", sink.request)
	}
}

func TestCodexInterruptUsesActiveTurn(t *testing.T) {
	reader, writer := io.Pipe()
	interactionCtx, interactionCancel := context.WithCancel(context.Background())
	provider := &CodexProvider{
		ctx:               context.Background(),
		stdin:             writer,
		pending:           map[string]chan codexResponse{},
		threadID:          "thread-1",
		activeTurn:        "turn-1",
		interactionCtx:    interactionCtx,
		interactionCancel: interactionCancel,
		done:              make(chan struct{}),
	}
	interruptDone := make(chan error, 1)
	go func() { interruptDone <- provider.Interrupt(t.Context()) }()
	var request struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		} `json:"params"`
	}
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/interrupt" || request.Params.ThreadID != "thread-1" || request.Params.TurnID != "turn-1" {
		t.Fatalf("Codex interrupt request = %#v", request)
	}
	provider.pendingMu.Lock()
	pending := provider.pending[strconv.FormatInt(request.ID, 10)]
	provider.pendingMu.Unlock()
	pending <- codexResponse{Result: json.RawMessage(`{}`)}
	if err := <-interruptDone; err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	select {
	case <-interactionCtx.Done():
	default:
		t.Fatal("Codex interaction context was not cancelled")
	}
}

func TestCodexOpenThreadReplacesUnmaterializedThread(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "codex-thread-id")
	if err := os.WriteFile(statePath, []byte("thread-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	provider := &CodexProvider{
		config:  ProviderConfig{StateDir: stateDir},
		ctx:     context.Background(),
		stdin:   writer,
		pending: map[string]chan codexResponse{},
		done:    make(chan struct{}),
	}
	requestDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(reader)
		for i, wantMethod := range []string{"thread/resume", "thread/start"} {
			var request struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
				Params struct {
					ThreadID     string `json:"threadId"`
					ExcludeTurns bool   `json:"excludeTurns"`
				} `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				requestDone <- err
				return
			}
			if request.Method != wantMethod {
				requestDone <- fmt.Errorf("request %d method = %q, want %q", i, request.Method, wantMethod)
				return
			}
			if i == 0 && (request.Params.ThreadID != "thread-1" || !request.Params.ExcludeTurns) {
				requestDone <- fmt.Errorf("resume params = %#v, want thread ID with excluded turns", request.Params)
				return
			}
			provider.pendingMu.Lock()
			pending := provider.pending[strconv.FormatInt(request.ID, 10)]
			provider.pendingMu.Unlock()
			if i == 0 {
				pending <- codexResponse{Error: &struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}{Code: -32600, Message: "no rollout found"}}
				continue
			}
			pending <- codexResponse{Result: json.RawMessage(`{
				"thread":{"id":"thread-2"},
				"model":"gpt-5.6-sol",
				"reasoningEffort":"high"
			}`)}
		}
		requestDone <- nil
	}()
	if err := provider.openThread(t.Context()); err != nil {
		t.Fatalf("openThread() error = %v", err)
	}
	if requestErr := <-requestDone; requestErr != nil {
		t.Fatal(requestErr)
	}
	if provider.threadID != "thread-2" {
		t.Fatalf("thread ID = %q, want thread-2", provider.threadID)
	}
	status := provider.runtimeStatusSnapshot()
	if status.Model != "gpt-5.6-sol" || status.Effort != "high" {
		t.Fatalf("new Codex thread runtime status = %#v", status)
	}
	if status.Usage == nil ||
		status.Usage.InputTokens != 0 ||
		status.Usage.OutputTokens != 0 {
		t.Fatalf("new Codex thread usage = %#v, want zero snapshot", status.Usage)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "thread-2\n" {
		t.Fatalf("saved thread ID = %q, want thread-2", data)
	}
}

func TestCodexOpenThreadDoesNotFabricateResumedUsage(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "codex-thread-id"), []byte("thread-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	provider := &CodexProvider{
		config: ProviderConfig{
			StateDir: stateDir,
			Effort:   "low",
		},
		ctx:     context.Background(),
		stdin:   writer,
		pending: map[string]chan codexResponse{},
		done:    make(chan struct{}),
	}
	requestDone := make(chan error, 1)
	go func() {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ThreadID     string `json:"threadId"`
				ExcludeTurns bool   `json:"excludeTurns"`
			} `json:"params"`
		}
		if err := json.NewDecoder(reader).Decode(&request); err != nil {
			requestDone <- err
			return
		}
		if request.Method != "thread/resume" ||
			request.Params.ThreadID != "thread-1" ||
			!request.Params.ExcludeTurns {
			requestDone <- fmt.Errorf("resume request = %#v", request)
			return
		}
		provider.pendingMu.Lock()
		pending := provider.pending[strconv.FormatInt(request.ID, 10)]
		provider.pendingMu.Unlock()
		pending <- codexResponse{Result: json.RawMessage(`{
			"thread":{"id":"thread-1"},
			"model":"gpt-5.6-sol",
			"reasoningEffort":"xhigh"
		}`)}
		requestDone <- nil
	}()

	if err := provider.openThread(t.Context()); err != nil {
		t.Fatalf("openThread() error = %v", err)
	}
	if requestErr := <-requestDone; requestErr != nil {
		t.Fatal(requestErr)
	}
	status := provider.runtimeStatusSnapshot()
	if status.Model != "gpt-5.6-sol" || status.Effort != "xhigh" {
		t.Fatalf("resumed Codex thread runtime status = %#v", status)
	}
	if status.Usage != nil {
		t.Fatalf("resumed Codex thread usage = %#v, want unavailable", status.Usage)
	}

	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{AgentType: "codex"}, journal, provider)
	if initial := server.runtimeStatusSnapshot(); initial.Usage != nil || initial.Effort != "xhigh" {
		t.Fatalf("resumed server runtime status = %#v", initial)
	}
}

func TestCodexReadLoopAcceptsLargeMessage(t *testing.T) {
	response := make(chan codexResponse, 1)
	provider := &CodexProvider{
		pending: map[string]chan codexResponse{"1": response},
		done:    make(chan struct{}),
	}
	message, err := json.Marshal(map[string]any{
		"id": 1,
		"result": map[string]any{
			"thread":  map[string]string{"id": "thread-1"},
			"payload": strings.Repeat("x", 8*1024*1024),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	message = append(message, '\n')

	provider.readLoop(bytes.NewReader(message))
	if provider.readErr != nil {
		t.Fatalf("readLoop() error = %v", provider.readErr)
	}
	select {
	case got := <-response:
		if id := codexThreadID(got.Result); id != "thread-1" {
			t.Fatalf("thread ID = %q, want thread-1", id)
		}
	default:
		t.Fatal("large Codex response was not delivered")
	}
}

func TestIsMissingCodexRollout(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "short message",
			err:  &codexRequestError{method: "thread/resume", code: -32600, message: "no rollout found"},
			want: true,
		},
		{
			name: "message with matching thread ID",
			err:  &codexRequestError{method: "thread/resume", code: -32600, message: "no rollout found for thread id thread-1"},
			want: true,
		},
		{
			name: "message with different thread ID",
			err:  &codexRequestError{method: "thread/resume", code: -32600, message: "no rollout found for thread id thread-2"},
		},
		{
			name: "different method",
			err:  &codexRequestError{method: "thread/start", code: -32600, message: "no rollout found"},
		},
		{name: "different error", err: errors.New("no rollout found")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingCodexRollout(tt.err, "thread-1"); got != tt.want {
				t.Fatalf("isMissingCodexRollout() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCodexOpenThreadReturnsResumeFailure(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "codex-thread-id"), []byte("thread-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	provider := &CodexProvider{
		config:  ProviderConfig{StateDir: stateDir},
		ctx:     context.Background(),
		stdin:   writer,
		pending: map[string]chan codexResponse{},
		done:    make(chan struct{}),
	}
	requestDone := make(chan error, 1)
	go func() {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(reader).Decode(&request); err != nil {
			requestDone <- err
			return
		}
		if request.Method != "thread/resume" {
			requestDone <- fmt.Errorf("method = %q, want thread/resume", request.Method)
			return
		}
		var response codexResponse
		if err := json.Unmarshal([]byte(`{"error":{"code":-32000,"message":"thread data missing"}}`), &response); err != nil {
			requestDone <- err
			return
		}
		provider.pendingMu.Lock()
		pending := provider.pending[strconv.FormatInt(request.ID, 10)]
		provider.pendingMu.Unlock()
		pending <- response
		requestDone <- nil
	}()
	err := provider.openThread(t.Context())
	if requestErr := <-requestDone; requestErr != nil {
		t.Fatal(requestErr)
	}
	if err == nil || !strings.Contains(err.Error(), "resuming Codex thread") {
		t.Fatalf("openThread() error = %v", err)
	}
}

type collectingSink struct {
	events []Event
}

type inputAnswerSink struct {
	answers map[string][]string
	request InputRequest
}

func (s *inputAnswerSink) Emit(Event) {}
func (s *inputAnswerSink) RequestInput(_ context.Context, request InputRequest) (map[string][]string, error) {
	s.request = request
	return s.answers, nil
}

func (s *collectingSink) Emit(event Event) { s.events = append(s.events, event) }
func (s *collectingSink) RequestInput(context.Context, InputRequest) (map[string][]string, error) {
	return nil, ErrInputCancelled
}

func waitForRuntime(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if Health(socketPath) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Session runtime did not listen on %s", socketPath)
}

func assertEventTypes(t *testing.T, events []Event, wanted ...string) {
	t.Helper()
	found := map[string]bool{}
	for _, event := range events {
		found[event.Type] = true
	}
	for _, eventType := range wanted {
		if !found[eventType] {
			t.Errorf("events did not contain %q: %#v", eventType, events)
		}
	}
}

func shortRuntimeTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
