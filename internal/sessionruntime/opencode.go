package sessionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	openCodeServerURL            = "http://127.0.0.1:4096"
	openCodeHealthRequestTimeout = time.Second
)

type openCodeClient struct {
	baseURL    *url.URL
	directory  string
	httpClient *http.Client
}

type openCodeHTTPError struct {
	statusCode int
	body       string
}

func (e *openCodeHTTPError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("OpenCode server returned HTTP %d", e.statusCode)
	}
	return fmt.Sprintf("OpenCode server returned HTTP %d: %s", e.statusCode, e.body)
}

type openCodeTurnResult struct {
	err         string
	interrupted bool
}

type openCodeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type openCodePart struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Tool      string `json:"tool"`
	State     struct {
		Status string `json:"status"`
	} `json:"state"`
}

// OpenCodeProvider owns one OpenCode server session and its event stream.
type OpenCodeProvider struct {
	config ProviderConfig
	client *openCodeClient
	ctx    context.Context
	cancel context.CancelFunc

	cmd           *exec.Cmd
	processExited chan struct{}
	eventExited   chan struct{}
	done          chan struct{}
	doneOnce      sync.Once
	errMu         sync.Mutex
	terminalErr   error

	sessionMu sync.RWMutex
	sessionID string
	turnMu    sync.Mutex
	activeMu  sync.Mutex

	activeSink        EventSink
	turnDone          chan openCodeTurnResult
	activeReady       chan struct{}
	interactionCtx    context.Context
	interactionCancel context.CancelFunc
	activeStarted     bool
	activeFinished    bool
	interrupted       bool
	activeError       string
	messageRoles      map[string]string
	partTypes         map[string]string
	partText          map[string]string
	toolStates        map[string]string
	questions         map[string]struct{}
	permissions       map[string]struct{}
}

// NewOpenCodeProvider starts the loopback OpenCode server and opens one resumable session.
func NewOpenCodeProvider(ctx context.Context, config ProviderConfig) (*OpenCodeProvider, error) {
	if _, err := openCodeModel(config.Model, config.Effort); err != nil {
		return nil, err
	}
	client, err := newOpenCodeClient(openCodeServerURL, config.WorkingDir, nil)
	if err != nil {
		return nil, err
	}
	provider := newOpenCodeProviderState(ctx, config, client)
	command := exec.CommandContext(provider.ctx, "opencode", "serve", "--hostname", "127.0.0.1", "--port", "4096")
	command.Dir = config.WorkingDir
	command.Env = openCodeCommandEnvironment(config.Environment, config.Model)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		provider.cancel()
		return nil, fmt.Errorf("starting OpenCode server: %w", err)
	}
	provider.cmd = command
	provider.processExited = make(chan struct{})
	go provider.waitForProcess()

	startupCtx, startupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startupCancel()
	if err := provider.waitForHealth(startupCtx); err != nil {
		_ = provider.Close()
		return nil, err
	}
	if err := provider.initialize(startupCtx); err != nil {
		_ = provider.Close()
		return nil, err
	}
	return provider, nil
}

func newOpenCodeProviderWithClient(ctx context.Context, config ProviderConfig, client *openCodeClient) (*OpenCodeProvider, error) {
	provider := newOpenCodeProviderState(ctx, config, client)
	if err := provider.initialize(ctx); err != nil {
		_ = provider.Close()
		return nil, err
	}
	return provider, nil
}

func newOpenCodeProviderState(ctx context.Context, config ProviderConfig, client *openCodeClient) *OpenCodeProvider {
	providerCtx, cancel := context.WithCancel(ctx)
	return &OpenCodeProvider{
		config: config,
		client: client,
		ctx:    providerCtx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

func newOpenCodeClient(rawURL, directory string, httpClient *http.Client) (*openCodeClient, error) {
	baseURL, err := url.Parse(rawURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid OpenCode server URL %q", rawURL)
	}
	if directory == "" {
		return nil, errors.New("OpenCode working directory must not be empty")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &openCodeClient{baseURL: baseURL, directory: directory, httpClient: httpClient}, nil
}

func (p *OpenCodeProvider) initialize(ctx context.Context) error {
	if err := os.MkdirAll(p.config.StateDir, 0700); err != nil {
		return fmt.Errorf("creating OpenCode state directory: %w", err)
	}
	if err := p.startEventStream(ctx); err != nil {
		return err
	}
	return p.openSession(ctx)
}

func (p *OpenCodeProvider) waitForHealth(ctx context.Context) error {
	return p.waitForHealthWithRequestTimeout(ctx, openCodeHealthRequestTimeout)
}

func (p *OpenCodeProvider) waitForHealthWithRequestTimeout(ctx context.Context, requestTimeout time.Duration) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		var health struct {
			Healthy bool `json:"healthy"`
		}
		requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		lastErr = p.client.doJSON(requestCtx, http.MethodGet, "/global/health", false, nil, &health)
		cancel()
		if lastErr == nil && health.Healthy {
			return nil
		}
		if lastErr == nil {
			lastErr = errors.New("OpenCode server reported unhealthy")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for OpenCode server health: %w (last attempt: %v)", ctx.Err(), lastErr)
		case <-p.done:
			if err := p.providerError(); err != nil {
				return err
			}
			return errors.New("OpenCode server stopped during startup")
		case <-ticker.C:
		}
	}
}

func (p *OpenCodeProvider) startEventStream(ctx context.Context) error {
	ready := make(chan error, 1)
	p.eventExited = make(chan struct{})
	go p.readEvents(ready)
	select {
	case err := <-ready:
		if err != nil {
			return fmt.Errorf("opening OpenCode event stream: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("opening OpenCode event stream: %w", ctx.Err())
	case <-p.done:
		if err := p.providerError(); err != nil {
			return err
		}
		return errors.New("OpenCode event stream stopped during startup")
	}
}

func (p *OpenCodeProvider) openSession(ctx context.Context) error {
	statePath := filepath.Join(p.config.StateDir, "opencode-session-id")
	if data, err := os.ReadFile(statePath); err == nil {
		sessionID := strings.TrimSpace(string(data))
		if sessionID == "" {
			return errors.New("resuming OpenCode session: saved session ID is empty")
		}
		var session struct {
			ID string `json:"id"`
		}
		err := p.client.doJSON(ctx, http.MethodGet, "/session/"+url.PathEscape(sessionID), true, nil, &session)
		if err != nil {
			return fmt.Errorf("resuming OpenCode session %q: %w", sessionID, err)
		}
		if session.ID != sessionID {
			return fmt.Errorf("resuming OpenCode session %q: response did not include the saved session ID", sessionID)
		}
		p.setSessionID(sessionID)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading saved OpenCode session ID: %w", err)
	}

	request := map[string]any{
		"agent": "build",
		"permission": []map[string]string{{
			"permission": "*",
			"pattern":    "*",
			"action":     "allow",
		}},
	}
	model, err := openCodeModel(p.config.Model, p.config.Effort)
	if err != nil {
		return err
	}
	if model != nil {
		request["model"] = model
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := p.client.doJSON(ctx, http.MethodPost, "/session", true, request, &session); err != nil {
		return fmt.Errorf("starting OpenCode session: %w", err)
	}
	if session.ID == "" {
		return errors.New("starting OpenCode session: response did not include a session ID")
	}
	p.setSessionID(session.ID)
	if err := os.WriteFile(statePath, []byte(session.ID+"\n"), 0600); err != nil {
		return fmt.Errorf("saving OpenCode session ID: %w", err)
	}
	return nil
}

func openCodeModel(model, effort string) (map[string]string, error) {
	if model == "" {
		return nil, nil
	}
	providerID, modelID, found := strings.Cut(model, "/")
	if !found || providerID == "" || modelID == "" {
		return nil, fmt.Errorf("OpenCode model %q must use provider/model format", model)
	}
	result := map[string]string{"providerID": providerID, "id": modelID}
	if effort != "" {
		result["variant"] = openCodeVariant(providerID, effort)
	}
	return result, nil
}

func openCodeVariant(provider, effort string) string {
	normalized := strings.ToLower(effort)
	switch provider {
	case "anthropic":
		if normalized == "max" || normalized == "xhigh" {
			return "max"
		}
		return "high"
	case "google":
		if normalized == "minimal" || normalized == "low" {
			return "low"
		}
		return "high"
	default:
		return normalized
	}
}

// RunTurn submits one prompt to the persistent OpenCode session.
func (p *OpenCodeProvider) RunTurn(ctx context.Context, prompt string, sink EventSink) error {
	p.turnMu.Lock()
	defer p.turnMu.Unlock()

	done := make(chan openCodeTurnResult, 1)
	ready := make(chan struct{})
	interactionCtx, interactionCancel := context.WithCancel(ctx)
	p.activeMu.Lock()
	p.activeSink = sink
	p.turnDone = done
	p.activeReady = ready
	p.interactionCtx = interactionCtx
	p.interactionCancel = interactionCancel
	p.activeStarted = false
	p.activeFinished = false
	p.interrupted = false
	p.activeError = ""
	p.messageRoles = map[string]string{}
	p.partTypes = map[string]string{}
	p.partText = map[string]string{}
	p.toolStates = map[string]string{}
	p.questions = map[string]struct{}{}
	p.permissions = map[string]struct{}{}
	p.activeMu.Unlock()
	defer func() {
		interactionCancel()
		p.activeMu.Lock()
		select {
		case <-ready:
		default:
			close(ready)
		}
		p.activeSink = nil
		p.turnDone = nil
		p.activeReady = nil
		p.interactionCtx = nil
		p.interactionCancel = nil
		p.messageRoles = nil
		p.partTypes = nil
		p.partText = nil
		p.toolStates = nil
		p.questions = nil
		p.permissions = nil
		p.activeMu.Unlock()
	}()

	request := map[string]any{
		"parts": []map[string]string{{"type": "text", "text": prompt}},
	}
	if err := p.client.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(p.currentSessionID())+"/prompt_async", true, request, nil); err != nil {
		return fmt.Errorf("starting OpenCode turn: %w", err)
	}
	p.activeMu.Lock()
	close(ready)
	p.activeMu.Unlock()

	select {
	case result := <-done:
		if result.interrupted {
			return ErrTurnInterrupted
		}
		if result.err != "" {
			return errors.New(result.err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		if err := p.providerError(); err != nil {
			return err
		}
		return errors.New("OpenCode server stopped")
	}
}

// Interrupt stops the active OpenCode turn without deleting its session.
func (p *OpenCodeProvider) Interrupt(ctx context.Context) error {
	p.activeMu.Lock()
	ready := p.activeReady
	p.activeMu.Unlock()
	if ready == nil {
		return ErrNoActiveTurn
	}
	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errors.New("OpenCode server stopped")
	}

	p.activeMu.Lock()
	if p.turnDone == nil || p.activeFinished {
		p.activeMu.Unlock()
		return ErrNoActiveTurn
	}
	p.interrupted = true
	p.activeStarted = true
	cancel := p.interactionCancel
	p.activeMu.Unlock()

	var aborted bool
	if err := p.client.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(p.currentSessionID())+"/abort", true, nil, &aborted); err != nil {
		p.activeMu.Lock()
		p.interrupted = false
		p.activeMu.Unlock()
		return fmt.Errorf("interrupting OpenCode turn: %w", err)
	}
	if !aborted {
		p.activeMu.Lock()
		p.interrupted = false
		p.activeMu.Unlock()
		return errors.New("interrupting OpenCode turn: OpenCode reported no active turn")
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

func (p *OpenCodeProvider) readEvents(ready chan<- error) {
	defer close(p.eventExited)
	response, err := p.client.eventStream(p.ctx)
	if err != nil {
		ready <- err
		p.finish(err)
		return
	}
	defer response.Body.Close()
	ready <- nil

	scanner := newProviderScanner(response.Body)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				p.handleEvent([]byte(strings.Join(data, "\n")))
				data = data[:0]
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil && p.ctx.Err() == nil {
		p.finish(fmt.Errorf("reading OpenCode event stream: %w", err))
		return
	}
	if p.ctx.Err() == nil {
		p.finish(errors.New("OpenCode event stream stopped"))
	}
}

func (p *OpenCodeProvider) handleEvent(data []byte) {
	var event openCodeEvent
	if json.Unmarshal(data, &event) != nil {
		return
	}
	switch event.Type {
	case "message.updated":
		p.handleMessageUpdated(event.Properties)
	case "message.part.updated":
		p.handlePartUpdated(event.Properties)
	case "message.part.delta":
		p.handlePartDelta(event.Properties)
	case "session.status":
		p.handleSessionStatus(event.Properties)
	case "session.idle":
		p.handleSessionIdle(event.Properties)
	case "session.error":
		p.handleSessionError(event.Properties)
	case "question.asked", "question.v2.asked":
		p.handleQuestion(event.Type, event.Properties)
	case "permission.asked", "permission.v2.asked":
		p.handlePermission(event.Type, event.Properties)
	}
}

func (p *OpenCodeProvider) handleMessageUpdated(raw json.RawMessage) {
	var properties struct {
		Info struct {
			ID        string          `json:"id"`
			SessionID string          `json:"sessionID"`
			Role      string          `json:"role"`
			Error     json.RawMessage `json:"error"`
		} `json:"info"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.Info.SessionID != p.currentSessionID() {
		return
	}
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	if p.activeSink == nil {
		return
	}
	p.messageRoles[properties.Info.ID] = properties.Info.Role
	if message := openCodeErrorText(properties.Info.Error); message != "" {
		p.activeError = message
	}
}

func (p *OpenCodeProvider) handlePartUpdated(raw json.RawMessage) {
	var properties struct {
		Part  openCodePart `json:"part"`
		Delta string       `json:"delta"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.Part.SessionID != p.currentSessionID() {
		return
	}
	part := properties.Part
	p.activeMu.Lock()
	if p.activeSink == nil {
		p.activeMu.Unlock()
		return
	}
	p.partTypes[part.ID] = part.Type
	sink := p.activeSink
	role := p.messageRoles[part.MessageID]
	text := ""
	if part.Type == "text" && role == "assistant" {
		emitted := p.partText[part.ID]
		if properties.Delta != "" {
			text = properties.Delta
			emitted += properties.Delta
		}
		if strings.HasPrefix(part.Text, emitted) && len(part.Text) > len(emitted) {
			text += part.Text[len(emitted):]
			emitted = part.Text
		}
		p.partText[part.ID] = emitted
	}
	toolEvents := p.openCodeToolEventsLocked(part)
	p.activeMu.Unlock()
	if text != "" {
		sink.Emit(Event{Type: EventAssistantDelta, Text: text})
	}
	for _, event := range toolEvents {
		sink.Emit(event)
	}
}

func (p *OpenCodeProvider) handlePartDelta(raw json.RawMessage) {
	var properties struct {
		SessionID string `json:"sessionID"`
		MessageID string `json:"messageID"`
		PartID    string `json:"partID"`
		Field     string `json:"field"`
		Delta     string `json:"delta"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.SessionID != p.currentSessionID() || properties.Field != "text" || properties.Delta == "" {
		return
	}
	p.activeMu.Lock()
	if p.activeSink == nil || p.partTypes[properties.PartID] != "text" || p.messageRoles[properties.MessageID] != "assistant" {
		p.activeMu.Unlock()
		return
	}
	p.partText[properties.PartID] += properties.Delta
	sink := p.activeSink
	p.activeMu.Unlock()
	sink.Emit(Event{Type: EventAssistantDelta, Text: properties.Delta})
}

func (p *OpenCodeProvider) openCodeToolEventsLocked(part openCodePart) []Event {
	if part.Type != "tool" || part.ID == "" {
		return nil
	}
	name := part.Tool
	if name == "" {
		name = "tool"
	}
	previous := p.toolStates[part.ID]
	status := part.State.Status
	events := []Event{}
	if previous == "" {
		events = append(events, Event{Type: EventToolStarted, ToolID: part.ID, ToolName: name, Status: "running"})
	}
	if (status == "completed" || status == "error") && previous != "completed" && previous != "error" {
		result := "completed"
		if status == "error" {
			result = "failed"
		}
		events = append(events, Event{Type: EventToolCompleted, ToolID: part.ID, ToolName: name, Status: result})
	}
	p.toolStates[part.ID] = status
	return events
}

func (p *OpenCodeProvider) handleSessionStatus(raw json.RawMessage) {
	var properties struct {
		SessionID string          `json:"sessionID"`
		Status    json.RawMessage `json:"status"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.SessionID != p.currentSessionID() {
		return
	}
	status := ""
	var object struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(properties.Status, &object) == nil {
		status = object.Type
	}
	if status == "" {
		_ = json.Unmarshal(properties.Status, &status)
	}
	if status == "busy" {
		p.activeMu.Lock()
		if p.turnDone != nil {
			p.activeStarted = true
		}
		p.activeMu.Unlock()
		return
	}
	if status == "idle" {
		p.completeActiveTurn(false)
	}
}

func (p *OpenCodeProvider) handleSessionIdle(raw json.RawMessage) {
	var properties struct {
		SessionID string `json:"sessionID"`
	}
	if json.Unmarshal(raw, &properties) == nil && properties.SessionID == p.currentSessionID() {
		p.completeActiveTurn(false)
	}
}

func (p *OpenCodeProvider) handleSessionError(raw json.RawMessage) {
	var properties struct {
		SessionID string          `json:"sessionID"`
		Error     json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.SessionID != p.currentSessionID() {
		return
	}
	message := openCodeErrorText(properties.Error)
	if message == "" {
		message = "OpenCode session failed"
	}
	p.activeMu.Lock()
	p.activeError = message
	p.activeStarted = true
	p.activeMu.Unlock()
	p.completeActiveTurn(true)
}

func (p *OpenCodeProvider) completeActiveTurn(force bool) {
	p.activeMu.Lock()
	if p.turnDone == nil || p.activeFinished || (!force && !p.activeStarted) {
		p.activeMu.Unlock()
		return
	}
	p.activeFinished = true
	result := openCodeTurnResult{err: p.activeError, interrupted: p.interrupted}
	done := p.turnDone
	p.activeMu.Unlock()
	select {
	case done <- result:
	default:
	}
}

func (p *OpenCodeProvider) handleQuestion(eventType string, raw json.RawMessage) {
	var properties struct {
		ID        string `json:"id"`
		RequestID string `json:"requestID"`
		SessionID string `json:"sessionID"`
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
			Multiple bool `json:"multiple"`
		} `json:"questions"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.SessionID != p.currentSessionID() {
		return
	}
	requestID := properties.ID
	if requestID == "" {
		requestID = properties.RequestID
	}
	if requestID == "" || len(properties.Questions) == 0 {
		return
	}

	questions := make([]InputQuestion, 0, len(properties.Questions))
	for index, question := range properties.Questions {
		if question.Question == "" {
			return
		}
		options := make([]InputOption, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, InputOption{Label: option.Label, Description: option.Description})
		}
		questions = append(questions, InputQuestion{
			ID:          fmt.Sprintf("question-%d", index+1),
			Header:      question.Header,
			Question:    question.Question,
			Options:     options,
			MultiSelect: question.Multiple,
		})
	}

	p.activeMu.Lock()
	if p.activeSink == nil {
		p.activeMu.Unlock()
		return
	}
	if _, exists := p.questions[requestID]; exists {
		p.activeMu.Unlock()
		return
	}
	p.questions[requestID] = struct{}{}
	sink := p.activeSink
	interactionCtx := p.interactionCtx
	p.activeMu.Unlock()

	answers, err := sink.RequestInput(interactionCtx, InputRequest{ID: "opencode-" + requestID, Questions: questions})
	if err != nil {
		p.rejectQuestion(eventType, requestID)
		return
	}
	ordered := make([][]string, 0, len(questions))
	for _, question := range questions {
		ordered = append(ordered, answers[question.ID])
	}
	path, includeDirectory := p.questionPath(eventType, requestID, "reply")
	if err := p.client.doJSON(p.ctx, http.MethodPost, path, includeDirectory, map[string]any{"answers": ordered}, nil); err != nil {
		p.failActiveTurn(fmt.Errorf("replying to OpenCode question: %w", err))
	}
}

func (p *OpenCodeProvider) rejectQuestion(eventType, requestID string) {
	path, includeDirectory := p.questionPath(eventType, requestID, "reject")
	if err := p.client.doJSON(p.ctx, http.MethodPost, path, includeDirectory, nil, nil); err != nil && p.ctx.Err() == nil {
		p.failActiveTurn(fmt.Errorf("rejecting OpenCode question: %w", err))
	}
}

func (p *OpenCodeProvider) questionPath(eventType, requestID, action string) (string, bool) {
	if eventType == "question.v2.asked" {
		return "/api/session/" + url.PathEscape(p.currentSessionID()) + "/question/" + url.PathEscape(requestID) + "/" + action, false
	}
	return "/question/" + url.PathEscape(requestID) + "/" + action, true
}

func (p *OpenCodeProvider) handlePermission(eventType string, raw json.RawMessage) {
	var properties struct {
		ID        string `json:"id"`
		RequestID string `json:"requestID"`
		SessionID string `json:"sessionID"`
	}
	if json.Unmarshal(raw, &properties) != nil || properties.SessionID != p.currentSessionID() {
		return
	}
	requestID := properties.ID
	if requestID == "" {
		requestID = properties.RequestID
	}
	if requestID == "" {
		return
	}
	p.activeMu.Lock()
	if p.activeSink == nil {
		p.activeMu.Unlock()
		return
	}
	if _, exists := p.permissions[requestID]; exists {
		p.activeMu.Unlock()
		return
	}
	p.permissions[requestID] = struct{}{}
	p.activeMu.Unlock()

	path := "/permission/" + url.PathEscape(requestID) + "/reply"
	includeDirectory := true
	if eventType == "permission.v2.asked" {
		path = "/api/session/" + url.PathEscape(p.currentSessionID()) + "/permission/" + url.PathEscape(requestID) + "/reply"
		includeDirectory = false
	}
	if err := p.client.doJSON(p.ctx, http.MethodPost, path, includeDirectory, map[string]string{"reply": "once"}, nil); err != nil {
		p.failActiveTurn(fmt.Errorf("approving OpenCode permission: %w", err))
	}
}

func (p *OpenCodeProvider) failActiveTurn(err error) {
	p.activeMu.Lock()
	p.activeError = err.Error()
	p.activeStarted = true
	p.activeMu.Unlock()
	p.completeActiveTurn(true)
	abortCtx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	_ = p.client.doJSON(abortCtx, http.MethodPost, "/session/"+url.PathEscape(p.currentSessionID())+"/abort", true, nil, nil)
}

func (p *OpenCodeProvider) setSessionID(sessionID string) {
	p.sessionMu.Lock()
	p.sessionID = sessionID
	p.sessionMu.Unlock()
}

func (p *OpenCodeProvider) currentSessionID() string {
	p.sessionMu.RLock()
	defer p.sessionMu.RUnlock()
	return p.sessionID
}

func openCodeErrorText(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var value struct {
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &value) == nil {
		if value.Data.Message != "" {
			return value.Data.Message
		}
		if value.Message != "" {
			return value.Message
		}
		if len(value.Error) > 0 {
			return openCodeErrorText(value.Error)
		}
	}
	text := string(bytes.TrimSpace(raw))
	if len(text) > 4096 {
		text = text[:4096]
	}
	return text
}

func (p *OpenCodeProvider) waitForProcess() {
	err := p.cmd.Wait()
	close(p.processExited)
	if p.ctx.Err() != nil {
		p.finish(nil)
		return
	}
	if err != nil {
		p.finish(fmt.Errorf("OpenCode server stopped: %w", err))
		return
	}
	p.finish(errors.New("OpenCode server stopped"))
}

func (p *OpenCodeProvider) finish(err error) {
	if err != nil && p.ctx.Err() == nil {
		p.errMu.Lock()
		if p.terminalErr == nil {
			p.terminalErr = err
		}
		p.errMu.Unlock()
	}
	p.cancelActiveInteraction()
	p.cancel()
	p.doneOnce.Do(func() { close(p.done) })
}

func (p *OpenCodeProvider) cancelActiveInteraction() {
	p.activeMu.Lock()
	cancel := p.interactionCancel
	p.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *OpenCodeProvider) providerError() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.terminalErr
}

// Done closes when the OpenCode server can no longer serve turns.
func (p *OpenCodeProvider) Done() <-chan struct{} {
	return p.done
}

// Close stops the OpenCode server and event stream.
func (p *OpenCodeProvider) Close() error {
	p.cancelActiveInteraction()
	p.cancel()
	p.doneOnce.Do(func() { close(p.done) })
	if p.cmd != nil && p.processExited != nil {
		select {
		case <-p.processExited:
		case <-time.After(5 * time.Second):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			<-p.processExited
		}
	}
	if p.eventExited != nil {
		select {
		case <-p.eventExited:
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}

func openCodeCommandEnvironment(current []string, model string) []string {
	result := append([]string(nil), current...)
	key := processEnvironmentValue(current, "OPENCODE_API_KEY")
	if key == "" {
		return result
	}
	provider := "anthropic"
	if value, _, found := strings.Cut(model, "/"); found && value != "" {
		provider = value
	}
	name := ""
	switch provider {
	case "anthropic":
		name = "ANTHROPIC_API_KEY"
	case "openai":
		name = "OPENAI_API_KEY"
	case "google":
		name = "GEMINI_API_KEY"
	case "groq":
		name = "GROQ_API_KEY"
	case "xai":
		name = "XAI_API_KEY"
	case "zai", "zai-coding-plan":
		name = "ZHIPU_API_KEY"
	case "opencode", "zen":
		return result
	default:
		name = "ANTHROPIC_API_KEY"
	}
	return replaceProcessEnv(result, name, key)
}

func processEnvironmentValue(environment []string, name string) string {
	prefix := name + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}

func (c *openCodeClient) doJSON(ctx context.Context, method, path string, includeDirectory bool, requestValue, responseValue any) error {
	var body io.Reader
	if requestValue != nil {
		data, err := json.Marshal(requestValue)
		if err != nil {
			return fmt.Errorf("encoding OpenCode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.requestURL(path, includeDirectory), body)
	if err != nil {
		return err
	}
	if requestValue != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return &openCodeHTTPError{statusCode: response.StatusCode, body: strings.TrimSpace(string(data))}
	}
	if responseValue == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(responseValue); err != nil {
		return fmt.Errorf("decoding OpenCode response: %w", err)
	}
	return nil
}

func (c *openCodeClient) eventStream(ctx context.Context) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.requestURL("/event", true), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return nil, &openCodeHTTPError{statusCode: response.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return response, nil
}

func (c *openCodeClient) requestURL(path string, includeDirectory bool) string {
	value := *c.baseURL
	value.Path = strings.TrimRight(value.Path, "/") + path
	if includeDirectory {
		query := value.Query()
		query.Set("directory", c.directory)
		value.RawQuery = query.Encode()
	}
	return value.String()
}
