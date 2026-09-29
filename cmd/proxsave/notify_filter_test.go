package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
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
	notifyfilter.FetchDeliveryStatus = func(context.Context, *http.Client, string, string, string) (health.DeliveryStatus, error) {
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
			fallback := "notifications init: notification filter fallback=always reason=" + tc.wantReason
			if got := strings.Contains(buf.String(), fallback); got != (tc.wantReason != "") {
				t.Fatalf("fallback DEBUG line present=%v, want %v (%q) in:\n%s", got, tc.wantReason != "", fallback, buf.String())
			}
		})
	}
}

// The initialization block writes the approved lines in the approved order: the setting, its
// DEBUG details, the Healthchecks status, then the filter. INFO lines carry no parentheses.
func TestLogNotifyFilterInitWritesTheApprovedLines(t *testing.T) {
	cfg := centralizedNotifyConfig(t)
	stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, buf := debugLogger(t)

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, notifyfilter.SectionInitialized)

	out := buf.String()
	last := -1
	for _, want := range []string{
		"Notification setting: NOTIFY_ON=warning",
		"notifications init: notify_on=warning source=default",
		"notifications init: healthchecks mode=centralized",
		"notifications init: healthchecks delivery state=ready alive_routes=0/0 backup_routes=0/0 policy_confirmed=true",
		"Healthchecks status: ready",
		"Notification filter: warning",
	} {
		i := strings.Index(out, want)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
		if i < last {
			t.Fatalf("%q is out of order in:\n%s", want, out)
		}
		last = i
	}
	for _, line := range strings.Split(out, "\n") {
		for _, prefix := range []string{"Notification setting:", "Healthchecks status:", "Notification filter:"} {
			if i := strings.Index(line, prefix); i >= 0 && strings.ContainsAny(line[i:], "()") {
				t.Fatalf("INFO line carries an explanation in parentheses: %q", line)
			}
		}
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
	for _, want := range []string{
		"Notification setting: NOTIFY_ON=warning",
		"Healthchecks status: disabled",
		"Notification filter: always",
		"notifications init: notification filter fallback=always reason=healthchecks_disabled",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "healthchecks mode=") {
		t.Fatalf("no mode line when Healthchecks is off:\n%s", out)
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
	logger, _ := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)
	*now = now.Add(notifyfilter.Validity - time.Second)

	if got := refresh(context.Background()); got != config.NotifyOnWarning {
		t.Fatalf("refresh = %q; want the initialization decision %q", got, config.NotifyOnWarning)
	}
	if *reads != 1 {
		t.Fatalf("relay read %d times; want 1, the answer is still valid", *reads)
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

	if got := refresh(context.Background()); got != config.NotifyOnAlways {
		t.Fatalf("refresh = %q; want %q from the new, degraded answer", got, config.NotifyOnAlways)
	}
	if *reads != 1 {
		t.Fatalf("first stub read %d times; want 1", *reads)
	}
	for _, want := range []string{
		"notifications dispatch: healthchecks delivery state=degraded",
		"notifications dispatch: notification filter fallback=always reason=alerts_not_verified",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, buf.String())
		}
	}

	// The new answer is fresh again: a second refresh reuses it.
	again := stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	if got := refresh(context.Background()); got != config.NotifyOnAlways || *again != 0 {
		t.Fatalf("second refresh = %q after %d reads; want the reused %q and no read", got, *again, config.NotifyOnAlways)
	}
}

// A decision that read no relay answer (self mode, Healthchecks off, daemon down) has nothing
// that can expire, so the refresh never reads the relay.
func TestNotifyFilterRefreshNeverReadsWithoutARelayAnswer(t *testing.T) {
	now := fakeNotifyClock(t)
	cfg := centralizedNotifyConfig(t)
	cfg.HealthcheckMode = config.HealthcheckModeSelf
	reads := stubDeliveryStatus(t, relayAnswer("ready", config.NotifyOnWarning, true, "telegram"), nil)
	logger, _ := debugLogger(t)

	d := notifyfilter.Decide(context.Background(), cfg, logger, notifyfilter.SectionInitialized, "notifications init")
	refresh := notifyFilterRefresh(d, cfg, logger, notifyfilter.SectionInitialized)
	*now = now.Add(time.Hour)

	if got := refresh(context.Background()); got != config.NotifyOnWarning || *reads != 0 {
		t.Fatalf("refresh = %q after %d relay reads; want %q and none", got, *reads, config.NotifyOnWarning)
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

	if got := refresh(context.Background()); got != config.NotifyOnAlways || *reads != 1 {
		t.Fatalf("refresh = %q after %d reads; want always and 1 read", got, *reads)
	}
}
