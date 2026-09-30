// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// handoverFixture gives the helper a STATEFUL fake crontab: what the reader returns reflects
// what the writer wrote. That is what makes the ORDER observable. With a fixed reader stub the
// adoption would find the proxsave line whether it ran before or after the removal, and the
// ordering this function exists to guarantee would be pinned by nothing.
func handoverFixture(t *testing.T, lines []string) (string, func() []string) {
	t.Helper()
	origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
	origPaths := systemCronPaths
	t.Cleanup(func() {
		crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite
		systemCronPaths = origPaths
	})
	state := append([]string(nil), lines...)
	crontabReadLinesFn = func(context.Context) ([]string, error) { return append([]string(nil), state...), nil }
	crontabWriteLinesFn = func(_ context.Context, next []string) error {
		state = append([]string(nil), next...)
		return nil
	}
	systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}

	configPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\nSCHEDULER_TIME=02:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, func() []string { return state }
}

// reportsUnremovedCronEntry is the removal block for a line the switch could not delete: the
// entry and its schedule as a detail, the reason, then the WARNING outcome, in that order.
func reportsUnremovedCronEntry(out, schedule string) bool {
	parts := []string{
		"Removing proxsave cron entry...",
		"cron removal: crontab write failed error=",
		"cron removal: still present line=",
		"  Cron entry: " + schedule,
		"Crontab could not be written",
		"WARNING  \u26a0 Proxsave cron entry: not removed, remove it by hand to avoid a double backup",
	}
	at := 0
	for _, p := range parts {
		i := strings.Index(out[at:], p)
		if i < 0 {
			return false
		}
		at += i + len(p)
	}
	return !strings.Contains(out, "daemon: failed to remove the cron entry")
}

// prepareCronHandoverForDaemon is the part of the cron -> daemon switch that a test can drive:
// everything around it in applyDaemonMode shells out to systemctl. Three guarantees live here
// and each one used to be unpinned, so deleting the line that provides it left the suite green.
func TestPrepareCronHandoverForDaemon(t *testing.T) {
	t.Run("the run time is adopted BEFORE the line that carries it is deleted", func(t *testing.T) {
		configPath, crontab := handoverFixture(t, []string{
			"0 21 * * * /usr/local/bin/proxsave --backup",
			"0 6 * * * /usr/bin/rsync /a /b",
		})

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		// 21:00 is only knowable from the cron line, and that line is gone by the time this
		// function returns. Adopting after the removal reads an emptied crontab and keeps 02:00.
		if !strings.Contains(string(data), "SCHEDULER_TIME=21:00") {
			t.Errorf("the run time must be adopted before the removal, config:\n%s", data)
		}
		if outcome.Removed != 1 || !outcome.Verified {
			t.Errorf("the proxsave line must still be removed and counted, got %+v", outcome)
		}
		remaining := crontab()
		if len(remaining) != 1 || !strings.Contains(remaining[0], "rsync") {
			t.Errorf("the operator's unrelated line must survive, got %v", remaining)
		}
	})

	t.Run("a surviving unmanaged schedule is counted into the outcome", func(t *testing.T) {
		wrapper := "30 02 * * * " + absentWrapper(t)
		configPath, _ := handoverFixture(t, []string{wrapper})

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		// The count is the only thing that reaches the dashboard result screen, where the log
		// is muted for the whole operation.
		if outcome.UnmanagedSchedules != 1 {
			t.Errorf("UnmanagedSchedules = %d, want 1", outcome.UnmanagedSchedules)
		}
	})

	t.Run("an ordinary host: nothing adopted, nothing counted", func(t *testing.T) {
		configPath, _ := handoverFixture(t, []string{"0 6 * * * /usr/bin/rsync /a /b"})

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "SCHEDULER_TIME=02:00") {
			t.Errorf("no proxsave line means no adoption, config:\n%s", data)
		}
		if outcome.UnmanagedSchedules != 0 || outcome.Removed != 0 {
			t.Errorf("nothing to remove and nothing unmanaged, got %+v", outcome)
		}
		if !outcome.Verified {
			t.Error("the crontab was read, so the outcome must be verified")
		}
	})
}

// The adoption writes SCHEDULER_TIME before the removal deletes the line that carries it, and
// that order is right: the line is the only record of the hour. What was missing is what happens
// when the removal then FAILS. The line survives at 21:00 and the daemon has just been pointed at
// 21:00, so the two meet in the same minute every night and one exits 16 - a collision ProxSave
// created itself, on a host that had exactly one schedule before.
//
// Two guards, in the order the operator meets them: read the crontab ONCE up front so an
// unreadable one is known before anything is written, and REPORT the surviving entry when the
// removal did not take it away.
//
// The second used to put the hour back instead, and that was worse than doing nothing.
// SCHEDULER_TIME is a template variable ProxSave owns, so a failed crontab write is no reason to
// rewrite it, and where the variable had been absent the restore wrote the compiled default -
// turning "never recorded" into "recorded as 02:00", which is the gate that stops any later
// install or upgrade adopting the host's real run time from the line that is still there.
func TestPrepareCronHandoverProtectsTheAdoptedTime(t *testing.T) {
	seed := func(t *testing.T) string {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "backup.env")
		if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\nSCHEDULER_TIME=02:00\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return configPath
	}
	pinPaths := func(t *testing.T) {
		t.Helper()
		orig := systemCronPaths
		t.Cleanup(func() { systemCronPaths = orig })
		systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}
	}
	storedTime := func(t *testing.T, configPath string) string {
		t.Helper()
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "SCHEDULER_TIME=") {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "SCHEDULER_TIME="))
			}
		}
		return ""
	}

	t.Run("removal succeeds: the adopted hour stands", func(t *testing.T) {
		pinPaths(t)
		configPath := seed(t)
		origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
		t.Cleanup(func() { crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite })
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error { return nil }

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		if got := storedTime(t, configPath); got != "21:00" {
			t.Errorf("SCHEDULER_TIME = %q, want the adopted 21:00", got)
		}
		if outcome.Removed != 1 {
			t.Errorf("the line must be removed, got %+v", outcome)
		}
	})

	// SCHEDULER_TIME is a template variable ProxSave owns; a failed removal is no reason to
	// rewrite it, and rewriting it was worse than leaving it. Writing the compiled default over
	// an absent variable turned "never recorded" into "recorded as 02:00", which is the gate
	// that stops any later install or upgrade adopting the host's real run time from the
	// crontab. And when the surviving line ran at 02:00 already, the restore put the daemon in
	// exactly that minute while announcing it had avoided one.
	//
	// The adopted hour stays, and the operator is told the line is still there. That is the only
	// thing that can actually fix it: ProxSave has just proved it cannot remove that line.
	// The warning was gated on the ADOPTION, and an adoption only happens when the hour actually
	// changes. On the host ProxSave installed itself - SCHEDULER_TIME already equal to the cron
	// line's hour - nothing is adopted, so nothing was said, on exactly the host where the
	// surviving line and the daemon are guaranteed to share the minute.
	t.Run("nothing adopted because the hour already matched: still reported", func(t *testing.T) {
		pinPaths(t)
		configPath := seed(t) // SCHEDULER_TIME=02:00
		origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
		t.Cleanup(func() { crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite })
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"0 2 * * * /usr/local/bin/proxsave --backup"}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			return errors.New("crontab update failed: read-only file system")
		}

		origLog := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)

		prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		if !reportsUnremovedCronEntry(buf.String(), "daily at 02:00") {
			t.Errorf("the surviving entry must be reported whether or not an hour was adopted, out=%q", buf.String())
		}
	})

	// And nothing to remove is nothing to report.
	t.Run("no proxsave line at all: nothing is reported", func(t *testing.T) {
		pinPaths(t)
		configPath := seed(t)
		origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
		t.Cleanup(func() { crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite })
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"0 6 * * * /usr/bin/rsync /a /b"}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			return errors.New("crontab update failed: read-only file system")
		}

		origLog := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)

		prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		if strings.Contains(buf.String(), "Removing proxsave cron entry") || strings.Contains(buf.String(), "not removed") {
			t.Errorf("there was no proxsave entry to remove, out=%q", buf.String())
		}
	})

	// The variable was PRESENT and said something else. The adoption overwrote it, the removal
	// failed, and the adopted hour still stays: only removing the line fixes anything, and
	// rewriting ProxSave's own variable on a failed crontab write fixes nothing.
	t.Run("removal fails with a recorded hour: the adopted one stays", func(t *testing.T) {
		pinPaths(t)
		configPath := seed(t) // SCHEDULER_TIME=02:00
		origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
		t.Cleanup(func() { crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite })
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			return errors.New("crontab update failed: read-only file system")
		}

		origLog := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)

		prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		if got := storedTime(t, configPath); got != "21:00" {
			t.Errorf("SCHEDULER_TIME = %q, want the adopted 21:00 left alone", got)
		}
		if !reportsUnremovedCronEntry(buf.String(), "daily at 21:00") {
			t.Errorf("the surviving entry must be reported, out=%q", buf.String())
		}
	})

	t.Run("removal fails: the adopted hour stays and the line is reported", func(t *testing.T) {
		pinPaths(t)
		configPath := filepath.Join(t.TempDir(), "backup.env")
		if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
		t.Cleanup(func() { crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite })
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			return []string{"0 21 * * * /usr/local/bin/proxsave --backup"}, nil
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			return errors.New("crontab update failed: read-only file system")
		}

		origLog := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(origLog) })
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		if got := storedTime(t, configPath); got != "21:00" {
			t.Errorf("SCHEDULER_TIME = %q, want the adopted 21:00 left alone", got)
		}
		if !reportsUnremovedCronEntry(buf.String(), "daily at 21:00") {
			t.Errorf("the operator must be told the line is still there and at what time, out=%q", buf.String())
		}
		if strings.Contains(buf.String(), "put back") {
			t.Errorf("nothing is put back any more, out=%q", buf.String())
		}
		if outcome.Verified {
			t.Errorf("a failed write may not be reported as verified, got %+v", outcome)
		}
	})

	t.Run("the crontab cannot be read: one read, nothing written, and it is said", func(t *testing.T) {
		pinPaths(t)
		configPath := seed(t)
		origRead, origWrite := crontabReadLinesFn, crontabWriteLinesFn
		t.Cleanup(func() { crontabReadLinesFn, crontabWriteLinesFn = origRead, origWrite })
		reads := 0
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			reads++
			return nil, errors.New(`exec: "crontab": executable file not found in $PATH`)
		}
		crontabWriteLinesFn = func(context.Context, []string) error {
			t.Error("an unreadable crontab must not be written to")
			return nil
		}

		orig := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(orig) })
		var buf bytes.Buffer
		def := logging.New(types.LogLevelDebug, false)
		def.SetOutput(&buf)
		logging.SetDefaultLogger(def)

		outcome := prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

		if got := storedTime(t, configPath); got != "02:00" {
			t.Errorf("SCHEDULER_TIME = %q, want it untouched", got)
		}
		if outcome.Verified {
			t.Errorf("nothing could be read, so nothing is verified, got %+v", outcome)
		}
		// The point of the up-front read is the ORDER: the failure is known while the config is
		// still untouched. On this path it is also the only read, because the guard returns
		// before the steps that would read again.
		if reads != 1 {
			t.Errorf("an unreadable crontab must stop the handover at the first read, got %d reads", reads)
		}
		if !strings.Contains(buf.String(), "could not be read") {
			t.Errorf("the operator must be told the crontab could not be read, out=%q", buf.String())
		}
	})
}

// A removal that worked is a block too: its evidence names the line it took away, and the
// outcome comes last. The two blocks keep their order: adoption first, removal second.
func TestPrepareCronHandoverReportsTheRemoval(t *testing.T) {
	const line = "0 3 * * 1 /usr/local/bin/proxsave --backup"
	configPath, crontab := handoverFixture(t, []string{line})
	_, buf := captureDefaultLog(t)

	prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

	if len(crontab()) != 0 {
		t.Fatalf("the line must be removed, crontab=%v", crontab())
	}
	out := buf.String()
	assertInOrder(t, out,
		scheduleAdoptHeader, "✓ Backup schedule: adopted from cron entry",
		"Removing proxsave cron entry...",
		`cron removal: removed line="`+line+`" verified=true`,
		"✓ Proxsave cron entry: removed")
	if strings.Contains(out, "not removed") {
		t.Errorf("a removal that worked may not warn, out=%q", out)
	}
}

// The detail names the whole cadence of the line that stayed, not only its time.
func TestPrepareCronHandoverNamesTheWeeklyEntryItCouldNotRemove(t *testing.T) {
	configPath, _ := handoverFixture(t, []string{"0 3 * * 1 /usr/local/bin/proxsave --backup"})
	crontabWriteLinesFn = func(context.Context, []string) error {
		return errors.New("crontab update failed: read-only file system")
	}
	def, buf := captureDefaultLog(t)

	prepareCronHandoverForDaemon(context.Background(), configPath, "/usr/local/bin/proxsave", nil)

	if !reportsUnremovedCronEntry(buf.String(), "weekly, Monday at 03:00") {
		t.Errorf("the surviving weekly entry must be reported, out=%q", buf.String())
	}
	if def.WarningCount() != 1 {
		t.Errorf("warnings = %d, want exactly the removal one", def.WarningCount())
	}
}
