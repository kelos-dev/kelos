package claudecode

import "testing"

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
