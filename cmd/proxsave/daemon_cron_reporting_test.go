// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"io"
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
	origSystem := systemCronProxsaveRefsFn
	t.Cleanup(func() {
		restartVerifyBackupRunning = origRun
		removeDaemonServiceFn = origRemove
		migrateLegacyCronEntriesFn = origMigrate
		wrapperCronLinesFn = origWrapper
		crontabReadLinesFn = origRead
		crontabWriteLinesFn = origWrite
		systemCronProxsaveRefsFn = origSystem
	})
	restartVerifyBackupRunning = func(string) bool { return false } // idle: no teardown defer
	removeDaemonServiceFn = func(context.Context, *logging.BootstrapLogger) error { return nil }
	// applyCronMode reads the SYSTEM cron habitat to report a schedule it cannot own
	// (#298), and systemCronPaths points at the REAL /etc. Unstubbed, every test built on
	// this fixture would read the /etc/crontab and /etc/cron.d of the machine running the
	// suite - and would still pass there, which is exactly why it has to be pinned here
	// rather than left to be noticed later. "No system cron entry" is the ordinary host.
	systemCronProxsaveRefsFn = func() []indirectCronRef { return nil }

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

	if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
		t.Fatalf("applyCronMode: %v", err)
	}

	// Nothing is written to the crontab at all: the append is skipped, and the branch no
	// longer DELETES either. Deleting was the old behaviour and it was a defect: the
	// detector's rules answer "named after proxsave", not "runs a proxsave backup", so an
	// ordinary "*/5 * * * * /usr/local/bin/proxsave-metrics-exporter" reached this branch
	// and took the host's only real backup line with it, at INFO level and exit 0, with
	// nothing on the host able to repair it. Skipping the append alone cannot unschedule
	// anyone: its worst case on a misidentification is that nothing changes.
	if written != nil {
		t.Fatalf("the wrapper branch must not rewrite the crontab at all, got %v", written)
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

	if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
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
//
// The second half is the #298 follow-up. A ProxSave schedule under /etc is an INDEPENDENT
// fact: it is read from files, not from `crontab -l`, so a failed crontab read is no
// reason to stay quiet about it. It still does not gate the append - nothing does - which
// is why this fail-open rule needed no splitting: the two facts are reported separately
// because they are separately true, not weighed against each other.
func TestApplyCronModeFallsBackToTheAppendWhenTheCrontabIsUnreadable(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, errors.New("crontab -l boom") }
	wrapperCronLinesFn = func([]string) []string {
		t.Error("the detector must not be consulted when the crontab could not be read")
		return nil
	}
	systemCronConsulted := false
	systemCronProxsaveRefsFn = func() []indirectCronRef {
		systemCronConsulted = true
		return nil
	}
	migrated := false
	migrateLegacyCronEntriesFn = func(context.Context, string, string, *logging.BootstrapLogger, string) {
		migrated = true
	}

	if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
		t.Fatalf("applyCronMode: %v", err)
	}
	if !migrated {
		t.Fatal("an unreadable crontab must fall back to writing the cron line, never to leaving the host unscheduled")
	}
	if !systemCronConsulted {
		t.Fatal("a failed `crontab -l` says nothing about /etc: the system-cron advisory must still run, or the host is told its revert restored the only schedule when a second one is sitting in /etc/cron.d")
	}
}

// The decision this whole change turns on, stated as a test so it cannot be quietly
// reversed: a ProxSave schedule found under /etc is REPORTED, never acted on. The revert
// still writes its canonical cron line.
//
// Suppressing it instead - the symmetrical-looking move, since the root-crontab wrapper
// branch does exactly that - was considered and rejected. The evidence is weaker there
// (rules 1 to 3 answer "named after proxsave", not "runs a proxsave backup":
// /opt/proxsave/script/prune.sh, a nightly "nice rsync -a /opt/proxsave/ ..." mirror and a
// maintenance script with a COMMENTED-OUT proxsave call all match), and the cost of being
// wrong is inverted. A false positive on the warn path costs a paragraph the operator can
// ignore; on a write gate it costs the host every future backup, silently, because
// ProxSave cannot edit /etc to repair it and no later run exists to re-check. F09-06
// already ranks those: a double schedule is a recoverable annoyance, an unscheduled host
// is silent data loss.
func TestApplyCronModeStillWritesTheCronLineWhenSystemCronAlsoSchedulesIt(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }
	wrapperCronLinesFn = func([]string) []string { return nil }
	systemCronProxsaveRefsFn = func() []indirectCronRef {
		return []indirectCronRef{{
			Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
			Command: "/usr/local/sbin/proxsave-nas-guard",
			Reason:  "its command \"proxsave-nas-guard\" is named after proxsave",
			Source:  "/etc/cron.d/proxsave-guard",
		}}
	}
	migrated := ""
	migrateLegacyCronEntriesFn = func(_ context.Context, _, _ string, _ *logging.BootstrapLogger, schedule string) {
		migrated = schedule
	}
	crontabWriteLinesFn = func(context.Context, []string) error {
		t.Error("a /etc finding must not send the revert down the removal path: it is not a wrapper this host's crontab owns")
		return nil
	}

	if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", nil, true); err != nil {
		t.Fatalf("applyCronMode: %v", err)
	}
	if migrated != "00 02 * * *" {
		t.Fatalf("a ProxSave schedule under /etc must NOT cancel the revert's own cron line (that would leave the host unscheduled if the finding was wrong or the entry is later deleted), got %q", migrated)
	}
}

// The advisory is the only thing this whole path delivers, and until this test existed
// nothing asserted it was ever PRINTED: replacing the emit loop in applyCronMode with
// `_ = systemCronScheduleAdvisory(...)` left the suite green. A renderer that is unit
// tested but never wired is indistinguishable from no feature at all.
//
// It asserts on the real console bytes rather than on a second call to the renderer,
// because "the operator saw it" is the property that was missing, not "the string can be
// produced". A BootstrapLogger writes its warnings to stderr, so stderr is the channel.
func TestApplyCronModeEmitsTheSystemCronAdvisory(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }
	wrapperCronLinesFn = func([]string) []string { return nil }
	migrateLegacyCronEntriesFn = func(context.Context, string, string, *logging.BootstrapLogger, string) {}
	systemCronProxsaveRefsFn = func() []indirectCronRef {
		return []indirectCronRef{{
			Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
			Command: "/usr/local/sbin/proxsave-nas-guard",
			Reason:  "its command \"proxsave-nas-guard\" is named after proxsave",
			Source:  "/etc/cron.d/proxsave-guard",
		}}
	}

	seen := captureConsole(t, func() {
		if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", logging.NewBootstrapLogger(), true); err != nil {
			t.Fatalf("applyCronMode: %v", err)
		}
	})
	for _, want := range []string{
		"possible ProxSave cron line(s) under /etc",
		"/etc/cron.d/proxsave-guard",
		"/etc unchanged",
	} {
		if !strings.Contains(seen, want) {
			t.Errorf("applyCronMode must emit the advisory line %q, got:\n%s", want, seen)
		}
	}
}

// ONE problem, ONE warning - the shape the other two #298 blocks already use
// (warnIndirectProxsaveCronOnDaemonInstall, maybeAutoMigrateDaemon). This block emitted every
// line it printed at WARNING, so a single finding was counted three times in the run's
// "WARNINGS/ERRORS DURING RUN (warnings=N)" recap and two findings four times, and that count
// is what an operator scans.
//
// The findings are INFO: the header carrying the count, then one item per line. The line that
// stays WARNING is the one that says what ProxSave did about them.
func TestApplyCronModeAdvisoryUsesOneWarning(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }
	wrapperCronLinesFn = func([]string) []string { return nil }
	migrateLegacyCronEntriesFn = func(context.Context, string, string, *logging.BootstrapLogger, string) {}
	// TWO findings, so a per-line WARNING cannot be mistaken for the one-warning shape.
	systemCronProxsaveRefsFn = func() []indirectCronRef {
		return []indirectCronRef{
			{
				Line:    "0 5 * * * root /usr/local/bin/proxsave --backup",
				Command: "/usr/local/bin/proxsave",
				Reason:  `"proxsave" is the proxsave binary; /etc cron lines stay untouched`,
				Source:  "/etc/cron.d/proxsave",
			},
			{
				Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
				Command: "/usr/local/sbin/proxsave-nas-guard",
				Reason:  "script /usr/local/sbin/proxsave-nas-guard calls the proxsave binary",
				Source:  "/etc/crontab",
			},
		}
	}

	seen := captureConsole(t, func() {
		if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", logging.NewBootstrapLogger(), true); err != nil {
			t.Fatalf("applyCronMode: %v", err)
		}
	})

	if got := strings.Count(seen, "WARNING"); got != 1 {
		t.Errorf("two findings are still ONE problem: want 1 WARNING, got %d, out=%q", got, seen)
	}
	// The surviving WARNING is the ownership line, and nothing else may be promoted to it.
	warn := ""
	for _, line := range strings.Split(seen, "\n") {
		if strings.Contains(line, "WARNING") {
			warn = line
			break
		}
	}
	if !strings.Contains(warn, "/etc unchanged") {
		t.Errorf("the WARNING must be the ownership line, got %q", warn)
	}
	// It has to stand alone: DEBUG_LEVEL=warning (internal/cli/args.go) hides the INFO lines
	// above it, so the count has to be in this line or an operator reading only warnings never
	// learns that anything was found at all.
	if !strings.Contains(warn, "2 line(s) in /etc unchanged") {
		t.Errorf("the WARNING must carry the count so it survives DEBUG_LEVEL=warning, got %q", warn)
	}
	// The findings are still printed, just below the verdict.
	for _, want := range []string{
		"2 possible ProxSave cron line(s) under /etc",
		"0 5 * * * root /usr/local/bin/proxsave --backup",
		"17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
	} {
		if !strings.Contains(seen, want) {
			t.Errorf("the block must still state %q, out=%q", want, seen)
		}
	}
}

// The counterweight: an ordinary host must not be told anything about /etc.
func TestApplyCronModeStaysSilentWithoutASystemCronFinding(t *testing.T) {
	cfg, configPath := cronModeFixture(t)

	crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }
	wrapperCronLinesFn = func([]string) []string { return nil }
	migrateLegacyCronEntriesFn = func(context.Context, string, string, *logging.BootstrapLogger, string) {}
	systemCronProxsaveRefsFn = func() []indirectCronRef { return nil }

	seen := captureConsole(t, func() {
		if _, err := applyCronMode(context.Background(), cfg, configPath, "/usr/local/bin/proxsave", logging.NewBootstrapLogger(), true); err != nil {
			t.Fatalf("applyCronMode: %v", err)
		}
	})
	if strings.Contains(seen, "/etc") {
		t.Errorf("a host with no system-cron finding must hear nothing about /etc, got:\n%s", seen)
	}
}

// captureConsole runs fn with BOTH os.Stdout and os.Stderr replaced by one pipe and returns
// everything written, in order. Both streams are captured because that is what an operator
// sees: the daemon paths narrate status as bare text on stdout (bootstrap.Println) and put
// only real faults on stderr (bootstrap.Warning), so a test that watched one stream would
// silently stop seeing a message the day its level changed.
//
// The pipe is drained in a goroutine so a message larger than the pipe buffer cannot
// deadlock the test, and the streams are restored before the read completes.
func captureConsole(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	os.Stdout, os.Stderr = origOut, origErr
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
