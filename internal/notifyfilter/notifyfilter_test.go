package notifyfilter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	FetchDeliveryStatus = func(context.Context, *http.Client, string, string, string, *logging.Logger, string) (health.DeliveryStatus, error) {
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
			fallback := strings.Contains(buf.String(), "op: filter fallback=always reason="+tc.reason)
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

// The relay read gets the decision's logger and operation, so its transport stages land in the
// run log under "notifications init" or "notifications dispatch".
func TestDecideHandsItsLoggerAndOperationToTheRelayRead(t *testing.T) {
	var gotLogger *logging.Logger
	var gotOp string
	orig := FetchDeliveryStatus
	t.Cleanup(func() { FetchDeliveryStatus = orig })
	FetchDeliveryStatus = func(_ context.Context, _ *http.Client, _, _, _ string, logger *logging.Logger, op string) (health.DeliveryStatus, error) {
		gotLogger, gotOp = logger, op
		return answer("ready", "warning", true, 0), nil
	}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})
	cfg := &config.Config{BaseDir: t.TempDir(), HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized,
		NotifyOn: config.NotifyOnWarning, TelegramEnabled: true}

	Decide(context.Background(), cfg, logger, SectionInitialized, "notifications dispatch")

	if gotLogger != logger || gotOp != "notifications dispatch" {
		t.Fatalf("relay read got logger %p op %q; want %p and %q", gotLogger, gotOp, logger, "notifications dispatch")
	}
}

// The outcome closes the block: applied when the run applies the threshold it asked for, not
// applied when it fell back. The symbol marks the outcome, never a value.
func TestOutcome(t *testing.T) {
	cases := []struct {
		d    Decision
		want string
	}{
		{Decision{Requested: "warning", Effective: "warning"}, "✓ Notification filter: applied"},
		{Decision{Requested: "always", Effective: "always"}, "✓ Notification filter: applied"},
		{Decision{Requested: "warning", Effective: "always"}, "⚠ Notification filter: not applied"},
		{Decision{Requested: "failure", Effective: "always"}, "⚠ Notification filter: not applied"},
	}
	for _, tc := range cases {
		if got := tc.d.Outcome(); got != tc.want {
			t.Errorf("Outcome(%s -> %s) = %q; want %q", tc.d.Requested, tc.d.Effective, got, tc.want)
		}
		if got := tc.d.Applied(); got != (tc.d.Requested == tc.d.Effective) {
			t.Errorf("Applied(%s -> %s) = %v", tc.d.Requested, tc.d.Effective, got)
		}
	}
}

// Decide names why the relay's answer could not be used, from what the real read returned: no
// HTTP answer, a status other than 200, or a 200 that is not a usable status. A request that
// could not be built, an answer that was used, and an error no kind matches name nothing.
func TestDecideNamesWhyTheRelayAnswerWasUnavailable(t *testing.T) {
	serve := func(t *testing.T, h http.HandlerFunc) string {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv.URL
	}
	answering := func(status int, body string) func(*testing.T) string {
		return func(t *testing.T) string {
			return serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			})
		}
	}
	cases := []struct {
		name        string
		host        func(*testing.T) string
		stub        error // replaces the real read when set
		reason      string
		unavailable string
		stage       string // the failed stage the read logged, when it failed in transport
	}{
		{name: "connection refused", host: func(t *testing.T) string {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			srv.Close()
			return srv.URL
		}, reason: ReasonStatusUnavailable, unavailable: UnavailableUnreachable, stage: "connect"},
		{name: "hung up without an answer", host: func(t *testing.T) string {
			return serve(t, func(w http.ResponseWriter, _ *http.Request) {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_ = conn.Close()
				}
			})
		}, reason: ReasonStatusUnavailable, unavailable: UnavailableUnreachable, stage: "response"},
		{name: "answer broke off", host: func(t *testing.T) string {
			return serve(t, func(w http.ResponseWriter, _ *http.Request) {
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nabc")
				_ = rw.Flush()
			})
		}, reason: ReasonStatusUnavailable, unavailable: UnavailableUnreachable, stage: "response"},
		{name: "http 503", host: answering(http.StatusServiceUnavailable, `{"error":"HC_DELIVERY_DISABLED"}`),
			reason: ReasonStatusUnavailable, unavailable: UnavailableHTTPStatus},
		{name: "http 404", host: answering(http.StatusNotFound, ``), reason: ReasonStatusUnavailable, unavailable: UnavailableHTTPStatus},
		{name: "bad JSON", host: answering(http.StatusOK, `{"schema_version": 1, "state": `),
			reason: ReasonStatusUnavailable, unavailable: UnavailableUnusable},
		{name: "unknown schema", host: answering(http.StatusOK, `{"schema_version": 2, "state": "ready", "valid_for_seconds": 120}`),
			reason: ReasonStatusUnavailable, unavailable: UnavailableUnusable},
		{name: "unknown state", host: answering(http.StatusOK, `{"schema_version": 1, "state": "project_missing", "valid_for_seconds": 120}`),
			reason: ReasonStatusUnavailable, unavailable: UnavailableUnusable},
		{name: "expired evaluation", host: answering(http.StatusOK, `{"schema_version": 1, "state": "ready", "age_seconds": 120, "valid_for_seconds": 120}`),
			reason: ReasonStatusUnavailable, unavailable: UnavailableUnusable},
		{name: "request not built", host: func(*testing.T) string { return "http://[::1" },
			reason: ReasonStatusUnavailable, unavailable: "", stage: "build"},
		{name: "usable answer", host: answering(http.StatusOK, `{"schema_version": 1, "state": "degraded", "valid_for_seconds": 120}`),
			reason: ReasonAlertsNotVerified, unavailable: ""},
		{name: "error of no known kind", stub: errors.New("down"), reason: ReasonStatusUnavailable, unavailable: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{BaseDir: t.TempDir(), HealthcheckEnabled: true, HealthcheckMode: config.HealthcheckModeCentralized,
				NotifyOn: config.NotifyOnWarning, TelegramEnabled: true, ServerID: "123456789012"}
			if tc.stub != nil {
				stubRelay(t, health.DeliveryStatus{}, tc.stub)
			} else {
				cfg.ServerAPIHost = tc.host(t)
			}

			var buf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&buf)

			d := Decide(context.Background(), cfg, logger, SectionInitialized, "op")

			if d.Reason != tc.reason || d.Unavailable != tc.unavailable {
				t.Fatalf("decision = reason %q unavailable %q; want %q %q", d.Reason, d.Unavailable, tc.reason, tc.unavailable)
			}
			if failed := strings.Contains(buf.String(), "op: failed stage="); failed != (tc.stage != "") ||
				(tc.stage != "" && !strings.Contains(buf.String(), "op: failed stage="+tc.stage+" ")) {
				t.Fatalf("want failed stage %q in:\n%s", tc.stage, buf.String())
			}
		})
	}
}
