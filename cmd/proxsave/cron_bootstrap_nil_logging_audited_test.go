package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Regression for cron-failure-warnings-lost-when-bootstrap-nil (2026-06-09 audit):
// migrateLegacyCronEntries mixed nil-safe logBootstrap* helpers with direct
// bootstrap.Warning/Debug calls. BootstrapLogger methods are nil-receiver safe but
// silently early-return, so with a nil bootstrap the key failure warnings ("Unable
// to inspect existing cron entries", "Failed to update cron entries") were dropped
// instead of falling back to the global logger. All those calls were converted to the
// nil-safe helpers; this pins that a nil bootstrap still emits via the global logger.
func TestLogBootstrapHelpers_FallBackToGlobalLoggerWhenNil(t *testing.T) {
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })

	var buf bytes.Buffer
	custom := logging.New(types.LogLevelDebug, false)
	custom.SetOutput(&buf)
	logging.SetDefaultLogger(custom)

	logBootstrapWarning(nil, "sentinel-warning-%d", 42)
	logBootstrapInfo(nil, "sentinel-info")
	logBootstrapDebug(nil, "sentinel-debug")

	out := buf.String()
	for _, want := range []string{"sentinel-warning-42", "sentinel-info", "sentinel-debug"} {
		if !strings.Contains(out, want) {
			t.Errorf("a nil bootstrap dropped %q instead of routing to the global logger; output:\n%s", want, out)
		}
	}
}
