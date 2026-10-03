package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
)

// scheduleRelay is a fake /api/healthcheck/config that records every poll and answers it as
// reply says (reply nil: 200 with the URLs and both acks applied).
type scheduleRelay struct {
	mu     sync.Mutex
	polls  []url.Values
	reply  func(n int, q url.Values) (int, any)
	server *httptest.Server
}

func newScheduleRelay(t *testing.T, reply func(n int, q url.Values) (int, any)) *scheduleRelay {
	t.Helper()
	r := &scheduleRelay{reply: reply}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		r.mu.Lock()
		r.polls = append(r.polls, q)
		n, reply := len(r.polls), r.reply
		r.mu.Unlock()
		status, body := http.StatusOK, any(relayBody(q, true, true))
		if reply != nil {
			status, body = reply(n, q)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *scheduleRelay) setReply(reply func(n int, q url.Values) (int, any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reply = reply
}

func (r *scheduleRelay) sent() []url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]url.Values(nil), r.polls...)
}

// relayBody is a 200 answer to poll q: the ping URLs, the notify ack and the schedule ack.
func relayBody(q url.Values, scheduleApplied, notifyApplied bool) map[string]any {
	channels := []string{}
	if v := q.Get("channels"); v != "" && v != "none" {
		channels = strings.Split(v, ",")
	}
	week := int64(7 * 86400)
	grace := int64(3600)
	return map[string]any{
		"mode":            "centralized",
		"alive_ping_url":  "https://hc.invalid/ping/alive",
		"backup_ping_url": "https://hc.invalid/ping/backup",
		"notify_policy": health.NotifyPolicyAck{
			ContractVersion: 1, Requested: q.Get("notify_on"), Applied: notifyApplied, Mode: "event_driven", Channels: channels,
		},
		"schedule": health.ScheduleAck{Requested: q.Get("frequency"), Applied: scheduleApplied, Timeout: &week, Grace: &grace},
	}
}

// negotiationDaemon is a centralized daemon at scheduleNow with a relay secret, the given
// SCHEDULER_* values, NOTIFY_ON=warning with email and Telegram, and the ping URLs cached in
// backup.env, whose start attempts are a millisecond apart.
func negotiationDaemon(t *testing.T, relayURL, freq, weekday, monthDay, hhmm string) *daemon {
	t.Helper()
	base := t.TempDir()
	if err := identity.PersistNotifySecret(context.Background(), base, relayTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	return &daemon{
		cfg: &config.Config{
			BaseDir:              base,
			SchedulerFrequency:   freq,
			SchedulerWeekday:     weekday,
			SchedulerMonthDay:    monthDay,
			SchedulerTime:        hhmm,
			HealthcheckEnabled:   true,
			HealthcheckMode:      config.HealthcheckModeCentralized,
			ServerID:             "123456789012",
			ServerAPIHost:        relayURL,
			NotifyOn:             config.NotifyOnWarning,
			EmailEnabled:         true,
			TelegramEnabled:      true,
			HealthcheckAliveURL:  "https://hc.invalid/ping/cached-alive",
			HealthcheckBackupURL: "https://hc.invalid/ping/cached-backup",
		},
		configPath:                 scheduleSource,
		now:                        func() time.Time { return scheduleNow },
		scheduleAttemptGapOverride: time.Millisecond,
	}
}

var (
	elapsedRE = regexp.MustCompile(`elapsed=\d+ms`)
	portRE    = regexp.MustCompile(`127\.0\.0\.1:\d+`)
)

// normalized is the log with what changes from run to run replaced: the relay's address by
// RELAY, the daemon's BASE_DIR by BASE, an elapsed time by elapsed=Nms.
func normalized(lines []string, d *daemon) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.ReplaceAll(l, d.cfg.BaseDir, "BASE")
		l = elapsedRE.ReplaceAllString(l, "elapsed=Nms")
		l = portRE.ReplaceAllString(l, "RELAY")
		out = append(out, l)
	}
	return out
}

// startLines runs the start (logScheduleStart) and the scheduler through its first next-backup
// line, and returns the log normalized.
func startLines(t *testing.T, d *daemon) []string {
	t.Helper()
	logs := captureDaemonLog(t)
	runScheduleStart(t, d)
	return normalized(logs(), d)
}

const stateFile = "BASE/daemon_state/.schedule_state.json"

// appliedAttempt is the DEBUG evidence of a first attempt the relay confirms.
func appliedAttempt(freq, notifyOn, channels string) []string {
	return []string{
		"DEBUG schedule: attempt=1/3 url=http://RELAY/api/healthcheck/config frequency=" + freq + " notify_on=" + notifyOn + " channels=" + channels,
		"DEBUG schedule: attempt=1/3 connected",
		"DEBUG schedule: attempt=1/3 request written",
		"DEBUG schedule: attempt=1/3 response http=200 elapsed=Nms",
		"DEBUG schedule: attempt=1/3 ack frequency=" + freq + " applied=true",
		"DEBUG schedule: saved confirmed=" + freq + " to " + stateFile,
	}
}

var notifyApplied = []string{
	"INFO Applying notify level...",
	"DEBUG notify policy: read notify_on=warning channels=email,telegram source=" + scheduleSource,
	"DEBUG notify policy: ack notify_on=warning channels=email,telegram mode=event_driven applied=true",
	"INFO   Notify level: warning",
	"INFO ✓ Notify level: applied",
}

func joinLines(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestScheduleCentralizedWeeklyApplied(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	got := startLines(t, d)
	assertLogExact(t, got, joinLines(
		[]string{
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source=" + scheduleSource,
			"DEBUG schedule: read last_confirmed=none source=" + stateFile + " default=daily",
		},
		appliedAttempt("weekly", "warning", "email,telegram"),
		[]string{
			"INFO   Frequency: weekly",
			"INFO   Weekday: Monday",
			"INFO   Time: 02:00",
			"INFO ✓ Backup schedule: applied",
		},
		notifyApplied,
		[]string{
			"DEBUG schedule: next run frequency=weekly weekday=mon monthday=1 time=02:00 at=2026-10-05T02:00:00Z",
			"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
		},
	)...)
	if polls := relay.sent(); len(polls) != 1 || polls[0].Get("frequency") != "weekly" || polls[0].Get("channels") != "email,telegram" || polls[0].Get("notify_on") != "warning" {
		t.Fatalf("relay polls = %v; want one with frequency=weekly, the channels and notify_on", polls)
	}
	if r := d.getReporter(); r == nil || !r.HasAliveURL() || !r.HasBackupURL() {
		t.Fatal("the start poll's ping URLs were not put to use")
	}
}

func TestScheduleCentralizedMonthlyApplied(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	d := negotiationDaemon(t, relay.server.URL, "monthly", "mon", "15", "02:00")
	got := startLines(t, d)
	assertLogExact(t, got, joinLines(
		[]string{
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=monthly weekday=mon monthday=15 time=02:00 source=" + scheduleSource,
			"DEBUG schedule: read last_confirmed=none source=" + stateFile + " default=daily",
		},
		appliedAttempt("monthly", "warning", "email,telegram"),
		[]string{
			"INFO   Frequency: monthly",
			"INFO   Day of month: 15",
			"INFO   Time: 02:00",
			"INFO ✓ Backup schedule: applied",
		},
		notifyApplied,
		[]string{
			"DEBUG schedule: next run frequency=monthly weekday=mon monthday=15 time=02:00 at=2026-10-15T02:00:00Z",
			"INFO daemon: next backup at 2026-10-15 02:00 (in 14d 14h1m28s)",
		},
	)...)
}

// A centralized daily that the relay confirms prints the applied block too.
func TestScheduleCentralizedDailyApplied(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	d := negotiationDaemon(t, relay.server.URL, "daily", "mon", "1", "03:00")
	got := startLines(t, d)
	assertLogExact(t, got, joinLines(
		[]string{
			"INFO Applying backup schedule...",
			"DEBUG schedule: read frequency=daily weekday=mon monthday=1 time=03:00 source=" + scheduleSource,
			"DEBUG schedule: read last_confirmed=none source=" + stateFile + " default=daily",
		},
		appliedAttempt("daily", "warning", "email,telegram"),
		[]string{
			"INFO   Frequency: daily",
			"INFO   Time: 03:00",
			"INFO ✓ Backup schedule: applied",
		},
		notifyApplied,
		[]string{
			"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=03:00 at=2026-10-01T03:00:00Z",
			"INFO daemon: next backup at 2026-10-01 03:00 (in 15h1m28s)",
		},
	)...)
}

// pendingSchedule is the schedule outcome of a weekly start the relay did not confirm, with no
// confirmed cadence on disk: daily is in effect.
func pendingSchedule(why string) []string {
	return []string{
		"DEBUG schedule: fallback frequency=daily reason=unconfirmed retry_every=5m0s",
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 02:00",
		"INFO   In effect: daily at 02:00",
		"INFO " + why,
		"INFO ⚠ Backup schedule: pending, no action needed",
	}
}

func notifyPending(evidence, why string) []string {
	return []string{
		"INFO Applying notify level...",
		"DEBUG notify policy: read notify_on=warning channels=email,telegram source=" + scheduleSource,
		evidence,
		"INFO   Notify level: warning",
		"INFO   In effect: always",
		"INFO " + why,
		"INFO ⚠ Notify level: pending, no action needed",
	}
}

var dailyNextRun = []string{
	"DEBUG schedule: next run frequency=daily weekday=mon monthday=1 time=02:00 at=2026-10-01T02:00:00Z",
	"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
}

var weeklyStartRead = []string{
	"INFO Applying backup schedule...",
	"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source=" + scheduleSource,
	"DEBUG schedule: read last_confirmed=none source=" + stateFile + " default=daily",
}

// answeredAttempts is the DEBUG evidence of three attempts the relay answered with status,
// each ending with outcome; the second and third reuse the first one's connection.
func answeredAttempts(status, outcome string, extra ...string) []string {
	var out []string
	for i, n := range []string{"1/3", "2/3", "3/3"} {
		connected := "connected"
		if i > 0 {
			connected = "connected reused=true"
		}
		out = append(out,
			"DEBUG schedule: attempt="+n+" url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
			"DEBUG schedule: attempt="+n+" "+connected,
			"DEBUG schedule: attempt="+n+" request written",
			"DEBUG schedule: attempt="+n+" response http="+status+" elapsed=Nms",
		)
		for _, e := range extra {
			out = append(out, "DEBUG schedule: attempt="+n+" "+e)
		}
		out = append(out, "DEBUG schedule: attempt="+n+" "+outcome)
	}
	return out
}

// closedRelayURL is the address of a relay that refuses the connection.
func closedRelayURL() string {
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	return u
}

// A relay that refuses the connection: three attempts, each failed at connect, daily stays in
// effect, and all three blocks say the server is not reachable; the cached URLs stay in use.
func TestScheduleCentralizedPendingNotReachable(t *testing.T) {
	d := negotiationDaemon(t, closedRelayURL(), "weekly", "mon", "1", "02:00")
	got := startLines(t, d)
	var attempts []string
	for _, n := range []string{"1/3", "2/3", "3/3"} {
		attempts = append(attempts,
			"DEBUG schedule: attempt="+n+" url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
			"DEBUG schedule: attempt="+n+" failed stage=connect error=Get: dial tcp RELAY: connect: connection refused",
		)
	}
	assertLogExact(t, got, joinLines(
		weeklyStartRead,
		attempts,
		pendingSchedule("ProxSave HC Server not reachable"),
		notifyPending("DEBUG notify policy: same response as schedule, failed stage=connect", "ProxSave HC Server not reachable"),
		[]string{
			"INFO Applying healthchecks ping URLs...",
			"DEBUG ping urls: same response as schedule, failed stage=connect",
			"DEBUG ping urls: fallback cached alive_url=set backup_url=set source=" + scheduleSource,
			"INFO   Ping URLs: cached from backup.env",
			"INFO ProxSave HC Server not reachable",
			"INFO ⚠ Healthchecks ping URLs: not refreshed",
		},
		dailyNextRun,
	)...)
	if r := d.getReporter(); r == nil || !r.HasAliveURL() {
		t.Fatal("the cached ping URLs were not put to use")
	}
}

// A relay that answers 503: not ready, and with no URL cached in backup.env the ping URLs block
// is the one WARNING.
func TestScheduleCentralizedPendingNotReady(t *testing.T) {
	relay := newScheduleRelay(t, func(int, url.Values) (int, any) {
		return http.StatusServiceUnavailable, map[string]string{"error": "HC_NOT_READY"}
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "", ""
	got := startLines(t, d)
	assertLogExact(t, got, joinLines(
		weeklyStartRead,
		answeredAttempts("503", "failed http=503 error=healthcheck config: provisioning not ready",
			`response body="{\"error\":\"HC_NOT_READY\"}"`),
		pendingSchedule("ProxSave HC Server not ready"),
		notifyPending("DEBUG notify policy: same response as schedule, failed http=503", "ProxSave HC Server not ready"),
		[]string{
			"INFO Applying healthchecks ping URLs...",
			"DEBUG ping urls: same response as schedule, failed http=503",
			"DEBUG ping urls: fallback cached alive_url=none backup_url=none source=" + scheduleSource,
			"INFO   Ping URLs: none",
			"INFO ProxSave HC Server not ready",
			"WARNING ⚠ Healthchecks ping URLs: not available",
		},
		dailyNextRun,
	)...)
	if r := d.getReporter(); r != nil {
		t.Fatal("a reporter was built with no URL anywhere")
	}
}

// A 200 without the schedule ack: the relay did not confirm; the URLs and the notify level came
// with it, so those blocks are fine and there is no ping URLs block.
func TestScheduleCentralizedPendingAckMissing(t *testing.T) {
	relay := newScheduleRelay(t, func(_ int, q url.Values) (int, any) {
		body := relayBody(q, true, true)
		delete(body, "schedule")
		return http.StatusOK, body
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	got := startLines(t, d)
	assertLogExact(t, got, joinLines(
		weeklyStartRead,
		answeredAttempts("200", "ack missing"),
		pendingSchedule("ProxSave HC Server did not confirm"),
		notifyApplied,
		dailyNextRun,
	)...)
	if polls := relay.sent(); len(polls) != 3 {
		t.Fatalf("relay polls = %d; want 3 attempts", len(polls))
	}
}

// A 200 whose schedule ack says applied=false: the relay did not confirm.
func TestScheduleCentralizedPendingNotApplied(t *testing.T) {
	relay := newScheduleRelay(t, func(_ int, q url.Values) (int, any) {
		return http.StatusOK, relayBody(q, false, false)
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	got := startLines(t, d)
	assertLogExact(t, got, joinLines(
		weeklyStartRead,
		answeredAttempts("200", "ack frequency=weekly applied=false"),
		pendingSchedule("ProxSave HC Server did not confirm"),
		notifyPending("DEBUG notify policy: ack notify_on=warning channels=email,telegram mode=event_driven applied=false", "ProxSave HC Server did not confirm"),
		dailyNextRun,
	)...)
}

// The second attempt confirms: no third one, and the block is the applied one.
func TestScheduleCentralizedSecondAttemptConfirms(t *testing.T) {
	relay := newScheduleRelay(t, func(n int, q url.Values) (int, any) {
		if n == 1 {
			return http.StatusServiceUnavailable, map[string]string{"error": "HC_NOT_READY"}
		}
		return http.StatusOK, relayBody(q, true, true)
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	got := startLines(t, d)
	if polls := relay.sent(); len(polls) != 2 {
		t.Fatalf("relay polls = %d; want 2, the second confirming", len(polls))
	}
	assertLogSequence(t, got,
		"DEBUG schedule: attempt=1/3 failed http=503 error=healthcheck config: provisioning not ready",
		"DEBUG schedule: attempt=2/3 ack frequency=weekly applied=true",
		"INFO ✓ Backup schedule: applied",
		"INFO ✓ Notify level: applied",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)
	assertNoLogLine(t, got, "attempt=3/3")
	assertNoLogLine(t, got, "Ping URLs")
}

// The confirmed weekly survives a restart: the next start reads it from daemon_state/, and while
// the relay is unreachable weekly stays in effect.
func TestScheduleConfirmedCadenceSurvivesARestart(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	first := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	_ = startLines(t, first)

	path := health.ScheduleStatePath(first.cfg.BaseDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("schedule state not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("schedule state mode = %o, want 0600", info.Mode().Perm())
	}
	st, found, err := health.ReadScheduleState(first.cfg.BaseDir)
	if err != nil || !found {
		t.Fatalf("read schedule state = %v, %v", found, err)
	}
	want := health.ScheduleCadence{Frequency: "weekly", Weekday: "mon", MonthDay: 1, Time: "02:00"}
	if st.LastConfirmed != "weekly" || st.Configured == nil || *st.Configured != want || st.SchemaVersion != 1 {
		t.Fatalf("schedule state = %+v (configured %+v); want last_confirmed=weekly, configured %+v", st, st.Configured, want)
	}

	restarted := negotiationDaemon(t, closedRelayURL(), "weekly", "mon", "1", "02:00")
	restarted.cfg.BaseDir = first.cfg.BaseDir
	got := startLines(t, restarted)
	assertLogSequence(t, got,
		"DEBUG schedule: read last_confirmed=weekly source="+stateFile,
		"DEBUG schedule: fallback frequency=weekly reason=unconfirmed retry_every=5m0s",
		"INFO   In effect: weekly, Monday at 02:00",
		"INFO ProxSave HC Server not reachable",
		"INFO ⚠ Backup schedule: pending, no action needed",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)
}

// A heartbeat retry the relay still does not confirm is DEBUG only: the retry, the attempt and
// why it failed; no INFO, no new next-backup line.
func TestScheduleHeartbeatRetryFailureIsDebugOnly(t *testing.T) {
	t.Run("not reachable", func(t *testing.T) {
		d := negotiationDaemon(t, closedRelayURL(), "weekly", "mon", "1", "02:00")
		_ = startLines(t, d)
		logs := captureDaemonLog(t)
		if !d.retryScheduleOnHeartbeat(context.Background()) {
			t.Fatal("the heartbeat sent no retry while the schedule is pending")
		}
		assertLogExact(t, normalized(logs(), d),
			"DEBUG schedule: heartbeat retry wanted=weekly last_confirmed=none",
			"DEBUG schedule: attempt=1/1 url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
			"DEBUG schedule: attempt=1/1 failed stage=connect error=Get: dial tcp RELAY: connect: connection refused",
		)
	})
	t.Run("did not confirm", func(t *testing.T) {
		relay := newScheduleRelay(t, func(_ int, q url.Values) (int, any) { return http.StatusOK, relayBody(q, false, true) })
		d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
		_ = startLines(t, d)
		logs := captureDaemonLog(t)
		if !d.retryScheduleOnHeartbeat(context.Background()) {
			t.Fatal("the heartbeat sent no retry while the schedule is pending")
		}
		assertLogExact(t, normalized(logs(), d),
			"DEBUG schedule: heartbeat retry wanted=weekly last_confirmed=none",
			"DEBUG schedule: attempt=1/1 url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
			"DEBUG schedule: attempt=1/1 connected reused=true",
			"DEBUG schedule: attempt=1/1 request written",
			"DEBUG schedule: attempt=1/1 response http=200 elapsed=Nms",
			"DEBUG schedule: attempt=1/1 ack frequency=weekly applied=false",
		)
	})
}

// waitForLine waits until the log holds line, the scheduler running on another goroutine.
func waitForLine(t *testing.T, logs func() []string, line string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countLogLines(logs(), line) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log never reached %q\nlog:\n%s", line, strings.Join(logs(), "\n"))
}

// A heartbeat retry the relay confirms prints the whole applied block, its evidence after the
// header, and the scheduler, which was waiting on the daily cadence, prints the next backup of
// the weekly one and waits on it. The start's notify level was applied: no block for it.
func TestScheduleHeartbeatConfirmationReArmsTheWait(t *testing.T) {
	relay := newScheduleRelay(t, func(_ int, q url.Values) (int, any) { return http.StatusOK, relayBody(q, false, true) })
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	logs := captureDaemonLog(t)
	d.logScheduleStart(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- d.scheduleLoop(ctx) }()
	waitForLine(t, logs, "INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)")
	mark := len(logs())

	relay.setReply(nil)
	if !d.retryScheduleOnHeartbeat(ctx) {
		t.Fatal("the heartbeat sent no retry while the schedule is pending")
	}
	waitForLine(t, logs, "INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)")
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scheduleLoop did not return")
	}
	assertLogExact(t, normalized(logs()[mark:], d),
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source="+scheduleSource,
		"DEBUG schedule: read last_confirmed=none source="+stateFile,
		"DEBUG schedule: heartbeat retry wanted=weekly last_confirmed=none",
		"DEBUG schedule: attempt=1/1 url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
		"DEBUG schedule: attempt=1/1 connected reused=true",
		"DEBUG schedule: attempt=1/1 request written",
		"DEBUG schedule: attempt=1/1 response http=200 elapsed=Nms",
		"DEBUG schedule: attempt=1/1 ack frequency=weekly applied=true",
		"DEBUG schedule: saved confirmed=weekly to "+stateFile,
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"DEBUG schedule: next run frequency=weekly weekday=mon monthday=1 time=02:00 at=2026-10-05T02:00:00Z",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)

	// Confirmed: the next heartbeat sends nothing.
	polls := len(relay.sent())
	if d.retryScheduleOnHeartbeat(context.Background()) || len(relay.sent()) != polls {
		t.Fatal("a heartbeat retried a schedule the relay had confirmed")
	}
	if st, _, _ := health.ReadScheduleState(d.cfg.BaseDir); st.LastConfirmed != "weekly" {
		t.Fatalf("schedule state last_confirmed = %q, want weekly", st.LastConfirmed)
	}
}

// heartbeatConfirmation runs one heartbeat retry while scheduleLoop waits, and returns the log
// it wrote, through the next-backup line the confirmation re-arms.
func heartbeatConfirmation(t *testing.T, d *daemon, logs func() []string, firstNext, nextAfter string) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- d.scheduleLoop(ctx) }()
	waitForLine(t, logs, firstNext)
	mark := len(logs())
	if !d.retryScheduleOnHeartbeat(ctx) {
		t.Fatal("the heartbeat sent no retry while the schedule is pending")
	}
	waitForLine(t, logs, nextAfter)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scheduleLoop did not return")
	}
	return normalized(logs()[mark:], d)
}

// A start the relay did not answer left the notify level pending and no ping URLs: the heartbeat
// that gets the answer prints, after the schedule block, those two blocks applied, in the start's
// order, then the next backup (part B, point 2).
func TestScheduleHeartbeatAnswerResolvesTheStartBlocks(t *testing.T) {
	relay := newScheduleRelay(t, func(int, url.Values) (int, any) {
		return http.StatusServiceUnavailable, map[string]string{"error": "HC_NOT_READY"}
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "", ""
	logs := captureDaemonLog(t)
	d.logScheduleStart(context.Background())
	relay.setReply(nil)
	got := heartbeatConfirmation(t, d, logs,
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)")
	assertLogExact(t, got,
		"INFO Applying backup schedule...",
		"DEBUG schedule: read frequency=weekly weekday=mon monthday=1 time=02:00 source="+scheduleSource,
		"DEBUG schedule: read last_confirmed=none source="+stateFile,
		"DEBUG schedule: heartbeat retry wanted=weekly last_confirmed=none",
		"DEBUG schedule: attempt=1/1 url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
		"DEBUG schedule: attempt=1/1 connected reused=true",
		"DEBUG schedule: attempt=1/1 request written",
		"DEBUG schedule: attempt=1/1 response http=200 elapsed=Nms",
		"DEBUG schedule: attempt=1/1 ack frequency=weekly applied=true",
		"DEBUG schedule: saved confirmed=weekly to "+stateFile,
		"INFO   Frequency: weekly",
		"INFO   Weekday: Monday",
		"INFO   Time: 02:00",
		"INFO ✓ Backup schedule: applied",
		"INFO Applying notify level...",
		"DEBUG notify policy: read notify_on=warning channels=email,telegram source="+scheduleSource,
		"DEBUG notify policy: ack notify_on=warning channels=email,telegram mode=event_driven applied=true",
		"INFO   Notify level: warning",
		"INFO ✓ Notify level: applied",
		"INFO Applying healthchecks ping URLs...",
		"DEBUG ping urls: same response as schedule, alive_url=set backup_url=set",
		"INFO   Ping URLs: from ProxSave HC Server",
		"INFO ✓ Healthchecks ping URLs: applied",
		"DEBUG schedule: next run frequency=weekly weekday=mon monthday=1 time=02:00 at=2026-10-05T02:00:00Z",
		"INFO daemon: next backup at 2026-10-05 02:00 (in 4d 14h1m28s)",
	)
	if r := d.getReporter(); r == nil || !r.HasAliveURL() || !r.HasBackupURL() {
		t.Fatal("the heartbeat's ping URLs were not put to use")
	}
}

// An answer that does not confirm the schedule still resolves the other blocks: the notify level
// and the ping URLs print applied, the schedule stays DEBUG only and pending, no next-backup line.
// They print once: the confirmation that follows prints the schedule block alone.
func TestScheduleHeartbeatAnswerWithoutConfirmationResolvesTheOtherBlocks(t *testing.T) {
	relay := newScheduleRelay(t, func(int, url.Values) (int, any) {
		return http.StatusServiceUnavailable, map[string]string{"error": "HC_NOT_READY"}
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "", ""
	_ = startLines(t, d)

	relay.setReply(func(_ int, q url.Values) (int, any) { return http.StatusOK, relayBody(q, false, true) })
	logs := captureDaemonLog(t)
	if !d.retryScheduleOnHeartbeat(context.Background()) {
		t.Fatal("the heartbeat sent no retry while the schedule is pending")
	}
	assertLogExact(t, normalized(logs(), d),
		"DEBUG schedule: heartbeat retry wanted=weekly last_confirmed=none",
		"DEBUG schedule: attempt=1/1 url=http://RELAY/api/healthcheck/config frequency=weekly notify_on=warning channels=email,telegram",
		"DEBUG schedule: attempt=1/1 connected reused=true",
		"DEBUG schedule: attempt=1/1 request written",
		"DEBUG schedule: attempt=1/1 response http=200 elapsed=Nms",
		"DEBUG schedule: attempt=1/1 ack frequency=weekly applied=false",
		"INFO Applying notify level...",
		"DEBUG notify policy: read notify_on=warning channels=email,telegram source="+scheduleSource,
		"DEBUG notify policy: ack notify_on=warning channels=email,telegram mode=event_driven applied=true",
		"INFO   Notify level: warning",
		"INFO ✓ Notify level: applied",
		"INFO Applying healthchecks ping URLs...",
		"DEBUG ping urls: same response as schedule, alive_url=set backup_url=set",
		"INFO   Ping URLs: from ProxSave HC Server",
		"INFO ✓ Healthchecks ping URLs: applied",
	)

	relay.setReply(nil)
	logs = captureDaemonLog(t)
	if !d.retryScheduleOnHeartbeat(context.Background()) {
		t.Fatal("the heartbeat sent no retry while the schedule is pending")
	}
	got := logs()
	assertLogSequence(t, got, "INFO Applying backup schedule...", "INFO ✓ Backup schedule: applied")
	assertNoLogLine(t, got, "Notify level")
	assertNoLogLine(t, got, "Ping URLs")
}

// A heartbeat tick whose schedule retry already polled the relay does not poll it a second time
// to re-resolve missing ping URLs: the retry was that poll.
func TestScheduleHeartbeatTickPollsOnce(t *testing.T) {
	relay := newScheduleRelay(t, func(int, url.Values) (int, any) {
		return http.StatusServiceUnavailable, map[string]string{"error": "HC_NOT_READY"}
	})
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "", ""
	_ = startLines(t, d)
	before := len(relay.sent())

	d.beatTick(context.Background())

	if n := len(relay.sent()) - before; n != 1 {
		t.Fatalf("one heartbeat tick polled the relay %d times; want 1", n)
	}
}

// run() in centralized mode: the schedule, notify level and ping URLs blocks, in that order,
// before the first next-backup line, and the start's failed poll is reported by the ping URLs
// block instead of the old "centralized fetch failed" WARNING.
func TestScheduleCentralizedRunPrintsTheBlocksInOrder(t *testing.T) {
	orig := daemonEvaluateUpdate
	t.Cleanup(func() { daemonEvaluateUpdate = orig })
	daemonEvaluateUpdate = func(context.Context, *logging.Logger, string) *UpdateInfo { return nil }

	scheduled := make(chan struct{})
	var once sync.Once
	logs := captureDaemonLogWith(t, func(entry string) {
		if strings.Contains(entry, "daemon: next backup at") {
			once.Do(func() { close(scheduled) })
		}
	})
	d := negotiationDaemon(t, closedRelayURL(), "weekly", "mon", "1", "02:00")
	d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "", ""
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
	got := logs()
	assertLogSequence(t, got,
		"INFO Applying backup schedule...",
		"INFO ⚠ Backup schedule: pending, no action needed",
		"INFO Applying notify level...",
		"INFO ⚠ Notify level: pending, no action needed",
		"INFO Applying healthchecks ping URLs...",
		"WARNING ⚠ Healthchecks ping URLs: not available",
		"INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)",
	)
	// The background loops' later re-resolves stay DEBUG: the start counted as the one warning.
	assertNoLogLine(t, got, "WARNING daemon: healthcheck centralized fetch failed")
}

// An invalid value in centralized mode: the daemon asks the relay for the daily it falls back
// to, and the not-applied block reports the outcome, the attempts inside it.
func TestScheduleCentralizedInvalidValueNegotiatesTheFallback(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	d := negotiationDaemon(t, relay.server.URL, "fortnightly", "mon", "1", "03:00")
	got := startLines(t, d)
	assertLogSequence(t, got,
		"INFO Applying backup schedule...",
		`DEBUG schedule: invalid SCHEDULER_FREQUENCY="fortnightly" error=frequency must be daily, weekly, or monthly`,
		"DEBUG schedule: attempt=1/3 url=http://RELAY/api/healthcheck/config frequency=daily notify_on=warning channels=email,telegram",
		"DEBUG schedule: attempt=1/3 ack frequency=daily applied=true",
		"INFO   Frequency: fortnightly",
		"INFO   In effect: daily at 03:00",
		`INFO Frequency "fortnightly" is not daily, weekly or monthly`,
		"WARNING ⚠ Backup schedule: not applied",
		"INFO ✓ Notify level: applied",
		"INFO daemon: next backup at 2026-10-01 03:00 (in 15h1m28s)",
	)
	assertNoLogLine(t, got, "Backup schedule: applied")
}

// A hand edit of backup.env applies at restart: monthly configured after a confirmed weekly
// is pending with weekly in effect, and the state records the new configuration.
func TestScheduleHandEditAppliesAtRestart(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	first := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	_ = startLines(t, first)

	relay.setReply(func(_ int, q url.Values) (int, any) { return http.StatusOK, relayBody(q, false, true) })
	restarted := negotiationDaemon(t, relay.server.URL, "monthly", "mon", "15", "02:00")
	restarted.cfg.BaseDir = first.cfg.BaseDir
	got := startLines(t, restarted)
	assertLogSequence(t, got,
		"DEBUG schedule: attempt=1/3 url=http://RELAY/api/healthcheck/config frequency=monthly notify_on=warning channels=email,telegram",
		"INFO   Frequency: monthly",
		"INFO   Day of month: 15",
		"INFO   In effect: weekly, Monday at 02:00",
		"INFO ⚠ Backup schedule: pending, no action needed",
	)
	st, _, _ := health.ReadScheduleState(first.cfg.BaseDir)
	if st.LastConfirmed != "weekly" || st.Configured == nil || st.Configured.Frequency != "monthly" || st.Configured.MonthDay != 15 {
		t.Fatalf("schedule state = %+v (configured %+v); want last_confirmed=weekly, configured monthly day 15", st, st.Configured)
	}
}

// An invalid value runs the daily fallback even when the relay confirmed monthly before and is
// unreachable now: the earlier confirmation must not postpone the backup by weeks (D1/D5).
func TestScheduleInvalidValueIgnoresAnEarlierConfirmation(t *testing.T) {
	for _, tc := range []struct{ name, freq, hhmm, inEffect, next string }{
		{"invalid time", "monthly", "25:99", "INFO   In effect: daily at 02:00", "INFO daemon: next backup at 2026-10-01 02:00 (in 14h1m28s)"},
		{"invalid frequency", "fortnightly", "04:15", "INFO   In effect: daily at 04:15", "INFO daemon: next backup at 2026-10-01 04:15 (in 16h16m28s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := negotiationDaemon(t, closedRelayURL(), tc.freq, "mon", "1", tc.hhmm)
			st := health.ScheduleState{LastConfirmed: "monthly", Healthchecks: health.ScheduleHealthchecksCentralized,
				Configured: &health.ScheduleCadence{Frequency: "monthly", Weekday: "mon", MonthDay: 1, Time: "02:00"}}
			if err := health.WriteScheduleState(d.cfg.BaseDir, st); err != nil {
				t.Fatal(err)
			}
			got := startLines(t, d)
			assertLogSequence(t, got, tc.inEffect, "WARNING ⚠ Backup schedule: not applied", tc.next)
			assertNoLogLine(t, got, "monthly, day 1")
		})
	}
}
