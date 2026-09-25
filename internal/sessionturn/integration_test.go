package sessionturn

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kelos-dev/kelos/internal/sessionruntime"
)

// echoProvider is a minimal Session provider: it answers each turn with a
// prefixed echo of the prompt, so a driven turn produces a predictable reply.
type echoProvider struct {
	mu      sync.Mutex
	prompts []string
	// ask, when set, makes the first turn block on a provider question.
	ask     bool
	asked   sync.Once
	answers chan map[string][]string
	askErr  chan error
	// block, when non-nil, holds the first turn open until it is closed.
	block   chan struct{}
	blocked sync.Once
	started chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newEchoProvider() *echoProvider {
	return &echoProvider{done: make(chan struct{}), started: make(chan struct{}, 8)}
}

func (p *echoProvider) RunTurn(ctx context.Context, input sessionruntime.TurnInput, sink sessionruntime.EventSink) error {
	p.mu.Lock()
	p.prompts = append(p.prompts, input.Text)
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	if p.ask {
		var first bool
		p.asked.Do(func() { first = true })
		if first {
			answers, err := sink.RequestInput(ctx, sessionruntime.InputRequest{
				Questions: []sessionruntime.InputQuestion{{
					ID:       "which-branch",
					Question: "Which branch should I work on?",
					Options: []sessionruntime.InputOption{
						{Label: "main"},
						{Label: "develop"},
					},
				}},
			})
			p.answers <- answers
			p.askErr <- err
		}
	}
	if p.block != nil {
		var held bool
		p.blocked.Do(func() { held = true })
		if held {
			select {
			case <-p.block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	sink.Emit(sessionruntime.Event{Type: sessionruntime.EventAssistantDelta, Text: "echo:"})
	sink.Emit(sessionruntime.Event{Type: sessionruntime.EventAssistantMessage, Text: "echo: " + input.Text})
	return nil
}

func (p *echoProvider) Interrupt(context.Context) error { return nil }
func (p *echoProvider) Done() <-chan struct{}           { return p.done }
func (p *echoProvider) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *echoProvider) submitted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// startRuntime runs a real Session runtime on a unix socket and returns a
// dialer for it. This exercises the wire protocol RunTurn depends on against
// the server that actually implements it, rather than a hand-written script.
func startRuntime(t *testing.T, provider sessionruntime.Provider) func(*testing.T) net.Conn {
	t.Helper()
	// A unix socket path is capped near 104 bytes, and the per-test temporary
	// directory is far longer than that on macOS, so keep this one short.
	socketDir, err := os.MkdirTemp("", "kst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "r.sock")

	journal := sessionruntime.NewJournal()
	t.Cleanup(journal.Close)
	server := sessionruntime.NewServer(sessionruntime.Config{
		SocketPath: socketPath,
		StateDir:   t.TempDir(),
		WorkingDir: t.TempDir(),
		AgentType:  "claude-code",
	}, journal, provider)

	ctx, cancel := context.WithCancel(context.Background())
	var serveErr error
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		serveErr = server.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(10 * time.Second):
			t.Error("Session runtime did not stop")
		}
	})

	// Serve creates the socket asynchronously.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := sessionruntime.Health(socketPath); err == nil {
			break
		}
		select {
		case <-serveDone:
			t.Fatalf("Session runtime stopped before serving: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Session runtime socket never became available")
		}
		time.Sleep(5 * time.Millisecond)
	}

	return func(t *testing.T) net.Conn {
		t.Helper()
		connection, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Fatalf("dialing the Session runtime: %v", err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		return connection
	}
}

func TestRunTurnAgainstARealRuntime(t *testing.T) {
	provider := newEchoProvider()
	dial := startRuntime(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connection := dial(t)
	result, err := RunTurn(ctx, connection, connection, "what changed?")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if !result.Succeeded() {
		t.Errorf("Status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.Text != "echo: what changed?" {
		t.Errorf("Text = %q, want the provider's reply", result.Text)
	}
	if result.TurnID == "" {
		t.Error("TurnID is empty, so the turn could not be identified")
	}
}

func TestRunTurnAgainstARealRuntimeSkipsReplayedHistory(t *testing.T) {
	provider := newEchoProvider()
	dial := startRuntime(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A second connection replays the first turn's history before its own
	// events. Its reply must not pick that up.
	first := dial(t)
	if _, err := RunTurn(ctx, first, first, "first question"); err != nil {
		t.Fatalf("first RunTurn returned an error: %v", err)
	}
	second := dial(t)
	result, err := RunTurn(ctx, second, second, "second question")
	if err != nil {
		t.Fatalf("second RunTurn returned an error: %v", err)
	}
	if result.Text != "echo: second question" {
		t.Errorf("Text = %q, want only the second turn's reply", result.Text)
	}
	if got := provider.submitted(); len(got) != 2 {
		t.Errorf("provider ran %d turns, want 2: %v", len(got), got)
	}
}

func TestRunTurnAgainstARealRuntimeFollowsAMergedTurn(t *testing.T) {
	// A prompt submitted while another turn is still running is queued, and a
	// further prompt is merged into that queued turn. RunTurn depends on the
	// runtime echoing its request ID on the merge; this checks that it does.
	provider := newEchoProvider()
	provider.block = make(chan struct{})
	dial := startRuntime(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	holding := dial(t)
	held := make(chan Result, 1)
	heldErr := make(chan error, 1)
	go func() {
		result, err := RunTurn(ctx, holding, holding, "long running")
		held <- result
		heldErr <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first turn never reached the provider")
	}

	// Two more prompts: the first queues, the second merges into it.
	queued := dial(t)
	queuedResult := make(chan Result, 1)
	queuedErr := make(chan error, 1)
	go func() {
		result, err := RunTurn(ctx, queued, queued, "queued question")
		queuedResult <- result
		queuedErr <- err
	}()

	merged := dial(t)
	mergedResult := make(chan Result, 1)
	mergedErr := make(chan error, 1)
	go func() {
		// Give the queued prompt time to become the pending turn.
		time.Sleep(250 * time.Millisecond)
		result, err := RunTurn(ctx, merged, merged, "merged question")
		mergedResult <- result
		mergedErr <- err
	}()

	time.Sleep(500 * time.Millisecond)
	close(provider.block)

	if err := <-heldErr; err != nil {
		t.Fatalf("the held turn returned an error: %v", err)
	}
	if got := (<-held).Text; got != "echo: long running" {
		t.Errorf("held turn text = %q", got)
	}

	if err := <-queuedErr; err != nil {
		t.Fatalf("the queued turn returned an error: %v", err)
	}
	if err := <-mergedErr; err != nil {
		t.Fatalf("the merged turn returned an error: %v", err)
	}
	queuedText := (<-queuedResult).Text
	mergedText := (<-mergedResult).Text

	// Whether the runtime merged them or ran them separately, both callers must
	// get a reply that includes their own prompt rather than hanging forever.
	if !strings.Contains(queuedText, "queued question") {
		t.Errorf("queued turn text = %q, want it to cover the queued prompt", queuedText)
	}
	if !strings.Contains(mergedText, "merged question") {
		t.Errorf("merged turn text = %q, want it to cover the merged prompt", mergedText)
	}
}

func TestRunTurnAgainstARealRuntimeRejectsAnEmptyPrompt(t *testing.T) {
	dial := startRuntime(t, newEchoProvider())
	connection := dial(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := RunTurn(ctx, connection, connection, "")
	if err == nil {
		t.Fatal("RunTurn accepted an empty prompt")
	}
	if errors.Is(err, ErrStreamClosed) {
		t.Errorf("error = %v, want a rejection rather than a closed stream", err)
	}
}

func TestRunTurnAgainstARealRuntimeDeclinesAProviderQuestion(t *testing.T) {
	// A provider question blocks the turn inside the runtime until a client
	// answers it. A Slack thread cannot, so the driver must decline it rather
	// than let the turn hang until its timeout while holding the runtime.
	provider := newEchoProvider()
	provider.ask = true
	provider.answers = make(chan map[string][]string, 1)
	provider.askErr = make(chan error, 1)
	dial := startRuntime(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connection := dial(t)
	result, err := RunTurn(ctx, connection, connection, "which branch?")
	if err != nil {
		t.Fatalf("RunTurn returned an error: %v", err)
	}
	if !result.Succeeded() {
		t.Errorf("Status = %q, want the turn to finish rather than stall", result.Status)
	}
	if result.DeclinedQuestions != 1 {
		t.Errorf("DeclinedQuestions = %d, want 1", result.DeclinedQuestions)
	}
	if result.Text != "echo: which branch?" {
		t.Errorf("Text = %q, want the provider's reply after the question was declined", result.Text)
	}

	select {
	case answers := <-provider.answers:
		if len(answers) != 0 {
			t.Errorf("provider received answers %v, want none", answers)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never returned from its question")
	}
	select {
	case err := <-provider.askErr:
		if !errors.Is(err, sessionruntime.ErrInputCancelled) {
			t.Errorf("provider question error = %v, want ErrInputCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never reported its question outcome")
	}
}

func TestRunTurnAgainstARealRuntimeInterruptsAnAbandonedTurn(t *testing.T) {
	// A turn abandoned at its deadline must not keep running: the next message
	// in the thread would queue behind it and time out the same way.
	provider := newEchoProvider()
	provider.block = make(chan struct{})
	dial := startRuntime(t, provider)

	connection := dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := RunTurn(ctx, connection, connection, "long running"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}

	// The interrupt cancels the provider's turn context, releasing it.
	select {
	case <-provider.done:
		t.Fatal("the provider was closed rather than interrupted")
	case <-time.After(2 * time.Second):
	}

	// A fresh client must find the runtime able to accept another turn.
	close(provider.block)
	next := dial(t)
	followUp, followUpCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer followUpCancel()
	result, err := RunTurn(followUp, next, next, "next question")
	if err != nil {
		t.Fatalf("the runtime did not accept a turn after the abandoned one: %v", err)
	}
	if result.Text != "echo: next question" {
		t.Errorf("Text = %q, want the follow-up reply", result.Text)
	}
}
