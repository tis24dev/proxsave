package install

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/notifyfilter"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestMain keeps every screen test off the real relay: unless a test says otherwise, the NOTIFY_ON
// decision reads no backup.env and the screen shows no Notifications block.
func TestMain(m *testing.M) {
	healthcheckNotifyFilter = func(context.Context, string, string, string, string, health.Diagnosis) orchestrator.HealthcheckNotifyFilter {
		return orchestrator.HealthcheckNotifyFilter{}
	}
	os.Exit(m.Run())
}

func decided(requested, effective, status, reason string) orchestrator.HealthcheckNotifyFilter {
	return orchestrator.HealthcheckNotifyFilter{Loaded: true, Decision: notifyfilter.Decision{
		Requested: requested, Effective: effective, Status: status, Reason: reason}}
}

// Every text of the block, as the maintainer approved it.
func TestHealthcheckNotifyBlockTexts(t *testing.T) {
	cases := []struct {
		name          string
		self          bool
		nf            orchestrator.HealthcheckNotifyFilter
		setting, curr string
	}{
		{"applied", false, decided("failure", "failure", notifyfilter.StatusReady, ""), "NOTIFY_ON=failure", "failure"},
		{"not configured", false, decided("failure", "always", notifyfilter.StatusNotConfigured, notifyfilter.ReasonAlertsNotVerified),
			"NOTIFY_ON=failure", "always (Healthchecks not configured)"},
		{"not verified", false, decided("warning", "always", notifyfilter.StatusNotVerified, notifyfilter.ReasonAlertsNotVerified),
			"NOTIFY_ON=warning", "always (Healthchecks not verified)"},
		{"degraded", false, decided("warning", "always", notifyfilter.StatusDegraded, notifyfilter.ReasonAlertsNotVerified),
			"NOTIFY_ON=warning", "always (Healthchecks degraded)"},
		{"relay state unknown", false, decided("warning", "always", notifyfilter.StatusUnknown, notifyfilter.ReasonAlertsNotVerified),
			"NOTIFY_ON=warning", "always (Healthchecks status unknown)"},
		{"relay unreachable", false, decided("warning", "always", notifyfilter.StatusUnknown, notifyfilter.ReasonStatusUnavailable),
			"NOTIFY_ON=warning", "always (Healthchecks status unavailable)"},
		{"policy not applied", false, decided("warning", "always", notifyfilter.StatusReady, notifyfilter.ReasonPolicyUnconfirmed),
			"NOTIFY_ON=warning", "always (setting not yet applied by the server)"},
		{"daemon down", false, decided("warning", "always", notifyfilter.StatusNotTransmitting, notifyfilter.ReasonNotTransmitting),
			"NOTIFY_ON=warning", "always (daemon not transmitting)"},
		{"always", false, decided("always", "always", notifyfilter.StatusNotConfigured, ""), "NOTIFY_ON=always", "always"},
		{"self mode", true, orchestrator.HealthcheckNotifyFilter{}, "Self mode", "Self mode"},
		{"self read from the file", false, orchestrator.HealthcheckNotifyFilter{Loaded: true, Self: true}, "Self mode", "Self mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nb := newHealthcheckNotifyBlock(tc.self, tc.nf)
			if nb == nil || nb.Setting != tc.setting || nb.Current != tc.curr {
				t.Fatalf("block = %+v; want Setting %q Current %q", nb, tc.setting, tc.curr)
			}
		})
	}

	def := decided("warning", "always", notifyfilter.StatusNotConfigured, notifyfilter.ReasonAlertsNotVerified)
	def.FromDefault = true
	if nb := newHealthcheckNotifyBlock(false, def); nb.Setting != "NOTIFY_ON=warning (default)" {
		t.Fatalf("absent variable: Setting = %q", nb.Setting)
	}
	bad := decided("always", "always", notifyfilter.StatusReady, "")
	bad.Invalid, bad.Raw = true, "warnig"
	if nb := newHealthcheckNotifyBlock(false, bad); nb.Setting != "NOTIFY_ON=warnig (not valid, always used)" || nb.Current != "always" {
		t.Fatalf("invalid value: block = %+v", nb)
	}
	if nb := newHealthcheckNotifyBlock(false, orchestrator.HealthcheckNotifyFilter{}); nb != nil {
		t.Fatalf("unreadable backup.env: block = %+v; want none", nb)
	}
}

// The block sits right above the sensors, below the Status, as plain lines with no outcome symbol.
func TestHealthcheckPromptPlacesTheNotificationsBlockAboveTheSensors(t *testing.T) {
	sensors := []health.SensorRow{{Name: "proxsave-alive", State: "ok", Level: health.SensorOk}}
	nb := &healthcheckNotifyBlock{Setting: "NOTIFY_ON=failure", Current: "always (Healthchecks not configured)"}
	v := ansi.Strip(buildHealthcheckPrompt(false, "", "", "", "WORKING", "It is reporting.", orchestrator.HealthcheckSetupLevelOk, sensors, nb))

	want := "It is reporting.\n\nNotifications:\nSetting: NOTIFY_ON=failure\nCurrent: always (Healthchecks not configured)\n\nSensors:\n"
	if !strings.Contains(v, want) {
		t.Fatalf("Notifications block not between Status and Sensors:\n%s", v)
	}
	for _, line := range strings.Split(v, "\n") {
		if (strings.HasPrefix(line, "Setting:") || strings.HasPrefix(line, "Current:")) && strings.ContainsAny(line, "✓✗⚠") {
			t.Fatalf("an outcome symbol on %q", line)
		}
	}

	if v := ansi.Strip(buildHealthcheckPrompt(false, "", "", "", "NOT CHECKED", "Choose Check.", orchestrator.HealthcheckSetupLevelNeutral, nil, nil)); strings.Contains(v, "Notifications:") {
		t.Fatalf("no block before a Check:\n%s", v)
	}
}

// In the dashboard the Check runs on entry, and the block shows the decision it took.
func TestRunHealthcheckSetupShowsTheNotificationsBlockAfterTheCheck(t *testing.T) {
	d := newDriver(t)
	orig, origCheck, origNF := healthcheckBuildBootstrap, healthcheckCheck, healthcheckNotifyFilter
	t.Cleanup(func() { healthcheckBuildBootstrap, healthcheckCheck, healthcheckNotifyFilter = orig, origCheck, origNF })
	healthcheckBuildBootstrap = healthcheckEligibleBootstrap
	transmitting := health.Diagnosis{State: health.TxTransmitting, DaemonUp: true}
	healthcheckCheck = func(context.Context, string, string, string, time.Duration) orchestrator.HealthcheckCheckResult {
		return orchestrator.HealthcheckCheckResult{Reachable: true, DaemonRead: true, Daemon: transmitting}
	}
	var gotDaemon health.Diagnosis
	var gotHost, gotID string
	healthcheckNotifyFilter = func(_ context.Context, _, _, host, id string, daemon health.Diagnosis) orchestrator.HealthcheckNotifyFilter {
		gotDaemon, gotHost, gotID = daemon, host, id
		return decided("failure", "failure", notifyfilter.StatusReady, "")
	}

	done := make(chan struct{})
	go func() {
		_, _ = RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", true)
		close(done)
	}()
	deadline := time.After(uitest.Deadline(10 * time.Second))
	for !strings.Contains(ansi.Strip(d.buf.String()), "Current: failure") {
		select {
		case <-deadline:
			t.Fatalf("no Notifications block after the Check:\n%s", ansi.Strip(d.buf.String()))
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	d.keys("down enter")
	<-done
	if gotDaemon != transmitting {
		t.Fatalf("the decision got daemon %+v; want the diagnosis of the same Check", gotDaemon)
	}
	if gotHost != "https://h" || gotID != "12345678" {
		t.Fatalf("the decision got host %q id %q; want the ones the bootstrap resolved", gotHost, gotID)
	}
}
