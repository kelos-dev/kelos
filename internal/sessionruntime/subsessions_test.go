package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionupdate"
)

func testSubsessionGroup(t *testing.T, stateDir string) *subsessionGroup {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	g := newSubsessionGroup(ctx, cancel, nil)
	g.newProvider = func(_ context.Context, config ProviderConfig) (Provider, error) {
		provider := &fakeProvider{}
		provider.SetEventSink(config.EventSink)
		return provider, nil
	}
	config := Config{
		StateDir: stateDir, WorkingDir: filepath.Join(stateDir, "repo"),
		SocketPath: filepath.Join(stateDir, "runtime.sock"), AgentType: "codex",
		Environment: os.Environ(), InitialPrompt: "Parent context",
	}
	if err := os.MkdirAll(config.WorkingDir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := openSessionServer(ctx, config, g.newProvider, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g.close()
		_ = root.closeProvider()
		root.events.close()
		root.journal.Close()
	})
	return g
}

func subsessionStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kelos-subs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestSubsessionSharesWorkspaceWithIndependentContext(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	if err := g.root.journal.Append(Event{Type: EventAssistantMessage, Text: "Private parent context"}); err != nil {
		t.Fatal(err)
	}
	child, err := g.create("Investigation", "")
	if err != nil {
		t.Fatal(err)
	}
	server, err := g.get(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if server.config.WorkingDir != g.root.config.WorkingDir || server.config.StateDir == g.root.config.StateDir {
		t.Fatalf("child config = %#v", server.config)
	}
	if server.config.InitialPrompt != "" || len(server.journal.Snapshot()) != 0 {
		t.Fatal("subsession inherited conversation context")
	}
	if server.attachmentStore != g.root.attachmentStore {
		t.Fatal("subsession cannot use Session attachments")
	}
	if err := server.submitMessage("!printf shared > result.txt", "child-request"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		provider := server.provider.(*fakeProvider)
		provider.mu.Lock()
		prompts := append([]string(nil), provider.prompts...)
		provider.mu.Unlock()
		if len(prompts) > 0 {
			if !reflect.DeepEqual(prompts, []string{"!printf shared > result.txt"}) {
				t.Fatalf("child prompts = %v", prompts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subsession did not run its prompt")
		}
		time.Sleep(time.Millisecond)
	}
	if err := server.submitClientMessage(t.Context(), "!printf shared > result.txt", "shell-request"); err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(g.root.config.WorkingDir, "result.txt"))
		if err == nil && string(data) == "shared" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shared workspace file = %q, error = %v", data, err)
		}
		time.Sleep(time.Millisecond)
	}
	for _, event := range g.root.journal.Snapshot() {
		if event.RequestID == "child-request" || event.RequestID == "shell-request" {
			t.Fatal("subsession messages leaked into parent history")
		}
	}
}

func TestSubsessionRestoresHistory(t *testing.T) {
	dir := subsessionStateDir(t)
	g := testSubsessionGroup(t, dir)
	child, err := g.create("Review", "")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := g.get(child.ID)
	if err := server.journal.Append(Event{Type: EventAssistantMessage, Text: "Retained child context"}); err != nil {
		t.Fatal(err)
	}
	g.close()
	g.root.journal.Close()

	restarted := testSubsessionGroup(t, dir)
	if err := restarted.restore(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restarted.list(), []Subsession{{Name: "Main conversation"}, child}) {
		t.Fatalf("restored subsessions = %#v", restarted.list())
	}
	server, _ = restarted.get(child.ID)
	if events := server.journal.Snapshot(); len(events) == 0 || events[0].Text != "Retained child context" {
		t.Fatalf("restored history = %#v", events)
	}
}

func TestSubsessionDeletionStopsWorkAndPreservesSharedWorkspace(t *testing.T) {
	dir := subsessionStateDir(t)
	g := testSubsessionGroup(t, dir)
	g.newProvider = func(_ context.Context, config ProviderConfig) (Provider, error) {
		provider := &fakeProvider{resume: make(chan struct{})}
		provider.SetEventSink(config.EventSink)
		return provider, nil
	}
	child, err := g.create("Investigation", "")
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := g.create("Review", "")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := g.get(child.ID)
	provider := server.provider.(*fakeProvider)
	if err := g.root.journal.Append(Event{Type: EventAssistantMessage, Text: "Main history"}); err != nil {
		t.Fatal(err)
	}
	workspaceFile := filepath.Join(g.root.config.WorkingDir, "shared.txt")
	if err := os.WriteFile(workspaceFile, []byte("Shared work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.submitMessage("Active work", "active"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		provider.mu.Lock()
		started := len(provider.prompts) > 0
		provider.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subsession work did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := g.remove(child.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.remove(child.ID); err != nil {
		t.Fatalf("retrying deletion: %v", err)
	}
	if _, err := g.get(child.ID); err == nil {
		t.Fatal("deleted subsession is still accessible")
	}
	if _, err := os.Stat(server.config.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted history still exists: %v", err)
	}
	if !provider.closed || g.ctx.Err() != nil {
		t.Fatal("deletion did not stop only the subsession provider")
	}
	if err := server.submitMessage("Rejected", "deleted"); err == nil {
		t.Fatal("deleted subsession accepted a message")
	}
	data, err := os.ReadFile(workspaceFile)
	if err != nil || string(data) != "Shared work" {
		t.Fatalf("shared file = %q, error = %v", data, err)
	}
	if events := g.root.journal.Snapshot(); len(events) != 1 || events[0].Text != "Main history" {
		t.Fatalf("main history = %#v", events)
	}
	other, err := g.get(sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.submitMessage("Continue review", "sibling"); err != nil {
		t.Fatal(err)
	}
	g.close()
	g.root.journal.Close()
	restarted := testSubsessionGroup(t, dir)
	if err := restarted.restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.get(child.ID); err == nil {
		t.Fatal("deleted subsession was restored")
	}
	if _, err := restarted.get(sibling.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSubsessionDeletionProtocol(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	child, err := g.create("Delete me", "")
	if err != nil {
		t.Fatal(err)
	}
	client, connection := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go g.root.handleConnection(g.ctx, connection)
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for _, id := range []string{child.ID, "", "../parent", child.ID} {
		if err := encoder.Encode(ClientRequest{Type: "subsession.delete", SubsessionID: id, RequestID: "delete"}); err != nil {
			t.Fatal(err)
		}
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.RequestID != "delete" {
			t.Fatalf("deletion response = %#v", event)
		}
		if id != child.ID {
			if event.Type != EventError || event.Status != "rejected" {
				t.Fatalf("invalid deletion response = %#v", event)
			}
		} else if event.Type != EventSubsessions || len(event.Subsessions) != 1 || event.Subsessions[0].ID != "" {
			t.Fatalf("deletion catalog = %#v", event)
		}
	}
}

func TestSubsessionDeletionDisconnectsRoutedClients(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	child, err := g.create("Delete me", "")
	if err != nil {
		t.Fatal(err)
	}
	client, connection := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go g.root.handleConnection(g.ctx, connection)
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(client).Encode(ClientRequest{Type: "subscribe", SubsessionID: child.ID}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(client)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventHistoryEnd {
			break
		}
	}
	if err := g.remove(child.ID); err != nil {
		t.Fatal(err)
	}
	for {
		var event Event
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("deleted client did not disconnect: %v", err)
		}
	}
}

func TestSubsessionDeletionFreesCapacity(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	var first Subsession
	for i := range maxSubsessions {
		child, err := g.create(fmt.Sprintf("Conversation %d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = child
		}
	}
	if err := g.remove(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.create("Replacement", ""); err != nil {
		t.Fatal(err)
	}
}

type deletingProvider struct {
	fakeProvider
	deleting chan struct{}
	release  chan struct{}
	err      error
	once     sync.Once
}

func (p *deletingProvider) deleteConversation(ctx context.Context) error {
	p.once.Do(func() { close(p.deleting) })
	if p.err != nil {
		return p.err
	}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSubsessionDeletionRetainsCleanupStateOnFailure(t *testing.T) {
	dir := subsessionStateDir(t)
	g := testSubsessionGroup(t, dir)
	provider := &deletingProvider{deleting: make(chan struct{}), err: errors.New("Cleanup unavailable")}
	g.newProvider = func(_ context.Context, config ProviderConfig) (Provider, error) {
		provider.SetEventSink(config.EventSink)
		return provider, nil
	}
	child, err := g.create("Delete me", "")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := g.get(child.ID)
	server.submitMu.Lock()
	server.outstanding = 1
	server.submitMu.Unlock()
	if err := g.remove(child.ID); err == nil || !strings.Contains(err.Error(), "Cleanup unavailable") {
		t.Fatalf("deletion error = %v", err)
	}
	if _, err := g.get(child.ID); err == nil || len(g.list()) != 1 {
		t.Fatal("failed cleanup left a stopped conversation registered")
	}
	g.root.submitMu.Lock()
	drained := g.childrenDrained(true)
	g.root.submitMu.Unlock()
	if !drained {
		t.Fatal("failed cleanup left the drain blocked")
	}
	g.publishMu.Lock()
	_, published := g.statuses[child.ID]
	g.publishMu.Unlock()
	if published {
		t.Fatal("failed cleanup retained the deleted conversation's status")
	}
	pending := filepath.Join(g.directory(), pendingDeletionPrefix+child.ID)
	if _, err := os.Stat(filepath.Join(pending, "subsession.json")); err != nil {
		t.Fatalf("deletion metadata was lost: %v", err)
	}
	g.close()
	g.root.journal.Close()
	restarted := testSubsessionGroup(t, dir)
	if err := restarted.restore(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.list()) != 1 {
		t.Fatal("pending deletion was restored as a conversation")
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending deletion was not cleaned up: %v", err)
	}
}

func TestSubsessionRestoreRetriesOpenCodeDeletion(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	var mu sync.Mutex
	var requests []string
	allowDeletion := false
	api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, request.Method+" "+request.URL.Path)
		if request.Method == http.MethodDelete && !allowDeletion {
			response.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = response.Write([]byte("true"))
	}))
	defer api.Close()
	client, err := newOpenCodeClient(api.URL, g.root.config.WorkingDir, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	parent := newOpenCodeProviderState(g.ctx, ProviderConfig{}, client)
	original := g.root.provider
	g.root.provider = parent
	defer func() {
		g.root.provider = original
		_ = parent.Close()
	}()
	pending := filepath.Join(g.directory(), pendingDeletionPrefix+"41a48f94-e5d5-4e6d-84c1-3a990221713a")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(pending, "opencode-session-id")
	if err := os.WriteFile(state, []byte("ses-child\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.restore(); err == nil {
		t.Fatal("provider cleanup failure was ignored")
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("provider cleanup state was lost: %v", err)
	}
	mu.Lock()
	allowDeletion = true
	mu.Unlock()
	if err := g.restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending deletion was not removed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"POST /session/ses-child/abort", "DELETE /session/ses-child", "POST /session/ses-child/abort", "DELETE /session/ses-child"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("cleanup requests = %v, want %v", requests, want)
	}
}

func TestSubsessionDeletionBlocksDrainUntilProviderCleanupCompletes(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	provider := &deletingProvider{deleting: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(provider.release) })
	g.newProvider = func(_ context.Context, config ProviderConfig) (Provider, error) {
		provider.SetEventSink(config.EventSink)
		return provider, nil
	}
	child, err := g.create("Delete me", "")
	if err != nil {
		t.Fatal(err)
	}
	removed := make(chan error, 1)
	go func() { removed <- g.remove(child.ID) }()
	select {
	case <-provider.deleting:
	case <-time.After(3 * time.Second):
		t.Fatal("provider cleanup did not start")
	}
	g.root.submitMu.Lock()
	drained := g.childrenDrained(false)
	g.root.submitMu.Unlock()
	if drained {
		t.Fatal("drain completed while provider cleanup was pending")
	}
	release.Do(func() { close(provider.release) })
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	g.root.submitMu.Lock()
	drained = g.childrenDrained(false)
	g.root.submitMu.Unlock()
	if !drained {
		t.Fatal("drain remained blocked after deletion")
	}
}

func TestSubsessionConnectionRoutesMessages(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	parentClient, parentServer := net.Pipe()
	t.Cleanup(func() { _ = parentClient.Close() })
	go g.root.handleConnection(g.ctx, parentServer)
	_ = parentClient.SetDeadline(time.Now().Add(3 * time.Second))
	encoder, decoder := json.NewEncoder(parentClient), json.NewDecoder(parentClient)
	if err := encoder.Encode(ClientRequest{Type: "subsession.create", Text: "Side task", RequestID: "create"}); err != nil {
		t.Fatal(err)
	}
	var created Event
	if err := decoder.Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Type != EventSubsessions || created.SubsessionID == "" || created.RequestID != "create" {
		t.Fatalf("creation response = %#v", created)
	}
	client, connection := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go g.root.handleConnection(g.ctx, connection)
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	encoder, decoder = json.NewEncoder(client), json.NewDecoder(client)
	if err := encoder.Encode(ClientRequest{Type: "subscribe", SubsessionID: created.SubsessionID}); err != nil {
		t.Fatal(err)
	}
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventHistoryEnd {
			break
		}
		if event.Type == EventUserMessage {
			t.Fatal("parent initial prompt appeared in child history")
		}
	}
	if err := encoder.Encode(ClientRequest{Type: "message", Text: "Independent request", RequestID: "message"}); err != nil {
		t.Fatal(err)
	}
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventTurnCompleted {
			break
		}
	}
	child, _ := g.get(created.SubsessionID)
	provider := child.provider.(*fakeProvider)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !reflect.DeepEqual(provider.prompts, []string{"Independent request"}) {
		t.Fatalf("child prompts = %v", provider.prompts)
	}
	if len(g.root.journal.Snapshot()) != 0 {
		t.Fatal("child request reached parent history")
	}
}

func TestSubsessionActivityBlocksDrain(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	child := NewServer(Config{}, NewJournal(), &fakeProvider{})
	t.Cleanup(child.journal.Close)
	child.subsessions = g
	g.children["child"] = &subsessionRuntime{Server: child}
	g.root.updateRequest = &sessionupdate.Request{ID: "update"}
	g.root.idleDrainRequest = &sessionupdate.Request{ID: "idle"}
	child.outstanding = 1
	update, idle := g.root.sessionDrainReports()
	if update.Phase != sessionupdate.PhaseDraining || idle.Phase != sessionupdate.PhaseDraining {
		t.Fatal("drain ignored child activity")
	}
	if err := child.submitMessage("blocked", "blocked"); err == nil {
		t.Fatal("subsession accepted work during drain")
	}
	if _, err := g.create("blocked", ""); err == nil {
		t.Fatal("created subsession during drain")
	}
	child.finishTurn()
	child.backgroundActive.Store(true)
	update, idle = g.root.sessionDrainReports()
	if update.Phase != sessionupdate.PhaseDrained || idle.Phase != sessionupdate.PhaseDraining {
		t.Fatal("idle drain ignored child background work")
	}
	child.backgroundActive.Store(false)
	child.publishSessionStatus = func(context.Context, bool, bool) error { return nil }
	child.seedRecoveredActivityPublish()
	_, idle = g.root.sessionDrainReports()
	if idle.Phase != sessionupdate.PhaseDraining {
		t.Fatal("idle drain ignored unpublished child activity")
	}
	child.completeSessionStatusPublish()
	_, idle = g.root.sessionDrainReports()
	if idle.Phase != sessionupdate.PhaseDrained {
		t.Fatal("idle drain remained blocked after all child work settled")
	}
}

func TestSubsessionPublisherAggregatesActivity(t *testing.T) {
	var published ObservedSessionStatus
	g := newSubsessionGroup(t.Context(), func() {}, func(_ context.Context, status ObservedSessionStatus) error {
		published = status
		return nil
	})
	parent, child := g.publisher(""), g.publisher("child")
	ctx := t.Context()
	parentStatus := ObservedSessionStatus{Model: "parent-model"}
	if err := parent(ctx, parentStatus); err != nil {
		t.Fatal(err)
	}
	notification := &kelos.SessionNotification{ID: "child-completion", EventTime: metav1.Now()}
	if err := child(ctx, ObservedSessionStatus{Active: true, WaitingForInput: true, LastNotification: notification}); err != nil {
		t.Fatal(err)
	}
	if err := parent(ctx, parentStatus); err != nil {
		t.Fatal(err)
	}
	if !published.Active || !published.WaitingForInput || published.Model != "parent-model" || published.LastNotification != notification {
		t.Fatalf("combined status = %#v", published)
	}
	if err := child(ctx, ObservedSessionStatus{}); err != nil {
		t.Fatal(err)
	}
	if published.Active || published.WaitingForInput || published.LastNotification != nil {
		t.Fatalf("idle status = %#v", published)
	}
}

func TestSubsessionRejectsUnknownIDAndInvalidName(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	for _, name := range []string{"", "  ", strings.Repeat("x", 65)} {
		if _, err := g.create(name, ""); err == nil {
			t.Fatalf("created subsession with invalid name %q", name)
		}
	}
	if _, err := g.get("../parent"); err == nil {
		t.Fatal("unknown subsession accepted")
	}
}

func TestSubsessionShutdownCancelsCreation(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	started := make(chan struct{})
	g.newProvider = func(ctx context.Context, _ ProviderConfig) (Provider, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	created := make(chan error, 1)
	go func() {
		_, err := g.create("Starting", "")
		created <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("subsession provider did not start")
	}
	closed := make(chan struct{})
	go func() {
		g.close()
		close(closed)
	}()
	select {
	case err := <-created:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("subsession creation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel subsession creation")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("subsession shutdown did not finish")
	}
}

func TestSubsessionCreationRequestIsIdempotentAcrossRestart(t *testing.T) {
	dir := subsessionStateDir(t)
	g := testSubsessionGroup(t, dir)
	first, err := g.create("Investigation", "create-request")
	if err != nil {
		t.Fatal(err)
	}
	retried, err := g.create("Investigation", "create-request")
	if err != nil || retried.ID != first.ID || len(g.list()) != 2 {
		t.Fatalf("retried creation = %#v, error = %v", retried, err)
	}
	if _, err := g.create("Different name", "create-request"); err == nil {
		t.Fatal("request ID was reused with a different name")
	}
	g.close()
	g.root.journal.Close()
	restarted := testSubsessionGroup(t, dir)
	if err := restarted.restore(); err != nil {
		t.Fatal(err)
	}
	retried, err = restarted.create("Investigation", "create-request")
	if err != nil || retried.ID != first.ID || len(restarted.list()) != 2 {
		t.Fatalf("retried creation after restart = %#v, error = %v", retried, err)
	}
}

func TestSubsessionCatalogBroadcastsActivityAndNotifications(t *testing.T) {
	g := newSubsessionGroup(t.Context(), func() {}, nil)
	g.root = NewServer(Config{}, NewJournal(), &fakeProvider{})
	child := NewServer(Config{}, NewJournal(), &fakeProvider{})
	t.Cleanup(g.root.journal.Close)
	t.Cleanup(child.journal.Close)
	g.children["child"] = &subsessionRuntime{Server: child}
	g.names["child"] = Subsession{ID: "child", Name: "Investigation"}
	updates, stop := g.subscribe()
	defer stop()
	<-updates
	child.activeTurn = "turn-1"
	child.pendingInputCount.Store(1)
	publisher := g.publisher("child")
	if err := publisher(t.Context(), ObservedSessionStatus{Active: true, WaitingForInput: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case conversations := <-updates:
		if len(conversations) != 2 || !conversations[1].Active || !conversations[1].WaitingForInput {
			t.Fatalf("active catalog = %#v", conversations)
		}
	case <-time.After(time.Second):
		t.Fatal("activity was not broadcast")
	}
	child.activeTurn = ""
	child.pendingInputCount.Store(0)
	if err := child.journal.Append(Event{Type: EventTurnCompleted, TurnID: "turn-1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	notification := child.journal.LatestNotification()
	if err := publisher(t.Context(), ObservedSessionStatus{LastNotification: notification}); err != nil {
		t.Fatal(err)
	}
	select {
	case conversations := <-updates:
		if conversations[1].Active || conversations[1].WaitingForInput || conversations[1].NotificationID != notification.ID {
			t.Fatalf("completed catalog = %#v", conversations)
		}
	case <-time.After(time.Second):
		t.Fatal("completion notification was not broadcast")
	}
}

func TestSubsessionFinishReportsOnlyPendingDrains(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	child := NewServer(Config{}, NewJournal(), &fakeProvider{})
	t.Cleanup(child.journal.Close)
	child.subsessions = g
	child.outstanding = 1
	child.finishTurn()
	select {
	case <-g.root.updateReport:
		t.Fatal("idle child completion requested a drain report")
	default:
	}
	g.root.updateRequest = &sessionupdate.Request{ID: "update"}
	child.outstanding = 1
	child.finishTurn()
	select {
	case <-g.root.updateReport:
	default:
		t.Fatal("child completion did not wake the pending root drain")
	}
}

func TestSubsessionPublisherWaitsForMainModel(t *testing.T) {
	var published []ObservedSessionStatus
	g := newSubsessionGroup(t.Context(), func() {}, func(_ context.Context, status ObservedSessionStatus) error {
		published = append(published, status)
		return nil
	})
	child := g.publisher("child")
	if err := child(t.Context(), ObservedSessionStatus{Active: true, Model: "child-model"}); err == nil || len(published) != 0 {
		t.Fatalf("child published before main status: %#v, error = %v", published, err)
	}
	if err := g.publisher("")(t.Context(), ObservedSessionStatus{Model: "main-model"}); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || !published[0].Active || published[0].Model != "main-model" {
		t.Fatalf("initial combined status = %#v", published)
	}
	workspace := &WorkspaceStatus{Branch: "feature", PullRequest: &kelos.SessionPullRequest{URL: "https://github.com/owner/repo/pull/42"}}
	if err := child(t.Context(), ObservedSessionStatus{Model: "child-model", WorkspaceStatus: workspace}); err != nil {
		t.Fatal(err)
	}
	if latest := published[len(published)-1]; latest.Model != "main-model" || latest.WorkspaceStatus != workspace {
		t.Fatalf("child workspace status = %#v", latest)
	}
}

func TestSubsessionNotificationsDoNotMoveBackwardAcrossRestarts(t *testing.T) {
	dir := subsessionStateDir(t)
	g := testSubsessionGroup(t, dir)
	var published ObservedSessionStatus
	g.publish = func(_ context.Context, status ObservedSessionStatus) error { published = status; return nil }
	older := &kelos.SessionNotification{ID: "older", EventTime: metav1.NewTime(time.Unix(100, 100))}
	newer := &kelos.SessionNotification{ID: "newer", EventTime: metav1.NewTime(time.Unix(100, 200))}
	if err := g.publisher("")(t.Context(), ObservedSessionStatus{LastNotification: older}); err != nil {
		t.Fatal(err)
	}
	if err := g.publisher("child")(t.Context(), ObservedSessionStatus{LastNotification: newer}); err != nil {
		t.Fatal(err)
	}
	if published.LastNotification != newer {
		t.Fatalf("latest notification = %#v", published.LastNotification)
	}
	if err := g.publisher("child")(t.Context(), ObservedSessionStatus{}); err != nil {
		t.Fatal(err)
	}
	if published.LastNotification != nil {
		t.Fatalf("resolved notification restored an older result: %#v", published.LastNotification)
	}
	g.close()
	g.root.journal.Close()
	restarted := testSubsessionGroup(t, dir)
	restarted.publish = g.publish
	if err := restarted.restore(); err != nil {
		t.Fatal(err)
	}
	if err := restarted.publisher("")(t.Context(), ObservedSessionStatus{LastNotification: older}); err != nil {
		t.Fatal(err)
	}
	if published.LastNotification != nil {
		t.Fatalf("restart restored an older result: %#v", published.LastNotification)
	}
	latest := &kelos.SessionNotification{ID: "latest", EventTime: metav1.NewTime(time.Unix(100, 300))}
	if err := restarted.publisher("")(t.Context(), ObservedSessionStatus{LastNotification: latest}); err != nil {
		t.Fatal(err)
	}
	if published.LastNotification != latest {
		t.Fatalf("fresh notification = %#v", published.LastNotification)
	}
}

func TestSubsessionCreationLeavesOtherConversationsAvailable(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	g.newProvider = func(ctx context.Context, config ProviderConfig) (Provider, error) {
		close(started)
		select {
		case <-release:
			provider := &fakeProvider{}
			provider.SetEventSink(config.EventSink)
			return provider, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	created := make(chan error, 1)
	go func() { _, err := g.create("Investigation", "create-request"); created <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("subsession startup did not begin")
	}
	submitted := make(chan error, 1)
	go func() { submitted <- g.root.submitMessage("Main conversation work", "main-request") }()
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("subsession startup blocked the main conversation")
	}
	turn := <-g.root.turns
	finished := make(chan struct{})
	go func() { g.root.runTurn(t.Context(), turn); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("subsession startup blocked main turn completion")
	}
	g.root.submitMu.Lock()
	g.root.updateRequest = &sessionupdate.Request{ID: "update"}
	g.root.submitMu.Unlock()
	update, _ := g.root.sessionDrainReports()
	if update.Phase != sessionupdate.PhaseDraining {
		t.Fatal("creation in progress did not block draining")
	}
	unblock()
	select {
	case err := <-created:
		if err == nil || !strings.Contains(err.Error(), "draining") {
			t.Fatalf("creation during drain = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("subsession creation did not settle")
	}
	update, _ = g.root.sessionDrainReports()
	if update.Phase != sessionupdate.PhaseDrained {
		t.Fatal("failed creation left the drain blocked")
	}
}

func TestSubsessionCapacityAllowsIdempotentRetries(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	g.root.config.SessionName = "shared"
	var first Subsession
	for i := 0; i < maxSubsessions; i++ {
		child, err := g.create(fmt.Sprintf("Conversation %d", i), fmt.Sprintf("request-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = child
		}
	}
	if _, err := g.create("Excess", "excess"); err == nil || err.Error() != `Session "shared" supports at most 8 subsessions` {
		t.Fatalf("capacity error = %v", err)
	}
	if retried, err := g.create(first.Name, "request-0"); err != nil || retried.ID != first.ID {
		t.Fatalf("retry at capacity = %#v, error = %v", retried, err)
	}
}

func TestSubsessionMetadataReplacementIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subsession.json")
	original := Subsession{ID: "child", Name: "Investigation"}
	if err := writeSubsessionState(path, original); err != nil {
		t.Fatal(err)
	}
	if err := writeSubsessionState(path, make(chan int)); err == nil {
		t.Fatal("invalid state was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var restored Subsession
	if err := json.Unmarshal(data, &restored); err != nil || restored != original {
		t.Fatalf("metadata after failed replacement = %#v, error = %v", restored, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("metadata directory after failed replacement = %v, error = %v", entries, err)
	}
}

func TestSubsessionRemovalWakesRootDrain(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	child := NewServer(Config{}, NewJournal(), &fakeProvider{})
	t.Cleanup(child.journal.Close)
	child.subsessions = g
	if err := child.submitMessage("Queued", "queued"); err != nil {
		t.Fatal(err)
	}
	g.root.idleDrainRequest = &sessionupdate.Request{ID: "idle"}
	if err := child.removePendingMessage("turn-1", 1, "remove"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.root.updateReport:
	default:
		t.Fatal("removing the last queued child message did not wake the root drain")
	}
}

func TestSubsessionUsesSharedWorkspaceObservation(t *testing.T) {
	g := testSubsessionGroup(t, subsessionStateDir(t))
	var published ObservedSessionStatus
	g.publish = func(_ context.Context, status ObservedSessionStatus) error { published = status; return nil }
	changesGit(t, g.root.config.WorkingDir, "init", "-b", "feature")
	child, err := g.create("Investigation", "create-request")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := g.get(child.ID)
	shared := WorkspaceStatus{Branch: "feature", PullRequest: &kelos.SessionPullRequest{URL: "https://github.com/owner/repo/pull/42"}}
	if err := writeWorkspaceStatus(g.root.config.StateDir, shared); err != nil {
		t.Fatal(err)
	}
	g.updateWorkspaceStatus(shared)
	for _, conversation := range []*Server{g.root, server.Server} {
		if status := conversation.runtimeStatusSnapshot(); status.Branch != "feature" || status.PullRequestNumber != 42 {
			t.Fatalf("conversation workspace status = %#v", status)
		}
	}
	if err := g.publisher("")(t.Context(), ObservedSessionStatus{Model: "main-model"}); err != nil {
		t.Fatal(err)
	}
	if err := server.publishSessionStatus(t.Context(), false, false); err != nil {
		t.Fatal(err)
	}
	g.publishMu.Lock()
	observed := published
	g.publishMu.Unlock()
	if observed.WorkspaceStatus == nil || !reflect.DeepEqual(*observed.WorkspaceStatus, shared) || observed.Model != "main-model" {
		t.Fatalf("published child workspace = %#v", observed)
	}
	select {
	case <-g.root.workspaceStatusRefreshes:
	default:
	}
	server.requestWorkspaceStatusRefresh(true)
	select {
	case <-g.root.workspaceStatusRefreshes:
	default:
		t.Fatal("child workspace refresh was not routed to the root")
	}
}
