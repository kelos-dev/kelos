package sessionruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/uuid"
)

type claudeTurnResult struct {
	error string
}

type claudeControlRequestEnvelope struct {
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	} `json:"request"`
}

// ClaudeProvider owns one bidirectional Claude Code stream-json process.
type ClaudeProvider struct {
	config ProviderConfig
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	writeMu sync.Mutex
	turnMu  sync.Mutex

	controlMu      sync.Mutex
	controlPending map[string]chan error
	nextControlID  atomic.Int64

	activeMu          sync.Mutex
	activeSink        EventSink
	turnDone          chan claudeTurnResult
	interactionCtx    context.Context
	interactionCancel context.CancelFunc
	seenTools         map[string]struct{}
	hadTextDelta      bool
	interrupted       bool

	sessionMu sync.Mutex
	sessionID string
	done      chan struct{}
	readErr   error
}

// NewClaudeProvider starts Claude Code in bidirectional stream-json mode.
func NewClaudeProvider(ctx context.Context, config ProviderConfig) (*ClaudeProvider, error) {
	sessionID := ""
	if data, err := os.ReadFile(filepath.Join(config.StateDir, "claude-session-id")); err == nil {
		sessionID = strings.TrimSpace(string(data))
	}
	resume := sessionID != ""
	if sessionID == "" {
		sessionID = string(uuid.NewUUID())
	}

	args := claudeCommandArgs(config, sessionID, resume)

	providerCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(providerCtx, "claude", args...)
	command.Dir = config.WorkingDir
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening Claude Code input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening Claude Code output: %w", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting Claude Code: %w", err)
	}

	provider := &ClaudeProvider{
		config:         config,
		ctx:            providerCtx,
		cancel:         cancel,
		cmd:            command,
		stdin:          stdin,
		sessionID:      sessionID,
		controlPending: map[string]chan error{},
		done:           make(chan struct{}),
	}
	go provider.readLoop(stdout)
	return provider, nil
}

func claudeCommandArgs(config ProviderConfig, sessionID string, resume bool) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--replay-user-messages",
		"--dangerously-skip-permissions",
		"--permission-prompt-tool", "stdio",
		"--verbose",
		"-p",
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	if config.Effort != "" {
		args = append(args, "--effort", config.Effort)
	}
	if config.PluginDir != "" {
		entries, _ := os.ReadDir(config.PluginDir)
		for _, entry := range entries {
			if entry.IsDir() {
				args = append(args, "--plugin-dir", filepath.Join(config.PluginDir, entry.Name()))
			}
		}
	}
	return args
}

// RunTurn sends one prompt while keeping the Claude Code process alive between turns.
func (p *ClaudeProvider) RunTurn(ctx context.Context, prompt string, sink EventSink) error {
	p.turnMu.Lock()
	defer p.turnMu.Unlock()

	done := make(chan claudeTurnResult, 1)
	interactionCtx, interactionCancel := context.WithCancel(ctx)
	p.activeMu.Lock()
	p.activeSink = sink
	p.turnDone = done
	p.interactionCtx = interactionCtx
	p.interactionCancel = interactionCancel
	p.seenTools = map[string]struct{}{}
	p.hadTextDelta = false
	p.interrupted = false
	p.activeMu.Unlock()
	defer func() {
		interactionCancel()
		p.activeMu.Lock()
		p.activeSink = nil
		p.turnDone = nil
		p.interactionCtx = nil
		p.interactionCancel = nil
		p.seenTools = nil
		p.interrupted = false
		p.activeMu.Unlock()
	}()

	p.sessionMu.Lock()
	sessionID := p.sessionID
	p.sessionMu.Unlock()
	message := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]string{{
				"type": "text",
				"text": prompt,
			}},
		},
		"parent_tool_use_id": nil,
		"session_id":         sessionID,
	}
	if err := p.write(message); err != nil {
		return err
	}

	select {
	case result := <-done:
		p.activeMu.Lock()
		interrupted := p.interrupted
		p.activeMu.Unlock()
		if interrupted {
			return ErrTurnInterrupted
		}
		if result.error != "" {
			return errors.New(result.error)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		if p.readErr != nil {
			return p.readErr
		}
		return errors.New("Claude Code stopped")
	}
}

// Interrupt stops the active Claude Code turn without closing its session.
func (p *ClaudeProvider) Interrupt(ctx context.Context) error {
	p.activeMu.Lock()
	if p.turnDone == nil {
		p.activeMu.Unlock()
		return ErrNoActiveTurn
	}
	p.interrupted = true
	p.activeMu.Unlock()
	if err := p.sendControlRequest(ctx, map[string]any{"subtype": "interrupt"}); err != nil {
		p.activeMu.Lock()
		if p.turnDone != nil {
			p.interrupted = false
		}
		p.activeMu.Unlock()
		return fmt.Errorf("interrupting Claude Code turn: %w", err)
	}
	p.activeMu.Lock()
	cancel := p.interactionCancel
	p.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (p *ClaudeProvider) write(value any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding Claude Code message: %w", err)
	}
	data = append(data, '\n')
	if _, err := p.stdin.Write(data); err != nil {
		return fmt.Errorf("writing Claude Code message: %w", err)
	}
	return nil
}

func (p *ClaudeProvider) readLoop(reader io.Reader) {
	defer close(p.done)
	scanner := newProviderScanner(reader)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			p.readErr = fmt.Errorf("decoding Claude Code event: %w", err)
			return
		}
		if header.Type == "control_request" {
			go p.handleControlRequest(line)
			continue
		}
		if header.Type == "control_response" {
			p.handleControlResponse(line)
			continue
		}

		p.activeMu.Lock()
		sink := p.activeSink
		done := p.turnDone
		p.activeMu.Unlock()
		result, err := p.handleClaudeLine(line, sink)
		if err != nil {
			p.readErr = err
			return
		}
		if result != nil && done != nil {
			if err := p.persistSessionID(); err != nil {
				p.readErr = fmt.Errorf("saving Claude Code session ID: %w", err)
				return
			}
			select {
			case done <- *result:
			default:
			}
		}
	}
	if err := scanner.Err(); err != nil {
		p.readErr = fmt.Errorf("reading Claude Code output: %w", err)
	}
}

func (p *ClaudeProvider) handleControlResponse(line []byte) {
	var envelope struct {
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.Response.RequestID == "" {
		return
	}
	var result error
	if envelope.Response.Subtype == "error" {
		result = errors.New(envelope.Response.Error)
	}
	p.controlMu.Lock()
	pending := p.controlPending[envelope.Response.RequestID]
	p.controlMu.Unlock()
	if pending != nil {
		pending <- result
	}
}

func (p *ClaudeProvider) sendControlRequest(ctx context.Context, request map[string]any) error {
	requestID := fmt.Sprintf("kelos-%d", p.nextControlID.Add(1))
	response := make(chan error, 1)
	p.controlMu.Lock()
	if p.controlPending == nil {
		p.controlPending = map[string]chan error{}
	}
	p.controlPending[requestID] = response
	p.controlMu.Unlock()
	defer func() {
		p.controlMu.Lock()
		delete(p.controlPending, requestID)
		p.controlMu.Unlock()
	}()
	if err := p.write(map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    request,
	}); err != nil {
		return err
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		if p.readErr != nil {
			return p.readErr
		}
		return errors.New("Claude Code stopped")
	}
}

func newProviderScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 8*1024*1024)
	return scanner
}

func (p *ClaudeProvider) handleClaudeLine(line []byte, sink EventSink) (*claudeTurnResult, error) {
	var envelope struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		IsError   bool            `json:"is_error"`
		Result    string          `json:"result"`
		SessionID string          `json:"session_id"`
		Event     json.RawMessage `json:"event"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil, fmt.Errorf("decoding Claude Code event: %w", err)
	}
	if envelope.SessionID != "" {
		p.sessionMu.Lock()
		p.sessionID = envelope.SessionID
		p.sessionMu.Unlock()
	}
	if sink == nil && envelope.Type != "result" {
		return nil, nil
	}

	switch envelope.Type {
	case "stream_event":
		p.emitClaudeStreamEvent(envelope.Event, sink)
	case "assistant", "user":
		p.emitClaudeMessage(envelope.Type, envelope.Message, sink)
	case "result":
		result := &claudeTurnResult{}
		if envelope.IsError || envelope.Subtype != "success" {
			result.error = envelope.Result
			if result.error == "" {
				result.error = "Claude Code returned an unsuccessful result"
			}
		}
		return result, nil
	}
	return nil, nil
}

func (p *ClaudeProvider) handleControlRequest(line []byte) {
	var envelope claudeControlRequestEnvelope
	if json.Unmarshal(line, &envelope) != nil || envelope.RequestID == "" {
		return
	}
	if envelope.Request.Subtype != "can_use_tool" {
		_ = p.writeClaudeControlError(envelope.RequestID, "unsupported control request: "+envelope.Request.Subtype)
		return
	}
	if envelope.Request.ToolName == "AskUserQuestion" {
		p.handleClaudeInputRequest(envelope)
		return
	}
	_ = p.writeClaudeControlError(envelope.RequestID, "unsupported tool request: "+envelope.Request.ToolName)
}

func (p *ClaudeProvider) handleClaudeInputRequest(envelope claudeControlRequestEnvelope) {
	var input struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(envelope.Request.Input, &input); err != nil || len(input.Questions) == 0 {
		_ = p.writeClaudeControlError(envelope.RequestID, "AskUserQuestion did not include valid questions")
		return
	}
	questions := make([]InputQuestion, 0, len(input.Questions))
	questionText := make(map[string]string, len(input.Questions))
	for index, question := range input.Questions {
		if question.Question == "" {
			_ = p.writeClaudeControlError(envelope.RequestID, "AskUserQuestion included an empty question")
			return
		}
		id := fmt.Sprintf("question-%d", index+1)
		options := make([]InputOption, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, InputOption{Label: option.Label, Description: option.Description})
		}
		questions = append(questions, InputQuestion{
			ID:          id,
			Header:      question.Header,
			Question:    question.Question,
			Options:     options,
			MultiSelect: question.MultiSelect,
		})
		questionText[id] = question.Question
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
	if sink == nil {
		_ = p.writeClaudeControlSuccess(envelope.RequestID, map[string]any{
			"behavior": "deny",
			"message":  "No Session client can answer the question",
		})
		return
	}
	answers, err := sink.RequestInput(interactionCtx, InputRequest{
		ID:        "claude-" + envelope.RequestID,
		Questions: questions,
	})
	if err != nil {
		_ = p.writeClaudeControlSuccess(envelope.RequestID, map[string]any{
			"behavior": "deny",
			"message":  "User cancelled the question",
		})
		return
	}
	var updatedInput map[string]any
	if json.Unmarshal(envelope.Request.Input, &updatedInput) != nil {
		updatedInput = map[string]any{}
	}
	providerAnswers := make(map[string]string, len(answers))
	for questionID, values := range answers {
		providerAnswers[questionText[questionID]] = strings.Join(values, ", ")
	}
	updatedInput["answers"] = providerAnswers
	_ = p.writeClaudeControlSuccess(envelope.RequestID, map[string]any{
		"behavior":               "allow",
		"updatedInput":           updatedInput,
		"decisionClassification": "user_temporary",
	})
}

func (p *ClaudeProvider) writeClaudeControlSuccess(requestID string, response any) error {
	p.sessionMu.Lock()
	sessionID := p.sessionID
	p.sessionMu.Unlock()
	return p.write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
		"session_id": sessionID,
	})
}

func (p *ClaudeProvider) writeClaudeControlError(requestID, message string) error {
	p.sessionMu.Lock()
	sessionID := p.sessionID
	p.sessionMu.Unlock()
	return p.write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      message,
		},
		"session_id": sessionID,
	})
}

func (p *ClaudeProvider) emitClaudeStreamEvent(raw json.RawMessage, sink EventSink) {
	if sink == nil {
		return
	}
	var event struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	switch event.Type {
	case "content_block_delta":
		if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			p.activeMu.Lock()
			p.hadTextDelta = true
			p.activeMu.Unlock()
			sink.Emit(Event{Type: EventAssistantDelta, Text: event.Delta.Text})
		}
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			p.emitClaudeToolStart(event.ContentBlock.ID, event.ContentBlock.Name, sink)
		}
	}
}

func (p *ClaudeProvider) emitClaudeMessage(messageType string, raw json.RawMessage, sink EventSink) {
	if sink == nil {
		return
	}
	var message struct {
		Content []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			Text      string `json:"text"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return
	}
	for _, block := range message.Content {
		if messageType == "assistant" && block.Type == "text" && block.Text != "" {
			p.activeMu.Lock()
			hadDelta := p.hadTextDelta
			p.activeMu.Unlock()
			if !hadDelta {
				sink.Emit(Event{Type: EventAssistantMessage, Text: block.Text})
			}
		}
		if messageType == "assistant" && block.Type == "tool_use" {
			p.emitClaudeToolStart(block.ID, block.Name, sink)
		}
		if messageType == "user" && block.Type == "tool_result" {
			status := "completed"
			if block.IsError {
				status = "failed"
			}
			sink.Emit(Event{Type: EventToolCompleted, ToolID: block.ToolUseID, Status: status})
		}
	}
}

func (p *ClaudeProvider) emitClaudeToolStart(id, name string, sink EventSink) {
	p.activeMu.Lock()
	if p.seenTools == nil {
		p.seenTools = map[string]struct{}{}
	}
	if _, exists := p.seenTools[id]; exists && id != "" {
		p.activeMu.Unlock()
		return
	}
	p.seenTools[id] = struct{}{}
	p.activeMu.Unlock()
	sink.Emit(Event{Type: EventToolStarted, ToolID: id, ToolName: name, Status: "running"})
}

func (p *ClaudeProvider) persistSessionID() error {
	p.sessionMu.Lock()
	sessionID := p.sessionID
	p.sessionMu.Unlock()
	if sessionID != "" {
		return writeClaudeSessionID(p.config.StateDir, sessionID)
	}
	return nil
}

func writeClaudeSessionID(stateDir, sessionID string) error {
	return os.WriteFile(filepath.Join(stateDir, "claude-session-id"), []byte(sessionID+"\n"), 0600)
}

// Done closes when the Claude Code process can no longer serve turns.
func (p *ClaudeProvider) Done() <-chan struct{} {
	return p.done
}

// Close stops the Claude Code process.
func (p *ClaudeProvider) Close() error {
	p.cancel()
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}
	if err := p.cmd.Wait(); err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			return err
		}
	}
	return nil
}
