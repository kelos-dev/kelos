package logging

import (
	"flag"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestSetupZapOptions_DefaultBehavior(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	opts, apply := SetupZapOptions(fs)

	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Default verbosity is 0 → info level (zapcore.Level 0).
	level, ok := opts.Level.(zapcore.Level)
	if !ok {
		t.Fatalf("expected zapcore.Level, got %T", opts.Level)
	}
	if level != zapcore.InfoLevel {
		t.Errorf("expected InfoLevel (0), got %v", level)
	}
}

func TestSetupZapOptions_HigherVerbosity(t *testing.T) {
	tests := []struct {
		verbosity string
		wantLevel zapcore.Level
	}{
		{"1", zapcore.Level(-1)},
		{"2", zapcore.Level(-2)},
		{"5", zapcore.Level(-5)},
	}

	for _, tt := range tests {
		t.Run("verbosity="+tt.verbosity, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			opts, apply := SetupZapOptions(fs)

			if err := fs.Parse([]string{"--verbosity", tt.verbosity}); err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := apply(); err != nil {
				t.Fatalf("apply: %v", err)
			}

			level, ok := opts.Level.(zapcore.Level)
			if !ok {
				t.Fatalf("expected zapcore.Level, got %T", opts.Level)
			}
			if level != tt.wantLevel {
				t.Errorf("expected level %v, got %v", tt.wantLevel, level)
			}
		})
	}
}

func TestSetupZapOptions_RawZapCompatibility(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	opts, apply := SetupZapOptions(fs)

	if err := fs.Parse([]string{"--zap-log-level", "debug"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// When --zap-log-level is used alone, apply must succeed and the zap
	// binding sets the level to debug internally. Verify the level was
	// preserved from the zap flag (not overwritten by verbosity default).
	atomicLevel, ok := opts.Level.(zap.AtomicLevel)
	if !ok {
		t.Fatalf("expected zap.AtomicLevel, got %T", opts.Level)
	}
	if atomicLevel.Level() != zapcore.DebugLevel {
		t.Errorf("expected DebugLevel, got %v", atomicLevel.Level())
	}
}

func TestSetupZapOptions_MutuallyExclusiveFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_, apply := SetupZapOptions(fs)

	if err := fs.Parse([]string{"--verbosity", "1", "--zap-log-level", "debug"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	err := apply()
	if err == nil {
		t.Fatal("expected error when both --verbosity and --zap-log-level are set")
	}
	if err.Error() != "--verbosity and --zap-log-level are mutually exclusive" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestSetupZapOptions_NegativeVerbosity(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_, apply := SetupZapOptions(fs)

	if err := fs.Parse([]string{"--verbosity", "-1"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	err := apply()
	if err == nil {
		t.Fatal("expected error for negative verbosity")
	}
}
