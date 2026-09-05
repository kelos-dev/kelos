package claudecode

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Degenerate-output thresholds. A run that took many turns but ended with a
// tiny final message is treated as degenerate: the agent did substantial work
// and then emitted fragments instead of an answer. The floors are deliberately
// conservative — a false positive costs at most one retry (invisible to the
// user) and never loses the response text, since the failed path still reports
// it.
const (
	degenerateTurnFloor = 10
	degenerateCharFloor = 200
)

// Result describes the fields Claude Code uses to report how an agent loop
// terminated.
type Result struct {
	Subtype        string
	IsError        bool
	StopReason     string
	TerminalReason string
	// Text is the result line's "result" field: the agent's final message.
	Text string
	// NumTurns is the result line's "num_turns" field. Older Claude Code
	// versions omit it, in which case it is zero.
	NumTurns int
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
	if r.IsDegenerateOutput() {
		return ResultIncomplete
	}
	return ResultCompleted
}

// IsDegenerateOutput reports whether the run did substantial work but ended
// with a final message too short to be a real answer. Claude Code reports such
// a run as a clean success, so content length is the only signal.
//
// The check measures the character length of the final message rather than
// usage.output_tokens, which is cumulative over the session and so stays large
// even when the final message is junk.
func (r Result) IsDegenerateOutput() bool {
	return r.NumTurns >= degenerateTurnFloor && r.textLen() < degenerateCharFloor
}

// textLen returns the number of characters in the trimmed final message.
// Runes rather than bytes, so the floor means the same thing for a non-ASCII
// answer as it does for an ASCII one.
func (r Result) textLen() int {
	return utf8.RuneCountInString(strings.TrimSpace(r.Text))
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
	if r.IsDegenerateOutput() {
		details = append(details, fmt.Sprintf("degenerate_output=turns=%d,chars=%d", r.NumTurns, r.textLen()))
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
		if r.IsDegenerateOutput() {
			return fmt.Sprintf("Claude Code returned a degenerate final message (%s)", r.Details())
		}
		return fmt.Sprintf("Claude Code run incomplete (%s)", r.Details())
	default:
		return ""
	}
}
