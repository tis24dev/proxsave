package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestExtractQueueID(t *testing.T) {
	if got := extractQueueID("", "queued as 1234ABCD123"); got != "1234ABCD123" {
		t.Fatalf("extractQueueID()=%q want %q", got, "1234ABCD123")
	}

	if got := extractQueueID("no match here"); got != "" {
		t.Fatalf("extractQueueID()=%q want empty", got)
	}

	if got := extractQueueID("queued as first", "queued as second"); got != "first" {
		t.Fatalf("extractQueueID()=%q want %q", got, "first")
	}
}

func TestSummarizeSendmailTranscript(t *testing.T) {
	transcript := strings.Join([]string{
		"Connecting to 127.0.0.1 via relay",
		"Connecting to mx.example.com via esmtp",
		"Recipient ok",
		"Sent (OK id=remote-123)",
		"Sent (LOCALQ123 Message accepted for delivery)",
		"Closing connection",
	}, "\n")

	highlights, remoteID, localQueueID := summarizeSendmailTranscript(transcript)
	if remoteID != "remote-123" {
		t.Fatalf("remoteID=%q want %q", remoteID, "remote-123")
	}
	if localQueueID != "LOCALQ123" {
		t.Fatalf("localQueueID=%q want %q", localQueueID, "LOCALQ123")
	}
	if len(highlights) == 0 {
		t.Fatalf("expected non-empty highlights")
	}
	joined := strings.ToLower(strings.Join(highlights, "\n"))
	for _, needle := range []string{"local relay connection", "remote relay connection", "recipient accepted", "queued message", "smtp session closed"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("expected highlights to contain %q; got:\n%s", needle, strings.Join(highlights, "\n"))
		}
	}
}

func TestInspectMailLogStatus(t *testing.T) {
	if _, err := exec.LookPath("tail"); err != nil {
		t.Skip("tail not available in PATH")
	}

	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "mail.log")

	queueID := "ABC123DEF"
	content := strings.Join([]string{
		"unrelated line",
		"postfix/qmgr[1]: " + queueID + ": from=<root@host>, size=123, nrcpt=1 (queue active)",
		"postfix/smtp[2]: " + queueID + ": to=<admin@example.com>, relay=mx.example.com[1.2.3.4]:25, delay=0.1, delays=0.01/0.01/0.05/0.03, dsn=2.0.0, status=sent (250 ok)",
	}, "\n") + "\n"

	if err := os.WriteFile(logFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	origPaths := mailLogPaths
	t.Cleanup(func() { mailLogPaths = origPaths })
	mailLogPaths = []string{logFile}

	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	status, matchedLine, usedPath := notifier.inspectMailLogStatus(queueID)
	if status != "sent" {
		t.Fatalf("status=%q want %q (matchedLine=%q)", status, "sent", matchedLine)
	}
	if usedPath != logFile {
		t.Fatalf("logPath=%q want %q", usedPath, logFile)
	}
	if !strings.Contains(matchedLine, "status=sent") {
		t.Fatalf("matchedLine=%q want to contain status=sent", matchedLine)
	}
}
