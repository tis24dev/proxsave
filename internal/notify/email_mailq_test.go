package notify

import (
	"context"
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

	queueID, line, err := notifier.detectQueueEntry(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatalf("detectQueueEntry() error=%v", err)
	}
	if queueID != "ABC123" {
		t.Fatalf("queueID=%q want %q", queueID, "ABC123")
	}
	if line == "" {
		t.Fatalf("expected a matched line for recipient")
	}
}

func TestEmailNotifierDetectQueueEntryNotFound(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	mockCmdEnv(t, "mailq", "Mail queue is empty", 0)

	queueID, line, err := notifier.detectQueueEntry(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatalf("detectQueueEntry() error=%v", err)
	}
	if queueID != "" || line != "" {
		t.Fatalf("detectQueueEntry()=(%q,%q) want empty", queueID, line)
	}
}
