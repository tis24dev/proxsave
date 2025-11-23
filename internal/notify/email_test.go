package notify

import (
	"context"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestNewEmailNotifierValidation(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	_, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: "invalid",
	}, types.ProxmoxBS, logger)
	if err == nil {
		t.Fatal("expected error for invalid delivery method")
	}

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliveryRelay,
		From:           "",
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notifier.config.From != "no-reply@proxmox.tis24.it" {
		t.Fatalf("expected default From, got %s", notifier.config.From)
	}
}

func TestEmailNotifierSendDisabled(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled: false,
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false when disabled")
	}
}
