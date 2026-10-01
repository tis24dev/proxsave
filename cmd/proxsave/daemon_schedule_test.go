package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/serverbot"
	"github.com/tis24dev/proxsave/internal/types"
)

// scheduleNow is Wednesday 2026-09-30 11:58:32: the next Monday 02:00 is 4d 14h1m28s away,
// the example the approved journal line was written from.
var scheduleNow = time.Date(2026, 9, 30, 11, 58, 32, 0, time.UTC)

const scheduleSource = "/opt/proxsave/env/backup.env"

// syncLogSink is a log sink that background goroutines may write while a test reads it.
// onEntry, when set, runs for every entry before it is stored, on the writing goroutine.
type syncLogSink struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	onEntry func(entry string)
}

func (s *syncLogSink) Write(p []byte) (int, error) {
	if s.onEntry != nil {
		s.onEntry(string(p))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureDaemonLog swaps the default logger for a debug logger writing into a buffer and
// returns a function that yields what was logged, one "LEVEL message" entry per line with the
// timestamp column dropped and the message kept verbatim (its leading indentation included).
func captureDaemonLog(t *testing.T) func() []string {
	return captureDaemonLogWith(t, nil)
}

// captureDaemonLogWith is captureDaemonLog with a hook run on every entry as it is written.
func captureDaemonLogWith(t *testing.T, onEntry func(entry string)) func() []string {
	t.Helper()
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	sink := &syncLogSink{onEntry: onEntry}
	l := logging.New(types.LogLevelDebug, false)
	l.SetOutput(sink)
	logging.SetDefaultLogger(l)
	return func() []string {
		var out []string
		for _, line := range strings.Split(strings.TrimRight(sink.String(), "\n"), "\n") {
			_, rest, ok := strings.Cut(line, "] ")
			if !ok || len(rest) < 9 {
				continue
			}
			// The level column is 9 wide ("DEBUG    ", "INFO     ", "WARNING  ").
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
		configPath: scheduleSource,
		now:        func() time.Time { return scheduleNow },
	}
}

// runScheduleStart drives what run() does for the schedule: the start block, then scheduleLoop
// through its first next-backup line, with a context that is already cancelled so no backup
// is ever launched: the timer is hours or days out, so the select can only take ctx.Done().
func runScheduleStart(t *testing.T, d *daemon) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.logScheduleStart(context.Background())
	if d.scheduleLoop(ctx) {
		t.Fatal("a cancelled scheduleLoop must not report an abandon")
	}
}

// assertLogExact fails unless the log is exactly want, line for line.
func assertLogExact(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("log:\n%s\nwant exactly:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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

func countLogLines(got []string, line string) int {
	n := 0
	for _, l := range got {
		if l == line {
			n++
		}
	}
	return n
}

func TestDaemonStartLineKeepsDetailsInDebug(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
	d.cfg.MaxRunDuration = time.Hour
	d.logStart()
	assertLogExact(t, logs(),
		"DEBUG daemon start: run_at=02:00 max_run=1h0m0s healthcheck=true mode=centralized",
		"INFO ProxSave daemon starting",
	)
}

func TestScheduleSelfWeeklyBlockAndNextRun(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source="+scheduleSource,
		"DEBUG schedule: healthchecks mode=self, no relay negotiation",
		"DEBUG schedule: saved configured frequency=weekly healthchecks=self to "+stateFile,
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 02:00",
		"INFO   Backup check: pinged weekly on your own server",
		"INFO ✓ Backup schedule: applied",
		"DEBUG schedule: next run frequency=weekly weekday=mon monthday=1 time=02:00 at=2026-10-05T02:00:00Z",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)
}

func TestScheduleSelfMonthlyNamesTheDayAndTheCheckPeriod(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "monthly", "mon", "15", "03:30", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=monthly weekday=mon monthday=15 time=03:30 source="+scheduleSource,
		"DEBUG schedule: healthchecks mode=self, no relay negotiation",
		"DEBUG schedule: saved configured frequency=monthly healthchecks=self to "+stateFile,
		"INFO   Frequency: monthly",
		"INFO   Day of month: 15",
		"INFO   Time: 03:30",
		"INFO   Backup check: pinged monthly on your own server",
		"INFO ✓ Backup schedule: applied",
		"DEBUG schedule: next run frequency=monthly weekday=mon monthday=15 time=03:30 at=2026-10-15T03:30:00Z",
		"INFO daemon: next backup at 2026-10-15 03:30 (in 14d 15h31m28s)",
	)
}

func TestScheduleSelfDailyHasNoBackupCheckLine(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "daily", "mon", "1", "02:00", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=daily weekday=mon monthday=1 time=02:00 source="+scheduleSource,
		"DEBUG schedule: healthchecks mode=self, no relay negotiation",
		"DEBUG schedule: saved configured frequency=daily healthchecks=self to "+stateFile,
		"INFO   Frequency: daily",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
}

func TestScheduleHealthchecksDisabledAppliesWithoutBackupCheck(t *testing.T) {
	logs := captureDaemonLog(t)
	// Mode centralized in the file, but healthchecks off: nothing to negotiate, applied at once.
	d := scheduleDaemon(t, "monthly", "mon", "15", "02:00", false, config.HealthcheckModeCentralized)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=monthly weekday=mon monthday=15 time=02:00 source="+scheduleSource,
		"DEBUG schedule: healthchecks disabled, no relay negotiation",
		"DEBUG schedule: saved configured frequency=monthly healthchecks=off to "+stateFile,
		"INFO   Frequency: monthly",
		"INFO   Day of month: 15",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"DEBUG schedule: next run frequency=monthly weekday=mon monthday=15 time=02:00 at=2026-10-15T02:00:00Z",
		"INFO daemon: next backup at 2026-10-15 02:00 (in 14d 14h1m28s)",
	)
}

// TestScheduleCentralizedNoRelaySecretIsPending: with no relay secret, and none provisioned (no
// ServerID: nothing was asked of the relay), no poll can be sent. The start prints the blocks of
// an unreachable relay (part B, point 3b): daily in effect, nothing confirmed on disk.
func TestScheduleCentralizedNoRelaySecretIsPending(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source="+scheduleSource,
		"DEBUG schedule: read last_confirmed=none source="+stateFile+" default=daily",
		"DEBUG schedule: not sent, no relay secret on disk (centralized provisioning pending)",
		"DEBUG schedule: fallback frequency=daily reason=unconfirmed retry_every=5m0s",
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 02:00",
		"INFO   In effect: daily at 02:00",
		"INFO ProxSave HC Server not reachable",
		"INFO ⚠ Backup schedule: pending, no action needed",
		"INFO Applying notify level...",
		`DEBUG notify policy: read notify_on="" requested=always channels=none source=`+scheduleSource,
		"DEBUG notify policy: same response as schedule, not sent",
		"INFO   Notify level: always",
		"INFO   In effect: always",
		"INFO ProxSave HC Server not reachable",
		"INFO ⚠ Notify level: pending, no action needed",
		"INFO Applying healthchecks ping URLs...",
		"DEBUG ping urls: same response as schedule, not sent",
		"DEBUG ping urls: fallback cached alive_url=none backup_url=none source="+scheduleSource,
		"INFO   Ping URLs: none",
		"INFO ProxSave HC Server not reachable",
		"WARNING ⚠ Healthchecks ping URLs: not available",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
	assertNoLogLine(t, logs(), "centralized fetch failed")
}

// TestScheduleCentralizedNoRelaySecretWhy: the why line of a start with no relay secret says how
// far the provisioning attempt got: no HTTP answer is not reachable, every answer that gave no
// secret is not ready.
func TestScheduleCentralizedNoRelaySecretWhy(t *testing.T) {
	cases := []struct {
		name string
		fail func() error
		why  string
	}{
		{"transport", func() error {
			return fmt.Errorf("relay provision: request failed: %w", &serverbot.TransportError{Op: "request", Stage: "connect"})
		}, "ProxSave HC Server not reachable"},
		{"rate limited", func() error { return &notify.RelayProvisionRateLimitError{RetryAfter: time.Minute} }, "ProxSave HC Server not ready"},
		{"unexpected status", func() error { return errors.New("relay provision: unexpected status 500") }, "ProxSave HC Server not ready"},
		{"already provisioned", func() error { return nil }, "ProxSave HC Server not ready"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := provisionRelaySecretFn
			t.Cleanup(func() { provisionRelaySecretFn = orig })
			provisionRelaySecretFn = func(context.Context, string, string, string, *logging.Logger) (bool, error) {
				return false, tc.fail()
			}
			logs := captureDaemonLog(t)
			d := scheduleDaemon(t, "daily", "mon", "1", "02:00", true, config.HealthcheckModeCentralized)
			d.cfg.ServerID = "123456789012"
			runScheduleStart(t, d)
			got := logs()
			assertLogSequence(t, got,
				"INFO "+tc.why,
				"INFO ⚠ Backup schedule: pending, no action needed",
				"INFO "+tc.why,
				"INFO ⚠ Notify level: pending, no action needed",
				"INFO "+tc.why,
				"WARNING ⚠ Healthchecks ping URLs: not available",
			)
			if n := countLogLines(got, "INFO "+tc.why); n != 3 {
				t.Fatalf("why line %q written %d times; want 3", tc.why, n)
			}
		})
	}
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

// TestScheduleConfirmedCadenceInvalidFallsBackToTheConfiguredTime: a confirmedCadence seam
// that returns a cadence Cadence.Next refuses runs daily at SCHEDULER_TIME when that is valid,
// at 02:00 when it is empty or invalid; never on the seam's own time.
func TestScheduleConfirmedCadenceInvalidFallsBackToTheConfiguredTime(t *testing.T) {
	invalidSeam := func(cron.Cadence) cron.Cadence {
		return cron.Cadence{Frequency: cron.FrequencyWeekly, Weekday: time.Weekday(9), MonthDay: 1, Time: "23:00"}
	}
	cases := []struct {
		name, hhmm, wantDebug, wantNext string
	}{
		{"valid time", "04:15",
			"DEBUG schedule: cadence in effect invalid error=invalid weekday 9, using daily at 04:15",
			"INFO daemon: next backup at 2026-10-01 04:15 (in 16h16m28s)"},
		{"empty time", "",
			"DEBUG schedule: cadence in effect invalid error=invalid weekday 9, using daily at 02:00",
			"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)"},
		{"invalid time", "25:99",
			"DEBUG schedule: cadence in effect invalid error=invalid weekday 9, using daily at 02:00",
			"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureDaemonLog(t)
			d := scheduleDaemon(t, "weekly", "mon", "1", tc.hhmm, true, config.HealthcheckModeCentralized)
			d.confirmedCadence = invalidSeam
			runScheduleStart(t, d)
			assertLogSequence(t, logs(), tc.wantDebug, tc.wantNext)
		})
	}
}

// TestScheduleWithoutFrequencyStaysDaily: a backup.env that predates SCHEDULER_FREQUENCY
// (the three values empty) keeps today's daily run at SCHEDULER_TIME, with nothing flagged.
func TestScheduleWithoutFrequencyStaysDaily(t *testing.T) {
	for _, mode := range []string{config.HealthcheckModeCentralized, config.HealthcheckModeSelf} {
		t.Run(mode, func(t *testing.T) {
			logs := captureDaemonLog(t)
			d := scheduleDaemon(t, "", "", "", "03:00", true, mode)
			// The URLs cached in backup.env: a centralized start with no relay secret then
			// reports them not refreshed, an INFO, instead of a WARNING with none at all.
			d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "https://hc.invalid/ping/alive", "https://hc.invalid/ping/backup"
			runScheduleStart(t, d)
			got := logs()
			assertLogSequence(t, got, "INFO daemon: next backup at 2026-10-01 03:00 (in 15h1m28s)")
			assertNoLogLine(t, got, "ERROR")
			assertNoLogLine(t, got, "WARNING")
			assertNoLogLine(t, got, "invalid")
			assertNoLogLine(t, got, "not negotiated")
		})
	}
}

// TestScheduleEmptyTimeIsTheSilentDefault: SCHEDULER_TIME present but empty is 02:00, with no
// ERROR or WARNING: the normal applied block, and the default recorded in DEBUG.
func TestScheduleEmptyTimeIsTheSilentDefault(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "fri", "1", "", false, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=fri monthday=1 time= source="+scheduleSource,
		"DEBUG schedule: time empty, default 02:00",
		"DEBUG schedule: healthchecks disabled, no relay negotiation",
		"DEBUG schedule: saved configured frequency=weekly healthchecks=off to "+stateFile,
		"INFO   Frequency: weekly",
		"INFO   Weekday: Friday",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"DEBUG schedule: next run frequency=weekly weekday=fri monthday=1 time=02:00 at=2026-10-02T02:00:00Z",
		"INFO daemon: next backup at 2026-10-02 02:00 (in 1d 14h1m28s)",
	)
}

// TestScheduleInvalidUnusedDayIsIgnored: a day the frequency does not use cannot move the
// backup, so an invalid one is recorded in DEBUG and the schedule is applied.
func TestScheduleInvalidUnusedDayIsIgnored(t *testing.T) {
	t.Run("weekday unused by monthly", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "monthly", "someday", "15", "02:00", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=monthly weekday=someday monthday=15 time=02:00 source="+scheduleSource,
			`DEBUG schedule: weekday="someday" unused by frequency=monthly, ignored`,
			"DEBUG schedule: healthchecks disabled, no relay negotiation",
			"DEBUG schedule: saved configured frequency=monthly healthchecks=off to "+stateFile,
			"INFO   Frequency: monthly",
			"INFO   Day of month: 15",
			"INFO   Time: 02:00",
			"INFO ✓ Backup schedule: applied",
			"DEBUG schedule: next run frequency=monthly weekday=mon monthday=15 time=02:00 at=2026-10-15T02:00:00Z",
			"INFO daemon: next backup at 2026-10-15 02:00 (in 14d 14h1m28s)",
		)
	})
	t.Run("monthday unused by weekly", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "weekly", "mon", "31", "02:00", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=weekly weekday=mon monthday=31 time=02:00 source="+scheduleSource,
			`DEBUG schedule: monthday="31" unused by frequency=weekly, ignored`,
			"DEBUG schedule: healthchecks disabled, no relay negotiation",
			"DEBUG schedule: saved configured frequency=weekly healthchecks=off to "+stateFile,
			"INFO   Frequency: weekly",
			"INFO   Weekday: Monday",
			"INFO   Time: 02:00",
			"INFO ✓ Backup schedule: applied",
			"DEBUG schedule: next run frequency=weekly weekday=mon monthday=1 time=02:00 at=2026-10-05T02:00:00Z",
			"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
		)
	})
	t.Run("both days unused by daily", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "daily", "someday", "31", "02:00", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=daily weekday=someday monthday=31 time=02:00 source="+scheduleSource,
			`DEBUG schedule: weekday="someday" unused by frequency=daily, ignored`,
			`DEBUG schedule: monthday="31" unused by frequency=daily, ignored`,
			"DEBUG schedule: healthchecks disabled, no relay negotiation",
			"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
			"INFO   Frequency: daily",
			"INFO   Time: 02:00",
			"INFO ✓ Backup schedule: applied",
			"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
			"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
		)
	})
}

func TestScheduleInvalidFrequencyRunsDailyAtTheConfiguredTime(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "fortnightly", "mon", "1", "04:15", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=fortnightly weekday=mon monthday=1 time=04:15 source="+scheduleSource,
		`DEBUG schedule: invalid SCHEDULER_FREQUENCY="fortnightly" error=frequency must be daily, weekly, or monthly`,
		"DEBUG schedule: saved configured frequency=daily healthchecks=self to "+stateFile,
		"INFO   Frequency: fortnightly",
		"INFO   Time: 04:15",
		"INFO   In effect: daily at 04:15",
		`INFO Frequency "fortnightly" is not daily, weekly or monthly`,
		"WARNING ⚠ Backup schedule: not applied",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=04:15 at=2026-10-01T04:15:00Z",
		"INFO daemon: next backup at 2026-10-01 04:15 (in 16h16m28s)",
	)
}

// TestScheduleInvalidUsedDayRunsDailyAtTheConfiguredTime: the day the frequency uses is
// invalid, so no day line; the run is daily at SCHEDULER_TIME.
func TestScheduleInvalidUsedDayRunsDailyAtTheConfiguredTime(t *testing.T) {
	t.Run("weekday", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "weekly", "someday", "1", "04:15", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=weekly weekday=someday monthday=1 time=04:15 source="+scheduleSource,
			`DEBUG schedule: invalid SCHEDULER_WEEKDAY="someday" error=weekday must be one of mon, tue, wed, thu, fri, sat, sun`,
			"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
			"INFO   Frequency: weekly",
			"INFO   Time: 04:15",
			"INFO   In effect: daily at 04:15",
			`INFO Weekday "someday" is not mon-sun`,
			"WARNING ⚠ Backup schedule: not applied",
			"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=04:15 at=2026-10-01T04:15:00Z",
			"INFO daemon: next backup at 2026-10-01 04:15 (in 16h16m28s)",
		)
	})
	t.Run("monthday", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "monthly", "mon", "31", "04:15", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=monthly weekday=mon monthday=31 time=04:15 source="+scheduleSource,
			`DEBUG schedule: invalid SCHEDULER_MONTHDAY="31" error=day of month must be between 1 and 28`,
			"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
			"INFO   Frequency: monthly",
			"INFO   Time: 04:15",
			"INFO   In effect: daily at 04:15",
			"INFO Day 31 is outside 1-28",
			"WARNING ⚠ Backup schedule: not applied",
			"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=04:15 at=2026-10-01T04:15:00Z",
			"INFO daemon: next backup at 2026-10-01 04:15 (in 16h16m28s)",
		)
	})
}

// TestScheduleNotAppliedShowsTheDefaultOfAnEmptyValue: in the not-applied block an empty
// variable is shown as the default it stands for (it is not invalid), never as a blank value.
func TestScheduleNotAppliedShowsTheDefaultOfAnEmptyValue(t *testing.T) {
	t.Run("empty time", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "fortnightly", "mon", "1", "", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=fortnightly weekday=mon monthday=1 time= source="+scheduleSource,
			`DEBUG schedule: invalid SCHEDULER_FREQUENCY="fortnightly" error=frequency must be daily, weekly, or monthly`,
			"DEBUG schedule: time empty, default 02:00",
			"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
			"INFO   Frequency: fortnightly",
			"INFO   Time: 02:00",
			"INFO   In effect: daily at 02:00",
			`INFO Frequency "fortnightly" is not daily, weekly or monthly`,
			"WARNING ⚠ Backup schedule: not applied",
			"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
			"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
		)
	})
	t.Run("empty frequency", func(t *testing.T) {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, "", "mon", "1", "25:99", false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertLogExact(t, normalized(logs(), d),
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency= weekday=mon monthday=1 time=25:99 source="+scheduleSource,
			`DEBUG schedule: invalid SCHEDULER_TIME="25:99" error=cron hour must be between 00 and 23`,
			"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
			"INFO   Frequency: daily",
			"INFO   Time: 25:99",
			"INFO   In effect: daily at 02:00",
			`INFO Time "25:99" is not HH:MM`,
			"WARNING ⚠ Backup schedule: not applied",
			"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
			"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
		)
	})
}

// TestScheduleNonNumericMonthDayIsQuoted: a day of month that is not a number gets the quoted
// line; a number outside the range keeps "Day 31 is outside 1-28" (TestScheduleInvalidUsedDay...).
func TestScheduleNonNumericMonthDayIsQuoted(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "monthly", "mon", "abc", "02:00", false, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=monthly weekday=mon monthday=abc time=02:00 source="+scheduleSource,
		`DEBUG schedule: invalid SCHEDULER_MONTHDAY="abc" error=day of month must be between 1 and 28`,
		"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
		"INFO   Frequency: monthly",
		"INFO   Time: 02:00",
		"INFO   In effect: daily at 02:00",
		`INFO Day "abc" is not 1-28`,
		"WARNING ⚠ Backup schedule: not applied",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
}

// TestScheduleInvalidTimeRunsDailyAt0200: a non-empty invalid SCHEDULER_TIME drops the whole
// cadence to daily at 02:00; the weekly frequency is not kept. The valid day is still shown.
func TestScheduleInvalidTimeRunsDailyAt0200(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "mon", "1", "25:99", false, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=25:99 source="+scheduleSource,
		`DEBUG schedule: invalid SCHEDULER_TIME="25:99" error=cron hour must be between 00 and 23`,
		"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 25:99",
		"INFO   In effect: daily at 02:00",
		`INFO Time "25:99" is not HH:MM`,
		"WARNING ⚠ Backup schedule: not applied",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
}

// TestScheduleTwoInvalidValues is the approved example: frequency=fortnightly, time=25:99.
func TestScheduleTwoInvalidValues(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "fortnightly", "mon", "1", "25:99", true, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=fortnightly weekday=mon monthday=1 time=25:99 source="+scheduleSource,
		`DEBUG schedule: invalid SCHEDULER_FREQUENCY="fortnightly" error=frequency must be daily, weekly, or monthly`,
		`DEBUG schedule: invalid SCHEDULER_TIME="25:99" error=cron hour must be between 00 and 23`,
		"DEBUG schedule: saved configured frequency=daily healthchecks=self to "+stateFile,
		"INFO   Frequency: fortnightly",
		"INFO   Time: 25:99",
		"INFO   In effect: daily at 02:00",
		`INFO Frequency "fortnightly" is not daily, weekly or monthly`,
		`INFO Time "25:99" is not HH:MM`,
		"WARNING ⚠ Backup schedule: not applied",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
}

// TestScheduleInvalidDayAndTimeKeepTheWhyOrder: the why-lines follow FREQUENCY, WEEKDAY,
// MONTHDAY, TIME, whatever order the checks run in.
func TestScheduleInvalidDayAndTimeKeepTheWhyOrder(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "monthly", "mon", "0", "7pm", false, config.HealthcheckModeSelf)
	runScheduleStart(t, d)
	assertLogExact(t, normalized(logs(), d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=monthly weekday=mon monthday=0 time=7pm source="+scheduleSource,
		`DEBUG schedule: invalid SCHEDULER_MONTHDAY="0" error=day of month must be between 1 and 28`,
		`DEBUG schedule: invalid SCHEDULER_TIME="7pm" error=cron time must be in HH:MM format`,
		"DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile,
		"INFO   Frequency: monthly",
		"INFO   Time: 7pm",
		"INFO   In effect: daily at 02:00",
		"INFO Day 0 is outside 1-28",
		`INFO Time "7pm" is not HH:MM`,
		"WARNING ⚠ Backup schedule: not applied",
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
}

// TestScheduleInvalidValueCentralizedGetsTheBlock: an invalid value is reported by the
// not-applied block in centralized mode too, where a valid schedule is DEBUG only.
func TestScheduleInvalidValueCentralizedGetsTheBlock(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "weekly", "someday", "1", "03:00", true, config.HealthcheckModeCentralized)
	runScheduleStart(t, d)
	got := logs()
	assertLogSequence(t, got,
		"INFO Applying backup schedule...",
		"INFO   In effect: daily at 03:00",
		`INFO Weekday "someday" is not mon-sun`,
		"WARNING ⚠ Backup schedule: not applied",
		"INFO daemon: next backup at 2026-10-01 03:00 (in 15h1m28s)",
	)
	assertNoLogLine(t, got, "centralized frequency")
}

// TestScheduleNoErrorLinesAnyMore: the branch's "daemon: invalid SCHEDULER_..." ERROR lines
// are gone, for every kind of invalid value and for an empty SCHEDULER_TIME.
func TestScheduleNoErrorLinesAnyMore(t *testing.T) {
	for _, v := range [][4]string{
		{"fortnightly", "mon", "1", "02:00"},
		{"weekly", "someday", "1", "02:00"},
		{"monthly", "mon", "31", "02:00"},
		{"daily", "mon", "1", "25:99"},
		{"daily", "mon", "1", ""},
	} {
		logs := captureDaemonLog(t)
		d := scheduleDaemon(t, v[0], v[1], v[2], v[3], false, config.HealthcheckModeSelf)
		runScheduleStart(t, d)
		assertNoLogLine(t, logs(), "ERROR")
	}
}

// driveTwoNextBackups runs the start block at scheduleNow, then scheduleLoop through two
// next-backup lines. In the loop the first clock read sees scheduleNow, every later one
// 2026-10-01 03:00, past the first run, so the first timer fires at once; BackupEnabled is
// false, so runOnce skips without starting a child. The context is cancelled from the loop's
// third clock read on, which only the second iteration makes, so the loop returns at its
// second select.
func driveTwoNextBackups(t *testing.T, d *daemon) {
	t.Helper()
	d.logScheduleStart(context.Background())
	later := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads atomic.Int32
	d.now = func() time.Time {
		n := reads.Add(1)
		if n == 1 {
			return scheduleNow
		}
		if n >= 3 {
			cancel()
		}
		return later
	}
	done := make(chan bool, 1)
	go func() { done <- d.scheduleLoop(ctx) }()
	select {
	case abandoned := <-done:
		if abandoned {
			t.Fatal("scheduleLoop must not report an abandon")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scheduleLoop never reached its second select")
	}
}

// TestScheduleNotAppliedBlockRepeatsBeforeEveryNextBackup: while a value is invalid, the
// whole block is printed again before every later next-backup line, once per line.
func TestScheduleNotAppliedBlockRepeatsBeforeEveryNextBackup(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "fortnightly", "mon", "1", "25:99", false, config.HealthcheckModeSelf)
	driveTwoNextBackups(t, d)
	block := []string{
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=fortnightly weekday=mon monthday=1 time=25:99 source=" + scheduleSource,
		`DEBUG schedule: invalid SCHEDULER_FREQUENCY="fortnightly" error=frequency must be daily, weekly, or monthly`,
		`DEBUG schedule: invalid SCHEDULER_TIME="25:99" error=cron hour must be between 00 and 23`,
		"INFO   Frequency: fortnightly",
		"INFO   Time: 25:99",
		"INFO   In effect: daily at 02:00",
		`INFO Frequency "fortnightly" is not daily, weekly or monthly`,
		`INFO Time "25:99" is not HH:MM`,
		"WARNING ⚠ Backup schedule: not applied",
	}
	// The start saves the cadence it runs on; the repeats only report.
	var want []string
	want = append(want, block[:4]...)
	want = append(want, "DEBUG schedule: saved configured frequency=daily healthchecks=off to "+stateFile)
	want = append(want, block[4:]...)
	want = append(want,
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 0s)",
		"INFO daemon: BACKUP_ENABLED=false; skipping the scheduled run (no outcome ping)",
	)
	want = append(want, block...)
	want = append(want,
		"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-02T02:00:00Z",
		"INFO daemon: next backup at 2026-10-02 02:00 (in 23h0m0s)",
	)
	assertLogExact(t, normalized(logs(), d), want...)
}

// TestScheduleAppliedBlockIsPrintedOnce: a valid schedule is reported at start only.
func TestScheduleAppliedBlockIsPrintedOnce(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "daily", "mon", "1", "02:00", false, config.HealthcheckModeSelf)
	driveTwoNextBackups(t, d)
	got := logs()
	if n := countLogLines(got, "INFO daemon: next backup at 2026-10-02 02:00 (in 23h0m0s)"); n != 1 {
		t.Fatalf("the second next-backup line appears %d times, want 1\nlog:\n%s", n, strings.Join(got, "\n"))
	}
	if n := countLogLines(got, "INFO Applying backup schedule..."); n != 1 {
		t.Fatalf("the applied block was printed %d times, want once at start\nlog:\n%s", n, strings.Join(got, "\n"))
	}
	if n := countLogLines(got, "INFO ✓ Backup schedule: applied"); n != 1 {
		t.Fatalf("the applied outcome was printed %d times, want once at start\nlog:\n%s", n, strings.Join(got, "\n"))
	}
}

// TestScheduleBlockPrecedesTheBackgroundLoops: run() prints the schedule block before it
// starts the heartbeat and update loops. When the block's first line is written, the test
// waits for the update loop to reach its update check: with the block emitted before the
// loops start there is no loop yet and the wait expires; with the block printed after them it
// would not.
func TestScheduleBlockPrecedesTheBackgroundLoops(t *testing.T) {
	updateStarted := make(chan struct{})
	var updateOnce sync.Once
	orig := daemonEvaluateUpdate
	t.Cleanup(func() { daemonEvaluateUpdate = orig })
	daemonEvaluateUpdate = func(context.Context, *logging.Logger, string) *UpdateInfo {
		updateOnce.Do(func() { close(updateStarted) })
		return nil
	}

	var loopsFirst atomic.Bool
	scheduled := make(chan struct{})
	var scheduledOnce sync.Once
	logs := captureDaemonLogWith(t, func(entry string) {
		switch {
		case strings.Contains(entry, "Applying backup schedule..."):
			select {
			case <-updateStarted:
				loopsFirst.Store(true)
			case <-time.After(500 * time.Millisecond):
			}
		case strings.Contains(entry, "daemon: next backup at"):
			scheduledOnce.Do(func() { close(scheduled) })
		}
	})

	d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", true, config.HealthcheckModeSelf)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- d.run(ctx) }()
	select {
	case <-scheduled:
	case <-time.After(10 * time.Second):
		t.Fatal("run() never reached the first next-backup line")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return")
	}

	if loopsFirst.Load() {
		t.Fatalf("the update loop was running before the schedule block\nlog:\n%s", strings.Join(logs(), "\n"))
	}
	assertLogSequence(t, logs(),
		"INFO Applying backup schedule...",
		"INFO ✓ Backup schedule: applied",
		"DEBUG daemon: heartbeat has no url yet (recording liveness, reason=no_url)",
	)
}

// TestScheduleRefusedSecondInstancePrintsNoBlock: a second daemon refused by the single-
// instance gate returns before the schedule block, so it prints none, not even for an invalid
// value.
func TestScheduleRefusedSecondInstancePrintsNoBlock(t *testing.T) {
	logs := captureDaemonLog(t)
	d := scheduleDaemon(t, "fortnightly", "mon", "1", "25:99", false, config.HealthcheckModeSelf)
	const incumbent = 424242
	if err := health.WriteDaemonPID(d.cfg.BaseDir, incumbent); err != nil {
		t.Fatalf("seed incumbent pid: %v", err)
	}
	withProbe(t, func(pid int) bool { return pid == incumbent })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := d.run(ctx); code != types.ExitBackupSkipped.Int() {
		t.Fatalf("second daemon exit = %d, want %d", code, types.ExitBackupSkipped.Int())
	}
	got := logs()
	assertNoLogLine(t, got, "Applying backup schedule")
	assertNoLogLine(t, got, "Backup schedule:")
	assertNoLogLine(t, got, "schedule:")
}
