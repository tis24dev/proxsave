package main

import (
	"context"
	"fmt"
	"os"
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
// PREPENDED, never replaced: the scripts run "sleep" and "cp" to spawn the descendant,
// and a PATH holding only this directory would turn every descendant case into a no-op
// that passes while proving nothing (the mistake run_hostname_timeout_test.go had to fix
// once, and the mistake an earlier version of this very file made). 0o755, matching
// internal/safeexec/waitdelay_test.go.
//
// PROXSAVE_TEST_DESCENDANT_SLEEP is derived from the SHIPPED CommandWaitDelay rather than
// hardcoded, so a descendant always outlives the budget a bound would impose. Raise or
// lower the constant and these cases keep testing the same thing.
func fakeCrontabForWrite(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil { //nolint:gosec // deliberately executable
		t.Fatalf("write fake crontab: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	installed := filepath.Join(dir, "installed")
	t.Setenv("PROXSAVE_TEST_CRONTAB_OUT", installed)
	t.Setenv("PROXSAVE_TEST_STAGED", filepath.Join(dir, "staged"))
	t.Setenv("PROXSAVE_TEST_DESCENDANT_SLEEP", fmt.Sprintf("%d", int((safeexec.CommandWaitDelay+time.Second).Seconds())))
	return installed
}

// descendantFinishesTheInstall is the shape that decides whether these writers may be
// bounded. crontab reads the whole table, hands the rest of the job to a descendant that
// inherited the CombinedOutput pipes, and exits 0 straight away. The descendant then
// writes to that inherited stdout AFTER the budget a bound would impose has expired.
//
// Cutting the drain calls closeDescriptors, which SIGPIPEs exactly this descendant, so
// under a bound the `cp` never runs and the operator's table is never installed.
const descendantFinishesTheInstall = `cat > "$PROXSAVE_TEST_STAGED"
( echo staging; sleep "$PROXSAVE_TEST_DESCENDANT_SLEEP"; echo committing; cp "$PROXSAVE_TEST_STAGED" "$PROXSAVE_TEST_CRONTAB_OUT" ) &
exit 0`

// TestCrontabWriteLinesLetsAWorkingDescendantFinish pins the decision NOT to bound this
// writer, and it pins it by consequence rather than by inspecting the source.
//
// safeexec.ApplyWaitDelay here would cut the drain while the descendant is still doing
// the install, kill it with SIGPIPE, and leave the crontab unwritten while the call
// reports ErrWaitDelay. Measured: bounded, table absent; unbounded, table installed. The
// reader beside this function IS bounded, and that asymmetry is deliberate: a reader
// loses nothing when its drain is cut, a writer loses the operator's crontab.
func TestCrontabWriteLinesLetsAWorkingDescendantFinish(t *testing.T) {
	installed := fakeCrontabForWrite(t, descendantFinishesTheInstall)
	want := "0 2 * * * /usr/local/bin/proxsave --backup\n"

	if err := crontabWriteLines(context.Background(), []string{strings.TrimSuffix(want, "\n")}); err != nil {
		t.Fatalf("crontabWriteLines = %v, want nil: crontab exited 0 and the table was delivered, so the only way to fail here is a drain bound reporting on work it interrupted", err)
	}

	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("the table was never installed (%v). A drain bound SIGPIPEs the descendant that finishes the install, which silently costs the operator their crontab", err)
	}
	if string(got) != want {
		t.Fatalf("installed crontab = %q, want %q. A truncated table here means the drain was cut mid hand-off", got, want)
	}
}

// TestMigrateLegacyCronEntriesLetsAWorkingDescendantFinish is the same decision at the
// second writer, where the stakes are highest. This one's error is DISCARDED
// (migrateLegacyCronEntries is void), and applyCronMode calls it to establish the cron
// fallback and then removes the daemon unit unconditionally. A bound that killed the
// descendant here would leave the host with no cron line AND no daemon, reporting nothing.
func TestMigrateLegacyCronEntriesLetsAWorkingDescendantFinish(t *testing.T) {
	installed := fakeCrontabForWrite(t, `if [ "$1" = "-l" ]; then
  echo "0 5 * * * /usr/local/bin/proxmox-backup --backup"
  exit 0
fi
`+descendantFinishesTheInstall)

	migrateLegacyCronEntries(context.Background(), t.TempDir(), "/usr/local/bin/proxsave", nil, "0 2 * * *")

	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("the migrated table was never installed (%v). This writer discards its error, so a bound killing the descendant is invisible: applyCronMode would go on to remove the daemon and leave no scheduler at all", err)
	}
	if !strings.Contains(string(got), "/usr/local/bin/proxsave") {
		t.Fatalf("migrated crontab = %q, want the repointed proxsave entry", got)
	}
}

// TestCrontabWriteLinesStillReportsARejectedTable is unrelated to the bound decision and
// survives it either way: a crontab that rejects the table exits non-zero, and both the
// status and crontab's own stderr have to reach the caller. CombinedOutput exists on this
// path for that diagnostic and nothing else.
func TestCrontabWriteLinesStillReportsARejectedTable(t *testing.T) {
	fakeCrontabForWrite(t, `cat > "$PROXSAVE_TEST_CRONTAB_OUT"
echo "errors in crontab file, can't install" >&2
exit 1`)

	err := crontabWriteLines(context.Background(), []string{"nonsense"})
	if err == nil {
		t.Fatal("a rejected crontab reported success")
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("err = %v, want the real exit status", err)
	}
	if !strings.Contains(err.Error(), "errors in crontab file") {
		t.Fatalf("err = %v, lost crontab's stderr. That diagnostic is the whole value of CombinedOutput here", err)
	}
}
