package orchestrator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression for nohup-disarm-cannot-stop-running-rollback (2026-06-09 audit): in
// the nohup fallback (no systemd unit) an in-progress rollback was undetectable, so
// commitNetworkConfig could report a successful commit while the script was actively
// reverting the network. The rollback script now writes a "<marker>.running" sentinel
// for the duration of the revert; rollbackAlreadyRunning stats it, turning the
// previously-microsecond commit-during-revert race into a deterministic, detectable
// state in nohup mode. Written after adding the sentinel.

func TestRollbackAlreadyRunning_DetectsRunningSentinelInNohupMode(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	marker := filepath.Join(t.TempDir(), "network_rollback_pending")
	if err := os.WriteFile(marker, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	// unitName empty => nohup fallback (no systemd unit to query).
	handle := &networkRollbackHandle{markerPath: marker}

	// No sentinel yet => not running (the normal sleep-window state, commit allowed).
	if rollbackAlreadyRunning(context.Background(), newDiscardLogger(), handle) {
		t.Fatalf("without the .running sentinel a nohup rollback must read as NOT running")
	}

	// The script signals it started the revert.
	if err := os.WriteFile(marker+".running", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !rollbackAlreadyRunning(context.Background(), newDiscardLogger(), handle) {
		t.Fatalf("with the .running sentinel present a nohup rollback must read as running")
	}
}

// End-to-end: a COMMIT arriving while the nohup rollback is mid-revert must be
// reported as NOT committed (previously it falsely reported success because the
// in-progress revert was invisible without a systemd unit).
func TestCommitNetworkConfig_NotCommittedWhileNohupRollbackRunning(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	marker := filepath.Join(dir, "network_rollback_pending")
	if err := os.WriteFile(marker, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker+".running", nil, 0o600); err != nil { // revert in progress
		t.Fatal(err)
	}

	f := &networkRollbackUIApplyFlow{
		ctx:    context.Background(),
		ui:     &fakeRestoreWorkflowUI{},
		logger: newDiscardLogger(),
		iface:  "",
		handle: &networkRollbackHandle{markerPath: marker}, // unitName empty => nohup
	}

	err := f.commitNetworkConfig()
	var nc *NetworkApplyNotCommittedError
	if !errors.As(err, &nc) {
		t.Fatalf("commitNetworkConfig while the rollback is running = %v; want *NetworkApplyNotCommittedError", err)
	}
	// commit must NOT have disarmed: marker and sentinel stay for the running revert.
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("marker must survive a not-committed late commit: %v", statErr)
	}
}

// The generated script must create the sentinel before reverting and remove it when
// it finishes, so its presence/absence faithfully tracks "revert in progress".
func TestBuildRollbackScript_RunningSentinelLifecycle(t *testing.T) {
	for _, bin := range []string{"sh", "tar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollback.log")
	marker := filepath.Join(dir, "marker")
	backup := filepath.Join(dir, "backup.tar.gz")
	if err := os.WriteFile(marker, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Invalid archive: tar -xzf fails at the gzip stage, writing nothing to /. The
	// sentinel is still created (before extract) and removed (cleanup), and
	// restartNetworking=false keeps the live network untouched.
	if err := os.WriteFile(backup, []byte("not a gzip tar archive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := buildRollbackScript(marker, backup, logPath, false)
	scriptPath := filepath.Join(dir, "rollback.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("sh", scriptPath).Run() // exits nonzero on the failed extract (covered elsewhere)

	// The sentinel-write code path must have been reached...
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logData), "Signalling rollback in progress") {
		t.Errorf("script did not signal rollback-in-progress; log:\n%s", logData)
	}
	// ...and the sentinel must be cleaned up afterwards (not left behind).
	if _, statErr := os.Stat(marker + ".running"); !os.IsNotExist(statErr) {
		t.Errorf(".running sentinel should be removed when the script finishes, err=%v", statErr)
	}
}
