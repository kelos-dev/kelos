package logging

import (
	"flag"
	"fmt"

	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	flagNameVerbosity   = "verbosity"
	flagNameZapLogLevel = "zap-log-level"
)

// SetupZapOptions configures zap.Options with a --verbosity flag.
// It registers --verbosity on the given FlagSet, binds the standard zap flags,
// and returns a function that must be called after flag.Parse() to validate
// flag combinations and apply the verbosity level.
//
// The returned apply function sets the zap log level based on verbosity:
//
//	0 → info and error only (default)
//	1 → V(1) messages and above
//	2 → V(2) messages and above, etc.
//
// If both --verbosity and --zap-log-level are explicitly provided, the apply
// function returns an error.
func SetupZapOptions(fs *flag.FlagSet) (opts *zap.Options, apply func() error) {
	var verbosity int
	fs.IntVar(&verbosity, flagNameVerbosity, 0, "Logging verbosity level. 0 = info/error only, higher values enable more debug output. Mutually exclusive with --zap-log-level.")

	opts = &zap.Options{}
	opts.BindFlags(fs)

	apply = func() error {
		verbositySet := false
		zapLogLevelSet := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == flagNameVerbosity {
				verbositySet = true
			}
			if f.Name == flagNameZapLogLevel {
				zapLogLevelSet = true
			}
		})

		if verbositySet && zapLogLevelSet {
			return fmt.Errorf("--%s and --%s are mutually exclusive", flagNameVerbosity, flagNameZapLogLevel)
		}

		// When --zap-log-level is explicitly set, defer entirely to zap.
		if zapLogLevelSet {
			return nil
		}

		if verbosity < 0 {
			return fmt.Errorf("--%s must be >= 0, got %d", flagNameVerbosity, verbosity)
		}

		// Apply the verbosity level.
		// zapcore levels are negative for debug: Level(-N) enables V(N).
		opts.Level = zapcore.Level(-verbosity)
		return nil
	}
	return opts, apply
}
