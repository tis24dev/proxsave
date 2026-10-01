package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/serverbot"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// The backup schedule negotiation of a centralized daemon (todo points 17-24, 34-43): the
// frequency rides the /api/healthcheck/config poll that carries notify_on and channels, and the
// relay confirms it in the same answer. Until it does, the cadence in effect stays the last one
// it confirmed (daily when none ever was). At start: up to scheduleStartAttempts attempts,
// scheduleAttemptGap apart; then one attempt at every heartbeat until it confirms.

const (
	// scheduleStartAttempts is how many polls the start sends before it settles on pending.
	scheduleStartAttempts = 3
	// scheduleAttemptGap is the pause between two start attempts.
	scheduleAttemptGap = 3 * time.Second
)

// pollReach is how far a poll got, which names the why line of a block that is not applied.
type pollReach int

const (
	reachNone     pollReach = iota // no HTTP answer: dns, connect, timeout
	reachNotReady                  // an HTTP status other than 200
	reachAnswered                  // 200: what it says decides
)

// why is the INFO line that says why a block is pending or not refreshed (todo points 50-52).
func (a pollReach) why() string {
	switch a {
	case reachNotReady:
		return "ProxSave HC Server not ready"
	case reachAnswered:
		return "ProxSave HC Server did not confirm"
	default:
		return "ProxSave HC Server not reachable"
	}
}

// answer is how far poll p got.
func (p configPoll) answer() pollReach {
	switch {
	case p.status == 0:
		return reachNone
	case p.status != http.StatusOK:
		return reachNotReady
	default:
		return reachAnswered
	}
}

// pollFailure names, for DEBUG, how a poll without a usable answer failed: the transport stage,
// the HTTP status, or the 200 that could not be used.
func pollFailure(p configPoll) string {
	switch {
	case !p.sent:
		return fmt.Sprintf("not sent error=%v", p.err)
	case p.status == 0:
		stage := "unknown"
		var te *serverbot.TransportError
		if errors.As(p.err, &te) && te.Stage != "" {
			stage = te.Stage
		}
		return "stage=" + stage
	case p.status != http.StatusOK:
		return fmt.Sprintf("http=%d", p.status)
	default:
		return fmt.Sprintf("http=%d error=%v", p.status, p.err)
	}
}

// wantedFrequency is the frequency a centralized poll sends: the one configured at start, or,
// before the start read it, the one d.cfg configures.
func (d *daemon) wantedFrequency() cron.Frequency {
	d.mu.Lock()
	f := d.scheduleWant
	d.mu.Unlock()
	if f != "" {
		return f
	}
	return readConfiguredCadence(d.cfg).cadence.Frequency
}

// heartbeatInterval is HEALTHCHECK_HEARTBEAT_INTERVAL, or its default.
func (d *daemon) heartbeatInterval() time.Duration {
	if d.cfg.HealthcheckHeartbeatInterval > 0 {
		return d.cfg.HealthcheckHeartbeatInterval
	}
	return defaultHeartbeatInterval
}

func (d *daemon) scheduleAttemptGap() time.Duration {
	if d.scheduleAttemptGapOverride > 0 {
		return d.scheduleAttemptGapOverride
	}
	return scheduleAttemptGap
}

// cadenceState is a cadence as .schedule_state.json records it.
func cadenceState(c cron.Cadence) *health.ScheduleCadence {
	return &health.ScheduleCadence{Frequency: string(c.Frequency), Weekday: cron.WeekdayName(c.Weekday), MonthDay: c.MonthDay, Time: c.Time}
}

// orNone is a frequency for a DEBUG line, "none" when there is none.
func orNone(f cron.Frequency) string {
	if f == "" {
		return "none"
	}
	return string(f)
}

// loadScheduleState reads the last confirmed frequency from .schedule_state.json, records the
// cadence configured at this start there, and returns the DEBUG line of what it read. A missing,
// unreadable or unknown value is no confirmed frequency: daily (todo point 22).
func (d *daemon) loadScheduleState(configured cron.Cadence) string {
	path := health.ScheduleStatePath(d.cfg.BaseDir)
	st, found, err := health.ReadScheduleState(d.cfg.BaseDir)
	var last cron.Frequency
	var line string
	switch {
	case err != nil:
		st = health.ScheduleState{}
		line = fmt.Sprintf("read last_confirmed=none source=%s error=%v default=daily", path, err)
	case !found || st.LastConfirmed == "":
		line = fmt.Sprintf("read last_confirmed=none source=%s default=daily", path)
	default:
		if f, perr := cron.ParseFrequency(st.LastConfirmed); perr != nil {
			line = fmt.Sprintf("read last_confirmed=%q source=%s invalid, default=daily", st.LastConfirmed, path)
			st.LastConfirmed = ""
		} else {
			last = f
			line = fmt.Sprintf("read last_confirmed=%s source=%s", last, path)
		}
	}
	st.Configured = cadenceState(configured)
	st.ConfiguredTS = d.now().Unix()
	d.mu.Lock()
	d.scheduleWant = configured.Frequency
	d.scheduleConfigured = configured
	d.scheduleLastConfirmed = last
	d.scheduleConfirmed = false
	d.scheduleState = st
	d.mu.Unlock()
	if werr := health.WriteScheduleState(d.cfg.BaseDir, st); werr != nil {
		line += fmt.Sprintf("; save configured failed error=%v", werr)
	}
	return line
}

// confirmSchedule records the relay's confirmation of freq, in memory and in
// .schedule_state.json, with the DEBUG line of the save.
func (d *daemon) confirmSchedule(freq cron.Frequency) {
	d.mu.Lock()
	d.scheduleLastConfirmed = freq
	d.scheduleConfirmed = freq == d.scheduleWant
	d.scheduleState.LastConfirmed = string(freq)
	d.scheduleState.ConfirmedTS = d.now().Unix()
	st := d.scheduleState
	d.mu.Unlock()
	path := health.ScheduleStatePath(d.cfg.BaseDir)
	logger := logging.GetDefaultLogger()
	if err := health.WriteScheduleState(d.cfg.BaseDir, st); err != nil {
		logging.DebugStep(logger, "schedule", "save failed confirmed=%s file=%s error=%v", freq, path, err)
		return
	}
	logging.DebugStep(logger, "schedule", "saved confirmed=%s to %s", freq, path)
}

// scheduleAttempt sends one schedule poll, its stages logged under "schedule: <prefix>".
func (d *daemon) scheduleAttempt(ctx context.Context, prefix string) configPoll {
	p := d.pollCentralized(ctx, "schedule", prefix)
	if !p.sent {
		logging.DebugStep(logging.GetDefaultLogger(), "schedule", "%s not sent error=%v", prefix, p.err)
		return p
	}
	d.afterFailedPoll(p)
	return p
}

// scheduleConfirmedBy reports whether poll p is the relay confirming the frequency it sent.
func scheduleConfirmedBy(p configPoll) bool {
	return p.sent && p.err == nil && p.cfg.Schedule.Confirms(string(p.frequency))
}

// logScheduleAck is the DEBUG evidence of what poll p answered about the schedule. A transport
// failure is already logged by the poll's own stages.
func logScheduleAck(prefix string, p configPoll) {
	logger := logging.GetDefaultLogger()
	switch {
	case !p.sent, p.err != nil && p.status == 0:
	case p.err != nil && p.status == http.StatusOK:
		logging.DebugStep(logger, "schedule", "%s failed %s", prefix, pollFailure(p))
	case p.err != nil:
		logging.DebugStep(logger, "schedule", "%s failed %s error=%v", prefix, pollFailure(p), p.err)
	case p.cfg.Schedule == nil:
		logging.DebugStep(logger, "schedule", "%s ack missing", prefix)
	default:
		ack := p.cfg.Schedule
		if ack.Requested != string(p.frequency) {
			logging.DebugStep(logger, "schedule", "%s ack frequency=%s applied=%t sent=%s", prefix, ack.Requested, ack.Applied, p.frequency)
			return
		}
		logging.DebugStep(logger, "schedule", "%s ack frequency=%s applied=%t", prefix, ack.Requested, ack.Applied)
	}
}

// logScheduleFallback is the DEBUG line of a schedule left unconfirmed: the frequency in effect
// meanwhile and when it is asked for again.
func (d *daemon) logScheduleFallback() {
	d.mu.Lock()
	configured := d.scheduleConfigured
	d.mu.Unlock()
	run, _, _ := d.cadenceToRun(configured, d.now())
	logging.DebugStep(logging.GetDefaultLogger(), "schedule", "fallback frequency=%s reason=unconfirmed retry_every=%s",
		run.Frequency, d.heartbeatInterval())
}

// startScheduleNegotiation is the centralized start (todo points 38-39, 42-43, 50-52): the
// backup schedule block around up to three polls, then the notify level block and, when no poll
// returned usable ping URLs, the ping URLs block, all read from those polls; the reporter is set
// from them. With no relay secret no poll can be sent and no block has a text for that: the
// schedule stays DEBUG only (an invalid value still gets its not-applied block) and run()
// resolves the ping URLs as before. It reports whether a poll was sent.
func (d *daemon) startScheduleNegotiation(ctx context.Context, reading scheduleReading) bool {
	configured := reading.cadence
	stateLine := d.loadScheduleState(configured)
	d.scheduleChanged = make(chan struct{}, 1)
	logger := logging.GetDefaultLogger()
	invalid := len(reading.invalid) > 0

	if d.relaySecret(ctx) == "" {
		if invalid {
			logging.Info("Applying backup schedule...")
		}
		d.logScheduleRead(reading)
		logging.DebugStep(logger, "schedule", "%s", stateLine)
		logging.DebugStep(logger, "schedule", "not sent, %v", errNoRelaySecret)
		d.logScheduleFallback()
		if invalid {
			d.logScheduleNotAppliedOutcome(reading)
		}
		return false
	}

	logging.Info("Applying backup schedule...")
	d.logScheduleRead(reading)
	logging.DebugStep(logger, "schedule", "%s", stateLine)
	last, good, haveGood, confirmed := d.runStartAttempts(ctx)
	if !confirmed {
		d.logScheduleFallback()
	}
	switch {
	case invalid:
		d.logScheduleNotAppliedOutcome(reading)
	case confirmed:
		logCadenceDetails(configured)
		logging.Info("%s Backup schedule: applied", theme.SymbolSuccess)
	default:
		logCadenceDetails(configured)
		run, _, _ := d.cadenceToRun(configured, d.now())
		logging.Info("  In effect: %s", cadenceLabel(run))
		logging.Info("%s", last.answer().why())
		logging.Info("%s Backup schedule: pending, no action needed", theme.SymbolWarning)
	}

	source := last
	if haveGood {
		source = good
	}
	if r := d.reporterFromPoll(source, true); r != nil {
		d.setReporter(r)
	}

	want := d.wantedNotifyPolicy()
	logging.Info("Applying notify level...")
	d.logNotifyPolicyRead(d.cfg.NotifyOn, want, d.configPath)
	if haveGood {
		d.logNotifyPolicyAnswer(good)
	} else {
		logging.DebugStep(logger, "notify policy", "same response as schedule, failed %s", pollFailure(last))
	}
	d.logNotifyLevelOutcome(want, last.answer())

	if !haveGood {
		d.logPingURLsNotRefreshed(last)
	}
	return true
}

// runStartAttempts sends the start polls until one confirms the schedule: last is the last poll
// sent, good the last one that returned the ping URLs.
func (d *daemon) runStartAttempts(ctx context.Context) (last, good configPoll, haveGood, confirmed bool) {
	for n := 1; n <= scheduleStartAttempts; n++ {
		if n > 1 {
			t := time.NewTimer(d.scheduleAttemptGap())
			select {
			case <-ctx.Done():
				t.Stop()
				return last, good, haveGood, false
			case <-t.C:
			}
		}
		prefix := fmt.Sprintf("attempt=%d/%d", n, scheduleStartAttempts)
		p := d.scheduleAttempt(ctx, prefix)
		if p.sent || !last.sent {
			last = p
		}
		if p.sent && p.err == nil {
			good, haveGood = p, true
		}
		logScheduleAck(prefix, p)
		if scheduleConfirmedBy(p) {
			d.confirmSchedule(p.frequency)
			return last, good, haveGood, true
		}
	}
	return last, good, haveGood, false
}

// logPingURLsNotRefreshed is the start block for ping URLs the polls did not return (todo point
// 43): the URLs cached in backup.env stay in use, or there are none.
func (d *daemon) logPingURLsNotRefreshed(last configPoll) {
	logger := logging.GetDefaultLogger()
	logging.Info("Applying healthchecks ping URLs...")
	logging.DebugStep(logger, "ping urls", "same response as schedule, failed %s", pollFailure(last))
	alive, backup := d.cfg.HealthcheckAliveURL != "", d.cfg.HealthcheckBackupURL != ""
	setOrNone := func(set bool) string {
		if set {
			return "set"
		}
		return "none"
	}
	logging.DebugStep(logger, "ping urls", "fallback cached alive_url=%s backup_url=%s source=%s",
		setOrNone(alive), setOrNone(backup), d.configPath)
	if alive || backup {
		logging.Info("  Ping URLs: cached from backup.env")
		logging.Info("%s", last.answer().why())
		logging.Info("%s Healthchecks ping URLs: not refreshed", theme.SymbolWarning)
		return
	}
	logging.Info("  Ping URLs: none")
	logging.Info("%s", last.answer().why())
	logging.Warning("%s Healthchecks ping URLs: not available", theme.SymbolWarning)
}

// retryScheduleOnHeartbeat is the heartbeat's retry of a schedule the relay has not confirmed
// (todo point 40): one attempt, attempt=1/1. A failure is DEBUG only. A confirmation prints the
// applied block (with an invalid value, whose block repeats before every next-backup line, only
// its DEBUG evidence) and wakes scheduleLoop, which prints the next backup of the cadence now in
// effect and waits on it. The poll's ping URLs and notify ack are kept like any poll's. It
// reports whether a poll was sent.
func (d *daemon) retryScheduleOnHeartbeat(ctx context.Context) bool {
	if !d.relayNegotiatesSchedule() {
		return false
	}
	d.mu.Lock()
	want, last, configured := d.scheduleWant, d.scheduleLastConfirmed, d.scheduleConfigured
	pending := want != "" && !d.scheduleConfirmed
	d.mu.Unlock()
	if !pending {
		return false
	}
	logger := logging.GetDefaultLogger()
	logging.DebugStep(logger, "schedule", "heartbeat retry wanted=%s last_confirmed=%s", want, orNone(last))
	const prefix = "attempt=1/1"
	p := d.scheduleAttempt(ctx, prefix)
	if !p.sent {
		return false
	}
	if p.err == nil {
		if r := d.reporterFromPoll(p, false); r != nil {
			d.setReporter(r)
		}
	}
	if !scheduleConfirmedBy(p) {
		logScheduleAck(prefix, p)
		return true
	}

	reading := readConfiguredCadence(d.cfg)
	if len(reading.invalid) > 0 {
		logScheduleAck(prefix, p)
		d.confirmSchedule(p.frequency)
	} else {
		logging.Info("Applying backup schedule...")
		d.logScheduleRead(reading)
		logging.DebugStep(logger, "schedule", "read last_confirmed=%s source=%s", orNone(last), health.ScheduleStatePath(d.cfg.BaseDir))
		logScheduleAck(prefix, p)
		d.confirmSchedule(p.frequency)
		logCadenceDetails(configured)
		logging.Info("%s Backup schedule: applied", theme.SymbolSuccess)
	}
	select {
	case d.scheduleChanged <- struct{}{}:
	default:
	}
	return true
}
