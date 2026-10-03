package main

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

const scheduleStatusStart = int64(1790856762)

// scheduleStatusCase is a live daemon started at scheduleStatusStart from scheduleSource, with
// the given state file (nil: none) and the backup.env values loaded now.
type scheduleStatusCase struct {
	state                    *health.ScheduleState
	freq, weekday, day, hhmm string
	alive                    bool
	runtimePath, currentPath string
}

func (tc scheduleStatusCase) compare(t *testing.T) scheduleComparison {
	t.Helper()
	base := t.TempDir()
	if tc.state != nil {
		if err := health.WriteScheduleState(base, *tc.state); err != nil {
			t.Fatalf("write state: %v", err)
		}
	}
	cfg := &config.Config{BaseDir: base, ConfigPath: scheduleSource, SchedulerFrequency: tc.freq,
		SchedulerWeekday: tc.weekday, SchedulerMonthDay: tc.day, SchedulerTime: tc.hhmm}
	if tc.currentPath != "" {
		cfg.ConfigPath = tc.currentPath
	}
	runtimePath := scheduleSource
	if tc.runtimePath != "" {
		runtimePath = tc.runtimePath
	}
	state := health.DaemonState{ProcessAlive: tc.alive, StartTS: scheduleStatusStart}
	runtime := daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: runtimePath, StartTS: scheduleStatusStart}
	if !tc.alive {
		runtime = daemonRuntimeDiagnostic{Availability: daemonRuntimeNotApplicable}
	}
	return compareBackupSchedule(state, runtime, cfg, nil, base)
}

func startedState(freq, weekday string, day int, hhmm, lastConfirmed, mode string) *health.ScheduleState {
	return &health.ScheduleState{
		LastConfirmed: lastConfirmed,
		Configured:    &health.ScheduleCadence{Frequency: freq, Weekday: weekday, MonthDay: day, Time: hhmm},
		ConfiguredTS:  scheduleStatusStart,
		Healthchecks:  mode,
	}
}

// scheduleBlockLines is the CLI block of c as "LEVEL message" entries.
func scheduleBlockLines(t *testing.T, c scheduleComparison) []string {
	t.Helper()
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	logBackupSchedule(logger, c)
	return debugEntries(buf.String())
}

func TestBackupScheduleBlock(t *testing.T) {
	cases := []struct {
		name string
		tc   scheduleStatusCase
		want []string
	}{
		{"centralized confirmed is in sync",
			scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "weekly", "centralized"), freq: "weekly", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: WEEKLY (Monday at 02:00)", "INFO   Configuration: WEEKLY (Monday at 02:00)", "INFO   Synchronization: IN SYNC"}},
		{"centralized not confirmed is pending, daily in effect",
			scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "", "centralized"), freq: "weekly", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)", "INFO   Configuration: WEEKLY (Monday at 02:00)",
				"INFO   Synchronization: PENDING (ProxSave switches to weekly automatically; no action needed)"}},
		{"a layout without healthchecks was written by a centralized daemon",
			scheduleStatusCase{state: startedState("monthly", "mon", 15, "02:00", "weekly", ""), freq: "monthly", weekday: "mon", day: "15", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: WEEKLY (Monday at 02:00)", "INFO   Configuration: MONTHLY (day 15 at 02:00)",
				"INFO   Synchronization: PENDING (ProxSave switches to monthly automatically; no action needed)"}},
		{"backup.env changed after the start",
			scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "weekly", "centralized"), freq: "monthly", weekday: "mon", day: "15", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: WEEKLY (Monday at 02:00)", "INFO   Configuration: MONTHLY (day 15 at 02:00)",
				"WARNING   Synchronization: OUT OF SYNC (restart the daemon to apply current schedule configuration)"}},
		{"a change wins over a pending confirmation",
			scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "", "centralized"), freq: "monthly", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)", "INFO   Configuration: MONTHLY (day 1 at 02:00)",
				"WARNING   Synchronization: OUT OF SYNC (restart the daemon to apply current schedule configuration)"}},
		{"self mode applies at once, never pending",
			scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "", "self"), freq: "weekly", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: WEEKLY (Monday at 02:00)", "INFO   Configuration: WEEKLY (Monday at 02:00)", "INFO   Synchronization: IN SYNC"}},
		{"healthchecks off applies at once",
			scheduleStatusCase{state: startedState("daily", "mon", 1, "02:00", "", "off"), freq: "daily", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)", "INFO   Configuration: DAILY (02:00)", "INFO   Synchronization: IN SYNC"}},
		{"a day the frequency does not use is ignored",
			scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "weekly", "centralized"), freq: "weekly", weekday: "mon", day: "20", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: WEEKLY (Monday at 02:00)", "INFO   Configuration: WEEKLY (Monday at 02:00)", "INFO   Synchronization: IN SYNC"}},
		{"monthly day changed after the start",
			scheduleStatusCase{state: startedState("monthly", "mon", 15, "02:00", "monthly", "centralized"), freq: "monthly", weekday: "mon", day: "20", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: MONTHLY (day 15 at 02:00)", "INFO   Configuration: MONTHLY (day 20 at 02:00)",
				"WARNING   Synchronization: OUT OF SYNC (restart the daemon to apply current schedule configuration)"}},
		{"daemon not running",
			scheduleStatusCase{freq: "daily", weekday: "mon", day: "1", hhmm: "02:00"},
			[]string{"INFO   Daemon now: NOT RUNNING", "INFO   Configuration: DAILY (02:00)", "INFO   Synchronization: NOT APPLICABLE"}},
		{"no schedule state",
			scheduleStatusCase{freq: "daily", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"WARNING   Daemon now: UNAVAILABLE (running daemon did not publish schedule state)", "INFO   Configuration: DAILY (02:00)",
				"WARNING   Synchronization: UNKNOWN (running daemon did not publish schedule state)"}},
		{"schedule state of an earlier start",
			scheduleStatusCase{state: func() *health.ScheduleState {
				st := startedState("daily", "mon", 1, "02:00", "daily", "centralized")
				st.ConfiguredTS = scheduleStatusStart - 1
				return st
			}(), freq: "daily", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"WARNING   Daemon now: UNAVAILABLE (schedule state does not match the live daemon)", "INFO   Configuration: DAILY (02:00)",
				"WARNING   Synchronization: UNKNOWN (schedule state does not match the live daemon)"}},
		{"invalid value in backup.env",
			scheduleStatusCase{state: startedState("daily", "mon", 1, "02:00", "daily", "centralized"), freq: "fortnightly", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)", `WARNING   Configuration: INVALID (Frequency "fortnightly" is not daily, weekly or monthly)`,
				`WARNING   Synchronization: UNKNOWN (Frequency "fortnightly" is not daily, weekly or monthly)`}},
		{"two invalid values, in the variables' order",
			scheduleStatusCase{state: startedState("daily", "mon", 1, "02:00", "daily", "centralized"), freq: "fortnightly", weekday: "mon", day: "1", hhmm: "25:99", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)",
				`WARNING   Configuration: INVALID (Frequency "fortnightly" is not daily, weekly or monthly; Time "25:99" is not HH:MM)`,
				`WARNING   Synchronization: UNKNOWN (Frequency "fortnightly" is not daily, weekly or monthly; Time "25:99" is not HH:MM)`}},
		{"started invalid, fixed since",
			scheduleStatusCase{state: func() *health.ScheduleState {
				st := startedState("daily", "mon", 1, "02:00", "daily", "centralized")
				st.ConfiguredInvalid = true
				return st
			}(), freq: "daily", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)", "INFO   Configuration: DAILY (02:00)",
				"WARNING   Synchronization: OUT OF SYNC (restart the daemon to apply current schedule configuration)"}},
		{"started invalid after a monthly confirmation runs daily",
			scheduleStatusCase{state: func() *health.ScheduleState {
				st := startedState("daily", "mon", 1, "02:00", "monthly", "centralized")
				st.ConfiguredInvalid = true
				return st
			}(), freq: "fortnightly", weekday: "mon", day: "1", hhmm: "02:00", alive: true},
			[]string{"INFO   Daemon now: DAILY (02:00)", `WARNING   Configuration: INVALID (Frequency "fortnightly" is not daily, weekly or monthly)`,
				`WARNING   Synchronization: UNKNOWN (Frequency "fortnightly" is not daily, weekly or monthly)`}},
		{"another configuration file",
			scheduleStatusCase{state: startedState("daily", "mon", 1, "02:00", "daily", "centralized"), freq: "daily", weekday: "mon", day: "1", hhmm: "02:00", alive: true,
				currentPath: "/opt/proxsave/configs/other.env"},
			[]string{"INFO   Daemon now: DAILY (02:00)", "INFO   Configuration: DAILY (02:00)",
				"WARNING   Synchronization: OUT OF SYNC (restart the daemon to apply current schedule configuration)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scheduleBlockLines(t, c.tc.compare(t))
			assertLogExact(t, got, append([]string{"INFO Backup schedule:"}, c.want...)...)
		})
	}
}

// An unreadable state file is named by its error.
func TestBackupScheduleUnreadableState(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(health.DaemonStateDir(base), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(health.ScheduleStatePath(base), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BaseDir: base, ConfigPath: scheduleSource, SchedulerFrequency: "daily", SchedulerTime: "02:00"}
	c := compareBackupSchedule(health.DaemonState{ProcessAlive: true, StartTS: scheduleStatusStart},
		daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: scheduleSource}, cfg, nil, base)
	got := scheduleBlockLines(t, c)
	if c.RunningState != scheduleSideUnavailable || !strings.HasPrefix(c.RunningReason, "parse schedule state: ") {
		t.Fatalf("running = %v %q; want unavailable with the parse error", c.RunningState, c.RunningReason)
	}
	assertLogSequence(t, got,
		"WARNING   Daemon now: UNAVAILABLE ("+c.RunningReason+")",
		"WARNING   Synchronization: UNKNOWN ("+c.RunningReason+")")
}

// An unreadable backup.env (the dashboard passes the loader's error): the sibling's words.
func TestBackupScheduleUnreadableConfiguration(t *testing.T) {
	base := t.TempDir()
	c := compareBackupSchedule(health.DaemonState{}, daemonRuntimeDiagnostic{Availability: daemonRuntimeNotApplicable}, nil,
		errors.New("open /opt/proxsave/configs/backup.env: permission denied"), base)
	assertLogExact(t, scheduleBlockLines(t, c),
		"INFO Backup schedule:",
		"INFO   Daemon now: NOT RUNNING",
		"WARNING   Configuration: UNKNOWN: current configuration could not be read: open /opt/proxsave/configs/backup.env: permission denied",
		"WARNING   Synchronization: UNKNOWN (current configuration could not be read: open /opt/proxsave/configs/backup.env: permission denied)",
	)
}

// The block's DEBUG evidence comes before it: the state file as read against the daemon start.
func TestBackupScheduleEvidencePrecedesTheBlock(t *testing.T) {
	tc := scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "", "centralized"), freq: "weekly", weekday: "mon", day: "1", hhmm: "02:00", alive: true}
	c := tc.compare(t)
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	logBackupSchedule(logger, c)
	got := debugEntries(buf.String())
	if len(got) < 4 || !strings.HasPrefix(got[0], "DEBUG daemon diagnostics: schedule state: path=") ||
		!strings.Contains(got[0], `healthchecks="centralized" configured={frequency=weekly weekday=mon monthday=1 time=02:00}`) ||
		!strings.Contains(got[0], "daemon_start_ts=1790856762") {
		t.Fatalf("first line is not the state evidence:\n%s", strings.Join(got, "\n"))
	}
	assertLogSequence(t, got,
		"DEBUG daemon diagnostics: schedule configuration: frequency=weekly weekday=mon monthday=1 time=02:00 source="+scheduleSource,
		"DEBUG daemon diagnostics: schedule config path: running="+scheduleSource+" current="+scheduleSource,
		"INFO Backup schedule:",
	)
}

var scheduleANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// The dashboard block: the same words as the CLI, after Running daemon loaded at and before the
// personal pre-run script.
func TestDashboardBackupScheduleBlock(t *testing.T) {
	tc := scheduleStatusCase{state: startedState("weekly", "mon", 1, "02:00", "", "centralized"), freq: "weekly", weekday: "mon", day: "1", hhmm: "02:00", alive: true}
	c := tc.compare(t)
	got := scheduleANSI.ReplaceAllString(buildDashboardBackupSchedule(c), "")
	want := strings.Join([]string{
		"Backup schedule:",
		"  Daemon now: DAILY (02:00)",
		"  Configuration: WEEKLY (Monday at 02:00)",
		"  Synchronization: PENDING (ProxSave switches to weekly automatically; no action needed)",
	}, "\n")
	if got != want {
		t.Fatalf("dashboard block:\n%s\nwant:\n%s", got, want)
	}

	d := daemonDiagnostics{
		Runtime:  daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: scheduleSource, StartTS: scheduleStatusStart},
		Schedule: c,
	}
	screen := scheduleANSI.ReplaceAllString(buildDaemonStatusPrompt(d), "")
	loaded := strings.Index(screen, "Running daemon loaded at:")
	block := strings.Index(screen, "Backup schedule:")
	pre := strings.Index(screen, "Personal pre-run script:")
	if loaded < 0 || block < loaded || pre < block {
		t.Fatalf("block out of place (loaded=%d block=%d pre=%d):\n%s", loaded, block, pre, screen)
	}
}

// A daemon that applies its cadence at once records it with how healthchecks ran and no
// confirmation, dropping one a centralized start left: back in centralized it runs daily until
// the relay confirms again (part C, case 1).
func TestScheduleSelfStartRecordsTheCadenceWithoutConfirmation(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		mode    string
		want    string
	}{
		{true, config.HealthcheckModeSelf, health.ScheduleHealthchecksSelf},
		{false, config.HealthcheckModeCentralized, health.ScheduleHealthchecksOff},
	} {
		t.Run(tc.want, func(t *testing.T) {
			d := scheduleDaemon(t, "weekly", "mon", "1", "02:00", tc.enabled, tc.mode)
			if err := health.WriteScheduleState(d.cfg.BaseDir, *startedState("weekly", "mon", 1, "02:00", "weekly", "centralized")); err != nil {
				t.Fatal(err)
			}
			_ = captureDaemonLog(t)
			runScheduleStart(t, d)
			st, found, err := health.ReadScheduleState(d.cfg.BaseDir)
			if err != nil || !found {
				t.Fatalf("schedule state = %v, %v", found, err)
			}
			want := health.ScheduleCadence{Frequency: "weekly", Weekday: "mon", MonthDay: 1, Time: "02:00"}
			if st.Healthchecks != tc.want || st.LastConfirmed != "" || st.Configured == nil || *st.Configured != want ||
				st.ConfiguredTS != scheduleNow.Unix() || st.ConfiguredInvalid {
				t.Fatalf("schedule state = %+v (configured %+v); want healthchecks=%s, no confirmation, configured %+v", st, st.Configured, tc.want, want)
			}
		})
	}
}

// A centralized start records how healthchecks ran and whether a value was invalid.
func TestScheduleCentralizedStartRecordsTheInvalidFallback(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	d := negotiationDaemon(t, relay.server.URL, "fortnightly", "mon", "1", "03:00")
	_ = startLines(t, d)
	st, _, _ := health.ReadScheduleState(d.cfg.BaseDir)
	if st.Healthchecks != health.ScheduleHealthchecksCentralized || !st.ConfiguredInvalid || st.Configured == nil ||
		st.Configured.Frequency != "daily" || st.Configured.Time != "03:00" {
		t.Fatalf("schedule state = %+v (configured %+v); want centralized, invalid, configured the daily 03:00 fallback", st, st.Configured)
	}
}

// The CLI places the block after Running daemon loaded at and before the personal pre-run script.
func TestDaemonStatusCLIPlacesTheBackupSchedule(t *testing.T) {
	tc := scheduleStatusCase{state: startedState("daily", "mon", 1, "02:00", "daily", "centralized"), freq: "daily", weekday: "mon", day: "1", hhmm: "02:00", alive: true}
	d := daemonDiagnostics{
		Runtime:  daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: scheduleSource, StartTS: scheduleStatusStart},
		Schedule: tc.compare(t),
	}
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	logDaemonDiagnostics(logger, d)
	got := debugEntries(buf.String())
	assertLogSequence(t, got,
		"INFO Running daemon configuration: "+scheduleSource,
		"INFO Backup schedule:",
		"INFO   Daemon now: DAILY (02:00)",
		"INFO   Configuration: DAILY (02:00)",
		"INFO   Synchronization: IN SYNC",
		"INFO Personal pre-run script:",
	)
}
