package orchestrator

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Regression for apply-failure-no-rollback (2026-06-09 audit): when
// applyNetworkFilesFromStage failed partway (e.g. /etc/network copied but
// writeFileAtomic of /etc/hostname failed), maybeInstallNetworkConfigFromStage
// returned the error and NEVER rolled back, leaving /etc half-written even though
// the rollback backup was already validated as available. Written after routing the
// apply-failure path through rollbackAfterFailedStagedNetworkApply.
func TestRollbackAfterFailedStagedNetworkApply_RollsBackAndReturnsOriginalError(t *testing.T) {
	origFS, origCmd, origTime := restoreFS, restoreCmd, restoreTime
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreTime = origFS, origCmd, origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	cmd := &FakeCommandRunner{}
	restoreCmd = cmd
	restoreTime = &FakeTime{Current: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}

	applyErr := errors.New("write /etc/hostname failed")
	got := rollbackAfterFailedStagedNetworkApply(context.Background(), newTestLogger(), applyErr, "/backup.tar")

	if !errors.Is(got, applyErr) {
		t.Fatalf("returned error = %v, want the original apply error", got)
	}

	// The rollback must have been attempted: the script is executed via `sh`.
	ranSh := false
	for _, c := range cmd.Calls {
		if strings.HasPrefix(c, "sh") {
			ranSh = true
			break
		}
	}
	if !ranSh {
		t.Fatalf("expected the rollback script to be invoked via sh; calls=%v", cmd.Calls)
	}
}
