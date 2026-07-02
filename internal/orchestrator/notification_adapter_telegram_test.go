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
