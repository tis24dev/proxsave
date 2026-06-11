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

// shFailingRunner makes the rollback script invocation (`sh <script>`) fail.
type shFailingRunner struct{ err error }

func (r shFailingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "sh" {
		return []byte("rollback boom"), r.err
	}
	return nil, nil
}

// When the apply fails AND the rollback also fails, the returned error must surface
// BOTH (and reference the rollback path), not just the apply error.
func TestRollbackAfterFailedStagedNetworkApply_SurfacesRollbackFailure(t *testing.T) {
	origFS, origCmd, origTime := restoreFS, restoreCmd, restoreTime
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreTime = origFS, origCmd, origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}

	rollbackErr := errors.New("rollback script exit 1")
	restoreCmd = shFailingRunner{err: rollbackErr}

	applyErr := errors.New("write /etc/hostname failed")
	got := rollbackAfterFailedStagedNetworkApply(context.Background(), newTestLogger(), applyErr, "/backup.tar")

	if got == nil {
		t.Fatal("expected a combined error when both apply and rollback fail")
	}
	if !errors.Is(got, applyErr) {
		t.Errorf("returned error must preserve the original apply error; got %v", got)
	}
	if !errors.Is(got, rollbackErr) {
		t.Errorf("returned error must surface the rollback failure; got %v", got)
	}
	if !strings.Contains(got.Error(), "/backup.tar") {
		t.Errorf("returned error should reference the rollback path; got %v", got)
	}
}
