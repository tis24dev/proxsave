package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Two arms aggregate their per-item outcome into a count and turn it into a
// FORMATTED STRING: applyPVEBackupJobsFromStage returns "applied=N failed=M" and
// applyStorageCfg's caller returns "storage.cfg applied with N failure(s)".
// Neither wraps anything, so errors.Is cannot match them against
// context.Canceled.
//
// That has two consequences on an aborted restore, and the loop is the cause of
// both. It keeps calling pvesh for every remaining item, each failing instantly
// on the dead context, and the aggregate it finally returns makes applyArm record
// the arm as an item that failed on its own. The restore then aborts with a
// diagnostic blaming the job or storage configuration for the operator's own
// abort.
//
// The fix is to propagate rather than count, and these pin both halves: the error
// IS the cancellation, and the loop stopped instead of churning.

func cancelledArmFakes(t *testing.T) (*FakeFS, *recordingPvesh) {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fs := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fs.Root) })
	restoreFS = fs
	cmd := &recordingPvesh{}
	restoreCmd = cmd
	return fs, cmd
}

// recordingPvesh answers "which pvesh" and records every other invocation, so a
// test can assert that the loop never reached the apply calls.
type recordingPvesh struct{ calls []string }

func (r *recordingPvesh) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, line)
	return nil, nil
}

func (r *recordingPvesh) applyCalls() []string {
	var out []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "pvesh create") || strings.HasPrefix(c, "pvesh set") {
			out = append(out, c)
		}
	}
	return out
}

func TestCancelledJobsArmPropagatesInsteadOfCounting(t *testing.T) {
	fs, cmd := cancelledArmFakes(t)
	jobs := strings.Join([]string{
		"vzdump: job-one",
		"\tschedule 02:00",
		"\tstorage local",
		"",
		"vzdump: job-two",
		"\tschedule 03:00",
		"\tstorage local",
		"",
	}, "\n")
	if err := fs.AddFile("/stage/etc/pve/jobs.cfg", []byte(jobs)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := applyPVEBackupJobsFromStage(ctx, logging.New(types.LogLevelError, false), "/stage")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled: applyArm cannot tell an abort from a job that failed", err)
	}
	if applies := cmd.applyCalls(); len(applies) != 0 {
		t.Fatalf("the loop kept applying after cancellation: %v", applies)
	}
}

func TestCancelledStorageArmPropagatesInsteadOfCounting(t *testing.T) {
	fs, cmd := cancelledArmFakes(t)
	cfg := strings.Join([]string{
		"dir: local",
		"\tpath /var/lib/vz",
		"",
		"nfs: backup_ext",
		"\tserver 10.0.0.1",
		"\texport /srv/backups",
		"",
	}, "\n")
	if err := fs.AddFile("/stage/etc/pve/storage.cfg", []byte(cfg)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	applied, failed, err := applyStorageCfg(ctx, "/stage/etc/pve/storage.cfg", logging.New(types.LogLevelError, false))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if failed != 0 {
		t.Fatalf("the cancellation was counted as %d storage failure(s)", failed)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0: nothing ran", applied)
	}
	if applies := cmd.applyCalls(); len(applies) != 0 {
		t.Fatalf("the loop kept applying after cancellation: %v", applies)
	}
}

// The mount guard arm was the third of the three, and the only one whose
// cancellation was SILENT rather than mislabelled: every outcome in its loop is a
// warning and a continue, and the function ends with return nil on purpose,
// because a mountpoint that could not be guarded must not fail the restore.
//
// A cancellation is not one of those outcomes. Without a check the loop walks the
// whole candidate list while the operator is aborting, and each iteration can
// mkdir, activate storage, mount, bind read-only and set chattr +i. Returning nil
// also hid the abort from applyArm, which saw a clean arm and carried on.
func TestCancelledMountGuardArmStopsInsteadOfGuarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := newTestLogger()

	origFS, origCmd, origGeteuid := restoreFS, restoreCmd, mountGuardGeteuid
	origRead, origMkdir := mountGuardReadFile, mountGuardMkdirAll
	origMount, origUnmount := mountGuardSysMount, mountGuardSysUnmount
	t.Cleanup(func() {
		restoreFS, restoreCmd, mountGuardGeteuid = origFS, origCmd, origGeteuid
		mountGuardReadFile, mountGuardMkdirAll = origRead, origMkdir
		mountGuardSysMount, mountGuardSysUnmount = origMount, origUnmount
	})

	restoreFS = osFS{}
	mountGuardGeteuid = func() int { return 0 }
	mountGuardReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/proc/self/mountinfo", "/proc/mounts":
			return []byte(""), nil
		default:
			return nil, os.ErrNotExist
		}
	}
	// If the loop is ever entered these must not run, so they fail the test loudly
	// rather than quietly doing nothing.
	mountGuardMkdirAll = func(string, os.FileMode) error {
		t.Error("a cancelled restore created a guard directory")
		return nil
	}
	mountGuardSysMount = func(string, string, string, uintptr, string) error {
		t.Error("a cancelled restore mounted a guard")
		return nil
	}
	mountGuardSysUnmount = func(string, int) error { return nil }

	stageRoot := t.TempDir()
	stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir staged storage.cfg dir: %v", err)
	}
	offlineID := uniquePveMountTestStorageID(t, "cancelled")
	if err := os.WriteFile(stageCfgPath, []byte("nfs: "+offlineID+"\n"), 0o644); err != nil {
		t.Fatalf("write staged storage.cfg: %v", err)
	}
	target := pveMountTargetForStorageID(offlineID)
	cleanupPveMountTestTarget(t, target)
	cleanupPveMountTestGuardDir(t, target)

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), stageRoot, "/")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled: the abort stayed invisible to applyArm", err)
	}
	if calls := strings.Join(fakeCmd.CallsList(), "\n"); strings.Contains(calls, "mount "+target) || strings.Contains(calls, "pvesm activate") {
		t.Fatalf("the loop kept guarding after cancellation: %v", fakeCmd.CallsList())
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("a cancelled restore recorded guards: %#v", got)
	}
}
