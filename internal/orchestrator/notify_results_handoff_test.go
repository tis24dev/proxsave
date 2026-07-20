package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
)

// recordNotifierStatus must capture EVERY channel's outcome into stats.NotifyResults keyed by
// display name, including Gotify/Webhook which have no legacy status field, and mapping the
// result to the same severity describeNotificationSeverity returns.
func TestRecordNotifierStatusPopulatesNotifyResultsForAllChannels(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	stats := &BackupStats{}
	cases := []struct {
		name    string
		result  *notify.NotificationResult
		wantSev string
	}{
		{"Email", &notify.NotificationResult{Success: true, Method: "email-relay"}, "ok"},
		{"Telegram", &notify.NotificationResult{Success: true, UsedFallback: true, Method: "tg"}, "warning"},
		{"Gotify", &notify.NotificationResult{Success: false, Error: errors.New("boom")}, "error"},
		{"Webhook", nil, "disabled"},
	}

	for _, tc := range cases {
		adapter := NewNotificationAdapter(&stubNotifier{name: tc.name}, logger)
		adapter.recordNotifierStatus(stats, tc.result)

		// Guard: the recorded severity is exactly what describeNotificationSeverity yields.
		if want := describeNotificationSeverity(tc.result); want != tc.wantSev {
			t.Fatalf("%s: describeNotificationSeverity = %q, test expects %q", tc.name, want, tc.wantSev)
		}
		if got := stats.NotifyResults[tc.name]; got != tc.wantSev {
			t.Fatalf("NotifyResults[%q] = %q, want %q", tc.name, got, tc.wantSev)
		}
	}

	if len(stats.NotifyResults) != 4 {
		t.Fatalf("NotifyResults should carry all four channels, got %#v", stats.NotifyResults)
	}
}

// An ENABLED channel with no registered adapter sent nothing: dispatch records an "error"
// outcome so its per-channel sensor goes DOWN. The Healthchecks section entry is a reporting
// surface, not a notify channel, so it never lands in NotifyResults even when unregistered.
func TestDispatchRecordsErrorForEnabledUninitializedChannel(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&bytes.Buffer{})

	o := &Orchestrator{
		logger: logger,
		cfg: &config.Config{
			GotifyEnabled:      true, // enabled but no adapter registered
			HealthcheckEnabled: true, // section enabled but unregistered too
		},
		notificationChannels: nil,
	}
	stats := &BackupStats{}
	o.dispatchNotifications(context.Background(), stats)

	if got := stats.NotifyResults["Gotify"]; got != "error" {
		t.Fatalf("NotifyResults[Gotify] = %q, want error", got)
	}
	if _, ok := stats.NotifyResults[healthchecksSectionName]; ok {
		t.Fatalf("the Healthchecks section must never appear in NotifyResults, got %#v", stats.NotifyResults)
	}
}

// persistNotifyResults is env-gated: it writes the daemon handoff file only when EnvRunID is
// set (the daemon's supervised child), stamping the run's rid; a bare run leaves no file.
func TestPersistNotifyResultsEnvGated(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&bytes.Buffer{})

	t.Run("writes when run id env is set", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv(health.EnvRunID, "ridX")
		o := &Orchestrator{
			logger:               logger,
			cfg:                  &config.Config{BaseDir: base, GotifyEnabled: true},
			notificationChannels: nil,
		}
		o.dispatchNotifications(context.Background(), &BackupStats{})

		nr, err := health.LoadNotifyResults(base)
		if err != nil {
			t.Fatalf("LoadNotifyResults: %v", err)
		}
		if nr.RID != "ridX" {
			t.Fatalf("handoff RID = %q, want ridX", nr.RID)
		}
		if got := nr.Results["Gotify"]; got != "error" {
			t.Fatalf("handoff Results[Gotify] = %q, want error", got)
		}
	})

	t.Run("no file without the env", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv(health.EnvRunID, "") // empty => rid guard fails, nothing persisted
		o := &Orchestrator{
			logger:               logger,
			cfg:                  &config.Config{BaseDir: base, GotifyEnabled: true},
			notificationChannels: nil,
		}
		o.dispatchNotifications(context.Background(), &BackupStats{})

		nr, err := health.LoadNotifyResults(base)
		if err != nil {
			t.Fatalf("LoadNotifyResults: %v", err)
		}
		if nr.RID != "" || nr.Results != nil {
			t.Fatalf("no handoff file should be written without EnvRunID, got %+v", nr)
		}
	})
}
