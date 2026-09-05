package orchestrator

import (
	"context"
	"errors"
	"os"
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
