package notifyfilter

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
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNegotiated(t *testing.T) {
	cases := map[string]*config.Config{
		config.NotifyOnAlways:  nil,
		config.NotifyOnFailure: {NotifyOn: config.NotifyOnFailure},
		config.NotifyOnWarning: {NotifyOn: config.NotifyOnWarning},
	}
	for want, cfg := range cases {
		if got := Negotiated(cfg); got != want {
			t.Errorf("Negotiated(%+v) = %q; want %q", cfg, got, want)
		}
	}
	for _, v := range []string{"", "warnig", "WARNING"} {
		if got := Negotiated(&config.Config{NotifyOn: v}); got != config.NotifyOnAlways {
			t.Errorf("Negotiated(%q) = %q; an unset or unrecognised value is negotiated as always", v, got)
		}
	}
}

func TestEnabledChannels(t *testing.T) {
	if got := EnabledChannels(nil); got == nil || len(got) != 0 {
		t.Fatalf("EnabledChannels(nil) = %#v; want an empty non-nil slice", got)
	}
	all := &config.Config{WebhookEnabled: true, TelegramEnabled: true, GotifyEnabled: true, EmailEnabled: true}
	if got := strings.Join(EnabledChannels(all), ","); got != "email,gotify,telegram,webhook" {
		t.Fatalf("EnabledChannels(all) = %q; want sorted", got)
	}
}

func TestSelfNotifyChecksConfigured(t *testing.T) {
	if SelfNotifyChecksConfigured(&config.Config{}) {
		t.Fatal("no HEALTHCHECK_NOTIFY_* set must read false")
	}
	for _, cfg := range []*config.Config{{HealthcheckNotifyEmailURL: "https://x"}, {HealthcheckNotifyWebhookID: "id"}} {
		if !SelfNotifyChecksConfigured(cfg) {
			t.Fatalf("%+v must read true", cfg)
		}
	}
}

func stubRelay(t *testing.T, st health.DeliveryStatus, err error) *int {
	t.Helper()
	reads := 0
	orig := FetchDeliveryStatus
	t.Cleanup(func() { FetchDeliveryStatus = orig })
	FetchDeliveryStatus = func(context.Context, *http.Client, string, string, string) (health.DeliveryStatus, error) {
		reads++
		return st, err
	}
	return &reads
}

func answer(state, requested string, applied bool, age int) health.DeliveryStatus {
	return health.DeliveryStatus{SchemaVersion: 1, State: state, AgeSeconds: age, ValidForSeconds: 120,
		NotifyPolicy: &health.NotifyPolicyAck{ContractVersion: 1, Requested: requested, Applied: applied, Channels: []string{"telegram"}}}
}

// Every branch of the decision, and the DEBUG line that explains a fallback.
func TestDecide(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{BaseDir: t.TempDir(), HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized,
			NotifyOn: config.NotifyOnWarning, TelegramEnabled: true}
	}
	cases := []struct {
		name                      string
		mutate                    func(*config.Config)
		section                   string
		relay                     health.DeliveryStatus
		relayErr                  error
		status, effective, reason string
		reads                     int
	}{
		{"disabled", nil, SectionDisabled, health.DeliveryStatus{}, nil, StatusDisabled, "always", ReasonHealthchecksDisabled, 0},
		{"not transmitting", nil, SectionNotTransmitting, health.DeliveryStatus{}, nil, StatusNotTransmitting, "always", ReasonNotTransmitting, 0},
		{"self", func(c *config.Config) { c.HealthcheckMode = config.HealthcheckModeSelf }, SectionInitialized, health.DeliveryStatus{}, nil, StatusSelf, "warning", "", 0},
		{"self with notify checks", func(c *config.Config) {
			c.HealthcheckMode, c.HealthcheckNotifyTelegramURL = config.HealthcheckModeSelf, "https://x"
		}, SectionInitialized, health.DeliveryStatus{}, nil, StatusSelf, "always", ReasonSelfNotifyChecks, 0},
		{"ready applied", nil, SectionInitialized, answer("ready", "warning", true, 0), nil, StatusReady, "warning", "", 1},
		{"ready not applied", nil, SectionInitialized, answer("ready", "warning", false, 0), nil, StatusReady, "always", ReasonPolicyUnconfirmed, 1},
		{"not configured", nil, SectionInitialized, answer("not_configured", "warning", true, 0), nil, StatusNotConfigured, "always", ReasonAlertsNotVerified, 1},
		{"unverified", nil, SectionInitialized, answer("unverified", "warning", true, 0), nil, StatusNotVerified, "always", ReasonAlertsNotVerified, 1},
		{"degraded", nil, SectionInitialized, answer("degraded", "warning", true, 0), nil, StatusDegraded, "always", ReasonAlertsNotVerified, 1},
		{"unknown", nil, SectionInitialized, answer("unknown", "warning", true, 0), nil, StatusUnknown, "always", ReasonAlertsNotVerified, 1},
		{"relay unreachable", nil, SectionInitialized, health.DeliveryStatus{}, errors.New("down"), StatusUnknown, "always", ReasonStatusUnavailable, 1},
		{"always needs nothing", func(c *config.Config) { c.NotifyOn = config.NotifyOnAlways }, SectionInitialized,
			answer("not_configured", "always", true, 0), nil, StatusNotConfigured, "always", "", 1},
		{"nil config", nil, "", health.DeliveryStatus{}, nil, StatusDisabled, "always", "", 0}, // requested always: nothing to fall back from
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			if tc.name == "nil config" {
				cfg = nil
			}
			reads := stubRelay(t, tc.relay, tc.relayErr)
			var buf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&buf)
			d := Decide(context.Background(), cfg, logger, tc.section, "op")
			if d.Status != tc.status || d.Effective != tc.effective || d.Reason != tc.reason || *reads != tc.reads {
				t.Fatalf("decision = %+v after %d reads; want status %q effective %q reason %q, %d reads",
					d, *reads, tc.status, tc.effective, tc.reason, tc.reads)
			}
			fallback := strings.Contains(buf.String(), "op: notification filter fallback=always reason="+tc.reason)
			if fallback != (tc.reason != "") {
				t.Fatalf("fallback DEBUG line present=%v; want %v:\n%s", fallback, tc.reason != "", buf.String())
			}
		})
	}
}

// The reuse window is the relay's remaining validity, at most Validity, measured with Now.
func TestDecideValidity(t *testing.T) {
	at := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	orig := Now
	t.Cleanup(func() { Now = orig })
	Now = func() time.Time { return at }
	cfg := &config.Config{BaseDir: t.TempDir(), HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized,
		NotifyOn: config.NotifyOnWarning, TelegramEnabled: true}

	stubRelay(t, answer("ready", "warning", true, 100), nil)
	if d := Decide(context.Background(), cfg, nil, SectionInitialized, ""); d.ReadAt != at || d.ValidFor != 20*time.Second {
		t.Fatalf("aged answer: ReadAt %v ValidFor %v; want %v and 20s", d.ReadAt, d.ValidFor, at)
	}
	stubRelay(t, health.DeliveryStatus{}, errors.New("down"))
	if d := Decide(context.Background(), cfg, nil, SectionInitialized, ""); d.ValidFor != Validity {
		t.Fatalf("unavailable answer: ValidFor %v; want the local %v", d.ValidFor, Validity)
	}
}
