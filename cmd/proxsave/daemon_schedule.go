package main

import (
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// invalidScheduleValue is one SCHEDULER_* value the daemon could not use, and what it uses
// instead.
type invalidScheduleValue struct {
	name  string // the backup.env variable
	value string // as read
	err   error
	using string // the replacement, as the ERROR line names it
}

// readConfiguredCadence is the cadence backup.env asks for, with the daemon's fallbacks
// applied. An invalid SCHEDULER_TIME becomes cron.DefaultTime, as it always has; it is parsed
// with no default, so an empty one is invalid too, exactly as cron.NextDaily treated it. An
// invalid frequency or day falls back to daily at that time: the same fallback the crontab
// line gets (configCronSchedule), so both schedulers run the same backup on a hand-edited
// file. A day the frequency does not use is still validated, for that same reason.
func readConfiguredCadence(cfg *config.Config) (cron.Cadence, []invalidScheduleValue) {
	var invalid []invalidScheduleValue
	hhmm, err := cron.NormalizeTime(cfg.SchedulerTime, "")
	if err != nil {
		invalid = append(invalid, invalidScheduleValue{"SCHEDULER_TIME", cfg.SchedulerTime, err, cron.DefaultTime})
		hhmm = cron.DefaultTime
	}
	freq, freqErr := cron.ParseFrequency(cfg.SchedulerFrequency)
	weekday, weekdayErr := cron.ParseWeekday(cfg.SchedulerWeekday)
	monthDay, monthDayErr := cron.ParseMonthDay(cfg.SchedulerMonthDay)
	daily := false
	for _, v := range []invalidScheduleValue{
		{"SCHEDULER_FREQUENCY", cfg.SchedulerFrequency, freqErr, string(cron.FrequencyDaily)},
		{"SCHEDULER_WEEKDAY", cfg.SchedulerWeekday, weekdayErr, string(cron.FrequencyDaily)},
		{"SCHEDULER_MONTHDAY", cfg.SchedulerMonthDay, monthDayErr, string(cron.FrequencyDaily)},
	} {
		if v.err != nil {
			invalid = append(invalid, v)
			daily = true
		}
	}
	if daily {
		return cron.Cadence{Frequency: cron.FrequencyDaily, Weekday: cron.DefaultWeekday, MonthDay: cron.DefaultMonthDay, Time: hhmm}, invalid
	}
	return cron.Cadence{Frequency: freq, Weekday: weekday, MonthDay: monthDay, Time: hhmm}, invalid
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

// unconfirmedCadence is the centralized cadence in effect while the relay has confirmed
// nothing: daily at the configured time (todo point 22, "no confirmed cadence on disk: the old
// one is daily"). Until the negotiation exists nothing is ever confirmed, so a weekly or
// monthly SCHEDULER_FREQUENCY keeps running daily in centralized mode; the backup check on the
// monitor still has a 24 h period, and a slower cadence would turn it DOWN.
func unconfirmedCadence(configured cron.Cadence) cron.Cadence {
	configured.Frequency = cron.FrequencyDaily
	return configured
}

// logScheduleRead is the evidence line for every schedule outcome: the raw values as read.
func (d *daemon) logScheduleRead() {
	logging.DebugStep(logging.GetDefaultLogger(), "schedule", "read frequency=%s weekday=%s monthday=%s time=%s source=%s",
		d.cfg.SchedulerFrequency, d.cfg.SchedulerWeekday, d.cfg.SchedulerMonthDay, d.cfg.SchedulerTime, d.configPath)
}

// logScheduleStart reports, once at start, the cadence the scheduler is about to run on.
//
// With healthchecks off or in self mode it is the approved block (todo points 34, 38, 41):
// "Applying backup schedule...", the evidence in DEBUG, the details, the outcome last.
//
// In centralized mode it is DEBUG only. The approved centralized blocks (applied after the
// relay confirms, pending while it does not: points 38-39) are made of the relay attempt lines
// the negotiation unit will produce; printing them without an attempt would claim one.
//
// With an invalid value there is no block either: the scheduler logs each invalid value as an
// ERROR before every next backup (scheduleLoop), and "applied" over a fallback would be false.
func (d *daemon) logScheduleStart() {
	configured, invalid := readConfiguredCadence(d.cfg)
	if len(invalid) > 0 {
		return
	}
	logger := logging.GetDefaultLogger()
	if d.relayNegotiatesSchedule() {
		d.logScheduleRead()
		if inEffect := d.cadenceInEffect(configured); inEffect.Frequency != configured.Frequency {
			logging.DebugStep(logger, "schedule", "centralized frequency=%s not negotiated yet, in effect %s",
				configured.Frequency, inEffect.Frequency)
		} else {
			logging.DebugStep(logger, "schedule", "centralized frequency=%s, in effect %s", configured.Frequency, inEffect.Frequency)
		}
		return
	}

	logging.Info("Applying backup schedule...")
	d.logScheduleRead()
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

// nextScheduledRun is when the next backup starts. Invalid values are logged here, on every
// computation, as the invalid SCHEDULER_TIME always was.
func (d *daemon) nextScheduledRun(now time.Time) time.Time {
	configured, invalid := readConfiguredCadence(d.cfg)
	if len(invalid) > 0 {
		d.logScheduleRead()
		for _, v := range invalid {
			logging.Error("daemon: invalid %s %q (%v); using %s", v.name, v.value, v.err, v.using)
		}
	}
	inEffect := d.cadenceInEffect(configured)
	next, err := inEffect.Next(now)
	if err != nil {
		// readConfiguredCadence validated every field, so only a confirmedCadence seam that
		// returned an invalid cadence gets here; never sleep on a zero time.
		logging.DebugStep(logging.GetDefaultLogger(), "schedule", "cadence in effect invalid error=%v, using daily at %s", err, cron.DefaultTime)
		next, _ = cron.NextDaily(now, cron.DefaultTime)
	}
	logging.DebugStep(logging.GetDefaultLogger(), "schedule", "next run frequency=%s weekday=%s monthday=%d time=%s at=%s",
		inEffect.Frequency, cron.WeekdayName(inEffect.Weekday), inEffect.MonthDay, inEffect.Time, next.Format(time.RFC3339))
	return next
}
