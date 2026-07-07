// manual_outcome.go carries the outcome of a STANDALONE backup (run by hand or the dashboard
// "run now", NOT the daemon's supervised child) to the resident DAEMON, which is the SOLE
// pinger. A standalone run never builds a Reporter and never writes the status file; it drops
// this per-run handoff and wakes the daemon with SIGUSR1, and the daemon pings + records the
// backup-outcome check through its own finish path. If no daemon is running, nothing pings
// (coherent: without the daemon, healthchecks transmits nothing anyway). It is a sibling of the
// status/notify-results files in the identity dir, written with the same atomic, non-immutable
// idiom (writeJSONAtomic), and stays logging-free + stdlib-only like its siblings.

package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ManualOutcome is the handoff a standalone backup writes for the daemon to ping. RID is a fresh
// run id for the /start-less finish ping (a standalone run has no daemon-assigned rid); TS is the
// unix SECONDS at handoff, which the daemon's staleness guard uses so a wake that arrives long
// after the run never flips the check; ExitCode is the run's process exit code, mapped to the
// /<code> backup-outcome ping.
type ManualOutcome struct {
	RID      string `json:"rid"`
	TS       int64  `json:"ts"`
	ExitCode int    `json:"exit_code"`
}

// ManualOutcomePath returns the per-run manual-outcome file path, a sibling of the status file in
// the identity dir (same same-uid, non-immutable rationale as StatusPath).
func ManualOutcomePath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".manual_backup_outcome.json")
}

// WriteManualOutcome writes the standalone-run outcome atomically (run side).
func WriteManualOutcome(baseDir, rid string, ts int64, exitCode int) error {
	return writeJSONAtomic(ManualOutcomePath(baseDir), ManualOutcome{RID: rid, TS: ts, ExitCode: exitCode})
}

// LoadManualOutcome reads the manual outcome tolerantly (daemon side): a missing OR empty file
// yields the zero value with a nil error (RID=="" then means "nothing handed off"). Only
// malformed JSON is an error, and even then the returned value is the zero value so a caller that
// ignores the error still sees a safe "nothing to do" state.
func LoadManualOutcome(baseDir string) (ManualOutcome, error) {
	var mo ManualOutcome
	data, err := os.ReadFile(ManualOutcomePath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return ManualOutcome{}, nil
		}
		return ManualOutcome{}, fmt.Errorf("read manual outcome: %w", err)
	}
	if len(data) == 0 {
		return ManualOutcome{}, nil
	}
	if err := json.Unmarshal(data, &mo); err != nil {
		return ManualOutcome{}, fmt.Errorf("parse manual outcome: %w", err)
	}
	return mo, nil
}

// RemoveManualOutcome deletes the handoff file (daemon side, after processing). A missing file is
// not an error: processed-once is idempotent, and it also guards a duplicate wake signal.
func RemoveManualOutcome(baseDir string) error {
	if err := os.Remove(ManualOutcomePath(baseDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove manual outcome: %w", err)
	}
	return nil
}
