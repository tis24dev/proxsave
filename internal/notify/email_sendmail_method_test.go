package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func writeCmd(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestEmailNotifier_SendSendmail_UsesConfiguredSendmailBinary(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	dir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "sendmail_capture.txt")
	t.Setenv("SENDMAIL_CAPTURE_PATH", capturePath)

	sendmailPath := writeCmd(t, dir, "sendmail", `#!/bin/sh
set -eu
cat > "${SENDMAIL_CAPTURE_PATH}"
echo "queued as TESTQID123"
exit 0
`)
	writeCmd(t, dir, "mailq", "#!/bin/sh\necho \"Mail queue is empty\"\nexit 0\n")
	writeCmd(t, dir, "tail", "#!/bin/sh\nexit 0\n")
	writeCmd(t, dir, "journalctl", "#!/bin/sh\nexit 0\n")
	writeCmd(t, dir, "systemctl", "#!/bin/sh\nexit 3\n")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	origSendmailPath := sendmailBinaryPath
	sendmailBinaryPath = sendmailPath
	t.Cleanup(func() { sendmailBinaryPath = origSendmailPath })

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliverySendmail,
		Recipient:      "admin@example.com",
		From:           "no-reply@proxmox.example.com",
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true, got false (err=%v)", result.Error)
	}
	if result.Method != "email-sendmail" {
		t.Fatalf("expected Method=email-sendmail, got %q", result.Method)
	}
	if got, ok := result.Metadata["mail_queue_id"].(string); !ok || got != "TESTQID123" {
		t.Fatalf("expected mail_queue_id=TESTQID123, got %#v", result.Metadata["mail_queue_id"])
	}
	if got, ok := result.Metadata["email_backend_path"].(string); !ok || strings.TrimSpace(got) != sendmailPath {
		t.Fatalf("expected email_backend_path=%q, got %#v", sendmailPath, result.Metadata["email_backend_path"])
	}

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read sendmail capture: %v", err)
	}
	msg := string(got)
	if !strings.Contains(msg, "To: admin@example.com\n") {
		t.Fatalf("expected To: admin@example.com header, got:\n%s", msg)
	}
}
