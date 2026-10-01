package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
)

// The backup schedule block of the daemon status (--daemon-status and the dashboard's daemon
// screen, todo points 30-31 and part C): the cadence the running daemon runs, the one backup.env
// configures now, and whether they agree. Collected once into daemonDiagnostics; the two
// renderers only print it.

// scheduleSideState is what one side of the comparison could tell.
type scheduleSideState int

const (
	scheduleSideKnown       scheduleSideState = iota // a cadence
	scheduleSideNotRunning                           // running side: no live daemon
	scheduleSideUnavailable                          // running side: no usable schedule state
	scheduleSideInvalid                              // current side: a SCHEDULER_* value is invalid
	scheduleSideUnknown                              // current side: backup.env could not be read
)

// scheduleSync is the verdict of the comparison.
type scheduleSync int

const (
	scheduleInSync scheduleSync = iota
	scheduleOutOfSync
	schedulePending
	scheduleSyncNotApplicable
	scheduleSyncUnknown
)

// scheduleOutOfSyncReason is the reason of OUT OF SYNC (todo point 31).
const scheduleOutOfSyncReason = "restart the daemon to apply current schedule configuration"

// The running-side reasons of UNAVAILABLE (part C, case 3); a read error is shown as it is.
const (
	scheduleStateMissingReason = "running daemon did not publish schedule state"
	scheduleStateStaleReason   = "schedule state does not match the live daemon"
)

// scheduleComparison is the backup schedule block, with the evidence its DEBUG lines name.
type scheduleComparison struct {
	Running       cron.Cadence
	RunningState  scheduleSideState
	RunningReason string
	Current       cron.Cadence
	CurrentState  scheduleSideState
	CurrentReason string
	Sync          scheduleSync
	SyncReason    string

	// Evidence: the state file as read, and the daemon start it was checked against.
	StatePath    string
	StateFound   bool
	State        health.ScheduleState
	DaemonStart  int64
	ConfigPath   string
	RunningPath  string
	PathsCompare bool
}

// compareBackupSchedule builds the backup schedule block from the live daemon's state, its
// .schedule_state.json, the runtime record and the configuration loaded now (cfgErr is why
// there is none, when the caller knows).
func compareBackupSchedule(state health.DaemonState, runtime daemonRuntimeDiagnostic, cfg *config.Config, cfgErr error, baseDir string) scheduleComparison {
	c := scheduleComparison{StatePath: health.ScheduleStatePath(baseDir), DaemonStart: state.StartTS}

	// The current side: backup.env as it is now.
	if cfg == nil {
		c.CurrentState = scheduleSideUnknown
		c.CurrentReason = personalScriptUnreadableReason(cfgErr)
	} else {
		c.ConfigPath = cfg.ConfigPath
		reading := readConfiguredCadence(cfg)
		if len(reading.invalid) > 0 {
			whys := make([]string, 0, len(reading.invalid))
			for _, v := range reading.invalid {
				whys = append(whys, v.why)
			}
			c.CurrentState = scheduleSideInvalid
			c.CurrentReason = strings.Join(whys, "; ")
		} else {
			c.Current = reading.cadence
		}
	}

	// The running side: what the live daemon recorded at its start, and since.
	var started cron.Cadence
	if !state.ProcessAlive {
		c.RunningState = scheduleSideNotRunning
	} else {
		st, found, err := health.ReadScheduleState(baseDir)
		c.StateFound, c.State = found, st
		switch {
		case err != nil:
			c.RunningState, c.RunningReason = scheduleSideUnavailable, err.Error()
		case !found || st.Configured == nil:
			c.RunningState, c.RunningReason = scheduleSideUnavailable, scheduleStateMissingReason
		case state.StartTS > 0 && st.ConfiguredTS < state.StartTS:
			c.RunningState, c.RunningReason = scheduleSideUnavailable, scheduleStateStaleReason
		default:
			cad, perr := cron.ParseCadence(st.Configured.Frequency, st.Configured.Weekday,
				strconv.Itoa(st.Configured.MonthDay), st.Configured.Time)
			if perr != nil {
				c.RunningState, c.RunningReason = scheduleSideUnavailable, "parse schedule state: "+perr.Error()
				break
			}
			started = cad
			c.Running = runningCadence(cad, st)
		}
	}
	if runtime.Availability == daemonRuntimeAvailable && cfg != nil {
		c.RunningPath, c.PathsCompare = runtime.ConfigPath, true
	}

	// The verdict, the current side first: with no configuration to read there is nothing to
	// compare, whatever the daemon is doing (as comparePersonalScript).
	switch {
	case c.CurrentState == scheduleSideUnknown:
		c.Sync, c.SyncReason = scheduleSyncUnknown, c.CurrentReason
	case c.RunningState == scheduleSideNotRunning:
		c.Sync = scheduleSyncNotApplicable
	case c.RunningState == scheduleSideUnavailable:
		c.Sync, c.SyncReason = scheduleSyncUnknown, c.RunningReason
	case c.CurrentState == scheduleSideInvalid:
		c.Sync, c.SyncReason = scheduleSyncUnknown, c.CurrentReason
	case c.State.ConfiguredInvalid,
		c.PathsCompare && filepath.Clean(c.RunningPath) != filepath.Clean(c.ConfigPath),
		!sameScheduleCadence(started, c.Current):
		// A file changed after the start wins over a confirmation still pending: only a
		// restart brings it (part C, combination).
		c.Sync, c.SyncReason = scheduleOutOfSync, scheduleOutOfSyncReason
	case c.Running.Frequency != started.Frequency:
		c.Sync = schedulePending
		c.SyncReason = fmt.Sprintf("ProxSave switches to %s automatically; no action needed", started.Frequency)
	default:
		c.Sync = scheduleInSync
	}
	return c
}

// runningCadence is the cadence a daemon that started on started runs now: started itself when
// it applied it at once, else started on the frequency the relay last confirmed, daily when none
// ever was (as cadenceInEffect).
func runningCadence(started cron.Cadence, st health.ScheduleState) cron.Cadence {
	if !st.Negotiated() {
		return started
	}
	confirmed, err := cron.ParseFrequency(st.LastConfirmed)
	if err != nil || st.LastConfirmed == "" {
		confirmed = cron.FrequencyDaily
	}
	started.Frequency = confirmed
	return started
}

// sameScheduleCadence reports whether a and b run the same backups: the frequency, the day the
// frequency uses and the time. A day the frequency does not use is ignored (D4).
func sameScheduleCadence(a, b cron.Cadence) bool {
	if a.Frequency != b.Frequency || a.Time != b.Time {
		return false
	}
	switch a.Frequency {
	case cron.FrequencyWeekly:
		return a.Weekday == b.Weekday
	case cron.FrequencyMonthly:
		return a.MonthDay == b.MonthDay
	}
	return true
}

// scheduleStatusValue is a cadence as the block shows it: the frequency in capitals and its
// detail, "DAILY" "02:00", "WEEKLY" "Monday at 02:00", "MONTHLY" "day 15 at 02:00".
func scheduleStatusValue(c cron.Cadence) (keyword, detail string) {
	switch c.Frequency {
	case cron.FrequencyWeekly:
		return "WEEKLY", fmt.Sprintf("%s at %s", c.Weekday, c.Time)
	case cron.FrequencyMonthly:
		return "MONTHLY", fmt.Sprintf("day %d at %s", c.MonthDay, c.Time)
	default:
		return "DAILY", c.Time
	}
}

// scheduleEvidence is the DEBUG evidence of the block: the state file as read against the
// daemon start, and the configuration compared.
func scheduleEvidence(c scheduleComparison) []string {
	configured := "none"
	if c.State.Configured != nil {
		sc := c.State.Configured
		configured = fmt.Sprintf("frequency=%s weekday=%s monthday=%d time=%s", sc.Frequency, sc.Weekday, sc.MonthDay, sc.Time)
	}
	lines := []string{
		fmt.Sprintf("schedule state: path=%s found=%t healthchecks=%q configured={%s} configured_invalid=%t configured_ts=%d last_confirmed=%q daemon_start_ts=%d",
			c.StatePath, c.StateFound, c.State.Healthchecks, configured, c.State.ConfiguredInvalid, c.State.ConfiguredTS,
			c.State.LastConfirmed, c.DaemonStart),
	}
	if c.CurrentState == scheduleSideKnown {
		lines = append(lines, fmt.Sprintf("schedule configuration: frequency=%s weekday=%s monthday=%d time=%s source=%s",
			c.Current.Frequency, cron.WeekdayName(c.Current.Weekday), c.Current.MonthDay, c.Current.Time, c.ConfigPath))
	}
	if c.PathsCompare {
		lines = append(lines, fmt.Sprintf("schedule config path: running=%s current=%s", c.RunningPath, c.ConfigPath))
	}
	return lines
}
