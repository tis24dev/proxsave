package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The closing line reads the logger's warning count, the one the CLI wrapper and the
// footer read, as well as the workflow flag: a warning logged outside the flagged steps
// used to leave "Restore completed successfully." above the wrapper's "completed with
// warnings".
func TestRestoreCompletionVerdictReadsTheLogger(t *testing.T) {
	cases := []struct {
		name        string
		flagged     bool
		warnLogged  bool
		wantVerdict string
	}{
		{"warning outside the flagged steps", false, true, "Restore completed with warnings."},
		{"flagged step", true, false, "Restore completed with warnings."},
		{"no warning", false, false, "Restore completed successfully."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(&buf)
			if tc.warnLogged {
				logger.Warning("Skipping /etc/sudoers restore: staged sudoers failed validation (visudo -c): exit status 1")
			}
			w := &restoreUIWorkflowRun{logger: logger, restoreHadWarnings: tc.flagged}
			w.logRestoreCompletion()

			log := buf.String()
			if !strings.Contains(log, tc.wantVerdict) {
				t.Fatalf("log misses %q:\n%s", tc.wantVerdict, log)
			}
			if strings.Count(log, "Restore completed") != 1 {
				t.Fatalf("want exactly one verdict line:\n%s", log)
			}
		})
	}
}
