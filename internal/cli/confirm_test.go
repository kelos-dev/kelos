package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirmOverride_AcceptsYes(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"Yes\n", true},
		{"n\n", false},
		{"no\n", false},
		{"N\n", false},
		{"\n", false},
		{"something\n", false},
		// Piped input without trailing newline (EOF).
		{"y", true},
		{"yes", true},
		{"n", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			old := stdinReader
			stdinReader = strings.NewReader(tt.input)
			defer func() { stdinReader = old }()

			got, err := confirmOverride("secret/test")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("confirmOverride() = %v, want %v for input %q", got, tt.want, tt.input)
			}
		})
	}
}

func TestConfirmOverride_EmptyInput(t *testing.T) {
	old := stdinReader
	stdinReader = bytes.NewReader(nil)
	defer func() { stdinReader = old }()

	got, err := confirmOverride("workspace/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false for empty input")
	}
}

func TestConfirmOverride_IncludesResourceName(t *testing.T) {
	old := stdinReader
	stdinReader = strings.NewReader("n\n")
	defer func() { stdinReader = old }()

	// This just verifies the function runs without error with a resource name.
	_, err := confirmOverride("secret/kelos-credentials")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
