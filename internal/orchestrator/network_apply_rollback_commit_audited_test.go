package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression for two 2026-06-09 audit findings on the live network apply/rollback
// flow, where the network path was the lone outlier (firewall/HA/access-control
// handle the same cases correctly):
//   - expired-window-returns-success: waitForCommit returned nil when the rollback
//     window had already elapsed, which the caller reads as "committed", while the
//     timer was still armed and about to revert the network.
//   - completed-rollback-misreported-as-commit: commitNetworkConfig only checked
//     `systemctl is-active` and so missed a rollback that had ALREADY run, reporting
//     a successful commit after the network was reverted.
// Written after the fix, hence the _audited suffix.

func useRealRestoreFS(t *testing.T) {
	t.Helper()
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })
}

// Bug #1: an expired rollback window must surface a not-committed error (with the
// rollback reported as armed), not nil.
func TestWaitForCommit_ExpiredWindowReportsNotCommitted(t *testing.T) {
	useRealRestoreFS(t)

	markerPath := filepath.Join(t.TempDir(), "network_rollback_pending")
	if err := os.WriteFile(markerPath, []byte("pending"), 0o600); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	ui := &fakeRestoreWorkflowUI{networkCommit: true} // if the prompt were (wrongly) reached and committed, commit would return nil
	f := &networkRollbackUIApplyFlow{
		ctx:            context.Background(),
		ui:             ui,
		logger:         newDiscardLogger(),
		iface:          "",
		diagnosticsDir: t.TempDir(),
		handle: &networkRollbackHandle{
			markerPath: markerPath,
			armedAt:    time.Now().Add(-2 * time.Hour), // window of 1h already elapsed
			timeout:    time.Hour,
		},
	}

	err := f.waitForCommit()

	var nc *NetworkApplyNotCommittedError
	if !errors.As(err, &nc) {
		t.Fatalf("waitForCommit on an expired window = %v; want *NetworkApplyNotCommittedError", err)
	}
	if !nc.RollbackArmed {
		t.Errorf("RollbackArmed = false; want true (marker present, rollback will fire)")
	}
	if len(ui.shownMessages) == 0 {
		t.Errorf("expected reconnect/rollback guidance to be shown")
	}
}

// Bug #2: a commit arriving after the rollback already ran (marker removed by the
// rollback script, systemd unit no longer active) must report not-committed.
func TestCommitNetworkConfig_AlreadyExecutedReportsNotCommitted(t *testing.T) {
	useRealRestoreFS(t)

	// markerPath intentionally NOT created -> rollback already executed.
	missingMarker := filepath.Join(t.TempDir(), "network_rollback_pending_gone")

	f := &networkRollbackUIApplyFlow{
		ctx:    context.Background(),
		ui:     &fakeRestoreWorkflowUI{},
		logger: newDiscardLogger(),
		iface:  "",
		handle: &networkRollbackHandle{
			markerPath: missingMarker,
			// unitName empty -> rollbackAlreadyRunning is a no-op (no systemctl call).
			armedAt: time.Now(),
			timeout: time.Hour,
		},
	}

	err := f.commitNetworkConfig()

	var nc *NetworkApplyNotCommittedError
	if !errors.As(err, &nc) {
		t.Fatalf("commitNetworkConfig with a completed rollback = %v; want *NetworkApplyNotCommittedError", err)
	}
	if nc.RollbackArmed {
		t.Errorf("RollbackArmed = true; want false (marker gone = rollback already ran)")
	}
}

// Positive control: when the rollback is still pending (marker present) and not
// running, a genuine commit must succeed and disarm (remove the marker).
func TestCommitNetworkConfig_CommitsWhenRollbackPendingAndNotRunning(t *testing.T) {
	useRealRestoreFS(t)

	markerPath := filepath.Join(t.TempDir(), "network_rollback_pending")
	if err := os.WriteFile(markerPath, []byte("pending"), 0o600); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	f := &networkRollbackUIApplyFlow{
		ctx:    context.Background(),
		ui:     &fakeRestoreWorkflowUI{},
		logger: newDiscardLogger(),
		iface:  "",
		handle: &networkRollbackHandle{
			markerPath: markerPath, // unitName empty -> disarm only removes the marker
			armedAt:    time.Now(),
			timeout:    time.Hour,
		},
	}

	if err := f.commitNetworkConfig(); err != nil {
		t.Fatalf("commitNetworkConfig on a pending, non-running rollback = %v; want nil", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("marker should have been removed by disarm on a successful commit")
	}
}
