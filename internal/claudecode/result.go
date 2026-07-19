package claudecode

import (
	"fmt"
	"strings"
)

// Result describes the fields Claude Code uses to report how an agent loop
// terminated.
type Result struct {
	Subtype        string
	IsError        bool
	StopReason     string
	TerminalReason string
}

// ResultStatus describes whether a Claude Code result represents normal
// completion, an explicit error, or an agent loop that stopped before
// completing.
type ResultStatus string

const (
	ResultCompleted  ResultStatus = "completed"
	ResultError      ResultStatus = "error"
	ResultIncomplete ResultStatus = "incomplete"
)

// Status classifies a Claude Code result. Empty reason fields are accepted for
// compatibility with older Claude Code versions.
func (r Result) Status() ResultStatus {
	if r.IsError || r.Subtype != "success" {
		return ResultError
	}
	if r.TerminalReason != "" && r.TerminalReason != "completed" {
		return ResultIncomplete
	}
	if r.StopReason != "" && r.StopReason != "end_turn" {
		return ResultIncomplete
	}
	return ResultCompleted
}

// Details returns the result fields that explain a non-completed status.
func (r Result) Details() string {
	var details []string
	if r.Status() == ResultError {
		if r.Subtype != "" {
			details = append(details, "subtype="+r.Subtype)
		}
		if r.IsError {
			details = append(details, "is_error=true")
		}
		return strings.Join(details, ", ")
	}
	if r.TerminalReason != "" && r.TerminalReason != "completed" {
		details = append(details, "terminal_reason="+r.TerminalReason)
	}
	if r.StopReason != "" && r.StopReason != "end_turn" {
		details = append(details, "stop_reason="+r.StopReason)
	}
	return strings.Join(details, ", ")
}

// FailureMessage returns a diagnostic for a non-completed result.
func (r Result) FailureMessage() string {
	switch r.Status() {
	case ResultError:
		if details := r.Details(); details != "" {
			return fmt.Sprintf("Claude Code returned an unsuccessful result (%s)", details)
		}
		return "Claude Code returned an unsuccessful result"
	case ResultIncomplete:
		return fmt.Sprintf("Claude Code run incomplete (%s)", r.Details())
	default:
		return ""
	}
}
