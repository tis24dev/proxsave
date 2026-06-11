package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Regression for apply-failure-leaves-armed-timer-unsurfaced (2026-06-09 audit):
// armRollbackAndApply armed the rollback then, when applyNetworkConfig failed,
// returned the raw error. The caller treats a non-NotCommitted error as a generic
// "apply step skipped or failed" and never surfaces that the rollback is armed and
// will revert the network. The apply-failure path now returns a
// NetworkApplyNotCommittedError so the rollback-armed/reconnect guidance is logged.
func TestArmRollbackAndApply_ApplyFailureSurfacesNotCommitted(t *testing.T) {
	origFS, origCmd, origTime := restoreFS, restoreCmd, restoreTime
	t.Cleanup(func() { restoreFS, restoreCmd, restoreTime = origFS, origCmd, origTime })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = &FakeCommandRunner{} // default success for the nohup arm command
	restoreTime = &FakeTime{Current: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}

	// Empty PATH: no ifreload/systemctl/ifup -> applyNetworkConfig fails via its
	// default branch; no systemd-run -> the rollback arms via the nohup fallback
	// (restoreCmd is mocked, so nothing actually backgrounds).
	t.Setenv("PATH", t.TempDir())

	f := &networkRollbackUIApplyFlow{
		ctx:                context.Background(),
		ui:                 &fakeRestoreWorkflowUI{},
		logger:             newDiscardLogger(),
		rollbackBackupPath: "/safety-backup.tar",
		timeout:            time.Hour,
		iface:              "",
		diagnosticsDir:     "",
	}

	err := f.armRollbackAndApply()

	var nc *NetworkApplyNotCommittedError
	if !errors.As(err, &nc) {
		t.Fatalf("armRollbackAndApply on apply failure = %v; want *NetworkApplyNotCommittedError", err)
	}
	if !nc.RollbackArmed {
		t.Errorf("RollbackArmed should be true: the timer was armed before the failed apply")
	}
	if f.handle == nil {
		t.Errorf("the rollback handle should be set (armed) even though apply failed")
	}
}
