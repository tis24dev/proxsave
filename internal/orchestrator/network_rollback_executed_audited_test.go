package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Regression for the rollbackAlreadyExecuted classification fix (code review
// 2026-06-09): the function returned true on ANY stat error of the marker file, so a
// transient/permission stat failure was misread as "rollback completed", which can
// wrongly classify a valid COMMIT as too-late. Only a not-exist result means the
// marker is gone; any other error is inconclusive and must read as NOT executed.
func TestRollbackAlreadyExecuted_OnlyMissingMarkerCountsAsExecuted(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	logger := newDiscardLogger()
	dir := t.TempDir()
	marker := filepath.Join(dir, "network_rollback_pending")
	handle := &networkRollbackHandle{markerPath: marker}

	// Case 1: marker present -> the rollback has NOT completed.
	restoreFS = osFS{}
	if err := os.WriteFile(marker, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rollbackAlreadyExecuted(logger, handle) {
		t.Errorf("a present marker must read as NOT executed")
	}

	// Case 2: marker missing (IsNotExist) -> the rollback completed.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if !rollbackAlreadyExecuted(logger, handle) {
		t.Errorf("a missing marker (IsNotExist) must read as executed")
	}

	// Case 3: a non-IsNotExist stat error (permission/I/O) -> inconclusive -> NOT
	// executed (was wrongly true before the fix).
	restoreFS = &FakeFS{StatErr: map[string]error{filepath.Clean(marker): errors.New("permission denied")}}
	if rollbackAlreadyExecuted(logger, handle) {
		t.Errorf("a transient/permission stat error must NOT be read as executed")
	}
}
