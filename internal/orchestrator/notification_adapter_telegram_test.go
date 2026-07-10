package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
)

// telegramOutcome runs Notify() for a Telegram stub with the given result and
// returns the captured log output plus the logger (for warning/error counters).
func telegramOutcome(t *testing.T, result *notify.NotificationResult) (string, *logging.Logger) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	notifier := &stubNotifier{name: "Telegram", enabled: true, result: result}
	adapter := NewNotificationAdapter(notifier, logger)
	if err := adapter.Notify(context.Background(), sampleBackupStats()); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	return buf.String(), logger
}

func TestTelegramOutcome_Delivered(t *testing.T) {
	out, logger := telegramOutcome(t, &notify.NotificationResult{
		Success:  true,
		Method:   "telegram",
		Duration: 50 * time.Millisecond,
		Metadata: map[string]interface{}{
			"relay_accepted":      true,
			"telegram_state":      "delivered",
			"telegram_message_id": int64(7),
		},
	})
	if !strings.Contains(out, "sent to ProxSave server") {
		t.Fatalf("missing first line (server acceptance): %q", out)
	}
	if !strings.Contains(out, "delivered to Telegram") {
		t.Fatalf("missing second line (delivered): %q", out)
	}
	if logger.ErrorCount() != 0 {
		t.Fatalf("delivered must produce no errors, got %d", logger.ErrorCount())
	}
}

// The key requirement: a permanent Telegram delivery failure shows a ❌ on the
// Telegram line, but ProxSave counts it as a WARNING (never an error) so the backup
// flow is not blocked.
func TestTelegramOutcome_FailedIsWarningNotError(t *testing.T) {
	out, logger := telegramOutcome(t, &notify.NotificationResult{
		Success:  true,
		Method:   "telegram",
		Duration: 50 * time.Millisecond,
		Metadata: map[string]interface{}{
			"relay_accepted": true,
			"telegram_state": "failed",
			"telegram_reason": "http_403",
		},
	})
	if !strings.Contains(out, "sent to ProxSave server") {
		t.Fatalf("missing first line: %q", out)
	}
	if !strings.Contains(out, "not delivered (bot blocked by the user)") {
		t.Fatalf("missing/incorrect failure line: %q", out)
	}
	if logger.ErrorCount() != 0 {
		t.Fatalf("Telegram delivery failure must NOT be an error (got %d): it must not block the backup", logger.ErrorCount())
	}
	if logger.WarningCount() < 1 {
		t.Fatalf("Telegram delivery failure must be counted as a warning, got %d", logger.WarningCount())
	}
}

// L3: the per-channel monitoring sensor must go DOWN ("error") when a
// relay-accepted Telegram message is polled as "failed", even though Success stays
// true (server acceptance). Otherwise NotifyResults reports "ok" on an undelivered
// message and the daemon's per-channel sensor stays UP. Success is left untouched.
func TestRecordNotifierStatus_TelegramFailedDeliveryDrivesSensorDown(t *testing.T) {
	cases := []struct {
		name  string
		state string
		want  string
	}{
		{"failed delivery drives sensor down", "failed", "error"},
		{"delivered stays ok", "delivered", "ok"},
		{"pending stays ok (may still deliver)", "pending", "ok"},
		{"unconfirmed stays ok (confirmation off)", "unconfirmed", "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&bytes.Buffer{})
			adapter := NewNotificationAdapter(&stubNotifier{name: "Telegram"}, logger)
			result := &notify.NotificationResult{
				Success: true, // relay accepted: the run's success signal
				Method:  "telegram",
				Metadata: map[string]interface{}{
					"relay_accepted": true,
					"telegram_state": c.state,
				},
			}
			stats := &BackupStats{}
			adapter.recordNotifierStatus(stats, result)
			if got := stats.NotifyResults["Telegram"]; got != c.want {
				t.Fatalf("telegram_state=%q: NotifyResults[Telegram]=%q, want %q", c.state, got, c.want)
			}
			// The sensor refinement must not flip the run's success signal.
			if !result.Success {
				t.Fatalf("recordNotifierStatus must not mutate result.Success")
			}
		})
	}
}

func TestTelegramOutcome_PendingIsInProgress(t *testing.T) {
	out, logger := telegramOutcome(t, &notify.NotificationResult{
		Success:  true,
		Method:   "telegram",
		Metadata: map[string]interface{}{
			"relay_accepted": true,
			"telegram_state": "pending",
		},
	})
	if !strings.Contains(out, "sent to ProxSave server") || !strings.Contains(out, "delivery in progress") {
		t.Fatalf("missing pending two-line output: %q", out)
	}
	if logger.ErrorCount() != 0 {
		t.Fatalf("pending must not be an error, got %d", logger.ErrorCount())
	}
}

func TestTelegramOutcome_RelayUnreachableIsWarning(t *testing.T) {
	out, logger := telegramOutcome(t, &notify.NotificationResult{
		Success: false,
		Method:  "telegram",
		Error:   errors.New("relay request failed"),
		Metadata: map[string]interface{}{
			"relay_accepted": false,
		},
	})
	if !strings.Contains(out, "could not send to ProxSave server") {
		t.Fatalf("missing server-unreachable line: %q", out)
	}
	if strings.Contains(out, "delivered to Telegram") {
		t.Fatalf("must not print a delivery line when the server did not accept: %q", out)
	}
	if logger.ErrorCount() != 0 {
		t.Fatalf("a failed notification must not block the backup (error count %d)", logger.ErrorCount())
	}
}

// F1: the first line reports the ACCEPTANCE latency (recorded before the poll), not
// the total duration (which includes up to the whole poll budget).
func TestTelegramOutcome_FirstLineUsesAcceptanceDuration(t *testing.T) {
	out, _ := telegramOutcome(t, &notify.NotificationResult{
		Success:  true,
		Method:   "telegram",
		Duration: 10 * time.Second, // total incl. poll
		Metadata: map[string]interface{}{
			"relay_accepted":        true,
			"relay_accept_duration": 42 * time.Millisecond,
			"telegram_state":        "delivered",
		},
	})
	if !strings.Contains(out, "sent to ProxSave server (in 42ms)") {
		t.Fatalf("first line must show acceptance latency (42ms): %q", out)
	}
	if strings.Contains(out, "in 10s") {
		t.Fatalf("first line must NOT show the poll-inflated total (10s): %q", out)
	}
}

// F2: with confirmation disabled the state is "unconfirmed" -> quiet, no warning.
func TestTelegramOutcome_UnconfirmedIsQuietNotWarning(t *testing.T) {
	out, logger := telegramOutcome(t, &notify.NotificationResult{
		Success:  true,
		Method:   "telegram",
		Metadata: map[string]interface{}{
			"relay_accepted": true,
			"telegram_state": "unconfirmed",
		},
	})
	if !strings.Contains(out, "sent to ProxSave server") {
		t.Fatalf("missing first line: %q", out)
	}
	if strings.Contains(out, "delivery in progress") {
		t.Fatalf("confirmation-disabled must NOT warn 'delivery in progress': %q", out)
	}
	if logger.WarningCount() != 0 {
		t.Fatalf("confirmation-disabled must emit no warning, got %d", logger.WarningCount())
	}
}

// F5: telegramDeliverySubstate must gate on the relay_accepted VALUE, so a failed
// relay (accepted=false) gets no substate, not a misleading "delivery unconfirmed".
func TestTelegramDeliverySubstate_ValueGated(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]interface{}
		want string
	}{
		{"failed relay", map[string]interface{}{"relay_accepted": false}, ""},
		{"confirmation disabled", map[string]interface{}{"relay_accepted": true, "telegram_state": "unconfirmed"}, ""},
		{"delivered", map[string]interface{}{"relay_accepted": true, "telegram_state": "delivered"}, "delivered"},
		{"pending", map[string]interface{}{"relay_accepted": true, "telegram_state": "pending"}, "queued"},
		{"non-relay path", map[string]interface{}{}, ""},
	}
	for _, c := range cases {
		got := telegramDeliverySubstate(&notify.NotificationResult{Metadata: c.meta})
		if got != c.want {
			t.Fatalf("%s: telegramDeliverySubstate=%q, want %q", c.name, got, c.want)
		}
	}
}

// No relay metadata (personal mode / legacy direct bot-token send) keeps the old
// generic single line.
func TestTelegramOutcome_NonRelayKeepsGenericLine(t *testing.T) {
	out, _ := telegramOutcome(t, &notify.NotificationResult{
		Success:  true,
		Method:   "telegram",
		Duration: 10 * time.Millisecond,
		Metadata: map[string]interface{}{}, // no relay_accepted key
	})
	if !strings.Contains(out, "notification completed successfully") {
		t.Fatalf("expected generic single line for the non-relay path: %q", out)
	}
	if strings.Contains(out, "sent to ProxSave server") {
		t.Fatalf("must not print the relay two-line output for the non-relay path: %q", out)
	}
}
