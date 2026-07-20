package main

import (
	"io"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// newWarnLogger returns a logger that has already logged a Warning (HasWarnings
// true) or a clean one, writing to io.Discard so nothing hits the console.
func newWarnLogger(t *testing.T, withWarning bool) *logging.Logger {
	t.Helper()
	l := logging.New(types.LogLevelInfo, false)
	l.SetOutput(io.Discard)
	if withWarning {
		l.Warning("provoke a warning")
		if !l.HasWarnings() {
			t.Fatalf("expected logger to report warnings after Warning()")
		}
	}
	return l
}

// TestExitCodeSeverity pins every arm of the shared classifier, including the
// HasWarnings flip on a clean exit 0.
func TestExitCodeSeverity(t *testing.T) {
	clean := newWarnLogger(t, false)
	warned := newWarnLogger(t, true)

	cases := []struct {
		name     string
		exitCode int
		logger   *logging.Logger
		want     exitSeverity
	}{
		{"interrupted", exitCodeInterrupted, clean, severityInterrupted},
		{"interrupted-nil-logger", exitCodeInterrupted, nil, severityInterrupted},
		{"clean-success", 0, clean, severityOK},
		{"clean-success-nil-logger", 0, nil, severityOK},
		{"success-with-warnings", 0, warned, severityWarning},
		{"generic-error-non-fatal", types.ExitGenericError.Int(), clean, severityWarning},
		{"generic-error-with-warnings", types.ExitGenericError.Int(), warned, severityWarning},
		{"config-error", types.ExitConfigError.Int(), clean, severityError},
		{"config-error-with-warnings", types.ExitConfigError.Int(), warned, severityError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeSeverity(tc.exitCode, tc.logger); got != tc.want {
				t.Fatalf("exitCodeSeverity(%d) = %d, want %d", tc.exitCode, got, tc.want)
			}
		})
	}
}

// TestFinalSummaryColorByteIdentity pins the exact ANSI codes the footer has
// always emitted per severity, proving the switch refactor is byte-identical.
func TestFinalSummaryColorByteIdentity(t *testing.T) {
	clean := newWarnLogger(t, false)
	warned := newWarnLogger(t, true)

	cases := []struct {
		name     string
		exitCode int
		logger   *logging.Logger
		want     string
	}{
		{"interrupted-magenta", exitCodeInterrupted, clean, "\033[35m"},
		{"success-with-warnings-yellow", 0, warned, "\033[33m"},
		{"clean-success-green", 0, clean, "\033[32m"},
		{"generic-error-yellow", types.ExitGenericError.Int(), clean, "\033[33m"},
		{"other-error-red", types.ExitConfigError.Int(), clean, "\033[31m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := finalSummaryColor(tc.exitCode, tc.logger); got != tc.want {
				t.Fatalf("finalSummaryColor(%d) = %q, want %q", tc.exitCode, got, tc.want)
			}
		})
	}
}
