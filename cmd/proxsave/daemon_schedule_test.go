package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// scheduleNow is Wednesday 2026-09-30 11:58:32: the next Monday 02:00 is 4d 14h1m28s away,
// the example the approved journal line was written from.
var scheduleNow = time.Date(2026, 9, 30, 11, 58, 32, 0, time.UTC)

// captureDaemonLog swaps the default logger for a debug logger writing into a buffer and
// returns a function that yields what was logged, one "LEVEL message" entry per line with the
// timestamp column dropped and the message kept verbatim (its leading indentation included).
func captureDaemonLog(t *testing.T) func() []string {
	t.Helper()
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	var buf bytes.Buffer
	l := logging.New(types.LogLevelDebug, false)
	l.SetOutput(&buf)
	logging.SetDefaultLogger(l)
	return func() []string {
		var out []string
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			_, rest, ok := strings.Cut(line, "] ")
			if !ok || len(rest) < 9 {
				continue
			}
			// The level column is 9 wide ("DEBUG    ", "INFO     ", "ERROR    ").
			out = append(out, strings.TrimSpace(rest[:9])+" "+rest[9:])
		}
		return out
	}
}

// scheduleDaemon builds a daemon whose scheduler reads the given SCHEDULER_* values.
func scheduleDaemon(t *testing.T, freq, weekday, monthDay, hhmm string, hcEnabled bool, hcMode string) *daemon {
	t.Helper()
	return &daemon{
		cfg: &config.Config{
			BaseDir:            t.TempDir(),
			SchedulerFrequency: freq,
			SchedulerWeekday:   weekday,
			SchedulerMonthDay:  monthDay,
			SchedulerTime:      hhmm,
			HealthcheckEnabled: hcEnabled,
			HealthcheckMode:    hcMode,
		},
		configPath: "/opt/proxsave/env/backup.env",
		now:        func() time.Time { return scheduleNow },
	}
}

// runScheduleStart drives scheduleLoop through its start and its first next-backup line with
// a context that is already cancelled, so no backup is ever launched: the timer is days or
// hours out, so the select can only take ctx.Done().
func runScheduleStart(t *testing.T, d *daemon) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.scheduleLoop(ctx) {
		t.Fatal("a cancelled scheduleLoop must not report an abandon")
	}
}

func assertLogSequence(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, line := range got {
		if i < len(want) && line == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Fatalf("log is missing %q (in order)\nlog:\n%s", want[i], strings.Join(got, "\n"))
	}
}

func assertNoLogLine(t *testing.T, got []string, substr string) {
	t.Helper()
	for _, line := range got {
		if strings.Contains(line, substr) {
			t.Fatalf("log must not contain %q, found %q\nlog:\n%s", substr, line, strings.Join(got, "\n"))
		}
	}
}

func TestDaemonStartLineKeepsDetailsInDebug(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
	d.cfg.MaxRunDuration = time.Hour
	d.logStart()
	got := logs()
	want := []string{
		"DEBUG daemon start: run_at=02:00 max_run=1h0m0s healthcheck=true mode=centralized",
		"INFO ProxSave daemon starting",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("start lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestScheduleSelfWeeklyBlockAndNextRun(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogSequence(t, logs(),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source=/opt/proxsave/env/backup.env",
		"DEBUG schedule: healthchecks mode=self, no relay negotiation",
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 02:00",
		"INFO   Backup check: pinged weekly on your own server",
		"INFO ✓ Backup schedule: applied",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)
	assertNoLogLine(t, logs(), "Day of month")
}

func TestScheduleSelfMonthlyNamesTheDayAndTheCheckPeriod(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "monthly", "mon", "15", "03:30", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogSequence(t, logs(),
		"INFO Applying backup schedule...",
		"INFO   Frequency: monthly",
		"INFO   Day of month: 15",
		"INFO   Time: 03:30",
		"INFO   Backup check: pinged monthly on your own server",
		"INFO ✓ Backup schedule: applied",
		"INFO daemon: next backup at 2026-10-15 03:30 (in 14d 15h31m28s)",
	)
	assertNoLogLine(t, logs(), "Weekday:")
}

func TestScheduleSelfDailyHasNoBackupCheckLine(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "daily", "mon", "1", "02:00", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogSequence(t, logs(),
		"INFO Applying backup schedule...",
		"DEBUG schedule: healthchecks mode=self, no relay negotiation",
		"INFO   Frequency: daily",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
	assertNoLogLine(t, logs(), "Backup check")
	assertNoLogLine(t, logs(), "Weekday:")
	assertNoLogLine(t, logs(), "Day of month")
}

func TestScheduleHealthchecksDisabledAppliesWithoutBackupCheck(t *testing.T) {
	logs := captureDaemonLog(t)
	// Mode centralized in the file, but healthchecks off: nothing to negotiate, applied at once.
	d := scheduleDaemon(t, "monthly", "mon", "15", "02:00", false, config.HealthcheckModeCentralized)
	runScheduleStart(t, d)
	assertLogSequence(t, logs(),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=monthly weekday=mon monthday=15 time=02:00 source=/opt/proxsave/env/backup.env",
		"DEBUG schedule: healthchecks disabled, no relay negotiation",
		"INFO   Frequency: monthly",
		"INFO   Day of month: 15",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"INFO daemon: next backup at 2026-10-15 02:00 (in 14d 14h1m28s)",
	)
	assertNoLogLine(t, logs(), "Backup check")
}

func TestScheduleCentralizedWeeklyRunsDailyUntilNegotiated(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
	runScheduleStart(t, d)
	got := logs()
	assertLogSequence(t, got,
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source=/opt/proxsave/env/backup.env",
		"DEBUG schedule: centralized frequency=weekly not negotiated yet, in effect daily",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
	assertNoLogLine(t, got, "Applying backup schedule")
	assertNoLogLine(t, got, "Backup schedule: applied")
	assertNoLogLine(t, got, "Frequency:")
}

func TestScheduleCentralizedDailyLogsNoBlock(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "daily", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
	runScheduleStart(t, d)
	got := logs()
	assertLogSequence(t, got, "INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)")
	for _, line := range got {
		if strings.HasPrefix(line, "INFO ") && line != "INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)" {
			t.Fatalf("centralized daily must add no INFO line, got %q", line)
		}
	}
	assertNoLogLine(t, got, "not negotiated")
}

// TestScheduleCentralizedUsesTheConfirmedCadence pins the seam the relay negotiation plugs
// into: whatever it reports in effect is what the scheduler runs.
func TestScheduleCentralizedUsesTheConfirmedCadence(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
	d.confirmedCadence = func(configured cron.Cadence) cron.Cadence { return configured }
	runScheduleStart(t, d)
	got := logs()
	assertLogSequence(t, got, "INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)")
	assertNoLogLine(t, got, "not negotiated")
}

// TestScheduleWithoutFrequencyStaysDaily: a backup.env that predates SCHEDULER_FREQUENCY
// (the three values empty) keeps today's daily run at SCHEDULER_TIME, with no error.
func TestScheduleWithoutFrequencyStaysDaily(t *testing.T) {
	for _, mode := range []string{config.HealthcheckModeCentralized, config.HealthcheckModeSelf} {
		t.Run(mode, func(t *testing.T) {
			logs := captureDaemonLog(t)
			d := scheduleDaemon(t, "", "", "", "03:00", true, mode)
			runScheduleStart(t, d)
			got := logs()
			assertLogSequence(t, got, "INFO daemon: next backup at 2026-10-01 03:00 (in 15h1m28s)")
			assertNoLogLine(t, got, "ERROR")
			assertNoLogLine(t, got, "not negotiated")
		})
	}
}

func TestScheduleInvalidFrequencyFallsBackToDailyAtTheConfiguredTime(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "fortnightly", "mon", "1", "04:15", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	got := logs()
	assertLogSequence(t, got,
		"DEBUG schedule: read frequency=fortnightly weekday=mon monthday=1 time=04:15 source=/opt/proxsave/env/backup.env",
		`ERROR daemon: invalid SCHEDULER_FREQUENCY "fortnightly" (frequency must be daily, weekly, or monthly); using daily`,
		"INFO daemon: next backup at 2026-10-01 04:15 (in 16h16m28s)",
	)
	// No "applied" over a fallback, and no outcome text that was never approved.
	assertNoLogLine(t, got, "Applying backup schedule")
	assertNoLogLine(t, got, "Backup schedule:")
}

func TestScheduleInvalidDayFallsBackToDaily(t *testing.T) {
	cases := []struct {
		name, freq, weekday, monthDay, wantErr string
	}{
		{"weekday", "weekly", "someday", "1", `ERROR daemon: invalid SCHEDULER_WEEKDAY "someday" (weekday must be one of mon, tue, wed, thu, fri, sat, sun); using daily`},
		{"monthday", "monthly", "mon", "31", `ERROR daemon: invalid SCHEDULER_MONTHDAY "31" (day of month must be between 1 and 28); using daily`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureDaemonLog(t)
			d := scheduleDaemon(t, tc.freq, tc.weekday, tc.monthDay, "02:00", false, config.HealthcheckModeSelf)
			runScheduleStart(t, d)
			assertLogSequence(t, logs(), tc.wantErr, "INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)")
		})
	}
}

// TestScheduleInvalidTimeKeepsTodaysFallback: an invalid SCHEDULER_TIME is replaced by
// DefaultTime with today's ERROR line, and a valid frequency is kept.
func TestScheduleInvalidTimeKeepsTodaysFallback(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "25:99", false, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	got := logs()
	assertLogSequence(t, got,
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=25:99 source=/opt/proxsave/env/backup.env",
		`ERROR daemon: invalid SCHEDULER_TIME "25:99" (cron hour must be between 00 and 23); using 02:00`,
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)
	assertNoLogLine(t, got, "Backup schedule:")
}

// TestScheduleEmptyTimeIsStillAnError: today an empty SCHEDULER_TIME is an ERROR and 02:00,
// not a silent default; the cadence parser's empty-means-default must not change that.
func TestScheduleEmptyTimeIsStillAnError(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "", "", "", "", false, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	got := logs()
	found := false
	for _, line := range got {
		if strings.HasPrefix(line, `ERROR daemon: invalid SCHEDULER_TIME "" (`) && strings.HasSuffix(line, "); using 02:00") {
			found = true
		}
	}
	if !found {
		t.Fatalf("empty SCHEDULER_TIME must keep its ERROR line\nlog:\n%s", strings.Join(got, "\n"))
	}
	assertLogSequence(t, got, "INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)")
}
