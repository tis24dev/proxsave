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

func TestEmailNotifier_SendSendmail_FailsWhenSendmailMissing(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	origSendmailPath := sendmailBinaryPath
	sendmailBinaryPath = filepath.Join(t.TempDir(), "missing-sendmail")
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
	if result.Success {
		t.Fatalf("expected Success=false when sendmail missing")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "sendmail not found") {
		t.Fatalf("expected sendmail not found error, got %v", result.Error)
	}
}

func TestEmailNotifier_SendSendmail_ReturnsErrorWhenSendmailCommandFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	dir := t.TempDir()
	sendmailPath := writeCmd(t, dir, "sendmail", `#!/bin/sh
set -eu
cat >/dev/null
echo "warning: simulated failure" >&2
exit 1
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
	if result.Success {
		t.Fatalf("expected Success=false when sendmail command fails")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "sendmail failed") {
		t.Fatalf("expected sendmail failed error, got %v", result.Error)
	}
}

func TestEmailNotifier_SendSendmail_DetectsQueueIDFromMailQueue(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	origPaths := mailLogPaths
	t.Cleanup(func() { mailLogPaths = origPaths })

	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "mail.log")
	mailLogPaths = []string{logFile}
	if err := os.WriteFile(logFile, []byte("postfix/smtp[2]: ABC123: status=deferred (timeout)\n"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	toolsDir := t.TempDir()
	sendmailPath := writeCmd(t, toolsDir, "sendmail", `#!/bin/sh
set -eu
cat >/dev/null
exit 0
`)
	countFile := filepath.Join(toolsDir, "mailq.count")
	t.Setenv("MAILQ_COUNT_FILE", countFile)
	writeCmd(t, toolsDir, "mailq", `#!/bin/sh
set -eu
count_file="${MAILQ_COUNT_FILE}"
n=0
if [ -f "$count_file" ]; then n=$(cat "$count_file"); fi
n=$((n+1))
echo "$n" > "$count_file"
if [ "$n" -eq 1 ]; then
  echo "Mail queue is empty"
  exit 0
fi
cat <<'EOF'
Mail queue status:
ABC123*  1234 Mon Jan  1 00:00:00 sender@example.com
                                         admin@example.com
-- 1 Kbytes in 1 Requests.
EOF
exit 0
`)
	writeCmd(t, toolsDir, "tail", "#!/bin/sh\nset -eu\ncat \"$3\"\n")
	writeCmd(t, toolsDir, "journalctl", "#!/bin/sh\nexit 0\n")
	writeCmd(t, toolsDir, "systemctl", "#!/bin/sh\nexit 3\n")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", toolsDir+string(os.PathListSeparator)+origPath)

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
	if got, ok := result.Metadata["mail_queue_id"].(string); !ok || got != "ABC123" {
		t.Fatalf("expected mail_queue_id=ABC123, got %#v", result.Metadata["mail_queue_id"])
	}
}
