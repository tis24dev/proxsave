// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// Issue #298: "Daemon mode enabled: <unit> is active and the cron entry was removed." was
// printed unconditionally at every call site, including the hosts where removeCanonicalCronEntry
// had matched nothing at all. cronRemovalClause is the single renderer that replaced it, so the
// contract it has to keep is narrow and absolute: an outcome that removed nothing may never
// produce a sentence claiming a removal.
func TestCronRemovalClauseStatesWhatActuallyHappened(t *testing.T) {
	tests := []struct {
		name    string
		outcome cronRemovalOutcome
		want    string
	}{
		{"one line: historic wording preserved", cronRemovalOutcome{Removed: 1, Verified: true}, "The cron entry was removed."},
		{"several lines: counted", cronRemovalOutcome{Removed: 3, Verified: true}, "3 proxsave cron entries were removed."},
		{"nothing matched: says so", cronRemovalOutcome{Verified: true}, "No proxsave cron entry was present to remove."},
		{"unverified: claims nothing about the crontab", cronRemovalOutcome{}, "The crontab could not be checked, so a proxsave cron entry may still be scheduled alongside it."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cronRemovalClause(tc.outcome); got != tc.want {
				t.Errorf("cronRemovalClause(%+v) = %q, want %q", tc.outcome, got, tc.want)
			}
		})
	}

	// The regression itself, stated as an invariant rather than a string comparison: no
	// outcome that deleted zero lines may ever render a removal claim.
	for _, outcome := range []cronRemovalOutcome{{Verified: true}, {}, {Removed: 0, Verified: false}} {
		if got := cronRemovalClause(outcome); strings.Contains(got, "was removed") || strings.Contains(got, "were removed") {
			t.Errorf("outcome %+v removed nothing but the report claims a removal: %q", outcome, got)
		}
	}
}

// removeCanonicalCronEntry is the only thing that knows whether a cron line really went
// away, so every caller's honesty depends on the count it returns.
func TestRemoveCanonicalCronEntryReportsWhatItRemoved(t *testing.T) {
	origRead := crontabReadLinesFn
	origWrite := crontabWriteLinesFn
	t.Cleanup(func() {
		crontabReadLinesFn = origRead
		crontabWriteLinesFn = origWrite
	})

	t.Run("canonical line: removed, counted, operator lines preserved", func(t *testing.T) {
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{
				"0 5 * * * /usr/local/bin/proxsave --backup",
				"0 6 * * * /usr/bin/rsync /a /b",
			}, nil
		}
		var written []string
		crontabWriteLinesFn = func(_ context.Context, lines []string) error {
			written = lines
			return nil
		}
		outcome, err := removeCanonicalCronEntry(context.Background(), []string{"/usr/local/bin/proxsave"}, nil)
		if err != nil {
			t.Fatalf("removeCanonicalCronEntry: %v", err)
		}
		if outcome.Removed != 1 || !outcome.Verified {
			t.Fatalf("want Removed=1 Verified=true, got %+v", outcome)
		}
		if len(written) != 1 || !strings.Contains(written[0], "rsync") {
			t.Fatalf("the unrelated operator line must survive, got %v", written)
		}
	})

	// The #298 host. The wrapper's command basename is "proxsave-nas-guard", which
	// commandTokenMatchesTarget deliberately does not match (widening it would delete jobs
	// that merely mention the binary). So nothing is removed - and the report must say so
	// instead of claiming a removal, and the operator's crontab must not be rewritten.
	t.Run("operator wrapper only: nothing removed, crontab untouched, no false claim", func(t *testing.T) {
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"30 02 * * * /usr/local/sbin/proxsave-nas-guard"}, nil
		}
		wrote := false
		crontabWriteLinesFn = func(context.Context, []string) error {
			wrote = true
			return nil
		}
		outcome, err := removeCanonicalCronEntry(context.Background(), []string{"/usr/local/bin/proxsave"}, nil)
		if err != nil {
			t.Fatalf("removeCanonicalCronEntry: %v", err)
		}
		if outcome.Removed != 0 || !outcome.Verified {
			t.Fatalf("want Removed=0 Verified=true, got %+v", outcome)
		}
		if wrote {
			t.Fatal("a no-op must never rewrite the operator's crontab")
		}
		if got := cronRemovalClause(outcome); strings.Contains(got, "removed") && !strings.Contains(got, "to remove") {
			t.Fatalf("the migration must not claim a removal it did not make: %q", got)
		}
	})

	t.Run("read failure: unverified and no write", func(t *testing.T) {
		crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, errors.New("crontab -l boom") }
		crontabWriteLinesFn = func(context.Context, []string) error {
			t.Error("a failed read must never lead to a write")
			return nil
		}
		outcome, err := removeCanonicalCronEntry(context.Background(), []string{"/usr/local/bin/proxsave"}, nil)
		if err == nil {
			t.Fatal("the read failure must be returned")
		}
		if outcome.Verified || outcome.Removed != 0 {
			t.Fatalf("a failed read verified nothing and removed nothing, got %+v", outcome)
		}
	})

	// The write half matters as much as the read half: reporting "nothing was present"
	// after a failed `crontab -` would trade the old lie for a new one.
	t.Run("write failure: unverified, never reported as an empty crontab", func(t *testing.T) {
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"0 5 * * * /usr/local/bin/proxsave --backup"}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error { return errors.New("crontab - boom") }
		outcome, err := removeCanonicalCronEntry(context.Background(), []string{"/usr/local/bin/proxsave"}, nil)
		if err == nil {
			t.Fatal("the write failure must be returned")
		}
		if outcome.Verified || outcome.Removed != 0 {
			t.Fatalf("a failed write verified nothing and removed nothing, got %+v", outcome)
		}
		if got := cronRemovalClause(outcome); strings.Contains(got, "No proxsave cron entry was present") {
			t.Fatalf("a failed write must not be reported as an empty crontab: %q", got)
		}
	})
}

// cronModeFixture wires the seams applyCronMode touches and returns the config it was
// given, mirroring the setup TestApplyCronModeProceedsWhenIdle already uses.
func cronModeFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	origRun := restartVerifyBackupRunning
	origRemove := removeDaemonServiceFn
	origMigrate := migrateLegacyCronEntriesFn
	origWrapper := wrapperCronLinesFn
	origRead := crontabReadLinesFn
	origWrite := crontabWriteLinesFn
	t.Cleanup(func() {
		restartVerifyBackupRunning = origRun
		removeDaemonServiceFn = origRemove
		migrateLegacyCronEntriesFn = origMigrate
		wrapperCronLinesFn = origWrapper
		crontabReadLinesFn = origRead
		crontabWriteLinesFn = origWrite
	})
	restartVerifyBackupRunning = func(string) bool { return false } // idle: no teardown defer
	removeDaemonServiceFn = func(context.Context, *logging.BootstrapLogger) error { return nil }

	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte("SCHEDULER_MODE=daemon\nSCHEDULER_TIME=02:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &config.Config{BaseDir: dir, SchedulerTime: "02:00"}, configPath
}

// The third duplicate source in #298. After reverting, applyCronMode appended a fresh
// canonical proxsave line at SCHEDULER_TIME while the operator's wrapper line was still
// there, so the host ended up with TWO backups at 02:00 and the second died on the per-run
// lock with exit 16. With a wrapper positively identified, no second line may be written.
func TestApplyCronModeDoesNotAddASecondScheduleNextToAWrapper(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	const wrapper = "30 02 * * * /usr/local/sbin/proxsave-nas-guard"
	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{wrapper, "0 2 * * * /usr/local/bin/proxsave --backup"}, nil
	}
	wrapperCronLinesFn = func([]string) []string { return []string{wrapper} }
	migrateLegacyCronEntriesFn = func(context.Context, string, string, *logging.BootstrapLogger, string) {
		t.Error("a wrapper host must NOT get a second canonical cron line appended")
	}
	var written []string
	crontabWriteLinesFn = func(_ context.Context, lines []string) error {
		written = lines
		return nil
	}

	if err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
		t.Fatalf("applyCronMode: %v", err)
	}

	// The redundant canonical line is dropped and the wrapper is left as the only schedule:
	// exactly one backup a night, the operator's own.
	if len(written) != 1 || written[0] != wrapper {
		t.Fatalf("the wrapper must be left as the sole schedule, got %v", written)
	}
	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), "SCHEDULER_MODE=cron") || !strings.Contains(string(data), "DAEMON_OPT_OUT=true") {
		t.Fatalf("the revert must still persist cron mode and the opt-out tombstone:\n%s", data)
	}
}

// Backward compatibility, and the reason the wrapper branch is gated on a POSITIVE
// identification: a host with no wrapper must still get its canonical cron line, or
// --daemon-remove would leave it unscheduled (F09-06).
func TestApplyCronModeStillWritesTheCronLineWithoutAWrapper(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) {
		return []string{"0 6 * * * /usr/bin/rsync /a /b"}, nil
	}
	wrapperCronLinesFn = func([]string) []string { return nil }
	migrated := ""
	migrateLegacyCronEntriesFn = func(_ context.Context, _, _ string, _ *logging.BootstrapLogger, schedule string) {
		migrated = schedule
	}
	crontabWriteLinesFn = func(context.Context, []string) error {
		t.Error("without a wrapper the crontab is rewritten by migrateLegacyCronEntries, not here")
		return nil
	}

	if err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
		t.Fatalf("applyCronMode: %v", err)
	}
	// cron.TimeToSchedule zero-pads both fields ("%02d %02d * * *"), so SCHEDULER_TIME=02:00
	// reaches migrateLegacyCronEntries as "00 02 * * *". The unpadded "0 2 * * *" is
	// migrateLegacyCronEntries' OWN fallback for an empty schedule, which is exactly the
	// value this test must not accept: it would pass even if applyCronMode stopped passing
	// the configured time at all.
	if migrated != "00 02 * * *" {
		t.Fatalf("a host with no wrapper must still get its cron line at SCHEDULER_TIME, got %q", migrated)
	}
}

// Fail-open: an unreadable crontab must be treated as "no wrapper" and keep today's append.
// Being scheduled twice is recoverable; being unscheduled is silent data loss.
func TestApplyCronModeFallsBackToTheAppendWhenTheCrontabIsUnreadable(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, errors.New("crontab -l boom") }
	wrapperCronLinesFn = func([]string) []string {
		t.Error("the detector must not be consulted when the crontab could not be read")
		return nil
	}
	migrated := false
	migrateLegacyCronEntriesFn = func(context.Context, string, string, *logging.BootstrapLogger, string) {
		migrated = true
	}

	if err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
		t.Fatalf("applyCronMode: %v", err)
	}
	if !migrated {
		t.Fatal("an unreadable crontab must fall back to writing the cron line, never to leaving the host unscheduled")
	}
}
