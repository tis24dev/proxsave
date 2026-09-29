package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// stubDeliveryStatus replaces the relay read for one test and counts the reads.
func stubDeliveryStatus(t *testing.T, st health.DeliveryStatus, err error) *int {
	t.Helper()
	calls := 0
	orig := fetchDeliveryStatus
	t.Cleanup(func() { fetchDeliveryStatus = orig })
	fetchDeliveryStatus = func(context.Context, *http.Client, string, string, string) (health.DeliveryStatus, error) {
		calls++
		return st, err
	}
	return &calls
}

// relayAnswer is a schema_version 1 answer in state, with an ack for requested on channels.
func relayAnswer(state, requested string, applied bool, channels ...string) health.DeliveryStatus {
	st := health.DeliveryStatus{SchemaVersion: 1, State: state}
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
		{name: "healthchecks off or unusable", section: hcSectionDisabled,
			wantStatus: hcStatusDisabled, wantEffective: config.NotifyOnAlways, wantReason: "healthchecks_disabled"},
		{name: "daemon not transmitting", section: hcSectionNotTransmitting,
			wantStatus: hcStatusNotTransmitting, wantEffective: config.NotifyOnAlways, wantReason: "not_transmitting"},

		{name: "self without notify checks", section: hcSectionInitialized,
			mutate:     func(c *config.Config) { c.HealthcheckMode = config.HealthcheckModeSelf },
			wantStatus: hcStatusSelf, wantEffective: config.NotifyOnWarning},
		{name: "self with a notify URL", section: hcSectionInitialized,
			mutate: func(c *config.Config) {
				c.HealthcheckMode = config.HealthcheckModeSelf
				c.HealthcheckNotifyTelegramURL = "https://hc.invalid/ping/notify-telegram"
			},
			wantStatus: hcStatusSelf, wantEffective: config.NotifyOnAlways, wantReason: "self_notify_urls_configured"},
		{name: "self with a notify check id", section: hcSectionInitialized,
			mutate: func(c *config.Config) {
				c.HealthcheckMode = config.HealthcheckModeSelf
				c.HealthcheckNotifyEmailID = "notify-email"
			},
			wantStatus: hcStatusSelf, wantEffective: config.NotifyOnAlways, wantReason: "self_notify_urls_configured"},

		{name: "centralized ready with the policy applied", section: hcSectionInitialized, relay: ready,
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnWarning, wantReads: 1},
		{name: "centralized ready with another threshold applied", section: hcSectionInitialized,
			relay:      relayAnswer("ready", config.NotifyOnFailure, true, "telegram"),
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized ready with another channel set applied", section: hcSectionInitialized,
			relay:      relayAnswer("ready", config.NotifyOnWarning, true, "email", "telegram"),
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized ready with the policy not applied", section: hcSectionInitialized,
			relay:      relayAnswer("ready", config.NotifyOnWarning, false, "telegram"),
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized ready from a relay without the ack", section: hcSectionInitialized,
			relay:      health.DeliveryStatus{SchemaVersion: 1, State: "ready"},
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnAlways, wantReason: "policy_unconfirmed", wantReads: 1},
		{name: "centralized not configured", section: hcSectionInitialized,
			relay:      relayAnswer("not_configured", config.NotifyOnWarning, true, "telegram"),
			wantStatus: hcStatusNotConfigured, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "centralized unverified", section: hcSectionInitialized,
			relay:      relayAnswer("unverified", config.NotifyOnWarning, true, "telegram"),
			wantStatus: hcStatusNotVerified, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "centralized degraded", section: hcSectionInitialized,
			relay:      relayAnswer("degraded", config.NotifyOnWarning, true, "telegram"),
			wantStatus: hcStatusDegraded, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "centralized unknown", section: hcSectionInitialized,
			relay:      relayAnswer("unknown", config.NotifyOnWarning, true, "telegram"),
			wantStatus: hcStatusUnknown, wantEffective: config.NotifyOnAlways, wantReason: "alerts_not_verified", wantReads: 1},
		{name: "relay unreachable", section: hcSectionInitialized,
			relayErr:   health.ErrDeliveryUnavailable,
			wantStatus: hcStatusUnknown, wantEffective: config.NotifyOnAlways, wantReason: "delivery_status_unavailable", wantReads: 1},

		{name: "NOTIFY_ON=always needs no confirmation", section: hcSectionInitialized,
			mutate:     func(c *config.Config) { c.NotifyOn = config.NotifyOnAlways },
			relay:      relayAnswer("not_configured", config.NotifyOnAlways, true, "telegram"),
			wantStatus: hcStatusNotConfigured, wantEffective: config.NotifyOnAlways, wantReads: 1},
		{name: "NOTIFY_ON=failure confirmed", section: hcSectionInitialized,
			mutate:     func(c *config.Config) { c.NotifyOn = config.NotifyOnFailure },
			relay:      relayAnswer("ready", config.NotifyOnFailure, true, "telegram"),
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnFailure, wantReads: 1},
		{name: "unrecognised NOTIFY_ON is requested as always", section: hcSectionInitialized,
			mutate:     func(c *config.Config) { c.NotifyOn = "only-when-broken" },
			relay:      relayAnswer("ready", config.NotifyOnAlways, true, "telegram"),
			wantStatus: hcStatusReady, wantEffective: config.NotifyOnAlways, wantReads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := centralizedNotifyConfig(t)
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			reads := stubDeliveryStatus(t, tc.relay, tc.relayErr)
			logger, buf := debugLogger(t)

			d := decideNotifyFilter(context.Background(), cfg, logger, tc.section, "notifications init")

			if d.status != tc.wantStatus || d.effective != tc.wantEffective || d.reason != tc.wantReason {
				t.Fatalf("decision = status %q effective %q reason %q; want %q %q %q",
					d.status, d.effective, d.reason, tc.wantStatus, tc.wantEffective, tc.wantReason)
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

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, hcSectionInitialized)

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

	logNotifyFilterInit(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger}, nil, hcSectionDisabled)

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
