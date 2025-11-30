package notify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
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

func TestEmailNotifierBasicAccessors(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliveryRelay,
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name := notifier.Name(); name != "Email" {
		t.Fatalf("Name()=%s want Email", name)
	}
	if !notifier.IsEnabled() {
		t.Fatalf("IsEnabled() should be true when enabled")
	}
	if notifier.IsCritical() {
		t.Fatalf("IsCritical() should always be false")
	}

	notifier.config.Enabled = false
	if notifier.IsEnabled() {
		t.Fatalf("IsEnabled() should reflect config changes")
	}
}

func TestDescribeEmailMethod(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"email-relay", "cloud relay"},
		{"email-sendmail", "sendmail"},
		{"email-sendmail-fallback", "sendmail fallback"},
		{"custom", "custom"},
	}
	for _, tt := range tests {
		if got := describeEmailMethod(tt.method); got != tt.want {
			t.Fatalf("describeEmailMethod(%s)=%s want %s", tt.method, got, tt.want)
		}
	}
}

func TestEmailNotifierDetectRecipientPVE(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{}, types.ProxmoxVE, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}
	mockCmdEnv(t, "pveum", `[{"userid":"root@pam","email":"root@example.com"}]`, 0)

	recipient, err := notifier.detectRecipient(context.Background())
	if err != nil {
		t.Fatalf("detectRecipient() error = %v", err)
	}
	if recipient != "root@example.com" {
		t.Fatalf("detectRecipient()=%s want root@example.com", recipient)
	}
}

func TestEmailNotifierDetectRecipientPBSNoEmail(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}
	mockCmdEnv(t, "proxmox-backup-manager", `[{"userid":"root@pam","email":""}]`, 0)

	if _, err := notifier.detectRecipient(context.Background()); err == nil {
		t.Fatalf("detectRecipient() should fail when root has no email")
	}
}

func TestEmailNotifierDetectRecipientUnknownType(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{}, types.ProxmoxUnknown, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}
	if _, err := notifier.detectRecipient(context.Background()); err == nil {
		t.Fatalf("detectRecipient() should fail for unknown type")
	}
}

func mockCmdEnv(t *testing.T, name, output string, exitCode int) {
	t.Helper()

	dir := t.TempDir()
	script := "#!/bin/sh\n"
	if exitCode == 0 {
		script += "cat <<'EOF'\n" + output + "\nEOF\n"
	} else {
		script += "exit " + fmt.Sprintf("%d", exitCode) + "\n"
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock command: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)
}
