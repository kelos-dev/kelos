package sessionruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type codexResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type codexTurnResult struct {
	status string
	error  string
}

type codexRequestError struct {
	method  string
	code    int
	message string
}

func (e *codexRequestError) Error() string {
	return fmt.Sprintf("Codex request %s failed (%d): %s", e.method, e.code, e.message)
}

// CodexProvider owns one Codex app-server thread.
type CodexProvider struct {
	config ProviderConfig
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	writeMu   sync.Mutex
	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan codexResponse

	activeMu          sync.Mutex
	activeSink        EventSink
	turnDone          chan codexTurnResult
	activeTurn        string
	activeReady       chan struct{}
	interactionCtx    context.Context
	interactionCancel context.CancelFunc
	turnMu            sync.Mutex

	threadID string
	done     chan struct{}
	readErr  error
}

// NewCodexProvider starts Codex app-server and opens one resumable thread.
func NewCodexProvider(ctx context.Context, config ProviderConfig) (*CodexProvider, error) {
	providerCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(providerCtx, "codex", "app-server", "--stdio")
	command.Dir = config.WorkingDir
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening Codex app-server input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening Codex app-server output: %w", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting Codex app-server: %w", err)
	}

	provider := &CodexProvider{
		config:  config,
		ctx:     providerCtx,
		cancel:  cancel,
		cmd:     command,
		stdin:   stdin,
		pending: map[string]chan codexResponse{},
		done:    make(chan struct{}),
	}
	go provider.readLoop(stdout)

	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()
	if _, err := provider.request(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "kelos-session", "version": "1"},
		"capabilities": map[string]any{
			"experimentalApi":                true,
			"mcpServerOpenaiFormElicitation": false,
			"requestAttestation":             false,
		},
	}); err != nil {
		provider.Close()
		return nil, fmt.Errorf("initializing Codex app-server: %w", err)
	}
	if err := provider.write(map[string]any{"method": "initialized"}); err != nil {
		provider.Close()
		return nil, fmt.Errorf("confirming Codex app-server initialization: %w", err)
	}
	if err := provider.openThread(initCtx); err != nil {
		provider.Close()
		return nil, err
	}
	return provider, nil
}

func (p *CodexProvider) openThread(ctx context.Context) error {
	statePath := filepath.Join(p.config.StateDir, "codex-thread-id")
	if data, err := os.ReadFile(statePath); err == nil {
		threadID := strings.TrimSpace(string(data))
		if threadID != "" {
			result, err := p.request(ctx, "thread/resume", p.threadParams(map[string]any{"threadId": threadID}))
			if err != nil {
				if isMissingCodexRollout(err, threadID) {
					log.Printf("Codex thread rollout missing, starting replacement thread threadID=%s", threadID)
					return p.startThread(ctx, statePath)
				}
				return fmt.Errorf("resuming Codex thread %q: %w", threadID, err)
			}
			id := codexThreadID(result)
			if id == "" {
				return fmt.Errorf("resuming Codex thread %q: response did not include a thread ID", threadID)
			}
			p.threadID = id
			return nil
		}
		return errors.New("resuming Codex thread: saved thread ID is empty")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading saved Codex thread ID: %w", err)
	}
	return p.startThread(ctx, statePath)
}

func (p *CodexProvider) startThread(ctx context.Context, statePath string) error {
	result, err := p.request(ctx, "thread/start", p.threadParams(map[string]any{}))
	if err != nil {
		return fmt.Errorf("starting Codex thread: %w", err)
	}
	p.threadID = codexThreadID(result)
	if p.threadID == "" {
		return errors.New("starting Codex thread: response did not include a thread ID")
	}
	if err := os.WriteFile(statePath, []byte(p.threadID+"\n"), 0600); err != nil {
		return fmt.Errorf("saving Codex thread ID: %w", err)
	}
	return nil
}

func isMissingCodexRollout(err error, threadID string) bool {
	var requestErr *codexRequestError
	return errors.As(err, &requestErr) &&
		requestErr.method == "thread/resume" &&
		(requestErr.message == "no rollout found" ||
			requestErr.message == "no rollout found for thread id "+threadID)
}

func (p *CodexProvider) threadParams(params map[string]any) map[string]any {
	params["cwd"] = p.config.WorkingDir
	params["approvalPolicy"] = "never"
	params["sandbox"] = "danger-full-access"
	if p.config.Model != "" {
		params["model"] = p.config.Model
	}
	return params
}

func codexThreadID(result json.RawMessage) string {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(result, &response) != nil {
		return ""
	}
	return response.Thread.ID
}

func codexTurnID(result json.RawMessage) string {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(result, &response) != nil {
		return ""
	}
	return response.Turn.ID
}

// RunTurn starts one Codex turn and waits for its completion notification.
func (p *CodexProvider) RunTurn(ctx context.Context, prompt string, sink EventSink) error {
	p.turnMu.Lock()
	defer p.turnMu.Unlock()

	done := make(chan codexTurnResult, 1)
	ready := make(chan struct{})
	interactionCtx, interactionCancel := context.WithCancel(ctx)
	p.activeMu.Lock()
	p.activeSink = sink
	p.turnDone = done
	p.activeReady = ready
	p.interactionCtx = interactionCtx
	p.interactionCancel = interactionCancel
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
		p.activeTurn = ""
		p.activeReady = nil
		p.interactionCtx = nil
		p.interactionCancel = nil
		p.activeMu.Unlock()
	}()

	params := map[string]any{
		"threadId": p.threadID,
		"input": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
	}
	if p.config.Model != "" {
		params["model"] = p.config.Model
	}
	if p.config.Effort != "" {
		params["effort"] = p.config.Effort
	}
	result, err := p.request(ctx, "turn/start", params)
	if err != nil {
		return fmt.Errorf("starting Codex turn: %w", err)
	}
	turnID := codexTurnID(result)
	if turnID == "" {
		return errors.New("starting Codex turn: response did not include a turn ID")
	}
	p.activeMu.Lock()
	p.activeTurn = turnID
	close(ready)
	p.activeMu.Unlock()

	select {
	case result := <-done:
		if result.status == "interrupted" {
			return ErrTurnInterrupted
		}
		if result.status == "failed" {
			if result.error == "" {
				result.error = "Codex turn " + result.status
			}
			return errors.New(result.error)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		if p.readErr != nil {
			return p.readErr
		}
		return errors.New("Codex app-server stopped")
	}
}

// Interrupt stops the active Codex turn without closing its thread.
func (p *CodexProvider) Interrupt(ctx context.Context) error {
	p.activeMu.Lock()
	turnID := p.activeTurn
	ready := p.activeReady
	p.activeMu.Unlock()
	if turnID == "" && ready == nil {
		return ErrNoActiveTurn
	}
	if turnID == "" {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return errors.New("Codex app-server stopped")
		}
		p.activeMu.Lock()
		turnID = p.activeTurn
		p.activeMu.Unlock()
		if turnID == "" {
			return ErrNoActiveTurn
		}
	}
	_, err := p.request(ctx, "turn/interrupt", map[string]string{
		"threadId": p.threadID,
		"turnId":   turnID,
	})
	if err != nil {
		return fmt.Errorf("interrupting Codex turn: %w", err)
	}
	p.activeMu.Lock()
	cancel := p.interactionCancel
	p.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (p *CodexProvider) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	response := make(chan codexResponse, 1)
	p.pendingMu.Lock()
	p.pending[key] = response
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, key)
		p.pendingMu.Unlock()
	}()

	if err := p.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case value := <-response:
		if value.Error != nil {
			return nil, &codexRequestError{method: method, code: value.Error.Code, message: value.Error.Message}
		}
		return value.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		if p.readErr != nil {
			return nil, p.readErr
		}
		return nil, errors.New("Codex app-server stopped")
	}
}

func (p *CodexProvider) write(value any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding Codex message: %w", err)
	}
	data = append(data, '\n')
	if _, err := p.stdin.Write(data); err != nil {
		return fmt.Errorf("writing Codex message: %w", err)
	}
	return nil
}

func (p *CodexProvider) readLoop(reader io.Reader) {
	defer close(p.done)
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 8*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			p.readErr = fmt.Errorf("decoding Codex app-server message: %w", err)
			return
		}
		if envelope.Method != "" {
			if len(envelope.ID) > 0 {
				go p.handleServerRequest(envelope.ID, envelope.Method, envelope.Params)
			} else {
				p.handleNotification(envelope.Method, envelope.Params)
			}
			continue
		}
		if len(envelope.ID) > 0 {
			key := strings.Trim(string(envelope.ID), "\"")
			p.pendingMu.Lock()
			response := p.pending[key]
			p.pendingMu.Unlock()
			if response != nil {
				response <- codexResponse{Result: envelope.Result, Error: envelope.Error}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		p.readErr = fmt.Errorf("reading Codex app-server output: %w", err)
	}
}

func (p *CodexProvider) handleNotification(method string, params json.RawMessage) {
	p.activeMu.Lock()
	sink := p.activeSink
	done := p.turnDone
	p.activeMu.Unlock()
	if sink == nil {
		return
	}

	switch method {
	case "item/agentMessage/delta":
		var value struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(params, &value) == nil && value.Delta != "" {
			sink.Emit(Event{Type: EventAssistantDelta, Text: value.Delta})
		}
	case "item/started", "item/completed":
		p.emitCodexItem(method, params, sink)
	case "turn/diff/updated":
		var value struct {
			Diff string `json:"diff"`
		}
		if json.Unmarshal(params, &value) == nil && value.Diff != "" {
			sink.Emit(Event{Type: EventFileDiff, Diff: value.Diff})
		}
	case "turn/completed":
		var value struct {
			Turn struct {
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &value) == nil && done != nil {
			result := codexTurnResult{status: value.Turn.Status}
			if len(value.Turn.Error) > 0 && string(value.Turn.Error) != "null" {
				result.error = string(value.Turn.Error)
			}
			select {
			case done <- result:
			default:
			}
		}
	case "error":
		sink.Emit(Event{Type: EventError, Text: string(params)})
	}
}

func (p *CodexProvider) emitCodexItem(method string, params json.RawMessage, sink EventSink) {
	var value struct {
		Item struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Text    string `json:"text"`
			Command string `json:"command"`
			Tool    string `json:"tool"`
			Server  string `json:"server"`
			Query   string `json:"query"`
			Status  string `json:"status"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &value) != nil {
		return
	}
	item := value.Item
	if item.Type == "agentMessage" && method == "item/completed" {
		sink.Emit(Event{Type: EventAssistantMessage, Text: item.Text})
		return
	}

	name := item.Type
	switch item.Type {
	case "commandExecution":
		name = item.Command
	case "mcpToolCall":
		name = item.Server + "/" + item.Tool
	case "dynamicToolCall", "collabAgentToolCall":
		name = item.Tool
	case "webSearch":
		name = "Web search: " + item.Query
	case "fileChange":
		name = "File changes"
	default:
		return
	}
	eventType := EventToolStarted
	status := "running"
	if method == "item/completed" {
		eventType = EventToolCompleted
		status = item.Status
		if status == "" {
			status = "completed"
		}
	}
	sink.Emit(Event{Type: eventType, ToolID: item.ID, ToolName: name, Status: status})
}

func (p *CodexProvider) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "currentTime/read":
		_ = p.writeCodexResponse(id, map[string]any{"currentTimeAt": time.Now().Unix()})
		return
	case "mcpServer/elicitation/request":
		p.emitUnsupportedRequest(method)
		_ = p.writeCodexResponse(id, map[string]any{"action": "decline"})
		return
	case "item/tool/call":
		p.emitUnsupportedRequest(method)
		_ = p.writeCodexResponse(id, map[string]any{
			"success": false,
			"contentItems": []map[string]string{{
				"type": "inputText",
				"text": "Kelos Session does not provide client-side dynamic tools",
			}},
		})
		return
	case "item/tool/requestUserInput":
		p.handleCodexInputRequest(id, params)
		return
	default:
		p.rejectUnsupportedRequest(id, method)
		return
	}
}

func (p *CodexProvider) handleCodexInputRequest(id json.RawMessage, params json.RawMessage) {
	var value struct {
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(params, &value); err != nil || len(value.Questions) == 0 {
		_ = p.writeCodexError(id, -32602, "Codex user input request did not include valid questions")
		return
	}
	questions := make([]InputQuestion, 0, len(value.Questions))
	for _, question := range value.Questions {
		if question.ID == "" || question.Question == "" {
			_ = p.writeCodexError(id, -32602, "Codex user input question is missing an ID or question")
			return
		}
		options := make([]InputOption, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, InputOption{Label: option.Label, Description: option.Description})
		}
		questions = append(questions, InputQuestion{
			ID:       question.ID,
			Header:   question.Header,
			Question: question.Question,
			Options:  options,
			Secret:   question.IsSecret,
		})
	}

	p.activeMu.Lock()
	sink := p.activeSink
	interactionCtx := p.interactionCtx
	p.activeMu.Unlock()
	if interactionCtx == nil {
		interactionCtx = p.ctx
	}
	if interactionCtx == nil {
		interactionCtx = context.Background()
	}
	answers := map[string][]string{}
	if sink != nil {
		resolved, err := sink.RequestInput(interactionCtx, InputRequest{
			ID:        "codex-" + strings.Trim(string(id), "\""),
			Questions: questions,
		})
		if err == nil {
			answers = resolved
		}
	}
	type answer struct {
		Answers []string `json:"answers"`
	}
	response := make(map[string]answer, len(answers))
	for questionID, values := range answers {
		response[questionID] = answer{Answers: values}
	}
	_ = p.writeCodexResponse(id, map[string]any{"answers": response})
}

func (p *CodexProvider) rejectUnsupportedRequest(id json.RawMessage, method string) {
	p.emitUnsupportedRequest(method)
	_ = p.writeCodexError(id, -32601, fmt.Sprintf("Codex server request %q is not supported by Kelos Session", method))
}

func (p *CodexProvider) emitUnsupportedRequest(method string) {
	p.activeMu.Lock()
	sink := p.activeSink
	p.activeMu.Unlock()
	if sink != nil {
		sink.Emit(Event{
			Type:   EventError,
			Text:   fmt.Sprintf("Codex server request %q is not supported", method),
			Status: "rejected",
		})
	}
}

func (p *CodexProvider) writeCodexResponse(id json.RawMessage, result any) error {
	return p.write(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{ID: id, Result: result})
}

func (p *CodexProvider) writeCodexError(id json.RawMessage, code int, message string) error {
	return p.write(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// Done closes when Codex app-server can no longer serve turns.
func (p *CodexProvider) Done() <-chan struct{} {
	return p.done
}

// Close stops Codex app-server.
func (p *CodexProvider) Close() error {
	p.cancel()
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}
	if err := p.cmd.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			return err
		}
	}
	return nil
}
