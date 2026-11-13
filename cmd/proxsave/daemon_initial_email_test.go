package main

import (
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
	"github.com/tis24dev/proxsave/internal/identity"
)

const testInitialEmail = "operator@example.org"

// emailRelay is a fake config endpoint that records the initial-email header of every poll and
// answers it with ack (nil: no answer about it, as an older relay).
type emailRelay struct {
	mu      sync.Mutex
	headers []string
	queries []string
	ack     map[string]string
	applied bool          // the notify policy ack
	release chan struct{} // when set, every answer waits until it is closed
}

func (e *emailRelay) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.mu.Lock()
	e.headers = append(e.headers, r.Header.Get("X-Proxsave-Account-Initial-Email"))
	e.queries = append(e.queries, r.URL.RawQuery)
	ack, applied, release := e.ack, e.applied, e.release
	e.mu.Unlock()
	if release != nil {
		<-release
	}
	body := map[string]any{
		"mode":            "centralized",
		"alive_ping_url":  "https://hc.invalid/ping/alive",
		"backup_ping_url": "https://hc.invalid/ping/backup",
		"notify_policy": map[string]any{
			"contract_version": 1, "requested": q.Get("notify_on"), "applied": applied, "channels": []string{"telegram"},
		},
	}
	if ack != nil && r.Header.Get("X-Proxsave-Account-Initial-Email") != "" {
		body["account_initial_email"] = ack
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (e *emailRelay) sent() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.headers...)
}

func (e *emailRelay) answer(ack map[string]string) {
	e.mu.Lock()
	e.ack = ack
	e.mu.Unlock()
}

// emailDaemon is a centralized daemon whose backup.env holds body and whose start config held
// HEALTHCHECK_ACCOUNT_INITIAL_EMAIL=start.
func emailDaemon(t *testing.T, relay *emailRelay, start, body string) *daemon {
	t.Helper()
	for _, key := range []string{"NOTIFY_ON", "TELEGRAM_ENABLED", "TELEGRAM_ENABLE", "EMAIL_ENABLED", "EMAIL_ENABLE",
		"GOTIFY_ENABLED", "GOTIFY_ENABLE", "WEBHOOK_ENABLED", "WEBHOOK_ENABLE", "HEALTHCHECK_ACCOUNT_INITIAL_EMAIL"} {
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
			ConfigPath:                     path,
			BaseDir:                        base,
			HealthcheckEnabled:             true,
			HealthcheckMode:                config.HealthcheckModeCentralized,
			HealthcheckAccountInitialEmail: start,
			ServerID:                       "123456789012",
			ServerAPIHost:                  server.URL,
			NotifyOn:                       config.NotifyOnWarning,
			TelegramEnabled:                true,
		},
		now: time.Now,
	}
}

const emailEnv = "HEALTHCHECK_ENABLED=true\nHEALTHCHECK_MODE=centralized\nNOTIFY_ON=warning\nTELEGRAM_ENABLED=true\n"

// infoLines are the log lines above DEBUG, as the operator reads them: without the timestamp and
// the level column ("INFO     ", "WARNING  "), indentation kept.
func infoLines(buf string) []string {
	var out []string
	for line := range strings.SplitSeq(buf, "\n") {
		i := strings.Index(line, "] ")
		if i < 0 || len(line) < i+2+9 {
			continue
		}
		level, msg := strings.TrimSpace(line[i+2:i+2+9]), line[i+2+9:]
		if level == "INFO" || level == "WARNING" {
			out = append(out, msg)
		}
	}
	return out
}

func startPoll(d *daemon) configPoll {
	p := d.pollCentralized(context.Background(), "", "")
	d.logInitialEmailOutcome(p)
	return p
}

func TestNoInitialEmailSendsNoHeader(t *testing.T) {
	relay := &emailRelay{ack: map[string]string{"state": "pending"}}
	d := emailDaemon(t, relay, "", emailEnv)
	buf := captureDaemonDebug(t)
	startPoll(d)
	if got := relay.sent(); len(got) != 1 || got[0] != "" {
		t.Fatalf("headers = %q, want one poll without it", got)
	}
	if strings.Contains(buf.String(), "portal email") {
		t.Fatalf("a block without the variable:\n%s", buf)
	}
}

func TestPendingAnswerKeepsSendingAndSaysWhy(t *testing.T) {
	relay := &emailRelay{ack: map[string]string{"state": "pending"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv)
	buf := captureDaemonDebug(t)
	startPoll(d)
	startPoll(d)
	if got := relay.sent(); len(got) != 2 || got[0] != testInitialEmail || got[1] != testInitialEmail {
		t.Fatalf("headers = %q", got)
	}
	want := []string{"Applying monitoring portal email...", "  Waiting for the confirmation link sent by mail",
		"⚠ Monitoring portal email: not applied"}
	if got := infoLines(buf.String()); strings.Join(got, "|") != strings.Join(append(want, want...), "|") {
		t.Fatalf("lines:\n%s", strings.Join(got, "\n"))
	}
	for _, q := range relay.queries {
		if strings.Contains(q, "example.org") {
			t.Fatalf("the address is in the query: %s", q)
		}
	}
	if strings.Contains(buf.String(), testInitialEmail) {
		t.Fatalf("the address reached the log:\n%s", buf)
	}
}

func TestLimitAnswerSaysTheLimit(t *testing.T) {
	relay := &emailRelay{ack: map[string]string{"state": "pending", "reason": "limit"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv)
	buf := captureDaemonDebug(t)
	startPoll(d)
	want := "Applying monitoring portal email...|  Daily mail limit reached, retried at the next run|⚠ Monitoring portal email: not applied"
	if got := strings.Join(infoLines(buf.String()), "|"); got != want {
		t.Fatalf("lines: %s", got)
	}
}

func TestAppliedAnswerIsSaidOnceAndTheAddressIsNotSentAgain(t *testing.T) {
	relay := &emailRelay{ack: map[string]string{"state": "applied"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv)
	buf := captureDaemonDebug(t)
	startPoll(d)
	startPoll(d)
	if got := relay.sent(); len(got) != 2 || got[0] != testInitialEmail || got[1] != "" {
		t.Fatalf("headers = %q, want the address once", got)
	}
	want := "Applying monitoring portal email...|✓ Monitoring portal email: applied"
	if got := strings.Join(infoLines(buf.String()), "|"); got != want {
		t.Fatalf("lines: %s", got)
	}
}

func TestRejectedAnswersSayWhyAtWarning(t *testing.T) {
	for reason, why := range map[string]string{
		"invalid":     "  Not a usable email address",
		"reserved":    "  An address of the ProxSave HC Server",
		"unconfirmed": "  3 confirmation mails expired; set another address",
	} {
		relay := &emailRelay{ack: map[string]string{"state": "rejected", "reason": reason}}
		d := emailDaemon(t, relay, testInitialEmail, emailEnv)
		buf := captureDaemonDebug(t)
		startPoll(d)
		startPoll(d)
		want := "Applying monitoring portal email...|" + why + "|⚠ Monitoring portal email: not applied"
		if got := strings.Join(infoLines(buf.String()), "|"); got != want {
			t.Errorf("%s: lines %s", reason, got)
		}
		if !strings.Contains(buf.String(), "WARNING") {
			t.Errorf("%s: the outcome is not a WARNING:\n%s", reason, buf)
		}
		if got := relay.sent(); got[1] != "" {
			t.Errorf("%s: sent again after a final answer: %q", reason, got)
		}
	}
}

func TestAnUnknownReasonHasNoWhyLine(t *testing.T) {
	relay := &emailRelay{ack: map[string]string{"state": "rejected", "reason": "something_new"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv)
	buf := captureDaemonDebug(t)
	startPoll(d)
	want := "Applying monitoring portal email...|⚠ Monitoring portal email: not applied"
	if got := strings.Join(infoLines(buf.String()), "|"); got != want {
		t.Fatalf("lines: %s", got)
	}
	if !strings.Contains(buf.String(), "reason=something_new") {
		t.Fatalf("the DEBUG evidence lacks the reason:\n%s", buf)
	}
}

func TestAnOlderRelayIsSilentAndAskedAgain(t *testing.T) {
	relay := &emailRelay{}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv)
	buf := captureDaemonDebug(t)
	startPoll(d)
	startPoll(d)
	if got := infoLines(buf.String()); len(got) != 0 {
		t.Fatalf("visible lines without an answer: %q", got)
	}
	if got := relay.sent(); got[1] != testInitialEmail {
		t.Fatalf("not asked again: %q", got)
	}
}

func TestScheduleNegotiationPollsNeverCarryTheAddress(t *testing.T) {
	relay := &emailRelay{ack: map[string]string{"state": "pending"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv)
	d.pollCentralizedTo(context.Background(), nil, "schedule", "")
	if got := relay.sent(); len(got) != 1 || got[0] != "" {
		t.Fatalf("headers = %q", got)
	}
}

func TestSelfModeNeverSendsIt(t *testing.T) {
	d := &daemon{cfg: &config.Config{HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeSelf,
		HealthcheckAccountInitialEmail: testInitialEmail}}
	if got := d.wantedInitialEmail(); got != "" {
		t.Fatalf("self mode wants %q", got)
	}
	d.cfg.HealthcheckMode, d.cfg.HealthcheckEnabled = config.HealthcheckModeCentralized, false
	if got := d.wantedInitialEmail(); got != "" {
		t.Fatalf("disabled healthchecks want %q", got)
	}
}

// Before a run, with the notify policy already confirmed: the address still goes out, on a poll
// without the notify level block, and its block follows.
func TestBeforeARunTheAddressGoesOutEvenWithAConfirmedPolicy(t *testing.T) {
	relay := &emailRelay{applied: true, ack: map[string]string{"state": "pending"}}
	d := emailDaemon(t, relay, "", emailEnv+"HEALTHCHECK_ACCOUNT_INITIAL_EMAIL="+testInitialEmail+"\n")
	d.notifyApplied = &notifyPolicy{notifyOn: config.NotifyOnWarning, channels: []string{"telegram"}}
	buf := captureDaemonDebug(t)
	d.refreshNotifyPolicy(context.Background())
	if got := relay.sent(); len(got) != 1 || got[0] != testInitialEmail {
		t.Fatalf("headers = %q", got)
	}
	want := "Applying monitoring portal email...|  Waiting for the confirmation link sent by mail|⚠ Monitoring portal email: not applied"
	if got := strings.Join(infoLines(buf.String()), "|"); got != want {
		t.Fatalf("lines: %s", got)
	}
}

// A before-run answer that comes back after the bounded wait is not logged then: the address stays
// due, goes out again before the next run, and that answer is logged once.
func TestALateBeforeRunAnswerIsSentAgainAndLoggedOnce(t *testing.T) {
	relay := &emailRelay{applied: true, ack: map[string]string{"state": "rejected", "reason": "invalid"},
		release: make(chan struct{})}
	d := emailDaemon(t, relay, "", emailEnv+"HEALTHCHECK_ACCOUNT_INITIAL_EMAIL="+testInitialEmail+"\n")
	d.notifyApplied = &notifyPolicy{notifyOn: config.NotifyOnWarning, channels: []string{"telegram"}}
	d.notifyRefreshWaitOverride = 50 * time.Millisecond
	logs := captureDaemonLog(t) // a locked sink: the late poll writes to it after the wait
	visible := func() []string {
		var out []string
		for _, line := range logs() {
			if strings.HasPrefix(line, "INFO ") || strings.HasPrefix(line, "WARNING ") {
				out = append(out, line)
			}
		}
		return out
	}

	d.refreshNotifyPolicy(context.Background())
	if got := visible(); len(got) != 0 {
		t.Fatalf("visible lines before the answer: %q", got)
	}
	close(relay.release) // the late answer arrives, after the wait
	d.notifyRefreshWaitOverride = 5 * time.Second
	d.refreshNotifyPolicy(context.Background()) // its poll waits for the late one, then sends again
	d.refreshNotifyPolicy(context.Background()) // the final answer was logged: nothing due

	if got := relay.sent(); len(got) != 2 || got[0] != testInitialEmail || got[1] != testInitialEmail {
		t.Fatalf("headers = %q, want the address twice", got)
	}
	want := "INFO Applying monitoring portal email...|INFO   Not a usable email address|" +
		"WARNING ⚠ Monitoring portal email: not applied"
	if got := strings.Join(visible(), "|"); got != want {
		t.Fatalf("lines: %s", got)
	}
}

// A confirmed policy and no address due: no request, as before.
func TestBeforeARunNothingIsSentWithoutAnAddressDue(t *testing.T) {
	relay := &emailRelay{applied: true, ack: map[string]string{"state": "applied"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv+"HEALTHCHECK_ACCOUNT_INITIAL_EMAIL="+testInitialEmail+"\n")
	d.notifyApplied = &notifyPolicy{notifyOn: config.NotifyOnWarning, channels: []string{"telegram"}}
	captureDaemonDebug(t)
	startPoll(d)                                // applied: final for this address
	d.refreshNotifyPolicy(context.Background()) // still the same address: nothing due
	if got := relay.sent(); len(got) != 1 {
		t.Fatalf("polls = %q, want only the first", got)
	}
}

// A hand edit to another address reaches the relay before the next run, the policy block first.
func TestBeforeARunAChangedAddressIsSentAfterTheNotifyBlock(t *testing.T) {
	relay := &emailRelay{applied: false, ack: map[string]string{"state": "rejected", "reason": "unconfirmed"}}
	d := emailDaemon(t, relay, testInitialEmail, emailEnv+"HEALTHCHECK_ACCOUNT_INITIAL_EMAIL=other@example.org\n")
	captureDaemonDebug(t)
	startPoll(d)
	relay.answer(map[string]string{"state": "pending"})
	buf := captureDaemonDebug(t)
	d.refreshNotifyPolicy(context.Background())
	if got := relay.sent(); got[len(got)-1] != "other@example.org" {
		t.Fatalf("headers = %q", got)
	}
	lines := infoLines(buf.String())
	if len(lines) < 4 || lines[0] != "Applying notify level..." ||
		lines[len(lines)-3] != "Applying monitoring portal email..." {
		t.Fatalf("order:\n%s", strings.Join(lines, "\n"))
	}
}

// At daemon start the schedule negotiation does the polling; the address rides its own poll after
// the start blocks (found live on pve1: the start path never used the poll that carried it).
func TestAtStartTheAddressGoesOutAfterTheStartBlocks(t *testing.T) {
	var mu sync.Mutex
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("X-Proxsave-Account-Initial-Email")
		mu.Lock()
		headers = append(headers, h)
		mu.Unlock()
		body := relayBody(r.URL.Query(), true, true)
		if h != "" {
			body["account_initial_email"] = map[string]string{"state": "pending"}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	d := negotiationDaemon(t, server.URL, "weekly", "mon", "1", "02:00")
	d.cfg.HealthcheckAccountInitialEmail = testInitialEmail
	got := strings.Join(startLines(t, d), "\n")
	block := "INFO Applying monitoring portal email...\nINFO   Waiting for the confirmation link sent by mail\n" +
		"INFO ⚠ Monitoring portal email: not applied"
	i, j := strings.Index(got, "INFO ✓ Notify level: applied"), strings.Index(got, block)
	if i < 0 || j < i {
		t.Fatalf("block missing or before the notify level block:\n%s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 || headers[0] != "" || headers[1] != testInitialEmail {
		t.Fatalf("headers = %q: want the schedule attempt without it, then its own poll", headers)
	}
}

// Without an address due the start sends nothing more than before.
func TestAtStartNoExtraPollWithoutAnAddress(t *testing.T) {
	relay := newScheduleRelay(t, nil)
	d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
	startLines(t, d)
	if polls := relay.sent(); len(polls) != 1 {
		t.Fatalf("polls = %d, want 1", len(polls))
	}
}
