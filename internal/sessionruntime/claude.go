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

	"github.com/kelos-dev/kelos/internal/claudecode"
	"k8s.io/apimachinery/pkg/util/uuid"
)

type claudeTurnResult struct {
	error string
}

type claudeEventEnvelope struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	ParentToolUseID string `json:"parent_tool_use_id"`
	OwnedBySubagent bool   `json:"owned_by_subagent"`
	SkipTranscript  bool   `json:"skip_transcript"`
	Ambient         bool   `json:"ambient"`
	TaskID          string `json:"task_id"`
	Patch           struct {
		Status string `json:"status"`
	} `json:"patch"`
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

	shellContextMu       sync.Mutex
	pendingShellCommands []shellCommandRecord

	controlMu      sync.Mutex
	controlPending map[string]chan error
	nextControlID  atomic.Int64

	activeMu          sync.Mutex
	eventSink         EventSink
	active            bool
	interactionCtx    context.Context
	interactionCancel context.CancelFunc
	seenTools         map[string]string
	hadTextDelta      bool
	blockText         map[int]*strings.Builder
	interrupted       bool
	tasks             map[string]struct{}

	statusMu sync.Mutex
	model    string
	usage    *RuntimeUsage
	// countedMessages holds assistant message IDs whose usage was already added
	// to the totals; Claude Code repeats a message's usage on every content-block
	// event for that message.
	countedMessages map[string]struct{}

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
		eventSink:      config.EventSink,
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

// SetEventSink installs the session's event consumer before submitting prompts.
func (p *ClaudeProvider) SetEventSink(sink EventSink) {
	p.activeMu.Lock()
	p.eventSink = sink
	p.activeMu.Unlock()
}

func (p *ClaudeProvider) beginTurn(ctx context.Context) {
	p.activeMu.Lock()
	if p.active {
		p.activeMu.Unlock()
		return
	}
	p.active = true
	p.interactionCtx, p.interactionCancel = context.WithCancel(ctx)
	p.seenTools = map[string]string{}
	p.hadTextDelta = false
	p.blockText = map[int]*strings.Builder{}
	p.interrupted = false
	sink := p.eventSink
	p.activeMu.Unlock()
	if sink != nil {
		sink.Emit(Event{Type: EventTurnStarted, Status: "running"})
		p.emitRuntimeStatus(sink)
	}
}

// StartTurn submits a prompt; replies and completion arrive through the session sink.
func (p *ClaudeProvider) StartTurn(ctx context.Context, input TurnInput) error {
	return p.startMessage(ctx, input, true)
}

const claudeGoalUsage = "Claude Code supports /goal [<condition>|clear]; use /interrupt to stop active work"

// StartGoal submits Claude Code's native goal command through its input stream.
func (p *ClaudeProvider) StartGoal(ctx context.Context, command goalCommand) error {
	text := "/goal"
	switch command.action {
	case goalCommandShow:
	case goalCommandCreate:
		text += " " + command.objective
	case goalCommandClear:
		text += " clear"
	default:
		return errors.New(claudeGoalUsage)
	}
	// Native commands must be the first input block. Shell results stay queued
	// for the next ordinary prompt so they cannot hide the command prefix.
	return p.startMessage(ctx, TurnInput{Text: text}, false)
}

func (p *ClaudeProvider) startMessage(ctx context.Context, input TurnInput, includeShellContext bool) error {
	p.beginTurn(ctx)

	p.sessionMu.Lock()
	sessionID := p.sessionID
	p.sessionMu.Unlock()
	p.shellContextMu.Lock()
	content := make([]map[string]string, 0, len(p.pendingShellCommands)+1)
	if includeShellContext {
		for _, record := range p.pendingShellCommands {
			content = append(content, map[string]string{
				"type": "text",
				"text": formatShellCommandRecord(record),
			})
		}
	}
	content = append(content, map[string]string{
		"type": "text",
		"text": attachmentPrompt(input),
	})
	message := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
		"parent_tool_use_id": nil,
		"session_id":         sessionID,
	}
	if err := p.write(message); err != nil {
		p.shellContextMu.Unlock()
		p.activeMu.Lock()
		p.active = false
		if p.interactionCancel != nil {
			p.interactionCancel()
		}
		p.interactionCtx, p.interactionCancel = nil, nil
		p.activeMu.Unlock()
		return err
	}
	if includeShellContext {
		p.pendingShellCommands = nil
	}
	p.shellContextMu.Unlock()

	return nil
}

func (p *ClaudeProvider) recordShellCommand(_ context.Context, record shellCommandRecord) error {
	p.shellContextMu.Lock()
	defer p.shellContextMu.Unlock()
	p.pendingShellCommands = append(p.pendingShellCommands, record)
	return nil
}

// Interrupt stops the active Claude Code turn without closing its session.
func (p *ClaudeProvider) Interrupt(ctx context.Context) error {
	p.activeMu.Lock()
	if !p.active {
		p.activeMu.Unlock()
		return ErrNoActiveTurn
	}
	p.interrupted = true
	p.activeMu.Unlock()
	if err := p.sendControlRequest(ctx, map[string]any{"subtype": "interrupt"}); err != nil {
		p.activeMu.Lock()
		if p.active {
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
		var event claudeEventEnvelope
		if err := json.Unmarshal(line, &event); err != nil {
			p.readErr = fmt.Errorf("decoding Claude Code event: %w", err)
			return
		}
		if event.Type == "control_request" {
			go p.handleControlRequest(line)
			continue
		}
		if event.Type == "control_response" {
			p.handleControlResponse(line)
			continue
		}

		subagent := event.ParentToolUseID != "" || event.OwnedBySubagent
		p.activeMu.Lock()
		active := p.active
		p.activeMu.Unlock()
		if event.SkipTranscript || event.Ambient || (subagent && (!active || event.Type == "result")) {
			continue
		}
		if !subagent {
			if event.Type == "assistant" || event.Type == "stream_event" || (event.Type == "system" && event.Subtype == "task_notification") {
				p.beginTurn(p.ctx)
			}
			p.recordTaskActivity(event)
		}
		p.activeMu.Lock()
		sink := p.eventSink
		p.activeMu.Unlock()
		result, err := p.handleClaudeLine(line, sink)
		if err != nil {
			p.readErr = err
			return
		}
		if result != nil {
			if err := p.persistSessionID(); err != nil {
				p.readErr = fmt.Errorf("saving Claude Code session ID: %w", err)
				return
			}
			p.activeMu.Lock()
			active, interrupted := p.active, p.interrupted
			p.active = false
			if p.interactionCancel != nil {
				p.interactionCancel()
			}
			p.interactionCtx, p.interactionCancel = nil, nil
			p.activeMu.Unlock()
			if active && sink != nil {
				status := "completed"
				if interrupted {
					status = "interrupted"
					result.error = ""
				} else if result.error != "" {
					status = "failed"
				}
				sink.Emit(Event{Type: EventTurnCompleted, Status: status, Text: result.error})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		p.readErr = fmt.Errorf("reading Claude Code output: %w", err)
	}
}

func (p *ClaudeProvider) recordTaskActivity(event claudeEventEnvelope) {
	if event.Type != "system" || event.TaskID == "" {
		return
	}
	p.activeMu.Lock()
	before := len(p.tasks)
	switch event.Subtype {
	case "task_started":
		if p.tasks == nil {
			p.tasks = map[string]struct{}{}
		}
		p.tasks[event.TaskID] = struct{}{}
	case "task_notification":
		delete(p.tasks, event.TaskID)
	case "task_updated":
		switch event.Patch.Status {
		case "completed", "failed", "killed":
			delete(p.tasks, event.TaskID)
		}
	}
	after, sink := len(p.tasks), p.eventSink
	p.activeMu.Unlock()
	if sink != nil && before != after {
		sink.Emit(Event{Type: EventRuntimeStatus, Runtime: &RuntimeStatus{BackgroundTasks: &after}})
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
		Type           string                      `json:"type"`
		Subtype        string                      `json:"subtype"`
		IsError        bool                        `json:"is_error"`
		Result         string                      `json:"result"`
		StopReason     string                      `json:"stop_reason"`
		TerminalReason string                      `json:"terminal_reason"`
		SessionID      string                      `json:"session_id"`
		Model          string                      `json:"model"`
		Usage          *claudeUsage                `json:"usage"`
		ModelUsage     map[string]claudeModelUsage `json:"modelUsage"`
		Event          json.RawMessage             `json:"event"`
		Message        json.RawMessage             `json:"message"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil, fmt.Errorf("decoding Claude Code event: %w", err)
	}
	if envelope.SessionID != "" {
		p.sessionMu.Lock()
		p.sessionID = envelope.SessionID
		p.sessionMu.Unlock()
	}
	switch envelope.Type {
	case "system":
		if envelope.Subtype == "init" {
			p.updateClaudeModel(envelope.Model, sink)
		}
	case "assistant":
		p.recordClaudeUsage(envelope.Message, sink)
	case "result":
		p.recordClaudeResultUsage(envelope.Usage, envelope.ModelUsage, sink)
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
		completion := claudecode.Result{
			Subtype:        envelope.Subtype,
			IsError:        envelope.IsError,
			StopReason:     envelope.StopReason,
			TerminalReason: envelope.TerminalReason,
		}
		status := completion.Status()
		if status != claudecode.ResultCompleted {
			if status == claudecode.ResultError {
				result.error = envelope.Result
			}
			if result.error == "" {
				result.error = completion.FailureMessage()
			}
		}
		return result, nil
	}
	return nil, nil
}

type claudeModelUsage struct {
	ContextWindow int64 `json:"contextWindow"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// updateClaudeModel records the model reported by Claude Code. The init event
// can arrive before any turn is active, so state is stored even without a sink.
func (p *ClaudeProvider) updateClaudeModel(model string, sink EventSink) {
	if model == "" {
		return
	}
	p.statusMu.Lock()
	changed := p.model != model
	p.model = model
	p.statusMu.Unlock()
	if changed && sink != nil {
		p.emitRuntimeStatus(sink)
	}
}

// recordClaudeUsage accumulates token usage from one assistant message event.
// This keeps totals moving while a turn runs; each result event then replaces
// them with Claude Code's authoritative session totals.
func (p *ClaudeProvider) recordClaudeUsage(raw json.RawMessage, sink EventSink) {
	var message struct {
		ID    string       `json:"id"`
		Model string       `json:"model"`
		Usage *claudeUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &message) != nil || message.Model == "<synthetic>" {
		return
	}
	changed := false
	p.statusMu.Lock()
	if message.Model != "" && message.Model != p.model {
		p.model = message.Model
		changed = true
	}
	if message.Usage != nil {
		input := message.Usage.InputTokens + message.Usage.CacheCreationInputTokens + message.Usage.CacheReadInputTokens
		output := message.Usage.OutputTokens
		if p.usage == nil {
			p.usage = &RuntimeUsage{}
		}
		if message.ID != "" {
			if _, counted := p.countedMessages[message.ID]; !counted {
				if p.countedMessages == nil {
					p.countedMessages = map[string]struct{}{}
				}
				p.countedMessages[message.ID] = struct{}{}
				p.usage.InputTokens += input
				p.usage.OutputTokens += output
				p.usage.TotalTokens += input + output
				changed = true
			}
		}
		if context := input + output; p.usage.ContextTokens != context {
			p.usage.ContextTokens = context
			changed = true
		}
	}
	p.statusMu.Unlock()
	if changed && sink != nil {
		p.emitRuntimeStatus(sink)
	}
}

// recordClaudeResultUsage replaces the running totals with a result event's
// usage and stores the context window from its per-model usage. Claude Code
// reports session-cumulative usage on result events, covering subagent and
// internal API calls that never appear as assistant events in the stream, so
// the result totals correct any drift in the per-message accumulation.
func (p *ClaudeProvider) recordClaudeResultUsage(usage *claudeUsage, models map[string]claudeModelUsage, sink EventSink) {
	changed := false
	p.statusMu.Lock()
	if usage != nil {
		input := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		output := usage.OutputTokens
		// Claude Code emits zeroed usage on synthetic result events; those must
		// not wipe the totals.
		if input > 0 || output > 0 {
			if p.usage == nil {
				p.usage = &RuntimeUsage{}
			}
			if p.usage.InputTokens != input || p.usage.OutputTokens != output || p.usage.TotalTokens != input+output {
				p.usage.InputTokens = input
				p.usage.OutputTokens = output
				p.usage.TotalTokens = input + output
				changed = true
			}
		}
	}
	window := models[p.model].ContextWindow
	if window == 0 {
		for _, modelUsage := range models {
			window = max(window, modelUsage.ContextWindow)
		}
	}
	if window > 0 {
		if p.usage == nil {
			p.usage = &RuntimeUsage{}
		}
		if p.usage.ContextWindow != window {
			p.usage.ContextWindow = window
			changed = true
		}
	}
	p.statusMu.Unlock()
	if changed && sink != nil {
		p.emitRuntimeStatus(sink)
	}
}

func (p *ClaudeProvider) emitRuntimeStatus(sink EventSink) {
	status := p.runtimeStatusSnapshot()
	if !status.empty() {
		sink.Emit(Event{Type: EventRuntimeStatus, Runtime: &status})
	}
}

func (p *ClaudeProvider) runtimeStatusSnapshot() RuntimeStatus {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	status := RuntimeStatus{Model: p.model, Effort: p.config.Effort}
	if p.usage != nil {
		usage := *p.usage
		status.Usage = &usage
	}
	return status
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
	sink := p.eventSink
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
		Index        int    `json:"index"`
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
			if p.blockText != nil {
				builder := p.blockText[event.Index]
				if builder == nil {
					builder = &strings.Builder{}
					p.blockText[event.Index] = builder
				}
				builder.WriteString(event.Delta.Text)
			}
			p.activeMu.Unlock()
			sink.Emit(Event{Type: EventAssistantDelta, Text: event.Delta.Text})
		}
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			p.emitClaudeToolStart(event.ContentBlock.ID, event.ContentBlock.Name, sink)
		}
	case "content_block_stop":
		// Close the streamed text bubble for this block so clients render
		// each text block separately instead of concatenating a turn's text.
		p.activeMu.Lock()
		builder := p.blockText[event.Index]
		delete(p.blockText, event.Index)
		p.activeMu.Unlock()
		if builder != nil && builder.Len() > 0 {
			sink.Emit(Event{Type: EventAssistantMessage, Text: builder.String()})
		}
	}
}

func (p *ClaudeProvider) emitClaudeMessage(messageType string, raw json.RawMessage, sink EventSink) {
	if sink == nil {
		return
	}
	var message struct {
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Text      string          `json:"text"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
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
			sink.Emit(Event{
				Type:     EventToolCompleted,
				ToolID:   block.ToolUseID,
				ToolName: p.claudeToolName(block.ToolUseID),
				Output:   claudeToolResultOutput(block.Content),
				Status:   status,
			})
		}
	}
}

func claudeToolResultOutput(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return truncateToolOutput(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	output := newBoundedToolOutput(maxToolOutputBytes)
	wroteOutput := false
	for _, block := range blocks {
		if block.Type != "text" || block.Text == "" {
			continue
		}
		if wroteOutput {
			output.WriteString("\n")
		}
		output.WriteString(block.Text)
		wroteOutput = true
	}
	return output.String()
}

func (p *ClaudeProvider) emitClaudeToolStart(id, name string, sink EventSink) {
	p.activeMu.Lock()
	if p.seenTools == nil {
		p.seenTools = map[string]string{}
	}
	if _, exists := p.seenTools[id]; exists && id != "" {
		p.activeMu.Unlock()
		return
	}
	p.seenTools[id] = name
	p.activeMu.Unlock()
	sink.Emit(Event{Type: EventToolStarted, ToolID: id, ToolName: name, Status: "running"})
}

func (p *ClaudeProvider) claudeToolName(id string) string {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	return p.seenTools[id]
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

func (p *ClaudeProvider) providerError() error {
	return p.readErr
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
