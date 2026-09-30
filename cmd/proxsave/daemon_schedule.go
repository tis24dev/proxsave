package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// invalidScheduleValue is one SCHEDULER_* value the daemon could not use.
type invalidScheduleValue struct {
	name  string // the backup.env variable
	value string // as read
	err   error
	why   string // the INFO line that names it in the not-applied block
}

// scheduleReading is what the daemon made of the four SCHEDULER_* values.
type scheduleReading struct {
	// cadence is the one the daemon asks for, fallbacks applied. Its Time is always valid:
	// SCHEDULER_TIME, or cron.DefaultTime when that is empty or invalid.
	cadence cron.Cadence
	// dayLine is the day detail of the not-applied block: set only when the frequency is
	// valid, uses a day, and that day is valid.
	dayLine string
	// invalid holds the values the daemon could not use, in the order FREQUENCY, WEEKDAY,
	// MONTHDAY, TIME. Any entry means the schedule is not applied.
	invalid []invalidScheduleValue
	// notes are DEBUG evidence of what was read but not used: an empty SCHEDULER_TIME, a day
	// the frequency does not use.
	notes []string
}

// readConfiguredCadence is the cadence backup.env asks for, with the daemon's fallbacks.
//
//   - SCHEDULER_TIME empty: cron.DefaultTime, silently; not an invalid value.
//   - SCHEDULER_TIME not empty and invalid: the whole cadence is daily at cron.DefaultTime.
//   - an invalid SCHEDULER_FREQUENCY, or an invalid day the frequency uses: daily at the
//     configured time (cron.DefaultTime when that is empty or invalid).
//   - an invalid day the frequency does not use is ignored, as cron.ParseCadence ignores it,
//     so this scheduler and the crontab line (configCronSchedule) run the same backup.
//
// With an invalid frequency no day is validated: the frequency that would use one is unknown.
func readConfiguredCadence(cfg *config.Config) scheduleReading {
	var r scheduleReading
	freq, freqErr := cron.ParseFrequency(cfg.SchedulerFrequency)
	weekday, weekdayErr := cron.ParseWeekday(cfg.SchedulerWeekday)
	monthDay, monthDayErr := cron.ParseMonthDay(cfg.SchedulerMonthDay)

	if freqErr != nil {
		r.invalid = append(r.invalid, invalidScheduleValue{"SCHEDULER_FREQUENCY", cfg.SchedulerFrequency, freqErr,
			fmt.Sprintf("Frequency %q is not daily, weekly or monthly", cfg.SchedulerFrequency)})
		if weekdayErr != nil {
			r.notes = append(r.notes, fmt.Sprintf("weekday=%q not checked, frequency invalid", cfg.SchedulerWeekday))
		}
		if monthDayErr != nil {
			r.notes = append(r.notes, fmt.Sprintf("monthday=%q not checked, frequency invalid", cfg.SchedulerMonthDay))
		}
	} else {
		if weekdayErr != nil {
			if freq == cron.FrequencyWeekly {
				r.invalid = append(r.invalid, invalidScheduleValue{"SCHEDULER_WEEKDAY", cfg.SchedulerWeekday, weekdayErr,
					fmt.Sprintf("Weekday %q is not mon-sun", cfg.SchedulerWeekday)})
			} else {
				r.notes = append(r.notes, fmt.Sprintf("weekday=%q unused by frequency=%s, ignored", cfg.SchedulerWeekday, freq))
				weekday = cron.DefaultWeekday
			}
		} else if freq == cron.FrequencyWeekly {
			r.dayLine = fmt.Sprintf("  Weekday: %s", weekday)
		}
		if monthDayErr != nil {
			if freq == cron.FrequencyMonthly {
				r.invalid = append(r.invalid, invalidScheduleValue{"SCHEDULER_MONTHDAY", cfg.SchedulerMonthDay, monthDayErr,
					fmt.Sprintf("Day %s is outside 1-%d", cfg.SchedulerMonthDay, cron.MaxMonthDay)})
			} else {
				r.notes = append(r.notes, fmt.Sprintf("monthday=%q unused by frequency=%s, ignored", cfg.SchedulerMonthDay, freq))
				monthDay = cron.DefaultMonthDay
			}
		} else if freq == cron.FrequencyMonthly {
			r.dayLine = fmt.Sprintf("  Day of month: %d", monthDay)
		}
	}

	hhmm := cron.DefaultTime
	timeInvalid := false
	if strings.TrimSpace(cfg.SchedulerTime) == "" {
		r.notes = append(r.notes, fmt.Sprintf("time empty, default %s", cron.DefaultTime))
	} else if norm, err := cron.NormalizeTime(cfg.SchedulerTime, ""); err != nil {
		r.invalid = append(r.invalid, invalidScheduleValue{"SCHEDULER_TIME", cfg.SchedulerTime, err,
			fmt.Sprintf("Time %q is not HH:MM", cfg.SchedulerTime)})
		timeInvalid = true
	} else {
		hhmm = norm
	}

	switch {
	case timeInvalid:
		r.cadence = dailyCadence(cron.DefaultTime)
	case len(r.invalid) > 0:
		r.cadence = dailyCadence(hhmm)
	default:
		r.cadence = cron.Cadence{Frequency: freq, Weekday: weekday, MonthDay: monthDay, Time: hhmm}
	}
	return r
}

// dailyCadence is the daily fallback at hhmm, carrying the default days.
func dailyCadence(hhmm string) cron.Cadence {
	return cron.Cadence{Frequency: cron.FrequencyDaily, Weekday: cron.DefaultWeekday, MonthDay: cron.DefaultMonthDay, Time: hhmm}
}

// cadenceLabel is the "In effect" value of the not-applied block.
func cadenceLabel(c cron.Cadence) string {
	switch c.Frequency {
	case cron.FrequencyWeekly:
		return fmt.Sprintf("weekly, %s at %s", c.Weekday, c.Time)
	case cron.FrequencyMonthly:
		return fmt.Sprintf("monthly, day %d at %s", c.MonthDay, c.Time)
	default:
		return fmt.Sprintf("daily at %s", c.Time)
	}
}

// relayNegotiatesSchedule says whether the cadence has to be confirmed by the ProxSave HC
// Server before it takes effect: only with healthchecks on and centralized, because there the
// relay owns the backup check's period (todo point 16). Self mode and healthchecks off apply
// the file's cadence at once (point 24). Any mode other than self is centralized, as in
// buildReporter and in the config loader's normalization.
func (d *daemon) relayNegotiatesSchedule() bool {
	return d.cfg.HealthcheckEnabled && d.cfg.HealthcheckMode != config.HealthcheckModeSelf
}

// cadenceInEffect is the cadence the scheduler runs on for the configured one.
func (d *daemon) cadenceInEffect(configured cron.Cadence) cron.Cadence {
	if !d.relayNegotiatesSchedule() {
		return configured
	}
	if d.confirmedCadence != nil {
		return d.confirmedCadence(configured)
	}
	return unconfirmedCadence(configured)
}

// cadenceToRun is the cadence the scheduler actually runs on and its next run after now: the
// cadence in effect, or, when the confirmedCadence seam returned one Cadence.Next refuses,
// daily at the configured time (SCHEDULER_TIME when valid, else cron.DefaultTime: exactly
// configured.Time). seamErr is why the cadence in effect was refused, nil when it was not.
// readConfiguredCadence always yields a valid cadence, so only the seam can get here.
func (d *daemon) cadenceToRun(configured cron.Cadence, now time.Time) (run cron.Cadence, next time.Time, seamErr error) {
	run = d.cadenceInEffect(configured)
	next, seamErr = run.Next(now)
	if seamErr == nil {
		return run, next, nil
	}
	run = dailyCadence(configured.Time)
	next, _ = run.Next(now)
	return run, next, seamErr
}

// unconfirmedCadence is the centralized cadence in effect while the relay has confirmed
// nothing: daily at the configured time (todo point 22, "no confirmed cadence on disk: the old
// one is daily"). Until the negotiation exists nothing is ever confirmed, so a weekly or
// monthly SCHEDULER_FREQUENCY keeps running daily in centralized mode; the backup check on the
// monitor still has a 24 h period, and a slower cadence would turn it DOWN.
func unconfirmedCadence(configured cron.Cadence) cron.Cadence {
	configured.Frequency = cron.FrequencyDaily
	return configured
}

// logScheduleRead is the evidence for every schedule outcome: the raw values as read, then
// what was read but not used.
func (d *daemon) logScheduleRead(reading scheduleReading) {
	logger := logging.GetDefaultLogger()
	logging.DebugStep(logger, "schedule", "read frequency=%s weekday=%s monthday=%s time=%s source=%s",
		d.cfg.SchedulerFrequency, d.cfg.SchedulerWeekday, d.cfg.SchedulerMonthDay, d.cfg.SchedulerTime, d.configPath)
	for _, v := range reading.invalid {
		logging.DebugStep(logger, "schedule", "invalid %s=%q error=%v", v.name, v.value, v.err)
	}
	for _, note := range reading.notes {
		logging.DebugStep(logger, "schedule", "%s", note)
	}
}

// logScheduleStart reports, once at start (run(), before the background loops), the cadence
// the scheduler is about to run on.
//
// With an invalid value it is the not-applied block (logScheduleNotApplied), in every mode.
//
// With healthchecks off or in self mode it is the approved block (todo points 34, 38, 41):
// "Applying backup schedule...", the evidence in DEBUG, the details, the outcome last.
//
// In centralized mode it is DEBUG only. The approved centralized blocks (applied after the
// relay confirms, pending while it does not: points 38-39) are made of the relay attempt lines
// the negotiation unit will produce; printing them without an attempt would claim one.
func (d *daemon) logScheduleStart() {
	reading := readConfiguredCadence(d.cfg)
	if len(reading.invalid) > 0 {
		d.logScheduleNotApplied(reading)
		return
	}
	configured := reading.cadence
	logger := logging.GetDefaultLogger()
	if d.relayNegotiatesSchedule() {
		d.logScheduleRead(reading)
		if inEffect := d.cadenceInEffect(configured); inEffect.Frequency != configured.Frequency {
			logging.DebugStep(logger, "schedule", "centralized frequency=%s not negotiated yet, in effect %s",
				configured.Frequency, inEffect.Frequency)
		} else {
			logging.DebugStep(logger, "schedule", "centralized frequency=%s, in effect %s", configured.Frequency, inEffect.Frequency)
		}
		return
	}

	logging.Info("Applying backup schedule...")
	d.logScheduleRead(reading)
	if !d.cfg.HealthcheckEnabled {
		logging.DebugStep(logger, "schedule", "healthchecks disabled, no relay negotiation")
	} else {
		logging.DebugStep(logger, "schedule", "healthchecks mode=self, no relay negotiation")
	}
	logging.Info("  Frequency: %s", configured.Frequency)
	switch configured.Frequency {
	case cron.FrequencyWeekly:
		logging.Info("  Weekday: %s", configured.Weekday)
	case cron.FrequencyMonthly:
		logging.Info("  Day of month: %d", configured.MonthDay)
	}
	logging.Info("  Time: %s", configured.Time)
	// On the operator's own server ProxSave does not manage the backup check's period, so a
	// slower cadence is worth naming there (point 24: only when it is not daily).
	if d.cfg.HealthcheckEnabled && configured.Frequency != cron.FrequencyDaily {
		logging.Info("  Backup check: pinged %s on your own server", configured.Frequency)
	}
	logging.Info("%s Backup schedule: applied", theme.SymbolSuccess)
}

// logScheduleNotApplied is the block for a schedule with an invalid value: the values as
// read, the cadence the daemon runs on instead, one line per invalid value, the outcome last.
func (d *daemon) logScheduleNotApplied(reading scheduleReading) {
	logging.Info("Applying backup schedule...")
	d.logScheduleRead(reading)
	logging.Info("  Frequency: %s", d.cfg.SchedulerFrequency)
	if reading.dayLine != "" {
		logging.Info("%s", reading.dayLine)
	}
	logging.Info("  Time: %s", d.cfg.SchedulerTime)
	run, _, _ := d.cadenceToRun(reading.cadence, d.now())
	logging.Info("  In effect: %s", cadenceLabel(run))
	for _, v := range reading.invalid {
		logging.Info("%s", v.why)
	}
	logging.Warning("%s Backup schedule: not applied", theme.SymbolWarning)
}

// logScheduleNotAppliedAgain repeats the not-applied block before every later next-backup
// line while a value stays invalid. A valid schedule is reported once, at start.
func (d *daemon) logScheduleNotAppliedAgain() {
	if reading := readConfiguredCadence(d.cfg); len(reading.invalid) > 0 {
		d.logScheduleNotApplied(reading)
	}
}

// nextScheduledRun is when the next backup starts.
func (d *daemon) nextScheduledRun(now time.Time) time.Time {
	reading := readConfiguredCadence(d.cfg)
	run, next, seamErr := d.cadenceToRun(reading.cadence, now)
	if seamErr != nil {
		logging.DebugStep(logging.GetDefaultLogger(), "schedule", "cadence in effect invalid error=%v, using daily at %s", seamErr, run.Time)
	}
	logging.DebugStep(logging.GetDefaultLogger(), "schedule", "next run frequency=%s weekday=%s monthday=%d time=%s at=%s",
		run.Frequency, cron.WeekdayName(run.Weekday), run.MonthDay, run.Time, next.Format(time.RFC3339))
	return next
}
