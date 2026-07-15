package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenCodeProviderStreamsOrderedEvents(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{
		WorkingDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Model:      "openai/gpt-5",
		Effort:     "high",
	})

	created := fake.createdSessionRequest()
	model, ok := created["model"].(map[string]any)
	if !ok || model["providerID"] != "openai" || model["id"] != "gpt-5" || model["variant"] != "high" {
		t.Fatalf("OpenCode session model = %#v", created["model"])
	}
	permissions, ok := created["permission"].([]any)
	if !ok || len(permissions) != 1 {
		t.Fatalf("OpenCode session permissions = %#v", created["permission"])
	}
	permission, ok := permissions[0].(map[string]any)
	if !ok || permission["permission"] != "*" || permission["pattern"] != "*" || permission["action"] != "allow" {
		t.Fatalf("OpenCode session permission = %#v", permissions[0])
	}

	sink := newOpenCodeTestSink(nil)
	turnDone := make(chan error, 1)
	go func() { turnDone <- provider.RunTurn(t.Context(), "hello", sink) }()
	request := receiveOpenCodePrompt(t, fake.prompts)
	parts, ok := request["parts"].([]any)
	if !ok || len(parts) != 1 || parts[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("OpenCode prompt request = %#v", request)
	}

	fake.emit("session.status", map[string]any{"sessionID": fake.sessionID, "status": map[string]string{"type": "busy"}})
	fake.emit("permission.asked", map[string]string{"id": "permission-1", "sessionID": fake.sessionID})
	fake.emit("message.updated", map[string]any{"info": map[string]string{"id": "message-1", "sessionID": fake.sessionID, "role": "assistant"}})
	fake.emit("message.part.updated", map[string]any{"part": map[string]any{"id": "text-1", "messageID": "message-1", "sessionID": fake.sessionID, "type": "text", "text": ""}})
	fake.emit("message.part.delta", map[string]any{"sessionID": fake.sessionID, "messageID": "message-1", "partID": "text-1", "field": "text", "delta": "hello"})
	fake.emit("message.part.updated", map[string]any{"part": map[string]any{"id": "reasoning-1", "messageID": "message-1", "sessionID": fake.sessionID, "type": "reasoning"}})
	fake.emit("message.part.delta", map[string]any{"sessionID": fake.sessionID, "messageID": "message-1", "partID": "reasoning-1", "field": "text", "delta": "private reasoning"})
	fake.emit("message.part.updated", map[string]any{"part": map[string]any{"id": "tool-1", "messageID": "message-1", "sessionID": fake.sessionID, "type": "tool", "tool": "bash", "state": map[string]string{"status": "running"}}})
	fake.emit("message.part.updated", map[string]any{"part": map[string]any{"id": "tool-1", "messageID": "message-1", "sessionID": fake.sessionID, "type": "tool", "tool": "bash", "state": map[string]string{"status": "completed"}}})
	fake.emit("message.part.delta", map[string]any{"sessionID": fake.sessionID, "messageID": "message-1", "partID": "text-1", "field": "text", "delta": " world"})
	fake.emit("message.part.updated", map[string]any{"part": map[string]any{"id": "text-1", "messageID": "message-1", "sessionID": fake.sessionID, "type": "text", "text": "hello world"}})
	fake.emit("session.idle", map[string]string{"sessionID": fake.sessionID})

	if err := receiveOpenCodeResult(t, turnDone); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	select {
	case reply := <-fake.permissionReplies:
		if reply != "once" {
			t.Fatalf("OpenCode permission reply = %q, want once", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode permission was not approved")
	}
	want := []Event{
		{Type: EventAssistantDelta, Text: "hello"},
		{Type: EventToolStarted, ToolID: "tool-1", ToolName: "bash", Status: "running"},
		{Type: EventToolCompleted, ToolID: "tool-1", ToolName: "bash", Status: "completed"},
		{Type: EventAssistantDelta, Text: " world"},
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenCode events = %#v, want %#v", got, want)
	}
}

func TestOpenCodeProviderAnswersQuestion(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{WorkingDir: t.TempDir(), StateDir: t.TempDir()})
	sink := newOpenCodeTestSink(map[string][]string{"question-1": {"PostgreSQL"}})
	turnDone := make(chan error, 1)
	go func() { turnDone <- provider.RunTurn(t.Context(), "choose a database", sink) }()
	receiveOpenCodePrompt(t, fake.prompts)

	fake.emit("session.status", map[string]any{"sessionID": fake.sessionID, "status": map[string]string{"type": "busy"}})
	fake.emit("question.asked", map[string]any{
		"id":        "question-request-1",
		"sessionID": fake.sessionID,
		"questions": []map[string]any{{
			"header":   "Database",
			"question": "Which database?",
			"options":  []map[string]string{{"label": "PostgreSQL", "description": "Relational database"}},
		}},
	})

	select {
	case request := <-sink.inputs:
		if request.ID != "opencode-question-request-1" || len(request.Questions) != 1 || request.Questions[0].ID != "question-1" {
			t.Fatalf("OpenCode input request = %#v", request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode question was not forwarded")
	}
	select {
	case answers := <-fake.questionReplies:
		if !reflect.DeepEqual(answers, [][]string{{"PostgreSQL"}}) {
			t.Fatalf("OpenCode question answers = %#v", answers)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode question was not answered")
	}
	fake.emit("session.idle", map[string]string{"sessionID": fake.sessionID})
	if err := receiveOpenCodeResult(t, turnDone); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
}

func TestOpenCodeProviderInterruptsActiveTurn(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	provider := newTestOpenCodeProvider(t, fake, ProviderConfig{WorkingDir: t.TempDir(), StateDir: t.TempDir()})
	turnDone := make(chan error, 1)
	go func() { turnDone <- provider.RunTurn(t.Context(), "keep working", newOpenCodeTestSink(nil)) }()
	receiveOpenCodePrompt(t, fake.prompts)

	if err := provider.Interrupt(t.Context()); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	select {
	case <-fake.aborts:
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode abort endpoint was not called")
	}
	fake.emit("session.idle", map[string]string{"sessionID": fake.sessionID})
	if err := receiveOpenCodeResult(t, turnDone); !errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("RunTurn() error = %v, want %v", err, ErrTurnInterrupted)
	}
}

func TestOpenCodeProviderFailsWhenSavedSessionIsMissing(t *testing.T) {
	fake := newFakeOpenCodeServer(t)
	fake.resumeFound = false
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "opencode-session-id"), []byte("ses-missing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workingDir := t.TempDir()
	client, err := newOpenCodeClient(fake.server.URL, workingDir, fake.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = newOpenCodeProviderWithClient(t.Context(), ProviderConfig{WorkingDir: workingDir, StateDir: stateDir}, client)
	if err == nil || !strings.Contains(err.Error(), `resuming OpenCode session "ses-missing"`) {
		t.Fatalf("newOpenCodeProviderWithClient() error = %v", err)
	}
	if fake.createCount() != 0 {
		t.Fatalf("OpenCode created %d replacement sessions", fake.createCount())
	}
	fake.close()
}

func TestOpenCodeProviderRetriesStalledHealthRequest(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests++
		attempt := requests
		mu.Unlock()
		if attempt == 1 {
			<-request.Context().Done()
			return
		}
		writeOpenCodeJSON(response, map[string]bool{"healthy": true})
	}))
	defer server.Close()

	client, err := newOpenCodeClient(server.URL, t.TempDir(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider := newOpenCodeProviderState(t.Context(), ProviderConfig{}, client)
	defer provider.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := provider.waitForHealthWithRequestTimeout(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("waitForHealthWithRequestTimeout() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests < 2 {
		t.Fatalf("OpenCode health requests = %d, want at least 2", requests)
	}
}

func TestOpenCodeCommandEnvironmentMapsProviderKey(t *testing.T) {
	tests := []struct {
		model string
		name  string
	}{
		{model: "", name: "ANTHROPIC_API_KEY"},
		{model: "anthropic/claude-sonnet", name: "ANTHROPIC_API_KEY"},
		{model: "openai/gpt-5", name: "OPENAI_API_KEY"},
		{model: "google/gemini", name: "GEMINI_API_KEY"},
		{model: "zai/glm", name: "ZHIPU_API_KEY"},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			environment := openCodeCommandEnvironment([]string{"PATH=/usr/bin", "OPENCODE_API_KEY=provider-key"}, test.model)
			if got := processEnvironmentValue(environment, test.name); got != "provider-key" {
				t.Fatalf("%s = %q, want provider-key", test.name, got)
			}
		})
	}
}

type fakeOpenCodeServer struct {
	t      *testing.T
	server *httptest.Server

	sessionID         string
	resumeFound       bool
	events            chan string
	prompts           chan map[string]any
	questionReplies   chan [][]string
	permissionReplies chan string
	aborts            chan struct{}

	mu             sync.Mutex
	createdRequest map[string]any
	createdCount   int
}

func newFakeOpenCodeServer(t *testing.T) *fakeOpenCodeServer {
	t.Helper()
	fake := &fakeOpenCodeServer{
		t:                 t,
		sessionID:         "ses-test",
		resumeFound:       true,
		events:            make(chan string, 64),
		prompts:           make(chan map[string]any, 4),
		questionReplies:   make(chan [][]string, 4),
		permissionReplies: make(chan string, 4),
		aborts:            make(chan struct{}, 4),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *fakeOpenCodeServer) serveHTTP(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/event":
		f.serveEvents(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/session":
		f.requireDirectory(request)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Errorf("decoding OpenCode session request: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.createdRequest = body
		f.createdCount++
		f.mu.Unlock()
		writeOpenCodeJSON(response, map[string]string{"id": f.sessionID})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/session/"):
		f.requireDirectory(request)
		if !f.resumeFound {
			http.Error(response, "session not found", http.StatusNotFound)
			return
		}
		writeOpenCodeJSON(response, map[string]string{"id": strings.TrimPrefix(request.URL.Path, "/session/")})
	case request.Method == http.MethodPost && request.URL.Path == "/session/"+f.sessionID+"/prompt_async":
		f.requireDirectory(request)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Errorf("decoding OpenCode prompt: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		f.prompts <- body
		response.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodPost && request.URL.Path == "/session/"+f.sessionID+"/abort":
		f.requireDirectory(request)
		f.aborts <- struct{}{}
		writeOpenCodeJSON(response, true)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/question/") && strings.HasSuffix(request.URL.Path, "/reply"):
		f.requireDirectory(request)
		var body struct {
			Answers [][]string `json:"answers"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Errorf("decoding OpenCode question reply: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		f.questionReplies <- body.Answers
		writeOpenCodeJSON(response, true)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/permission/") && strings.HasSuffix(request.URL.Path, "/reply"):
		f.requireDirectory(request)
		var body struct {
			Reply string `json:"reply"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Errorf("decoding OpenCode permission reply: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		f.permissionReplies <- body.Reply
		writeOpenCodeJSON(response, true)
	default:
		http.NotFound(response, request)
	}
}

func (f *fakeOpenCodeServer) serveEvents(response http.ResponseWriter, request *http.Request) {
	f.requireDirectory(request)
	flusher, ok := response.(http.Flusher)
	if !ok {
		f.t.Error("test HTTP server does not support streaming")
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(response, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
	flusher.Flush()
	for {
		select {
		case event := <-f.events:
			_, _ = fmt.Fprintf(response, "data: %s\n\n", event)
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (f *fakeOpenCodeServer) requireDirectory(request *http.Request) {
	if request.URL.Query().Get("directory") == "" {
		f.t.Errorf("OpenCode request %s did not include its working directory", request.URL.Path)
	}
}

func (f *fakeOpenCodeServer) emit(eventType string, properties any) {
	f.t.Helper()
	data, err := json.Marshal(map[string]any{"type": eventType, "properties": properties})
	if err != nil {
		f.t.Fatal(err)
	}
	f.events <- string(data)
}

func (f *fakeOpenCodeServer) createdSessionRequest() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createdRequest
}

func (f *fakeOpenCodeServer) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createdCount
}

func (f *fakeOpenCodeServer) close() {
	f.server.Close()
}

func newTestOpenCodeProvider(t *testing.T, fake *fakeOpenCodeServer, config ProviderConfig) *OpenCodeProvider {
	t.Helper()
	client, err := newOpenCodeClient(fake.server.URL, config.WorkingDir, fake.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := newOpenCodeProviderWithClient(t.Context(), config, client)
	if err != nil {
		fake.close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
		fake.close()
	})
	return provider
}

type openCodeTestSink struct {
	mu      sync.Mutex
	events  []Event
	inputs  chan InputRequest
	answers map[string][]string
}

func newOpenCodeTestSink(answers map[string][]string) *openCodeTestSink {
	return &openCodeTestSink{inputs: make(chan InputRequest, 4), answers: answers}
}

func (s *openCodeTestSink) Emit(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *openCodeTestSink) RequestInput(_ context.Context, request InputRequest) (map[string][]string, error) {
	s.inputs <- request
	return s.answers, nil
}

func (s *openCodeTestSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func receiveOpenCodePrompt(t *testing.T, prompts <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case prompt := <-prompts:
		return prompt
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode prompt was not submitted")
		return nil
	}
}

func receiveOpenCodeResult(t *testing.T, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode turn did not finish")
		return nil
	}
}

func writeOpenCodeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}
