package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// policyRelay is a fake config endpoint that records every contract-1 poll and acks it,
// applied or not.
type policyRelay struct {
	mu      sync.Mutex
	polls   []notifyPolicy
	applied bool
	release chan struct{} // when non-nil, every poll waits on it
	onPoll  func(n int)   // when non-nil, runs before answering poll n (1-based)
}

func (p *policyRelay) handler(w http.ResponseWriter, r *http.Request) {
	if p.release != nil {
		<-p.release
	}
	q := r.URL.Query()
	channels := []string{}
	if v := q.Get("channels"); v != "" && v != "none" {
		channels = strings.Split(v, ",")
	}
	p.mu.Lock()
	p.polls = append(p.polls, notifyPolicy{notifyOn: q.Get("notify_on"), channels: channels})
	applied, n, hook := p.applied, len(p.polls), p.onPoll
	p.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mode":            "centralized",
		"alive_ping_url":  "https://hc.invalid/ping/alive",
		"backup_ping_url": "https://hc.invalid/ping/backup",
		"notify_policy": health.NotifyPolicyAck{
			ContractVersion: 1, Requested: q.Get("notify_on"), Applied: applied, Channels: channels,
		},
	})
}

func (p *policyRelay) sent() []notifyPolicy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]notifyPolicy(nil), p.polls...)
}

// policyDaemon is a centralized daemon that started with NOTIFY_ON=warning and Telegram, whose
// backup.env now holds body. The shell cannot override what the file says.
func policyDaemon(t *testing.T, relay *policyRelay, body string) *daemon {
	t.Helper()
	for _, key := range []string{"NOTIFY_ON", "TELEGRAM_ENABLED", "TELEGRAM_ENABLE", "EMAIL_ENABLED", "EMAIL_ENABLE",
		"GOTIFY_ENABLED", "GOTIFY_ENABLE", "WEBHOOK_ENABLED", "WEBHOOK_ENABLE"} {
		t.Setenv(key, "")
	}
	base := t.TempDir()
	if err := identity.PersistNotifySecret(context.Background(), base, relayTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	path := filepath.Join(base, "backup.env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write backup.env: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(relay.handler))
	t.Cleanup(server.Close)
	return &daemon{
		cfg: &config.Config{
			ConfigPath:         path,
			BaseDir:            base,
			HealthcheckEnabled: true,
			HealthcheckMode:    config.HealthcheckModeCentralized,
			ServerID:           "123456789012",
			ServerAPIHost:      server.URL,
			NotifyOn:           config.NotifyOnWarning,
			TelegramEnabled:    true,
		},
		now: time.Now,
	}
}

func (d *daemon) appliedNotifyPolicy() *notifyPolicy {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.notifyApplied
}

// captureDaemonDebug routes the default logger, at DEBUG, into a buffer for the test.
func captureDaemonDebug(t *testing.T) *bytes.Buffer {
	t.Helper()
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	logging.SetDefaultLogger(logger)
	return &buf
}

var startupPolicy = notifyPolicy{notifyOn: config.NotifyOnWarning, channels: []string{"telegram"}}

// A hand edit of NOTIFY_ON reaches the relay before the next scheduled run, without a daemon
// restart, and the daemon keeps the relay's confirmation of it.
func TestRefreshNotifyPolicySendsAHandEditBeforeTheRun(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=failure\nTELEGRAM_ENABLED=true\n")
	d.notifyApplied = &startupPolicy
	logged := captureDaemonDebug(t)

	d.refreshNotifyPolicy(context.Background())

	want := notifyPolicy{notifyOn: config.NotifyOnFailure, channels: []string{"telegram"}}
	polls := relay.sent()
	if len(polls) != 1 || !polls[0].equal(want) {
		t.Fatalf("relay polls = %+v; want exactly one with %+v", polls, want)
	}
	if got := d.appliedNotifyPolicy(); got == nil || !got.equal(want) {
		t.Fatalf("applied policy = %+v; want %+v", got, want)
	}
	// The changed level is the approved block (todo point 42): the stages of the poll in DEBUG,
	// the level, applied.
	assertLogExact(t, normalized(debugEntries(logged.String()), d),
		"INFO Applying notify level...",
		"DEBUG notify policy: read notify_on=failure channels=telegram source="+strings.ReplaceAll(d.cfg.ConfigPath, d.cfg.BaseDir, "BASE"),
		"DEBUG notify policy: url=http://RELAY/api/healthcheck/config frequency=daily notify_on=failure channels=telegram",
		"DEBUG notify policy: connected",
		"DEBUG notify policy: request written",
		"DEBUG notify policy: response http=200 elapsed=Nms",
		"DEBUG notify policy: ack notify_on=failure channels=telegram mode= applied=true",
		"INFO   Notify level: failure",
		"INFO ✓ Notify level: applied",
	)
}

// debugEntries is a captureDaemonDebug buffer as "LEVEL message" entries, like captureDaemonLog.
func debugEntries(raw string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		_, rest, ok := strings.Cut(line, "] ")
		if !ok || len(rest) < 9 {
			continue
		}
		out = append(out, strings.TrimSpace(rest[:9])+" "+rest[9:])
	}
	return out
}

// A channel switched on by hand is part of what the run checks against the relay, so it is
// renegotiated the same way.
func TestRefreshNotifyPolicySendsAChannelSwitchedOnByHand(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=warning\nTELEGRAM_ENABLED=true\nGOTIFY_ENABLED=true\n")
	d.notifyApplied = &startupPolicy

	d.refreshNotifyPolicy(context.Background())

	want := notifyPolicy{notifyOn: config.NotifyOnWarning, channels: []string{"gotify", "telegram"}}
	if polls := relay.sent(); len(polls) != 1 || !polls[0].equal(want) {
		t.Fatalf("relay polls = %+v; want exactly one with %+v", polls, want)
	}
}

// The normal case: nothing changed and the relay confirmed it, so the run starts without a
// request. At 00:00 UTC every host starts together, and this is what keeps that free.
func TestRefreshNotifyPolicySkipsTheRelayWhenNothingChanged(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=warning\nTELEGRAM_ENABLED=true\n")
	d.notifyApplied = &startupPolicy
	logged := captureDaemonDebug(t)

	d.refreshNotifyPolicy(context.Background())

	if polls := relay.sent(); len(polls) != 0 {
		t.Fatalf("relay polls = %+v; want none when the confirmed policy is unchanged", polls)
	}
	// Nothing sent: one DEBUG line and no block (todo point 42).
	assertLogExact(t, debugEntries(logged.String()),
		"DEBUG notify policy: before run notify_on=warning channels=telegram already confirmed, not sent")
}

// A policy the relay did not apply (unreachable at daemon start, or a deferred conversion) is
// asked for again before every run until it is confirmed.
func TestRefreshNotifyPolicyRetriesAPolicyTheRelayDidNotApply(t *testing.T) {
	relay := &policyRelay{applied: false}
	d := policyDaemon(t, relay, "NOTIFY_ON=warning\nTELEGRAM_ENABLED=true\n")

	d.refreshNotifyPolicy(context.Background())
	d.refreshNotifyPolicy(context.Background())

	if polls := relay.sent(); len(polls) != 2 {
		t.Fatalf("relay polls = %d; want 2, one before each run while the policy is unapplied", len(polls))
	}
	if got := d.appliedNotifyPolicy(); got != nil {
		t.Fatalf("applied policy = %+v; want none while the relay answers applied=false", got)
	}
}

// The DEBUG line reports an unapplied answer as such, and an empty channel set as the "none"
// the poll sends.
func TestRefreshNotifyPolicyLogsAnUnappliedAnswer(t *testing.T) {
	relay := &policyRelay{applied: false}
	d := policyDaemon(t, relay, "NOTIFY_ON=warning\nTELEGRAM_ENABLED=false\n")
	logged := captureDaemonDebug(t)

	d.refreshNotifyPolicy(context.Background())

	// The relay answered and did not apply it: the pending form, every outcome notified meanwhile.
	assertLogExact(t, normalized(debugEntries(logged.String()), d),
		"INFO Applying notify level...",
		"DEBUG notify policy: read notify_on=warning channels=none source="+strings.ReplaceAll(d.cfg.ConfigPath, d.cfg.BaseDir, "BASE"),
		"DEBUG notify policy: url=http://RELAY/api/healthcheck/config frequency=daily notify_on=warning channels=none",
		"DEBUG notify policy: connected",
		"DEBUG notify policy: request written",
		"DEBUG notify policy: response http=200 elapsed=Nms",
		"DEBUG notify policy: ack notify_on=warning channels=none mode= applied=false",
		"INFO   Notify level: warning",
		"INFO   In effect: always",
		"INFO ProxSave HC Server did not confirm",
		"INFO ⚠ Notify level: pending, no action needed",
	)
}

// An unreachable relay before a run: the pending form with the network why line, and the run
// is held only by the bounded wait.
func TestRefreshNotifyPolicyUnreachableRelayIsPending(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=failure\nTELEGRAM_ENABLED=true\n")
	d.notifyApplied = &startupPolicy
	d.cfg.ServerAPIHost = closedRelayURL()
	logged := captureDaemonDebug(t)

	d.refreshNotifyPolicy(context.Background())

	got := normalized(debugEntries(logged.String()), d)
	assertLogSequence(t, got,
		"INFO Applying notify level...",
		"DEBUG notify policy: url=http://RELAY/api/healthcheck/config frequency=daily notify_on=failure channels=telegram",
		"DEBUG notify policy: failed stage=connect error=Get: dial tcp RELAY: connect: connection refused",
		"INFO   Notify level: failure",
		"INFO   In effect: always",
		"INFO ProxSave HC Server not reachable",
		"INFO ⚠ Notify level: pending, no action needed",
	)
	if got := d.appliedNotifyPolicy(); got == nil || !got.equal(startupPolicy) {
		t.Fatalf("applied policy = %+v; an unanswered poll must leave the last confirmation", got)
	}
}

// A relay slower than the bounded wait: pending, not reachable, and the run goes on.
func TestRefreshNotifyPolicyTimeoutIsPendingNotReachable(t *testing.T) {
	relay := &policyRelay{applied: true, release: make(chan struct{})}
	d := policyDaemon(t, relay, "NOTIFY_ON=failure\nTELEGRAM_ENABLED=true\n")
	d.notifyRefreshWaitOverride = 50 * time.Millisecond
	t.Cleanup(func() { close(relay.release) })
	logs := captureDaemonLog(t) // a locked sink: the poll is still writing to it after the wait

	d.refreshNotifyPolicy(context.Background())

	assertLogSequence(t, logs(),
		"INFO Applying notify level...",
		"DEBUG notify policy: no answer within 50ms",
		"INFO   Notify level: failure",
		"INFO   In effect: always",
		"INFO ProxSave HC Server not reachable",
		"INFO ⚠ Notify level: pending, no action needed",
	)
}

// A slow relay delays the run by the bounded wait and no more; the poll finishes on its own.
func TestRefreshNotifyPolicyWaitsForASlowRelayOnlyUpToTheBound(t *testing.T) {
	relay := &policyRelay{applied: true, release: make(chan struct{})}
	d := policyDaemon(t, relay, "NOTIFY_ON=failure\nTELEGRAM_ENABLED=true\n")
	d.notifyRefreshWaitOverride = 50 * time.Millisecond
	t.Cleanup(func() { close(relay.release) })

	started := time.Now()
	d.refreshNotifyPolicy(context.Background())
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("refresh took %s with a relay that never answers; want about the 50ms bound", elapsed)
	}
}

// Only a centralized daemon negotiates, and an unreadable backup.env keeps the last policy
// rather than sending a guess.
func TestRefreshNotifyPolicyStaysSilentWhereThereIsNothingToNegotiate(t *testing.T) {
	cases := map[string]func(d *daemon){
		"self mode":         func(d *daemon) { d.cfg.HealthcheckMode = config.HealthcheckModeSelf },
		"healthchecks off":  func(d *daemon) { d.cfg.HealthcheckEnabled = false },
		"unreadable config": func(d *daemon) { d.cfg.ConfigPath = filepath.Join(d.cfg.BaseDir, "missing.env") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			relay := &policyRelay{applied: true}
			d := policyDaemon(t, relay, "NOTIFY_ON=failure\nTELEGRAM_ENABLED=true\n")
			mutate(d)

			d.refreshNotifyPolicy(context.Background())

			if polls := relay.sent(); len(polls) != 0 {
				t.Fatalf("relay polls = %+v; want none", polls)
			}
		})
	}
}

// Every config poll sends the policy last read from backup.env, not the one from daemon start,
// so a heartbeat's lazy re-resolve cannot put the old policy back on the relay.
func TestEveryConfigPollSendsThePolicyLastRead(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=failure\nTELEGRAM_ENABLED=true\n")
	d.notifyApplied = &startupPolicy

	d.refreshNotifyPolicy(context.Background())
	_ = d.buildReporter(context.Background())

	want := notifyPolicy{notifyOn: config.NotifyOnFailure, channels: []string{"telegram"}}
	polls := relay.sent()
	if len(polls) != 2 || !polls[1].equal(want) {
		t.Fatalf("relay polls = %+v; want the later poll to carry %+v", polls, want)
	}
}

// A poll that sent a policy the daemon no longer wants (a newer one was asked while it was in
// flight) may reach the relay last and put the old policy back: the daemon sends the wanted one
// again, so the relay ends on the policy wanted now (Greptile review of PR #323, 2026-09-29).
func TestAStalePolicyPollIsFollowedByTheWantedOne(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=warning\nTELEGRAM_ENABLED=true\n")
	newer := notifyPolicy{notifyOn: config.NotifyOnFailure, channels: []string{"telegram"}}
	relay.onPoll = func(n int) {
		if n == 1 { // the operator's change lands while the first poll is on the wire
			d.mu.Lock()
			d.notifyWant = &newer
			d.mu.Unlock()
		}
	}

	_ = d.buildReporter(context.Background())

	polls := relay.sent()
	if len(polls) != 2 || !polls[0].equal(startupPolicy) || !polls[1].equal(newer) {
		t.Fatalf("relay polls = %+v; want the stale warning, then failure sent again", polls)
	}
	if got := d.appliedNotifyPolicy(); got == nil || !got.equal(newer) {
		t.Fatalf("applied policy = %+v; want %+v", got, newer)
	}
}

// Two changes while polls are in flight: every resend's answer is recorded, the last one included,
// so the daemon ends on the relay's confirmation of the policy wanted now (Greptile review of PR
// #323, 16:42).
func TestTheLastResendAnswerIsRecorded(t *testing.T) {
	relay := &policyRelay{applied: true}
	d := policyDaemon(t, relay, "NOTIFY_ON=warning\nTELEGRAM_ENABLED=true\n")
	second := notifyPolicy{notifyOn: config.NotifyOnFailure, channels: []string{"telegram"}}
	third := notifyPolicy{notifyOn: config.NotifyOnAlways, channels: []string{"telegram"}}
	relay.onPoll = func(n int) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch n {
		case 1:
			d.notifyWant = &second
		case 2:
			d.notifyWant = &third
		}
	}

	_ = d.buildReporter(context.Background())

	polls := relay.sent()
	if len(polls) != 3 || !polls[2].equal(third) {
		t.Fatalf("relay polls = %+v; want warning, failure, always", polls)
	}
	if got := d.appliedNotifyPolicy(); got == nil || !got.equal(third) {
		t.Fatalf("applied policy = %+v; want the last resend's confirmation %+v", got, third)
	}
}
