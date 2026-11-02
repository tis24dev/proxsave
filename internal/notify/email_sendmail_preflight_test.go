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

// The pre-flight checks before a sendmail send guess from a fixed list of services
// and config files. A working stock Debian exim4 (only exim4.conf.template ships) or a
// daemonless MTA (msmtp-mta, dma) reads as missing, and a WARNING there turned a
// delivered email into a run that exits 1. They are DEBUG only.
func TestSendmailPreflightGuessesWarnNothingOnADeliveredEmail(t *testing.T) {
	cases := []struct {
		name      string
		systemctl string
	}{
		{"exim4 active", "#!/bin/sh\nif [ \"$1\" = \"is-active\" ] && [ \"$2\" = \"exim4\" ]; then exit 0; fi\nexit 3\n"},
		{"daemonless MTA", "#!/bin/sh\nexit 3\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			capturePath := filepath.Join(t.TempDir(), "delivered.eml")
			t.Setenv("SENDMAIL_CAPTURE_PATH", capturePath)
			sendmailPath := writeCmd(t, dir, "sendmail", "#!/bin/sh\ncat > \"${SENDMAIL_CAPTURE_PATH}\"\nexit 0\n")
			writeCmd(t, dir, "mailq", "#!/bin/sh\necho \"Mail queue is empty\"\nexit 0\n")
			writeCmd(t, dir, "tail", "#!/bin/sh\nexit 0\n")
			writeCmd(t, dir, "journalctl", "#!/bin/sh\nexit 0\n")
			writeCmd(t, dir, "systemctl", tc.systemctl)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			orig := sendmailBinaryPath
			sendmailBinaryPath = sendmailPath
			t.Cleanup(func() { sendmailBinaryPath = orig })

			var out strings.Builder
			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(&out)
			n, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail,
				Recipient: "admin@example.com", From: "no-reply@proxmox.example.com"}, types.ProxmoxVE, logger)
			if err != nil {
				t.Fatalf("NewEmailNotifier: %v", err)
			}
			res, err := n.Send(context.Background(), createTestNotificationData())
			if err != nil || res == nil || !res.Success {
				t.Fatalf("Send: err=%v res=%+v", err, res)
			}
			if b, err := os.ReadFile(capturePath); err != nil || !strings.Contains(string(b), "To: admin@example.com") {
				t.Fatalf("message not handed to the MTA: err=%v", err)
			}
			if got := logger.WarningCount(); got != 0 {
				t.Fatalf("WarningCount=%d want 0 for a delivered email; output:\n%s", got, out.String())
			}
		})
	}
}
