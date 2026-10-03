package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/serverbot"
	"github.com/tis24dev/proxsave/internal/types"
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

// sameResponse is the DEBUG line of a block read from the schedule's poll p when p returned no
// usable answer: not sent at all, or how it failed.
func sameResponse(p configPoll) string {
	if !p.sent {
		return "same response as schedule, not sent"
	}
	return "same response as schedule, failed " + pollFailure(p)
}

// debugRecorder holds DEBUG lines back to write them later, in order. A heartbeat's schedule
// retry knows only after its poll whether its evidence belongs under an "Applying backup
// schedule..." header (a confirmation) or stands on its own (todo point 40: DEBUG only).
type debugRecorder struct {
	logger *logging.Logger
	mu     sync.Mutex
	lines  []string
}

// recordedDebugPrefix is what the recorder's logger writes between the timestamp and the
// message: the level, padded as every logger pads it.
const recordedDebugPrefix = "DEBUG    "

func newDebugRecorder() *debugRecorder {
	r := &debugRecorder{logger: logging.New(types.LogLevelDebug, false)}
	r.logger.SetOutput(logging.NewLineWriter(func(line string) {
		if _, rest, ok := strings.Cut(line, "] "); ok {
			line = strings.TrimPrefix(rest, recordedDebugPrefix)
		}
		r.mu.Lock()
		r.lines = append(r.lines, line)
		r.mu.Unlock()
	}))
	return r
}

// flush writes the held lines to the daemon's log, in the order they were recorded.
func (r *debugRecorder) flush() {
	r.mu.Lock()
	lines := r.lines
	r.lines = nil
	r.mu.Unlock()
	for _, line := range lines {
		logging.Debug("%s", line)
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
func (d *daemon) loadScheduleState(reading scheduleReading) string {
	configured := reading.cadence
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
	st.ConfiguredInvalid = len(reading.invalid) > 0
	st.Healthchecks = health.ScheduleHealthchecksCentralized
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

// saveScheduleStart records, for a daemon that applies its cadence at once (healthchecks off or
// self mode), the cadence configured at this start in .schedule_state.json, with no confirmed
// frequency (part C, case 1): --daemon-status reads what the daemon runs from it. It returns the
// DEBUG line of the save.
func (d *daemon) saveScheduleStart(reading scheduleReading) string {
	mode := health.ScheduleHealthchecksSelf
	if !d.cfg.HealthcheckEnabled {
		mode = health.ScheduleHealthchecksOff
	}
	st := health.ScheduleState{
		Configured:        cadenceState(reading.cadence),
		ConfiguredTS:      d.now().Unix(),
		ConfiguredInvalid: len(reading.invalid) > 0,
		Healthchecks:      mode,
	}
	path := health.ScheduleStatePath(d.cfg.BaseDir)
	if err := health.WriteScheduleState(d.cfg.BaseDir, st); err != nil {
		return fmt.Sprintf("save configured failed file=%s error=%v", path, err)
	}
	return fmt.Sprintf("saved configured frequency=%s healthchecks=%s to %s", reading.cadence.Frequency, mode, path)
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
	return d.scheduleAttemptTo(ctx, logging.GetDefaultLogger(), prefix)
}

// scheduleAttemptTo is scheduleAttempt with its DEBUG lines written to logger.
func (d *daemon) scheduleAttemptTo(ctx context.Context, logger *logging.Logger, prefix string) configPoll {
	p := d.pollCentralizedTo(ctx, logger, "schedule", prefix)
	if !p.sent {
		logging.DebugStep(logger, "schedule", "%s not sent error=%v", prefix, p.err)
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
	logScheduleAckTo(logging.GetDefaultLogger(), prefix, p)
}

// logScheduleAckTo is logScheduleAck written to logger.
func logScheduleAckTo(logger *logging.Logger, prefix string, p configPoll) {
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
// from them. With no relay secret, and none provisioned, no poll can be sent: the same blocks,
// their why line saying how far the provisioning got with the relay (part B, point 3b). It
// reports whether a poll was sent.
func (d *daemon) startScheduleNegotiation(ctx context.Context, reading scheduleReading) bool {
	configured := reading.cadence
	stateLine := d.loadScheduleState(reading)
	d.scheduleChanged = make(chan struct{}, 1)
	logger := logging.GetDefaultLogger()

	logging.Info("Applying backup schedule...")
	d.logScheduleRead(reading)
	logging.DebugStep(logger, "schedule", "%s", stateLine)
	var last, good configPoll
	var haveGood, confirmed bool
	var why pollReach
	sent := d.relaySecret(ctx) != ""
	if sent {
		last, good, haveGood, confirmed = d.runStartAttempts(ctx)
		why = last.answer()
	} else {
		last = configPoll{err: errNoRelaySecret}
		d.mu.Lock()
		why = d.provisionReach
		d.mu.Unlock()
		logging.DebugStep(logger, "schedule", "not sent, %v", errNoRelaySecret)
	}
	if !confirmed {
		d.logScheduleFallback()
	}
	switch {
	case len(reading.invalid) > 0:
		d.logScheduleNotAppliedOutcome(reading)
	case confirmed:
		logCadenceDetails(configured)
		logging.Info("%s Backup schedule: applied", theme.SymbolSuccess)
	default:
		logCadenceDetails(configured)
		run, _, _ := d.cadenceToRun(configured, d.now())
		logging.Info("  In effect: %s", cadenceLabel(run))
		logging.Info("%s", why.why())
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
		logging.DebugStep(logger, "notify policy", "%s", sameResponse(last))
	}
	d.logNotifyLevelOutcome(d.cfg.NotifyOn, want, why)

	if !haveGood {
		d.logPingURLsNotRefreshed(last, why)
	}
	return sent
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
// 43): the URLs cached in backup.env stay in use, or there are none; why names the reason. The
// block is remembered as open for logResolvedStartBlocks.
func (d *daemon) logPingURLsNotRefreshed(last configPoll, why pollReach) {
	logger := logging.GetDefaultLogger()
	d.mu.Lock()
	d.pingURLsBlockOpen = true
	d.mu.Unlock()
	logging.Info("Applying healthchecks ping URLs...")
	logging.DebugStep(logger, "ping urls", "%s", sameResponse(last))
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
		logging.Info("%s", why.why())
		logging.Info("%s Healthchecks ping URLs: not refreshed", theme.SymbolWarning)
		return
	}
	logging.Info("  Ping URLs: none")
	logging.Info("%s", why.why())
	logging.Warning("%s Healthchecks ping URLs: not available", theme.SymbolWarning)
}

// retryScheduleOnHeartbeat is the heartbeat's retry of a schedule the relay has not confirmed
// (todo point 40): one attempt, attempt=1/1. A failure is DEBUG only. A confirmation prints the
// applied block, its evidence after the header (with an invalid value, whose block repeats
// before every next-backup line, only its DEBUG evidence) and wakes scheduleLoop, which prints
// the next backup of the cadence now in effect and waits on it. An answer also prints the start
// blocks it resolves (logResolvedStartBlocks). The poll's ping URLs and notify ack are kept like
// any poll's. It reports whether a poll was sent.
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
	// The retry's evidence is held back until its outcome is known: a confirmation writes it
	// under the "Applying backup schedule..." header, anything else on its own.
	rec := newDebugRecorder()
	logging.DebugStep(rec.logger, "schedule", "heartbeat retry wanted=%s last_confirmed=%s", want, orNone(last))
	const prefix = "attempt=1/1"
	p := d.scheduleAttemptTo(ctx, rec.logger, prefix)
	if !p.sent {
		rec.flush()
		return false
	}
	if p.err == nil {
		if r := d.reporterFromPoll(p, false); r != nil {
			d.setReporter(r)
		}
	}
	if !scheduleConfirmedBy(p) {
		logScheduleAckTo(rec.logger, prefix, p)
		rec.flush()
		d.logResolvedStartBlocks(p)
		return true
	}

	reading := readConfiguredCadence(d.cfg)
	if len(reading.invalid) > 0 {
		logScheduleAckTo(rec.logger, prefix, p)
		rec.flush()
		d.confirmSchedule(p.frequency)
	} else {
		logging.Info("Applying backup schedule...")
		d.logScheduleRead(reading)
		logging.DebugStep(logger, "schedule", "read last_confirmed=%s source=%s", orNone(last), health.ScheduleStatePath(d.cfg.BaseDir))
		rec.flush()
		logScheduleAck(prefix, p)
		d.confirmSchedule(p.frequency)
		logCadenceDetails(configured)
		logging.Info("%s Backup schedule: applied", theme.SymbolSuccess)
	}
	d.logResolvedStartBlocks(p)
	select {
	case d.scheduleChanged <- struct{}{}:
	default:
	}
	return true
}

// logResolvedStartBlocks writes, after a heartbeat poll p the relay answered, the blocks left
// open that p resolves, in the start's order (part B, point 2): the notify level block that ended
// pending, once the relay confirms the level, and the start's ping URLs block, p having returned
// them.
func (d *daemon) logResolvedStartBlocks(p configPoll) {
	if !p.sent || p.err != nil {
		return
	}
	want := d.wantedNotifyPolicy()
	d.mu.Lock()
	notify := d.notifyBlockPending && d.notifyApplied != nil && d.notifyApplied.equal(want)
	raw := d.notifyBlockRaw
	ping := d.pingURLsBlockOpen
	d.pingURLsBlockOpen = false
	d.mu.Unlock()
	if notify {
		logging.Info("Applying notify level...")
		d.logNotifyPolicyRead(raw, want, d.configPath)
		d.logNotifyPolicyAnswer(p)
		d.logNotifyLevelOutcome(raw, want, p.answer())
	}
	if ping {
		logging.Info("Applying healthchecks ping URLs...")
		logging.DebugStep(logging.GetDefaultLogger(), "ping urls", "same response as schedule, alive_url=set backup_url=set")
		logging.Info("  Ping URLs: from ProxSave HC Server")
		logging.Info("%s Healthchecks ping URLs: applied", theme.SymbolSuccess)
	}
}
