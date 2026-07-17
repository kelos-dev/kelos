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

func TestServerRetriesWorkspaceStatusPublication(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.workspaceStatusRetryInterval = 10 * time.Millisecond
	server.workspaceStatusPublishInterval = time.Hour
	attempts := 0
	published := make(chan struct{}, 1)
	server.publishWorkspaceStatus = func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("not ready")
		}
		select {
		case published <- struct{}{}:
		default:
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runWorkspaceStatusPublishes(ctx)
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("workspace status publication was not retried")
	}
}

func TestServerRefreshesWorkspaceStatusAfterPeriodicPublication(t *testing.T) {
	journal := NewJournal()
	defer journal.Close()
	server := NewServer(Config{}, journal, &fakeProvider{})
	server.workspaceStatusRetryInterval = 10 * time.Millisecond
	server.workspaceStatusPublishInterval = time.Hour
	server.publishWorkspaceStatus = func(context.Context) error { return nil }
	server.refreshWorkspaceStatus = func(context.Context) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.runWorkspaceStatusPublishes(ctx)

	select {
	case <-server.workspaceStatusRefreshes:
	case <-time.After(time.Second):
		t.Fatal("periodic workspace status publication did not request a refresh")
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
	provider.handleNotification("item/completed", json.RawMessage(`{"item":{"type":"commandExecution","id":"tool-1","command":"make test","status":"completed"}}`))
	provider.handleNotification("turn/diff/updated", json.RawMessage(`{"diff":"+updated"}`))
	provider.handleNotification("turn/completed", json.RawMessage(`{"turn":{"status":"completed"}}`))
	assertEventTypes(t, sink.events, EventAssistantDelta, EventToolStarted, EventToolCompleted, EventFileDiff)
	select {
	case result := <-done:
		if result.status != "completed" || result.error != "" {
			t.Fatalf("Codex turn result = %#v", result)
		}
	default:
		t.Fatal("Codex turn completion was not delivered")
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
			pending <- codexResponse{Result: json.RawMessage(`{"thread":{"id":"thread-2"}}`)}
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
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "thread-2\n" {
		t.Fatalf("saved thread ID = %q, want thread-2", data)
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
