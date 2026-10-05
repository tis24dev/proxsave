package notify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestEmailNotifierCheckMailQueueEmpty(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	mockCmdEnv(t, "mailq", "Mail queue is empty", 0)

	count, err := notifier.checkMailQueue(context.Background())
	if err != nil {
		t.Fatalf("checkMailQueue() error=%v", err)
	}
	if count != 0 {
		t.Fatalf("checkMailQueue()=%d want 0", count)
	}
}

func TestEmailNotifierCheckMailQueueCountsEntries(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	mockCmdEnv(t, "mailq", `
Mail queue status:
ABC123*  1234 Mon Jan  1 00:00:00 sender@example.com
                                         admin@example.com
DEF456!  4321 Mon Jan  1 00:00:01 sender2@example.com
                                         ops@example.com
-- 2 Kbytes in 2 Requests.
`, 0)

	count, err := notifier.checkMailQueue(context.Background())
	if err != nil {
		t.Fatalf("checkMailQueue() error=%v", err)
	}
	if count != 4 {
		t.Fatalf("checkMailQueue()=%d want 4", count)
	}
}

func TestEmailNotifierDetectQueueEntryFindsRecipient(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	mockCmdEnv(t, "mailq", `
Mail queue status:
ABC123*  1234 Mon Jan  1 00:00:00 sender@example.com
                                         admin@example.com
DEF456!  4321 Mon Jan  1 00:00:01 sender2@example.com
                                         ops@example.com
`, 0)

	count, entries, err := notifier.checkMailQueueFor(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatalf("checkMailQueueFor() error=%v", err)
	}
	if count != 4 || len(entries) != 1 || entries[0].id != "ABC123" || entries[0].line != "admin@example.com" {
		t.Fatalf("checkMailQueueFor() = %d, %+v; want 4 and ABC123 for admin@example.com", count, entries)
	}
}

func TestEmailNotifierDetectQueueEntryNotFound(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	mockCmdEnv(t, "mailq", "Mail queue is empty", 0)

	count, entries, err := notifier.checkMailQueueFor(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatalf("checkMailQueueFor() error=%v", err)
	}
	if count != 0 || len(entries) != 0 {
		t.Fatalf("checkMailQueueFor() = %d, %+v; want empty", count, entries)
	}
}

func TestEmailNotifierTailMailLogSkipsWorkWhenContextCanceled(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	origMailLogPaths := mailLogPaths
	t.Cleanup(func() { mailLogPaths = origMailLogPaths })

	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "mail.log")
	if err := os.WriteFile(logFile, []byte("postfix/smtp[2]: ABC123: status=sent\n"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	mailLogPaths = []string{logFile}

	mockCmdEnv(t, "tail", "postfix/smtp[2]: ABC123: status=sent", 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lines, logPath := notifier.tailMailLog(ctx, 50)
	if len(lines) != 0 {
		t.Fatalf("tailMailLog() returned lines after context cancellation: %#v", lines)
	}
	if logPath != "" {
		t.Fatalf("tailMailLog() returned log path %q after context cancellation", logPath)
	}
}
