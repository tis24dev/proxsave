package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notifyfilter"
	"github.com/tis24dev/proxsave/internal/types"
)

// stubDeliveryStatus replaces the relay read for one test and counts the reads.
func stubDeliveryStatus(t *testing.T, st health.DeliveryStatus, err error) *int {
	t.Helper()
	calls := 0
	orig := notifyfilter.FetchDeliveryStatus
	t.Cleanup(func() { notifyfilter.FetchDeliveryStatus = orig })
	notifyfilter.FetchDeliveryStatus = func(context.Context, *http.Client, string, string, string, *logging.Logger, string) (health.DeliveryStatus, error) {
		calls++
		return st, err
	}
	return &calls
}

// relayAnswer is a schema_version 1 answer in state, with an ack for requested on channels.
func relayAnswer(state, requested string, applied bool, channels ...string) health.DeliveryStatus {
	st := health.DeliveryStatus{SchemaVersion: 1, State: state, ValidForSeconds: 120}
	st.NotifyPolicy = &health.NotifyPolicyAck{
		ContractVersion: 1, Requested: requested, Applied: applied, Mode: "event_driven", Channels: channels,
	}
	return st
}

// debugLogger writes DEBUG and above into the returned buffer, and is the default logger for
// the test so the package-level logging.Info lines land in the same buffer.
func debugLogger(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	orig := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(orig) })
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	logging.SetDefaultLogger(logger)
	return logger, &buf
}

// centralizedNotifyConfig is a centralized host with Telegram on and NOTIFY_ON=warning.
func centralizedNotifyConfig(t *testing.T) *config.Config {
	return &config.Config{
		BaseDir:            t.TempDir(),
		HealthcheckEnabled: true,
		HealthcheckMode:    config.HealthcheckModeCentralized,
		ServerID:           "123456789012",
		ServerAPIHost:      "https://relay.invalid",
		NotifyOn:           config.NotifyOnWarning,
		TelegramEnabled:    true,
	}
}

// Every way the run decides its filter: warning or failure applies only with a confirmed
// Healthchecks monitor; every other answer notifies every outcome, and says why in DEBUG.
func TestDecideNotifyFilter(t *testing.T) {
	ready := relayAnswer("ready", config.NotifyOnWarning, true, "telegram")
	cases := []struct {
		name          string
		mutate        func(cfg *config.Config)
		section       string
		relay         health.DeliveryStatus
		relayErr      error
		wantStatus    string
		wantEffective string
		wantReason    string
		wantReads     int
	}{
		{name: "healthchecks off or unusable", section: notifyfilter.SectionDisabled,
			wantStatus: notifyfilter.StatusDisabled, wantEffective: config.NotifyOnAlways, wantReason: "healthchecks_disabled"},
		{name: "daemon not transmitting", section: notifyfilter.SectionNotTransmitting,
			wantStatus: notifyfilter.StatusNotTransmitting, wantEffective: config.NotifyOnAlways, wantReason: "not_transmitting"},

		{name: "self without notify checks", section: notifyfilter.SectionInitialized,
			mutate:     func(c *config.Config) { c.HealthcheckMode = config.HealthcheckModeSelf },
			wantStatus: notifyfilter.StatusSelf, wantEffective: config.NotifyOnWarning},
		{name: "self with a notify URL", section: notifyfilter.SectionInitialized,
			mutate: func(c *config.Config) {
				c.HealthcheckMode = config.HealthcheckModeSelf
				c.HealthcheckNotifyTelegramURL = "https://hc.invalid/ping/notify-telegram"
			},
			wantStatus: notifyfilter.StatusSelf, wantEffective: config.NotifyOnAlways, wantReason: "self_notify_urls_configured"},
		{name: "self with a notify check id", section: notifyfilter.SectionInitialized,
			mutate: func(c *config.Config) {
				c.HealthcheckMode = config.HealthcheckModeSelf
				c.HealthcheckNotifyEmailID = "notify-email"
			},
			wantStatus: notifyfilter.StatusSelf, wantEffective: config.NotifyOnAlways, wantReason: "self_notify_urls_configured"},

		{name: "centralized ready with the policy applied", section: notifyfilter.SectionInitialized, relay: ready,
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnWarning, wantReads: 1},
		{name: "centralized ready with another threshold applied", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("ready", config.NotifyOnFailure, true, "telegram"),
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized ready with another channel set applied", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("ready", config.NotifyOnWarning, true, "email", "telegram"),
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized ready with the policy not applied", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("ready", config.NotifyOnWarning, false, "telegram"),
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized ready from a relay without the ack", section: notifyfilter.SectionInitialized,
			relay:      health.DeliveryStatus{SchemaVersion: 1, State: "ready", ValidForSeconds: 120},
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized not configured", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("not_configured", config.NotifyOnWarning, true, "telegram"),
			wantStatus: notifyfilter.StatusNotConfigured, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "centralized unverified", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("unverified", config.NotifyOnWarning, true, "telegram"),
			wantStatus: notifyfilter.StatusNotVerified, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "centralized degraded", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("degraded", config.NotifyOnWarning, true, "telegram"),
			wantStatus: notifyfilter.StatusDegraded, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "centralized unknown", section: notifyfilter.SectionInitialized,
			relay:      relayAnswer("unknown", config.NotifyOnWarning, true, "telegram"),
			wantStatus: notifyfilter.StatusUnknown, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "relay unreachable", section: notifyfilter.SectionInitialized,
			relayErr:   health.ErrDeliveryUnavailable,
			wantStatus: notifyfilter.StatusUnknown, wantEffective: config.NotifyOnAlways, wantReason: "delivery_status_unavailable", wantReads: 1},

		{name: "NOTIFY_ON=always needs no confirmation", section: notifyfilter.SectionInitialized,
			mutate:     func(c *config.Config) { c.NotifyOn = config.NotifyOnAlways },
			relay:      relayAnswer("not_configured", config.NotifyOnAlways, true, "telegram"),
			wantStatus: notifyfilter.StatusNotConfigured, wantEffective: config.NotifyOnAlways, wantReads: 1},
		{name: "NOTIFY_ON=failure confirmed", section: notifyfilter.SectionInitialized,
			mutate:     func(c *config.Config) { c.NotifyOn = config.NotifyOnFailure },
			relay:      relayAnswer("ready", config.NotifyOnFailure, true, "telegram"),
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnFailure, wantReads: 1},
		{name: "unrecognised NOTIFY_ON is requested as always", section: notifyfilter.SectionInitialized,
			mutate:     func(c *config.Config) { c.NotifyOn = "only-when-broken" },
			relay:      relayAnswer("ready", config.NotifyOnAlways, true, "telegram"),
			wantStatus: notifyfilter.StatusReady, wantEffective: config.NotifyOnAlways, wantReads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := centralizedNotifyConfig(t)
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			reads := stubDeliveryStatus(t, tc.relay, tc.relayErr)
			logger, buf := debugLogger(t)

			d := notifyfilter.Decide(context.Background(), cfg, logger, tc.section, "notifications init")

			if d.Status != tc.wantStatus || d.Effective != tc.wantEffective || d.Reason != tc.wantReason {
				t.Fatalf("decision = status %q effective %q reason %q; want %q %q %q",
					d.Status, d.Effective, d.Reason, tc.wantStatus, tc.wantEffective, tc.wantReason)
			}
			if *reads != tc.wantReads {
				t.Fatalf("relay read %d times; want %d", *reads, tc.wantReads)
			}
			fallback := "notifications init: filter fallback=always reason=" + tc.wantReason
			if got := strings.Contains(buf.String(), fallback); got != (tc.wantReason != "") {
				t.Fatalf("fallback DEBUG line present=%v, want %v (%q) in:\n%s", got, tc.wantReason != "", fallback, buf.String())
			}
		})
	}
}

// assertBlock checks that every want is in out, in order, that the block's last line is the
// outcome, and that no line of it is above INFO: a WARNING would raise the run's exit code.
func assertBlock(t *testing.T, out, outcome string, wants ...string) {
	t.Helper()
	last := -1
	for _, want := range wants {
		i := strings.Index(out, want)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
		if i < last {
			t.Fatalf("%q is out of order in:\n%s", want, out)
		}
		last = i
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	final := lines[len(lines)-1]
	if !strings.HasSuffix(final, outcome) || !strings.Contains(final, "INFO") {
		t.Fatalf("the block must end with the INFO outcome %q, ends with %q in:\n%s", outcome, final, out)
	}
	for _, line := range lines {
		if strings.Contains(line, "WARNING") || strings.Contains(line, "ERROR") {
			t.Fatalf("the notification filter block logged above INFO: %q", line)
		}
		for _, old := range []string{"Notification setting:", "Notification filter: always", "Notification filter: warning"} {
			if strings.Contains(line, old) {
				t.Fatalf("a replaced line is still written: %q", line)
			}
		}
	}
}

// The initialization block: INFO "Applying...", the DEBUG evidence, the INFO details indented
// two spaces, the outcome last.
func TestLogNotifyFilterInitWritesTheApprovedLines(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	assertBlock(t, buf.String(), "✓ Notification filter: applied",
		"INFO     Applying notification filter...",
		"notifications init: read notify_on=warning source=default",
		"notifications init: healthchecks mode=centralized section=initialized",
		"notifications init: delivery state=ready alive_routes=0/0 backup_routes=0/0 policy_confirmed=true",
		"  Setting: warning",
		"  Healthchecks status: ready",
		"  Filter in effect: warning",
		"✓ Notification filter: applied",
	)
	if strings.Contains(buf.String(), "filter fallback=") {
		t.Fatalf("an applied filter logged a fallback:\n%s", buf.String())
	}
}

// NOTIFY_ON read from backup.env names the file it was read from.
func TestLogNotifyFilterInitNamesTheConfigFile(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	cfg.NotifyOnSource, cfg.ConfigPath = "backup.env", "/opt/proxsave/env/backup.env"
	stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	if !strings.Contains(buf.String(), "notifications init: read notify_on=warning source=/opt/proxsave/env/backup.env") {
		t.Fatalf("missing the config path in:\n%s", buf.String())
	}
}

// A relay that does not confirm: the fallback is in DEBUG, the details say what is in effect,
// and the outcome is an INFO "not applied", never a WARNING.
func TestLogNotifyFilterInitNotApplied(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	stubDeliveryStatus(t, relayAnswer("degraded", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	assertBlock(t, buf.String(), "⚠ Notification filter: not applied",
		"INFO     Applying notification filter...",
		"notifications init: delivery state=degraded",
		"notifications init: filter fallback=always reason=alerts_not_verified",
		"  Setting: warning",
		"  Healthchecks status: degraded",
		"  Filter in effect: always",
		"⚠ Notification filter: not applied",
	)
}

// The real relay read: its URL and transport stages sit between the mode and the delivery state.
func TestLogNotifyFilterInitLogsTheRelayStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema_version":1,"state":"ready","valid_for_seconds":120,
			"notify_policy":{"contract_version":1,"requested":"warning","applied":true,"channels":["telegram"]}}`)
	}))
	defer srv.Close()
	cfg := centralizedNotifyConfig(t)
	cfg.ServerAPIHost = srv.URL
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	assertBlock(t, buf.String(), "✓ Notification filter: applied",
		"Applying notification filter...",
		"notifications init: healthchecks mode=centralized",
		"notifications init: url="+srv.URL+"/api/healthcheck/delivery-status",
		"notifications init: connected",
		"notifications init: request written",
		"notifications init: response http=200 elapsed=",
		"notifications init: delivery state=ready",
		"  Setting: warning",
		"✓ Notification filter: applied",
	)
}

// Self mode: same block, no relay, the DEBUG lines that apply.
func TestLogNotifyFilterInitSelfMode(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	cfg.HealthcheckMode = config.HealthcheckModeSelf
	reads := stubDeliveryStatus(t, health.DeliveryStatus{}, errors.New("must not be read"))
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	assertBlock(t, buf.String(), "✓ Notification filter: applied",
		"Applying notification filter...",
		"notifications init: healthchecks mode=self section=initialized",
		"notifications init: self notify_urls=false",
		"  Setting: warning",
		"  Healthchecks status: self",
		"  Filter in effect: warning",
	)
	if strings.Contains(buf.String(), "url=") || *reads != 0 {
		t.Fatalf("self mode read the relay (%d reads):\n%s", *reads, buf.String())
	}
}

// With Healthchecks off the block still states the setting, and the filter the run applies.
func TestLogNotifyFilterInitWithHealthchecksOff(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	cfg.HealthcheckEnabled = false
	reads := stubDeliveryStatus(t, health.DeliveryStatus{}, errors.New("must not be read"))
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionDisabled)

	out := buf.String()
	assertBlock(t, out, "⚠ Notification filter: not applied",
		"Applying notification filter...",
		"notifications init: read notify_on=warning source=default",
		"notifications init: filter fallback=always reason=healthchecks_disabled",
		"  Setting: warning",
		"  Healthchecks status: disabled",
		"  Filter in effect: always",
	)
	if strings.Contains(out, "healthchecks mode=") || strings.Contains(out, "url=") {
		t.Fatalf("no mode or relay line when Healthchecks is off:\n%s", out)
	}
	if *reads != 0 {
		t.Fatalf("relay read %d times with Healthchecks off; want 0", *reads)
	}
}

// fakeNotifyClock replaces the decision's clock for one test and returns a pointer to its time.
func fakeNotifyClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	orig := notifyfilter.Now
	t.Cleanup(func() { notifyfilter.Now = orig })
	notifyfilter.Now = func() time.Time { return now }
	return &now
}

// Before dispatch the run reuses its initialization decision while the relay's answer is younger
// than 120 s: 86% of the runs started at 00:00 UTC end within that, and read the relay once.
func TestNotifyFilterRefreshReusesAFreshAnswer(t *testing.T) {
	now := fakeNotifyClock(t)
	cfg := centralizedNotifyConfig(t)
	reads := stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)
	*now = now.Add(notifyfilter.Validity - time.Second)

	if got := refresh(context.Background()).Effective; got != config.NotifyOnWarning {
		t.Fatalf("refresh = %q; want the initialization decision %q", got, config.NotifyOnWarning)
	}
	if *reads != 1 {
		t.Fatalf("relay read %d times; want 1, the answer is still valid", *reads)
	}
	if want := "notifications dispatch: reused init decision age=1m59s valid_for=2m0s"; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
}

// An answer older than 120 s is read again before dispatch, and the new decision is the one
// applied: a relay that stopped confirming in the meantime brings the run back to always.
func TestNotifyFilterRefreshReadsAnExpiredAnswerAgain(t *testing.T) {
	now := fakeNotifyClock(t)
	cfg := centralizedNotifyConfig(t)
	reads := stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)
	stubDeliveryStatus(t, relayAnswer("degraded", config.NotifyOnWarning, true, "telegram"), nil)
	*now = now.Add(notifyfilter.Validity)

	if got := refresh(context.Background()).Effective; got != config.NotifyOnAlways {
		t.Fatalf("refresh = %q; want %q from the new, degraded answer", got, config.NotifyOnAlways)
	}
	if *reads != 1 {
		t.Fatalf("first stub read %d times; want 1", *reads)
	}
	for _, want := range []string{
		"notifications dispatch: delivery state=degraded",
		"notifications dispatch: filter fallback=always reason=alerts_not_verified",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "reused") {
		t.Fatalf("an expired answer was reported as reused:\n%s", buf.String())
	}

	// The new answer is fresh again: a second refresh reuses it, and says it is the dispatch one.
	again := stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	if got := refresh(context.Background()).Effective; got != config.NotifyOnAlways || *again != 0 {
		t.Fatalf("second refresh = %q after %d reads; want the reused %q and no read", got, *again, config.NotifyOnAlways)
	}
	if want := "notifications dispatch: reused dispatch decision age=0s"; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
}

// A decision that read no relay answer (self mode, Healthchecks off, daemon down) has nothing
// that can expire, so the refresh never reads the relay.
func TestNotifyFilterRefreshNeverReadsWithoutARelayAnswer(t *testing.T) {
	now := fakeNotifyClock(t)
	cfg := centralizedNotifyConfig(t)
	cfg.HealthcheckMode = config.HealthcheckModeSelf
	reads := stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)
	*now = now.Add(time.Hour)

	if got := refresh(context.Background()).Effective; got != config.NotifyOnWarning || *reads != 0 {
		t.Fatalf("refresh = %q after %d relay reads; want %q and none", got, *reads, config.NotifyOnWarning)
	}
	if want := "notifications dispatch: reused init decision relay_read=none status=self"; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
}

// The relay's own validity shortens the reuse: an answer already 100 s old when it arrived, of the 120 s the relay
// vouches for, is read again after 20 s, not after 120.
func TestNotifyFilterRefreshHonoursTheRelayValidity(t *testing.T) {
	now := fakeNotifyClock(t)
	cfg := centralizedNotifyConfig(t)
	aged := relayAnswer("ready", config.NotifyOnWarning, true, "telegram")
	aged.AgeSeconds = 100
	reads := stubDeliveryStatus(t, aged, nil)
	logger, _ := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)

	*now = now.Add(19 * time.Second)
	if refresh(context.Background()); *reads != 1 {
		t.Fatalf("relay read %d times 19 s in; want 1, the answer still has 1 s left", *reads)
	}
	*now = now.Add(time.Second)
	if refresh(context.Background()); *reads != 2 {
		t.Fatalf("relay read %d times 20 s in; want 2, the relay's validity is spent", *reads)
	}
}

// An answer that failed to arrive keeps the local 120 s: the dispatch does not retry a relay that
// just failed.
func TestNotifyFilterRefreshKeepsAnUnavailableAnswerForTheLocalWindow(t *testing.T) {
	now := fakeNotifyClock(t)
	cfg := centralizedNotifyConfig(t)
	reads := stubDeliveryStatus(t, health.DeliveryStatus{}, health.ErrDeliveryUnavailable)
	logger, _ := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)
	*now = now.Add(notifyfilter.Validity - time.Second)

	if got := refresh(context.Background()).Effective; got != config.NotifyOnAlways || *reads != 1 {
		t.Fatalf("refresh = %q after %d reads; want always and 1 read", got, *reads)
	}
}

// elapsedRe is the relay answer's timing, which changes from run to run.
var elapsedRe = regexp.MustCompile(`elapsed=\d+ms`)

// blockLines is the log as "LEVEL message" lines, without the timestamp and the level's padding,
// with the elapsed milliseconds of a relay answer written as "elapsed=N".
func blockLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i := strings.Index(line, "] "); strings.HasPrefix(line, "[") && i >= 0 {
			line = line[i+2:]
		}
		if len(line) > 9 {
			line = strings.TrimSpace(line[:8]) + " " + line[9:]
		}
		lines = append(lines, elapsedRe.ReplaceAllString(line, "elapsed=N"))
	}
	return lines
}

// assertLines checks the whole block, line by line.
func assertLines(t *testing.T, out string, want ...string) {
	t.Helper()
	if got := blockLines(out); !slices.Equal(got, want) {
		t.Fatalf("block:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// relayServer answers the delivery-status read with status and body.
func relayServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// closedRelay is the address of a relay that refuses the connection.
func closedRelay(t *testing.T) (url, addr string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url, addr = srv.URL, srv.Listener.Addr().String()
	srv.Close()
	return url, addr
}

// silentRelay is a relay that takes the request and hangs up without an answer.
func silentRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The why-line of a filter not applied: after the details, before the outcome, an INFO naming
// what could not vouch for the filter. The relay read is the real one, so the kind of failure is
// the one the transport and the answer produce.
func TestLogNotifyFilterInitSaysWhyTheRelayDidNotVouch(t *testing.T) {
	const op = "DEBUG notifications init: "
	head := []string{
		"INFO Applying notification filter...",
		op + "read notify_on=warning source=default",
		op + "healthchecks mode=centralized section=initialized",
	}
	tail := func(why string) []string {
		return []string{
			op + "filter fallback=always reason=delivery_status_unavailable",
			"INFO   Setting: warning",
			"INFO   Healthchecks status: unknown",
			"INFO   Filter in effect: always",
			"INFO " + why,
			"INFO ⚠ Notification filter: not applied",
		}
	}
	answered := func(url string, status int) []string {
		return []string{
			op + "url=" + url + "/api/healthcheck/delivery-status",
			op + "connected",
			op + "request written",
			op + "response http=" + strconv.Itoa(status) + " elapsed=N",
		}
	}
	cases := []struct {
		name  string
		relay func(t *testing.T) (url string, read []string)
		why   string
	}{
		{"connection refused", func(t *testing.T) (string, []string) {
			url, addr := closedRelay(t)
			refused := "Get: dial tcp " + addr + ": connect: connection refused"
			return url, []string{
				op + "url=" + url + "/api/healthcheck/delivery-status",
				op + "failed stage=connect error=" + refused,
				op + "delivery unavailable: healthcheck delivery status unavailable: request: " + refused,
			}
		}, "ProxSave HC Server not reachable"},
		{"no answer", func(t *testing.T) (string, []string) {
			url := silentRelay(t)
			return url, []string{
				op + "url=" + url + "/api/healthcheck/delivery-status",
				op + "connected",
				op + "request written",
				op + "failed stage=response error=Get: EOF",
				op + "delivery unavailable: healthcheck delivery status unavailable: request: Get: EOF",
			}
		}, "ProxSave HC Server not reachable"},
		{"http 503", func(t *testing.T) (string, []string) {
			url := relayServer(t, http.StatusServiceUnavailable, `{"error":"HC_DELIVERY_DISABLED"}`)
			return url, append(answered(url, 503),
				op+`response body="{\"error\":\"HC_DELIVERY_DISABLED\"}"`,
				op+"delivery unavailable: healthcheck delivery status unavailable: http 503")
		}, "ProxSave HC Server not ready"},
		{"http 404", func(t *testing.T) (string, []string) {
			url := relayServer(t, http.StatusNotFound, ``)
			return url, append(answered(url, 404),
				op+`response body=""`,
				op+"delivery unavailable: healthcheck delivery status unavailable: http 404")
		}, "ProxSave HC Server not ready"},
		{"200 with bad JSON", func(t *testing.T) (string, []string) {
			url := relayServer(t, http.StatusOK, `{"schema_version": 1, "state": `)
			return url, append(answered(url, 200),
				op+"delivery unavailable: healthcheck delivery status unavailable: bad JSON")
		}, "ProxSave HC Server did not confirm"},
		{"200 with an unknown schema", func(t *testing.T) (string, []string) {
			url := relayServer(t, http.StatusOK, `{"schema_version": 2, "state": "ready", "valid_for_seconds": 120}`)
			return url, append(answered(url, 200),
				op+`delivery unavailable: healthcheck delivery status unavailable: schema 2 state "ready"`)
		}, "ProxSave HC Server did not confirm"},
		{"200 with an unknown state", func(t *testing.T) (string, []string) {
			url := relayServer(t, http.StatusOK, `{"schema_version": 1, "state": "project_missing", "valid_for_seconds": 120}`)
			return url, append(answered(url, 200),
				op+`delivery unavailable: healthcheck delivery status unavailable: schema 1 state "project_missing"`)
		}, "ProxSave HC Server did not confirm"},
		{"200 with an expired evaluation", func(t *testing.T) (string, []string) {
			url := relayServer(t, http.StatusOK, `{"schema_version": 1, "state": "ready", "age_seconds": 120, "valid_for_seconds": 120}`)
			return url, append(answered(url, 200),
				op+"delivery unavailable: healthcheck delivery status unavailable: expired (age 120s, valid for 120s)")
		}, "ProxSave HC Server did not confirm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := centralizedNotifyConfig(t)
			url, read := tc.relay(t)
			cfg.ServerAPIHost = url
			logger, buf := debugLogger(t)

			logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

			want := append(append(append([]string{}, head...), read...), tail(tc.why)...)
			assertLines(t, buf.String(), want...)
		})
	}
}

// The why-lines that do not come from a failed relay read: a relay that answered ready without
// confirming the policy, and self mode with notify URLs of the operator's own server. Every other
// filter not applied has no why-line, its Healthchecks status says why; an applied filter never
// has one.
func TestLogNotifyFilterInitWhyLineOrNone(t *testing.T) {
	const op = "DEBUG notifications init: "
	read := op + "read notify_on=warning source=default"
	centralized := op + "healthchecks mode=centralized section=initialized"
	details := func(status, effective string) []string {
		return []string{"INFO   Setting: warning", "INFO   Healthchecks status: " + status, "INFO   Filter in effect: " + effective}
	}
	notApplied, applied := "INFO ⚠ Notification filter: not applied", "INFO ✓ Notification filter: applied"
	delivery := func(state string, confirmed bool) string {
		return op + "delivery state=" + state + " alive_routes=0/0 backup_routes=0/0 policy_confirmed=" + strconv.FormatBool(confirmed) + " reasons="
	}
	lines := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		section string
		relay   health.DeliveryStatus
		want    []string
	}{
		{"ready, policy not confirmed", nil, notifyfilter.SectionInitialized,
			relayAnswer("ready", config.NotifyOnFailure, true, "telegram"),
			lines([]string{"INFO Applying notification filter...", read, centralized, delivery("ready", false),
				op + "filter fallback=always reason=policy_unconfirmed"}, details("ready", "always"),
				[]string{"INFO ProxSave HC Server did not confirm", notApplied})},
		{"self with a notify URL", func(c *config.Config) {
			c.HealthcheckMode, c.HealthcheckNotifyTelegramURL = config.HealthcheckModeSelf, "https://hc.invalid/ping/notify-telegram"
		}, notifyfilter.SectionInitialized, health.DeliveryStatus{},
			lines([]string{"INFO Applying notification filter...", read, op + "healthchecks mode=self section=initialized",
				op + "self notify_urls=true", op + "filter fallback=always reason=self_notify_urls_configured"},
				details("self", "always"), []string{"INFO Alerts on your own server not verified", notApplied})},

		{"healthchecks disabled", func(c *config.Config) { c.HealthcheckEnabled = false }, notifyfilter.SectionDisabled,
			health.DeliveryStatus{},
			lines([]string{"INFO Applying notification filter...", read, op + "filter fallback=always reason=healthchecks_disabled"},
				details("disabled", "always"), []string{notApplied})},
		{"not transmitting", nil, notifyfilter.SectionNotTransmitting, health.DeliveryStatus{},
			lines([]string{"INFO Applying notification filter...", read, op + "healthchecks mode=centralized section=not_transmitting",
				op + "filter fallback=always reason=not_transmitting"}, details("not transmitting", "always"), []string{notApplied})},
		{"not configured", nil, notifyfilter.SectionInitialized, relayAnswer("not_configured", config.NotifyOnWarning, true, "telegram"),
			lines([]string{"INFO Applying notification filter...", read, centralized, delivery("not_configured", true),
				op + "filter fallback=always reason=alerts_not_verified"}, details("not configured", "always"), []string{notApplied})},
		{"not verified", nil, notifyfilter.SectionInitialized, relayAnswer("unverified", config.NotifyOnWarning, true, "telegram"),
			lines([]string{"INFO Applying notification filter...", read, centralized, delivery("unverified", true),
				op + "filter fallback=always reason=alerts_not_verified"}, details("not verified", "always"), []string{notApplied})},
		{"degraded", nil, notifyfilter.SectionInitialized, relayAnswer("degraded", config.NotifyOnWarning, true, "telegram"),
			lines([]string{"INFO Applying notification filter...", read, centralized, delivery("degraded", true),
				op + "filter fallback=always reason=alerts_not_verified"}, details("degraded", "always"), []string{notApplied})},
		{"relay state unknown", nil, notifyfilter.SectionInitialized, relayAnswer("unknown", config.NotifyOnWarning, true, "telegram"),
			lines([]string{"INFO Applying notification filter...", read, centralized, delivery("unknown", true),
				op + "filter fallback=always reason=alerts_not_verified"}, details("unknown", "always"), []string{notApplied})},

		{"applied", nil, notifyfilter.SectionInitialized, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"),
			lines([]string{"INFO Applying notification filter...", read, centralized, delivery("ready", true)},
				details("ready", "warning"), []string{applied})},
		{"applied in self mode", func(c *config.Config) { c.HealthcheckMode = config.HealthcheckModeSelf },
			notifyfilter.SectionInitialized, health.DeliveryStatus{},
			lines([]string{"INFO Applying notification filter...", read, op + "healthchecks mode=self section=initialized",
				op + "self notify_urls=false"}, details("self", "warning"), []string{applied})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := centralizedNotifyConfig(t)
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			stubDeliveryStatus(t, tc.relay, nil)
			logger, buf := debugLogger(t)

			logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, tc.section)

			assertLines(t, buf.String(), tc.want...)
		})
	}
}

// NOTIFY_ON=always is applied whatever the relay says, so an unreachable relay gets no why-line.
func TestLogNotifyFilterInitNoWhyLineWhenAlwaysIsRequested(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	cfg.NotifyOn = config.NotifyOnAlways
	url, addr := closedRelay(t)
	cfg.ServerAPIHost = url
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	const op = "DEBUG notifications init: "
	refused := "Get: dial tcp " + addr + ": connect: connection refused"
	assertLines(t, buf.String(),
		"INFO Applying notification filter...",
		op+"read notify_on=always source=default",
		op+"healthchecks mode=centralized section=initialized",
		op+"url="+url+"/api/healthcheck/delivery-status",
		op+"failed stage=connect error="+refused,
		op+"delivery unavailable: healthcheck delivery status unavailable: request: "+refused,
		"INFO   Setting: always",
		"INFO   Healthchecks status: unknown",
		"INFO   Filter in effect: always",
		"INFO ✓ Notification filter: applied",
	)
}

// Every decision and its why-line: none for an applied filter, whatever its reason fields say, and
// none for a relay failure no row names.
func TestNotifyFilterWhy(t *testing.T) {
	notApplied := func(reason, unavailable string) notifyfilter.Decision {
		return notifyfilter.Decision{Requested: config.NotifyOnWarning, Effective: config.NotifyOnAlways, Reason: reason, Unavailable: unavailable}
	}
	cases := []struct {
		name string
		d    notifyfilter.Decision
		want string
	}{
		{"unreachable", notApplied(notifyfilter.ReasonStatusUnavailable, notifyfilter.UnavailableUnreachable), "ProxSave HC Server not reachable"},
		{"http status", notApplied(notifyfilter.ReasonStatusUnavailable, notifyfilter.UnavailableHTTPStatus), "ProxSave HC Server not ready"},
		{"unusable answer", notApplied(notifyfilter.ReasonStatusUnavailable, notifyfilter.UnavailableUnusable), "ProxSave HC Server did not confirm"},
		{"policy unconfirmed", notApplied(notifyfilter.ReasonPolicyUnconfirmed, ""), "ProxSave HC Server did not confirm"},
		{"self notify URLs", notApplied(notifyfilter.ReasonSelfNotifyChecks, ""), "Alerts on your own server not verified"},
		{"unavailable, no kind", notApplied(notifyfilter.ReasonStatusUnavailable, ""), ""},
		{"alerts not verified", notApplied(notifyfilter.ReasonAlertsNotVerified, ""), ""},
		{"not transmitting", notApplied(notifyfilter.ReasonNotTransmitting, ""), ""},
		{"healthchecks disabled", notApplied(notifyfilter.ReasonHealthchecksDisabled, ""), ""},
		{"applied with a failed read", notifyfilter.Decision{Requested: config.NotifyOnAlways, Effective: config.NotifyOnAlways,
			Unavailable: notifyfilter.UnavailableUnreachable}, ""},
		{"applied, whatever its reason says", notifyfilter.Decision{Requested: config.NotifyOnWarning, Effective: config.NotifyOnWarning,
			Reason: notifyfilter.ReasonStatusUnavailable, Unavailable: notifyfilter.UnavailableUnreachable}, ""},
	}
	for _, tc := range cases {
		if got := notifyFilterWhy(tc.d); got != tc.want {
			t.Errorf("%s: notifyFilterWhy = %q; want %q", tc.name, got, tc.want)
		}
	}
}
