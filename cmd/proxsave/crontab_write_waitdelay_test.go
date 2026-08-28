package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// fakeCrontabForWrite installs a fake `crontab` ahead of everything else on PATH and
// returns the path the fake installs its stdin into, so a case can assert on the table
// the writer actually delivered.
//
// PREPENDED, never replaced: the scripts run "sleep" and "cat" to spawn the grandchild,
// and a PATH holding only this directory would turn the grandchild case into a no-op
// that passes while proving nothing (the mistake run_hostname_timeout_test.go had to
// fix once). 0o755 and not 0o777, matching internal/safeexec/waitdelay_test.go.
func fakeCrontabForWrite(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake crontab: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	installed := filepath.Join(dir, "installed")
	t.Setenv("PROXSAVE_TEST_CRONTAB_OUT", installed)
	return installed
}

// holdsThePipeAndExitsCleanly is the defect driven rather than described: crontab
// installs the table and exits 0, so the context deadline never fires and there is
// nothing for a timeout to bound, while a backgrounded grandchild keeps stdout and
// stderr open. `&` in POSIX sh reassigns a background job's stdin to /dev/null, so
// this holder holds the OUTPUT pipes only, which is the shape a wedged helper makes.
const holdsThePipeAndExitsCleanly = `sleep 60 &
cat > "$PROXSAVE_TEST_CRONTAB_OUT"
exit 0`

// TestCrontabWriteLinesIsBoundedAndKeepsTheError gives the writer the bound its
// sibling reader already had. crontabReadLines calls safeexec.ApplyWaitDelay;
// crontabWriteLines did not, and CombinedOutput waits for the copy goroutines, so a
// grandchild holding the pipe wedges runInstall, runInstallTUI, runUpgrade and
// upgradeFinalizePhase for that grandchild's whole lifetime.
func TestCrontabWriteLinesIsBoundedAndKeepsTheError(t *testing.T) {
	// The shipped value deliberately, not a shrunken one: the number is load bearing in
	// both directions and TestCrontabWriteLinesDoesNotFailASlowButHealthyCrontab guards
	// the lower end. The cost is that this case takes about CommandWaitDelay to run.
	installed := fakeCrontabForWrite(t, holdsThePipeAndExitsCleanly)
	want := "0 2 * * * /usr/local/bin/proxsave --backup\n"

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- crontabWriteLines(context.Background(), []string{strings.TrimSuffix(want, "\n")})
	}()

	select {
	case err := <-done:
		if elapsed := time.Since(start); elapsed > safeexec.CommandWaitDelay+5*time.Second {
			t.Fatalf("crontabWriteLines took %s with a budget of %s; the drain was not bounded", elapsed, safeexec.CommandWaitDelay)
		}
		// The error must SURVIVE, and this half is the whole judgement. Swallowing it to
		// nil the way orchestrator.osCommandRunner.Run does is right THERE because of the
		// sink: its []byte IS the answer and is provably complete, so a cut drain costs
		// nothing. Here the answer is the SIDE EFFECT and the payload travels on stdin. A
		// descendant holding the stdin READ end leaves crontab installing a TRUNCATED
		// table and still exiting 0, and that case is indistinguishable from this one:
		// same ErrWaitDelay, same EMPTY capture (real `crontab -` prints nothing on
		// success), same elapsed time. A nil here reports a destroyed operator crontab as
		// installed, which is the rule internal/storage/cloud.go follows for rclone.
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("err = %v, want exec.ErrWaitDelay. Reporting nil here cannot tell a written crontab from a truncated one, because both exit 0 with no output", err)
		}
	case <-time.After(safeexec.CommandWaitDelay + 20*time.Second):
		t.Fatal("crontabWriteLines never returned: the context bounds the PROCESS, not the PIPE, so a grandchild holding stdout blocks the install for that grandchild's lifetime")
	}

	// The bound cuts the DRAIN, not the child, so the table this reported on is on disk.
	// Without this the error above could equally well mean the write never happened.
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("read installed crontab: %v", err)
	}
	if string(got) != want {
		t.Fatalf("installed crontab = %q, want %q. A bound that costs the child's work is not a bound, it is a truncation", got, want)
	}
}

// TestMigrateLegacyCronEntriesIsBoundedWhenADescendantHoldsThePipe covers the second
// writer, the writeCron closure, which is not reachable on its own. The fake answers
// `crontab -l` normally so readCron (already bounded) completes, and holds the pipe
// only on `crontab -`, which is the site under test.
func TestMigrateLegacyCronEntriesIsBoundedWhenADescendantHoldsThePipe(t *testing.T) {
	fakeCrontabForWrite(t, `if [ "$1" = "-l" ]; then
  echo "0 5 * * * /usr/local/bin/proxmox-backup --backup"
  exit 0
fi
sleep 60 &
cat > "$PROXSAVE_TEST_CRONTAB_OUT"
exit 0`)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		migrateLegacyCronEntries(context.Background(), t.TempDir(), "/usr/local/bin/proxsave", nil, "0 2 * * *")
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > safeexec.CommandWaitDelay+5*time.Second {
			t.Fatalf("migrateLegacyCronEntries took %s with a budget of %s; the write drain was not bounded", elapsed, safeexec.CommandWaitDelay)
		}
	case <-time.After(safeexec.CommandWaitDelay + 20*time.Second):
		t.Fatal("migrateLegacyCronEntries never returned: writeCron's CombinedOutput is unbounded, so a grandchild holding the pipe wedges runInstall and runInstallTUI with the install half done")
	}
}

// TestCrontabWriteLinesDoesNotFailASlowButHealthyCrontab pins the VALUE, not merely
// the presence of a bound, and it is the guard against the expensive failure mode:
// a false "crontab update failed" on install. The budget is a DRAIN allowance that
// starts only once the child has EXITED, so a child that is merely slow must be
// untouched by it. Without this case, setting CommandWaitDelay to one millisecond
// leaves the two cases above green while turning every cron install on a loaded host
// into a reported failure.
func TestCrontabWriteLinesDoesNotFailASlowButHealthyCrontab(t *testing.T) {
	installed := fakeCrontabForWrite(t, `cat > "$PROXSAVE_TEST_CRONTAB_OUT"
sleep 5
exit 0`)
	want := "0 2 * * * /usr/local/bin/proxsave --backup\n"

	start := time.Now()
	err := crontabWriteLines(context.Background(), []string{strings.TrimSuffix(want, "\n")})
	if err != nil {
		t.Fatalf("a crontab that ran %s, well past the %s budget, but exited cleanly returned %v. The budget starts when the child EXITS; one that bounds runtime turns slow hosts into failed installs", time.Since(start).Round(time.Second), safeexec.CommandWaitDelay, err)
	}
	got, err := os.ReadFile(installed)
	if err != nil || string(got) != want {
		t.Fatalf("installed crontab = %q (err %v), want %q", got, err, want)
	}
}

// TestCrontabWriteLinesStillReportsARejectedTable is the other half of the no-swallow
// decision, and it is the one that proves the bound cannot HIDE a defect. A crontab
// that rejects the table exits 1, and os/exec substitutes ErrWaitDelay only on an
// otherwise successful exit, so the real status and crontab's own stderr must both
// survive even with a descendant holding the pipe. CombinedOutput exists here for that
// diagnostic and nothing else.
func TestCrontabWriteLinesStillReportsARejectedTable(t *testing.T) {
	fakeCrontabForWrite(t, `cat > "$PROXSAVE_TEST_CRONTAB_OUT"
echo "errors in crontab file, can't install" >&2
sleep 60 &
exit 1`)

	err := crontabWriteLines(context.Background(), []string{"nonsense"})
	if err == nil {
		t.Fatal("a rejected crontab reported success")
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("err = %v: the bound replaced the command's own failure with its own, which hides the rejection", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("err = %v, want the real exit status", err)
	}
	if !strings.Contains(err.Error(), "errors in crontab file") {
		t.Fatalf("err = %v, lost crontab's stderr. That diagnostic is the whole value of CombinedOutput here", err)
	}
}
