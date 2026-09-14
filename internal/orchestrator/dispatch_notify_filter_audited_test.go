package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
)

// notifySpy is a NotificationChannel that only counts dispatches, under whatever display
// name the entries loop matches on.
type notifySpy struct {
	name  string
	calls int
}

func (s *notifySpy) Name() string { return s.name }
func (s *notifySpy) Notify(context.Context, *BackupStats) error {
	s.calls++
	return nil
}

// dispatchWith runs one notification phase with a single spy channel and returns the spy
// plus everything the logger emitted.
func dispatchWith(t *testing.T, cfg *config.Config, stats *BackupStats, name string) (*notifySpy, string) {
	t.Helper()
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	spy := &notifySpy{name: name}
	o := &Orchestrator{logger: logger, cfg: cfg, notificationChannels: []NotificationChannel{spy}}
	o.dispatchNotifications(context.Background(), stats)
	return spy, buf.String()
}

// The feature itself: a channel that is ENABLED is still not dispatched when the run's
// outcome sits below the NOTIFY_ON threshold. Both halves have to be asserted together --
// enabled-and-silent is the new state, and a test that only checked the silence would
// pass against a channel that was simply switched off.
func TestNotifyOnSuppressesAnEnabledChannelBelowTheThreshold(t *testing.T) {
	cases := []struct {
		name     string
		policy   string
		stats    *BackupStats
		wantSent bool
	}{
		// The default. Every install that predates the key lands here and nothing about
		// it may change, which is the single most important row in this file.
		{"always on a clean run", config.NotifyOnAlways, &BackupStats{}, true},
		{"always on a warning run", config.NotifyOnAlways, &BackupStats{ExitCode: 1, WarningCount: 1}, true},
		{"always on a failure", config.NotifyOnAlways, &BackupStats{ExitCode: 2, ErrorCount: 1}, true},

		// An unset NotifyOn is what every &config.Config{} literal in the tree carries,
		// and what a Config built by hand anywhere else will carry. It must behave as
		// "always" at the GATE, not merely in the parser those literals never run.
		{"unset policy on a clean run", "", &BackupStats{}, true},

		{"warning on a clean run", config.NotifyOnWarning, &BackupStats{}, false},
		{"warning on a warning run", config.NotifyOnWarning, &BackupStats{ExitCode: 1, WarningCount: 1}, true},
		// The row an operator gets wrong: "warning" includes failures.
		{"warning on a failure", config.NotifyOnWarning, &BackupStats{ExitCode: 2, ErrorCount: 1}, true},

		{"failure on a clean run", config.NotifyOnFailure, &BackupStats{}, false},
		{"failure on a warning run", config.NotifyOnFailure, &BackupStats{ExitCode: 1, WarningCount: 1}, false},
		{"failure on a failure", config.NotifyOnFailure, &BackupStats{ExitCode: 2, ErrorCount: 1}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{WebhookEnabled: true, NotifyOn: tc.policy}
			spy, out := dispatchWith(t, cfg, tc.stats, "Webhook")

			if tc.wantSent && spy.calls != 1 {
				t.Fatalf("Webhook dispatched %d times, want 1; log:\n%s", spy.calls, out)
			}
			if !tc.wantSent {
				if spy.calls != 0 {
					t.Fatalf("Webhook dispatched %d times, want 0 (NOTIFY_ON=%s); log:\n%s", spy.calls, tc.policy, out)
				}
				// The operator has to be able to tell "suppressed by policy" from
				// "switched off" and from "crashed", so the reason is in the log with
				// both the policy and the outcome that was compared against it.
				if !strings.Contains(out, "Webhook: NOTIFY_ON="+tc.policy) {
					t.Fatalf("a suppressed channel must say why, got:\n%s", out)
				}
				if strings.Contains(out, "Webhook: disabled") {
					t.Fatalf("a suppressed channel must not be reported as disabled, got:\n%s", out)
				}
			}
		})
	}
}

// The correctness blocker. Healthchecks is element 5 of the SAME entries slice as the
// four real channels, but it sends nothing: it renders the Phase-7 section and captures
// the portal magic-link onto stats.HealthcheckLink. Filtering it by outcome would delete
// the run's reporting surface on exactly the runs NOTIFY_ON exists to quieten -- and a
// clean run under NOTIFY_ON=failure is the case where every other entry is suppressed, so
// it is where a position-based gate would take this one down with them.
func TestNotifyOnNeverFiltersTheHealthchecksSection(t *testing.T) {
	for _, policy := range []string{config.NotifyOnWarning, config.NotifyOnFailure} {
		cfg := &config.Config{HealthcheckEnabled: true, NotifyOn: policy}
		spy, out := dispatchWith(t, cfg, &BackupStats{ExitCode: 0}, healthchecksSectionName)

		if spy.calls != 1 {
			t.Fatalf("NOTIFY_ON=%s on a clean run: Healthchecks dispatched %d times, want 1; log:\n%s", policy, spy.calls, out)
		}
		if strings.Contains(out, healthchecksSectionName+": NOTIFY_ON=") {
			t.Fatalf("NOTIFY_ON=%s must not emit a suppression line for the Healthchecks section, got:\n%s", policy, out)
		}
	}

	// Stated as a rule and not just as the observed behaviour of one name, because the
	// exemption is what a future Tier-2 entry in the same slice has to be added to.
	if !notifyOnExempt(healthchecksSectionName) {
		t.Fatal("the Healthchecks section must be exempt from NOTIFY_ON")
	}
	for _, name := range []string{"Email", "Telegram", "Gotify", "Webhook"} {
		if notifyOnExempt(name) {
			t.Fatalf("%s sends to the operator and must be subject to NOTIFY_ON", name)
		}
	}
}

// The second correctness blocker, and the one with no visible symptom until a grace
// window expires. setNotifyResult is otherwise only reachable from inside Notify, so a
// suppressed channel would leave stats.NotifyResults empty, persistNotifyResults would
// write {}, and the daemon bails on len(nr.Results)==0 -- meaning every quiet run leaves
// the per-channel proxsave-notify-* sensors unpinged until they go DOWN on their own. The
// operator then gets the alert storm they installed NOTIFY_ON to stop, from the one
// subsystem that is supposed to stay authoritative. "disabled" is the severity
// severityToSuffix already skips without pinging and pruneNotifyRecords already clears.
func TestASuppressedChannelIsRecordedAsDisabledForTheDaemonHandoff(t *testing.T) {
	cfg := &config.Config{WebhookEnabled: true, NotifyOn: config.NotifyOnFailure}
	stats := &BackupStats{}
	spy, out := dispatchWith(t, cfg, stats, "Webhook")

	if spy.calls != 0 {
		t.Fatalf("Webhook dispatched %d times, want 0; log:\n%s", spy.calls, out)
	}
	if len(stats.NotifyResults) == 0 {
		t.Fatal("NotifyResults is empty after a suppressed run; the daemon reads len(Results)==0 as \"nothing to report\" and skips the whole per-channel report")
	}
	if got := stats.NotifyResults["Webhook"]; got != "disabled" {
		t.Fatalf("NotifyResults[Webhook] = %q; want \"disabled\", the severity the daemon skips without pinging", got)
	}
}

// The remainder loop dispatches channels the fixed table does not name. It is gated under
// the same exemption, because an operator asking for quiet clean runs means all of their
// channels, not the four that happen to be in the slice today.
func TestNotifyOnAlsoGatesChannelsOutsideTheFixedTable(t *testing.T) {
	cfg := &config.Config{NotifyOn: config.NotifyOnWarning}
	spy, out := dispatchWith(t, cfg, &BackupStats{}, "Matrix")
	if spy.calls != 0 {
		t.Fatalf("an unlisted channel dispatched %d times on a clean run under NOTIFY_ON=warning, want 0; log:\n%s", spy.calls, out)
	}

	// ...and still dispatches when the threshold is met, so the gate is not just a
	// blanket removal of the remainder loop.
	spy, out = dispatchWith(t, cfg, &BackupStats{ExitCode: 1, WarningCount: 1}, "Matrix")
	if spy.calls != 1 {
		t.Fatalf("an unlisted channel dispatched %d times on a warning run, want 1; log:\n%s", spy.calls, out)
	}
}

// notifyOutcome is deliberately not a bare StatusFromExitCode. DispatchEarlyErrorNotification
// assembles its own BackupStats and sets Failed/ErrorCount; ExitCode is an int that nothing
// validates and that has a legal zero value. If the classification trusted the exit code
// alone, an early-init failure carrying 0 would read as a success and be suppressed -- the
// one outcome nobody configures NOTIFY_ON to hide. Today all four call sites set a non-zero
// code, so this is a landmine rather than a live defect, and it costs one condition to
// disarm.
func TestAFailedRunIsNeverClassifiedFromTheExitCodeAlone(t *testing.T) {
	cases := []struct {
		name  string
		stats *BackupStats
		want  notify.NotificationStatus
	}{
		{"clean", &BackupStats{ExitCode: 0}, notify.StatusSuccess},
		{"warning", &BackupStats{ExitCode: 1}, notify.StatusWarning},
		{"failure", &BackupStats{ExitCode: 2}, notify.StatusFailure},
		{"failed flag outranks a zero exit code", &BackupStats{Failed: true, ExitCode: 0}, notify.StatusFailure},
		{"errors outrank a zero exit code", &BackupStats{ErrorCount: 1, ExitCode: 0}, notify.StatusFailure},
		{"errors outrank a warning exit code", &BackupStats{ErrorCount: 1, ExitCode: 1}, notify.StatusFailure},
		// Unclassifiable, so it delivers: there is no such thing as a safe silence.
		{"nil stats", nil, notify.StatusFailure},
	}
	for _, tc := range cases {
		if got := notifyOutcome(tc.stats); got != tc.want {
			t.Errorf("%s: notifyOutcome = %s; want %s", tc.name, got, tc.want)
		}
	}

	// End to end through the strictest policy: an early-error run still notifies.
	cfg := &config.Config{WebhookEnabled: true, NotifyOn: config.NotifyOnFailure}
	spy, out := dispatchWith(t, cfg, &BackupStats{Failed: true, ExitCode: 0, ErrorCount: 1}, "Webhook")
	if spy.calls != 1 {
		t.Fatalf("a failed run dispatched %d times under NOTIFY_ON=failure, want 1; log:\n%s", spy.calls, out)
	}
}

// A value the loader could not understand is named in a warning and then ignored, exactly
// as an unrecognised EMAIL_DELIVERY_METHOD is. Quoting what the operator wrote is the
// whole point: the failure mode of a typo'd NOTIFY_ON is silence, and silence is
// indistinguishable from a backup that never ran. Delivery falls open in the meantime.
func TestAnUnrecognisedPolicyWarnsAndDeliversAnyway(t *testing.T) {
	cfg := &config.Config{WebhookEnabled: true, NotifyOn: "only-when-broken"}
	spy, out := dispatchWith(t, cfg, &BackupStats{}, "Webhook")

	if spy.calls != 1 {
		t.Fatalf("dispatched %d times, want 1: an unusable policy must not silence anything; log:\n%s", spy.calls, out)
	}
	if !strings.Contains(out, `NOTIFY_ON="only-when-broken"`) {
		t.Fatalf("the warning must quote the value the operator wrote, got:\n%s", out)
	}
	if !strings.Contains(out, "always|warning|failure") {
		t.Fatalf("the warning must name the accepted values, got:\n%s", out)
	}

	// A recognised policy, and an unset one, stay quiet about themselves.
	for _, policy := range []string{"", config.NotifyOnAlways, config.NotifyOnWarning} {
		_, out := dispatchWith(t, &config.Config{WebhookEnabled: true, NotifyOn: policy}, &BackupStats{}, "Webhook")
		if strings.Contains(out, "not recognized") {
			t.Fatalf("NOTIFY_ON=%q must not warn, got:\n%s", policy, out)
		}
	}
}

// NOTIFY_ON is layered ON TOP of the per-channel switches, not in place of them. A
// channel that is simply switched off keeps saying "disabled" and is never re-described
// as suppressed by policy, which would misreport why nothing was sent and send an
// operator looking for a threshold they never set. Nothing is registered here because
// nothing is registered there either: backup_notifications.go only reaches
// RegisterNotificationChannel after the channel's own *_ENABLED check has passed.
func TestNotifyOnDoesNotChangeHowADisabledChannelIsReported(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	stats := &BackupStats{}
	o := &Orchestrator{
		logger: logger,
		cfg:    &config.Config{NotifyOn: config.NotifyOnFailure}, // every channel off
	}
	o.dispatchNotifications(context.Background(), stats)

	out := buf.String()
	for _, name := range []string{"Email", "Telegram", "Gotify", "Webhook", healthchecksSectionName} {
		if !strings.Contains(out, name+": disabled") {
			t.Fatalf("%s is switched off and must still report as disabled, got:\n%s", name, out)
		}
		if strings.Contains(out, name+": NOTIFY_ON=") {
			t.Fatalf("%s is switched off and must not be attributed to NOTIFY_ON, got:\n%s", name, out)
		}
	}
	// Nothing was suppressed by policy, so nothing is handed to the daemon either: a
	// "disabled" row for a channel the operator never configured would be a phantom.
	if len(stats.NotifyResults) != 0 {
		t.Fatalf("NotifyResults = %v; a run where every channel is switched off records nothing", stats.NotifyResults)
	}
}
