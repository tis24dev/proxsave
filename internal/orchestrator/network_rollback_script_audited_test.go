package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Regression for rollback-tar-failure-swallowed (2026-06-09 audit): buildRollbackScript
// wrapped the tar extraction in an `if`, which suspends `set -e`, so a failed/corrupt
// extraction still let the script exit 0. rollbackNetworkFilesNow then returned nil and
// maybeInstallNetworkConfigFromStage falsely reported "rolled back to the pre-restore
// state". The script must now exit nonzero when the extract phase fails. Written after
// the fix, hence the _audited suffix.
//
// These tests run the REAL generated script but only along paths that never touch the
// live system: a failing extraction writes nothing (gzip rejects a non-archive before
// tar extracts), the prune phase is gated on a successful extract, and restartNetworking
// is false so no network reload commands run.

func TestBuildRollbackScript_ExitsNonzeroWhenExtractFails(t *testing.T) {
	for _, bin := range []string{"sh", "tar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "rollback.log")
	markerPath := filepath.Join(dir, "marker")
	backupPath := filepath.Join(dir, "backup.tar.gz")

	// Marker present so the script proceeds past the disarm check.
	if err := os.WriteFile(markerPath, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A deliberately invalid archive: `tar -xzf` fails at the gzip stage and extracts
	// nothing, so this never writes to the real root filesystem.
	if err := os.WriteFile(backupPath, []byte("this is not a gzip tar archive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := buildRollbackScript(markerPath, backupPath, logPath, false /* restartNetworking */)
	scriptPath := filepath.Join(dir, "rollback.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	err := exec.Command("sh", scriptPath).Run()
	if err == nil {
		t.Fatalf("rollback script exited 0 on a failed extraction; want nonzero exit so the caller can detect the failed rollback")
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("expected a nonzero exit (*exec.ExitError), got %T: %v", err, err)
	}

	// The marker must still be cleaned up even on the failure path.
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Errorf("marker should be removed even when the rollback extraction fails")
	}
}

func TestBuildRollbackScript_NoopExitsZeroWhenMarkerAbsent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	// Marker absent -> the script is a no-op (already disarmed) and must exit 0. No
	// tar runs, so nothing touches the filesystem.
	script := buildRollbackScript(
		filepath.Join(dir, "absent-marker"),
		filepath.Join(dir, "backup.tar.gz"),
		filepath.Join(dir, "rollback.log"),
		false,
	)
	scriptPath := filepath.Join(dir, "rollback.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("sh", scriptPath).Run(); err != nil {
		t.Fatalf("no-op rollback (marker absent) should exit 0, got: %v", err)
	}
}
