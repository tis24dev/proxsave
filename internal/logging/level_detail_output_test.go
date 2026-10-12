package logging_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestStructuredErrorDetailPreservesRenderedLevelAndCounts(t *testing.T) {
	const timestamp = "2026-10-12 12:00:00"
	const cause = "permission denied"
	for _, level := range []types.LogLevel{types.LogLevelInfo, types.LogLevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			line := logging.FormatConsoleLogLine(timestamp, level, fmt.Sprintf("    Error: %s", cause), false)
			want := fmt.Sprintf("[%s] %-8s     Error: %s\n", timestamp, level.String(), cause)
			if line != want {
				t.Fatalf("rendered detail = %q, want %q", line, want)
			}
			path := filepath.Join(t.TempDir(), "detail.log")
			if err := os.WriteFile(path, []byte(line), 0600); err != nil {
				t.Fatal(err)
			}
			categories, errors, warnings, notifications := orchestrator.ParseLogCounts(path, 10)
			if errors != 0 || warnings != 0 || notifications != 0 || len(categories) != 0 {
				t.Fatalf("detail counted as issue: categories=%v errors=%d warnings=%d notifications=%d", categories, errors, warnings, notifications)
			}
			// A real ERROR alongside the detail must still be counted once.
			control := logging.FormatConsoleLogLine(timestamp, types.LogLevelError, "backup failed", false)
			if err := os.WriteFile(path, []byte(strings.Join([]string{line, control}, "")), 0600); err != nil {
				t.Fatal(err)
			}
			_, errors, warnings, notifications = orchestrator.ParseLogCounts(path, 10)
			if errors != 1 || warnings != 0 || notifications != 0 {
				t.Fatalf("control counts = (%d, %d, %d), want (1, 0, 0)", errors, warnings, notifications)
			}
		})
	}
}
