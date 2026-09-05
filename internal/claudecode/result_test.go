package claudecode

import (
	"strings"
	"testing"
)

// garbledText is the final message from a real degenerate run: the agent did
// substantial work and then emitted markup fragments instead of an answer.
const garbledText = "`stale=False</li>\n</ul>\n</section\n</section>"

func TestResultStatus(t *testing.T) {
	tests := []struct {
		name        string
		result      Result
		wantStatus  ResultStatus
		wantDetails string
	}{
		{
			name:       "normal completion",
			result:     Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed"},
			wantStatus: ResultCompleted,
		},
		{
			name:       "older result without reasons",
			result:     Result{Subtype: "success"},
			wantStatus: ResultCompleted,
		},
		{
			name:        "pending tool use",
			result:      Result{Subtype: "success", StopReason: "tool_use"},
			wantStatus:  ResultIncomplete,
			wantDetails: "stop_reason=tool_use",
		},
		{
			name:        "turn limit",
			result:      Result{Subtype: "success", StopReason: "tool_use", TerminalReason: "max_turns"},
			wantStatus:  ResultIncomplete,
			wantDetails: "terminal_reason=max_turns, stop_reason=tool_use",
		},
		{
			name:        "output token limit",
			result:      Result{Subtype: "success", StopReason: "max_tokens", TerminalReason: "completed"},
			wantStatus:  ResultIncomplete,
			wantDetails: "stop_reason=max_tokens",
		},
		{
			name:        "error subtype",
			result:      Result{Subtype: "error_max_turns", IsError: true, StopReason: "tool_use", TerminalReason: "max_turns"},
			wantStatus:  ResultError,
			wantDetails: "subtype=error_max_turns, is_error=true",
		},
		{
			name:        "degenerate output after many turns",
			result:      Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: garbledText, NumTurns: 41},
			wantStatus:  ResultIncomplete,
			wantDetails: "degenerate_output=turns=41,chars=44",
		},
		{
			name:        "degenerate output at the turn floor",
			result:      Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: "</section>", NumTurns: degenerateTurnFloor},
			wantStatus:  ResultIncomplete,
			wantDetails: "degenerate_output=turns=10,chars=10",
		},
		{
			name:        "whitespace-only final message after many turns",
			result:      Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: "   \n  ", NumTurns: 41},
			wantStatus:  ResultIncomplete,
			wantDetails: "degenerate_output=turns=41,chars=0",
		},
		{
			name:       "brief answer after a couple of turns",
			result:     Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: "Fixed in v0.472.", NumTurns: 2},
			wantStatus: ResultCompleted,
		},
		{
			name:       "long answer after many turns",
			result:     Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: strings.Repeat("a", 800), NumTurns: 41},
			wantStatus: ResultCompleted,
		},
		{
			name:       "short answer just above the char floor after many turns",
			result:     Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: strings.Repeat("a", degenerateCharFloor), NumTurns: 41},
			wantStatus: ResultCompleted,
		},
		{
			name:       "num_turns absent on older claude code",
			result:     Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: garbledText},
			wantStatus: ResultCompleted,
		},
		{
			// The floor counts characters, not bytes: a multi-byte
			// answer of the same visible length is treated the same
			// as an ASCII one.
			name:        "short multi-byte final message after many turns",
			result:      Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: strings.Repeat("\u6f22", 80), NumTurns: 41},
			wantStatus:  ResultIncomplete,
			wantDetails: "degenerate_output=turns=41,chars=80",
		},
		{
			name:       "long multi-byte answer after many turns",
			result:     Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: strings.Repeat("\u6f22", degenerateCharFloor), NumTurns: 41},
			wantStatus: ResultCompleted,
		},
		{
			// An explicit error is a different failure, so the
			// degenerate token must not appear in its details even
			// though the short message trips the length floor.
			name:        "error result with a short message after many turns",
			result:      Result{Subtype: "error_max_turns", IsError: true, Text: "Reached max turns", NumTurns: 41},
			wantStatus:  ResultError,
			wantDetails: "subtype=error_max_turns, is_error=true",
		},
		{
			name:        "degenerate output alongside an explicit stop reason",
			result:      Result{Subtype: "success", StopReason: "max_tokens", Text: garbledText, NumTurns: 41},
			wantStatus:  ResultIncomplete,
			wantDetails: "stop_reason=max_tokens, degenerate_output=turns=41,chars=44",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Status(); got != tt.wantStatus {
				t.Fatalf("Status() = %q, want %q", got, tt.wantStatus)
			}
			if got := tt.result.Details(); got != tt.wantDetails {
				t.Fatalf("Details() = %q, want %q", got, tt.wantDetails)
			}
			if tt.wantStatus == ResultCompleted && tt.result.FailureMessage() != "" {
				t.Fatalf("FailureMessage() = %q, want empty", tt.result.FailureMessage())
			}
			if tt.wantStatus != ResultCompleted && tt.result.FailureMessage() == "" {
				t.Fatal("FailureMessage() is empty for non-completed result")
			}
		})
	}
}

// TestDegenerateFailureMessage pins the operator-facing diagnostic, which is
// what reaches Slack when both attempts of a Task are classified degenerate.
func TestDegenerateFailureMessage(t *testing.T) {
	r := Result{Subtype: "success", StopReason: "end_turn", TerminalReason: "completed", Text: garbledText, NumTurns: 41}
	want := "Claude Code returned a degenerate final message (degenerate_output=turns=41,chars=44)"
	if got := r.FailureMessage(); got != want {
		t.Fatalf("FailureMessage() = %q, want %q", got, want)
	}
	if !r.IsDegenerateOutput() {
		t.Fatal("IsDegenerateOutput() = false, want true")
	}
}
