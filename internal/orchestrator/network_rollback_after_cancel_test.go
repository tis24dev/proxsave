package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A Ctrl+C or SIGTERM during the network preflight cancelled the restore's ctx, and
// the rollback script ran under that same ctx: exec does not start a command whose
// ctx is already cancelled, so the files written to /etc stayed, unvalidated, until
// the next reboot. The rollback now runs under a ctx that the cancel does not reach.
func TestNetworkRollbackRunsAfterTheRestoreIsCancelled(t *testing.T) {
	origFS, origCmd, origTime := restoreFS, restoreCmd, restoreTime
	t.Cleanup(func() { restoreFS, restoreCmd, restoreTime = origFS, origCmd, origTime })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	cmd := &FakeCommandRunner{}
	restoreCmd = cmd
	restoreTime = &FakeTime{Current: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rollbackNetworkFilesNow(ctx, newTestLogger(), "/backup.tar", ""); err != nil {
		t.Fatalf("rollbackNetworkFilesNow: %v", err)
	}
	for i, call := range cmd.Calls {
		if strings.HasPrefix(call, "sh ") {
			if err := cmd.Contexts[i].Err(); err != nil {
				t.Fatalf("the rollback script got a cancelled ctx (%v): exec would not start it", err)
			}
			return
		}
	}
	t.Fatalf("the rollback script was not run; calls=%v", cmd.Calls)
}
