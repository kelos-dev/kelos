package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNoActiveTurn means the provider has no turn that can be interrupted.
	ErrNoActiveTurn = errors.New("Session has no active turn")
	// ErrTurnInterrupted means a client stopped the active provider turn.
	ErrTurnInterrupted = errors.New("Session turn was interrupted")
	// ErrInputCancelled means a client cancelled a provider input request.
	ErrInputCancelled = errors.New("Session input request was cancelled")
)

// ProviderConfig contains runtime configuration injected by the Session Pod.
type ProviderConfig struct {
	AgentType   string
	WorkingDir  string
	StateDir    string
	Model       string
	Effort      string
	PluginDir   string
	Environment []string
	EventSink   EventSink
}

// Provider owns a conversation whose events can arrive independently of prompts.
type Provider interface {
	// SetEventSink attaches the consumer for submitted and autonomous turns.
	SetEventSink(EventSink)
	// StartTurn submits a prompt. Turn completion is delivered to the event sink.
	StartTurn(ctx context.Context, input TurnInput) error
	Interrupt(ctx context.Context) error
	Done() <-chan struct{}
	Close() error
}

// providerStopError reads terminal diagnostics after the provider's Done closes.
func providerStopError(provider Provider) error {
	if source, ok := provider.(interface{ providerError() error }); ok {
		if err := source.providerError(); err != nil {
			return fmt.Errorf("Session provider stopped: %w", err)
		}
	}
	return errors.New("Session provider stopped")
}

type shellCommandRecord struct {
	command  string
	exitCode int
	duration time.Duration
	output   string
}

type shellCommandContextProvider interface {
	recordShellCommand(context.Context, shellCommandRecord) error
}

var (
	_ shellCommandContextProvider = (*ClaudeProvider)(nil)
	_ shellCommandContextProvider = (*CodexProvider)(nil)
	_ shellCommandContextProvider = (*OpenCodeProvider)(nil)
)

func formatShellCommandRecord(record shellCommandRecord) string {
	return fmt.Sprintf(
		"<user_shell_command>\n<command>\n%s\n</command>\n<result>\nExit code: %d\nDuration: %.4f seconds\nOutput:\n%s\n</result>\n</user_shell_command>",
		record.command,
		record.exitCode,
		record.duration.Seconds(),
		record.output,
	)
}

type goalProvider interface {
	StartGoal(context.Context, goalCommand) error
}

type goalControlProvider interface {
	ControlGoal(context.Context, goalCommand, EventSink) error
	ActiveGoal() *Goal
}

// NewProvider creates the configured conversation adapter.
func NewProvider(ctx context.Context, config ProviderConfig) (Provider, error) {
	switch config.AgentType {
	case "claude-code":
		return NewClaudeProvider(ctx, config)
	case "codex":
		return NewCodexProvider(ctx, config)
	case "opencode":
		return NewOpenCodeProvider(ctx, config)
	default:
		return nil, fmt.Errorf("unsupported Session agent type %q", config.AgentType)
	}
}
