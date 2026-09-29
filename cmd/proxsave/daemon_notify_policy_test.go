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
	applied := p.applied
	p.mu.Unlock()
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
	if line := "daemon: notification policy sent notify_on=failure channels=telegram applied=true"; !strings.Contains(logged.String(), line) {
		t.Fatalf("missing DEBUG line %q in:\n%s", line, logged.String())
	}
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
	if strings.Contains(logged.String(), "notification policy sent") {
		t.Fatalf("nothing was sent, so nothing may be logged as sent:\n%s", logged.String())
	}
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

	if line := "daemon: notification policy sent notify_on=warning channels=none applied=false"; !strings.Contains(logged.String(), line) {
		t.Fatalf("missing DEBUG line %q in:\n%s", line, logged.String())
	}
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
