package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// decidedOrchestrator is an orchestrator whose backup.env asks for NOTIFY_ON=failure, with one
// enabled Webhook spy and a logger writing into the returned buffer.
func decidedOrchestrator() (*Orchestrator, *notifySpy, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	spy := &notifySpy{name: "Webhook"}
	o := &Orchestrator{
		logger:               logger,
		cfg:                  &config.Config{WebhookEnabled: true, NotifyOn: config.NotifyOnFailure},
		notificationChannels: []NotificationChannel{spy},
	}
	return o, spy, &buf
}

// Dispatch applies the filter decided for the run, never the raw setting: with the Healthchecks
// monitor not confirmed the decision is always, and a clean run is notified even though
// backup.env asks for failure only.
func TestDispatchAppliesTheDecidedFilterNotTheSetting(t *testing.T) {
	for _, decided := range []string{config.NotifyOnAlways, ""} {
		o, spy, buf := decidedOrchestrator()
		o.notifyFilterDispatch = decided

		o.dispatchNotifications(context.Background(), &BackupStats{})

		if spy.calls != 1 {
			t.Fatalf("decided %q: Webhook dispatched %d times, want 1; log:\n%s", decided, spy.calls, buf.String())
		}
		if !strings.Contains(buf.String(), "Notifications: sending") {
			t.Fatalf("decided %q: missing \"Notifications: sending\" in:\n%s", decided, buf.String())
		}
	}
}

// The notification boundary asks the refresh once, states the filter, then dispatches with it.
func TestStartNotificationGroupAppliesTheRefreshedFilter(t *testing.T) {
	o, spy, buf := decidedOrchestrator()
	refreshes := 0
	o.SetNotifyFilter(func(context.Context) string {
		refreshes++
		return config.NotifyOnFailure
	})

	o.startNotificationGroup(context.Background(), &BackupStats{})

	out := buf.String()
	if refreshes != 1 {
		t.Fatalf("refresh asked %d times, want 1", refreshes)
	}
	if spy.calls != 0 {
		t.Fatalf("a clean run under the decided failure filter dispatched %d times, want 0; log:\n%s", spy.calls, out)
	}
	last := -1
	for _, want := range []string{
		"Notification filter: failure",
		"notifications dispatch: run outcome=success warnings=0 errors=0",
		"Notifications: skipped",
		"Webhook: filtered",
	} {
		i := strings.Index(out, want)
		if i < 0 || i < last {
			t.Fatalf("missing or out of order %q in:\n%s", want, out)
		}
		last = i
	}
}

// Without a decision handed over (cmd/proxsave never initialized the filter) the boundary
// notifies every outcome, whatever backup.env asks for.
func TestStartNotificationGroupWithoutADecisionNotifiesEverything(t *testing.T) {
	o, spy, buf := decidedOrchestrator()

	o.startNotificationGroup(context.Background(), &BackupStats{})

	if spy.calls != 1 {
		t.Fatalf("Webhook dispatched %d times, want 1; log:\n%s", spy.calls, buf.String())
	}
	if !strings.Contains(buf.String(), "Notification filter: always") {
		t.Fatalf("missing \"Notification filter: always\" in:\n%s", buf.String())
	}
}

// An early error does not go through the refresh and is notified whatever the setting.
func TestAnEarlyErrorIsNotifiedWhateverTheSetting(t *testing.T) {
	o, spy, buf := decidedOrchestrator()
	refreshes := 0
	o.SetNotifyFilter(func(context.Context) string {
		refreshes++
		return config.NotifyOnFailure
	})

	o.DispatchEarlyErrorNotification(context.Background(), &EarlyErrorState{
		Phase: "config", Error: errors.New("broken"), ExitCode: types.ExitConfigError, Timestamp: time.Now(),
	})

	if spy.calls != 1 {
		t.Fatalf("early error dispatched %d times, want 1; log:\n%s", spy.calls, buf.String())
	}
	if refreshes != 0 {
		t.Fatalf("early error asked the refresh %d times, want 0", refreshes)
	}
	if !strings.Contains(buf.String(), "Notifications: sending") {
		t.Fatalf("missing \"Notifications: sending\" in:\n%s", buf.String())
	}
}
