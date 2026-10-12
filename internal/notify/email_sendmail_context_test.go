package notify

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestEmailNotifierSendmailExpiredContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"cancelled", context.Canceled},
		{"deadline_exceeded", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "sendmail-started")
			t.Setenv("SUPPORT_TEST_SENDMAIL_MARKER", marker)
			original := sendmailBinaryPath
			t.Cleanup(func() { sendmailBinaryPath = original })
			sendmailBinaryPath = writeCmd(t, dir, "sendmail", "#!/bin/sh\n: > \"$SUPPORT_TEST_SENDMAIL_MARKER\"\nexit 0\n")
			// Supply every preflight command from this fixture directory. None may
			// start with an expired context; the real system MTA is never invoked.
			for _, name := range []string{"systemctl", "postconf", "mailq", "tail", "journalctl"} {
				writeCmd(t, dir, name, "#!/bin/sh\nexit 1\n")
			}
			t.Setenv("PATH", dir)
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(io.Discard)
			notifier, err := NewEmailNotifier(EmailConfig{
				Enabled: true, DeliveryMethod: EmailDeliverySendmail,
				Recipient: "maintainer@example.invalid", From: "host@example.invalid",
				SubjectOverride: "SUPPORT REQUEST", AttachLogFile: true,
			}, types.ProxmoxVE, logger)
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if tc.err == context.Canceled {
				ctx, cancel = context.WithCancel(context.Background())
			} else {
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			}
			cancel()
			data := createTestNotificationData()
			data.LogFilePath = filepath.Join(dir, "support.log")
			if err := os.WriteFile(data.LogFilePath, []byte("support test diagnostic\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := notifier.Send(ctx, data)
			if err != nil {
				t.Fatalf("delivery failures should be carried by NotificationResult: %v", err)
			}
			if result == nil || result.Success || !errors.Is(result.Error, tc.err) {
				t.Fatalf("want unsuccessful sendmail result wrapping %v; got %+v", tc.err, result)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sendmail must not start with an expired context; marker stat: %v", err)
			}
		})
	}
}
