package notify

import (
	"bytes"
	"io"
	"os"
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

	toolDir := t.TempDir()
	writeCmd(t, toolDir, "tail", "#!/bin/sh\nset -eu\ncat \"$3\"\n")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+origPath)

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

func TestEmailNotifierCheckRecentMailLogsDetectsErrors(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "mail.log")

	content := strings.Join([]string{
		"ok line",
		"postfix/smtp[2]: something failed due to timeout",
		"postfix/smtp[2]: connection refused by remote",
		"postfix/smtp[2]: status=deferred (host not found)",
	}, "\n") + "\n"
	if err := os.WriteFile(logFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	origPaths := mailLogPaths
	t.Cleanup(func() { mailLogPaths = origPaths })
	mailLogPaths = []string{logFile}

	toolDir := t.TempDir()
	writeCmd(t, toolDir, "tail", "#!/bin/sh\nset -eu\ncat \"$3\"\n")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+origPath)

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	lines := notifier.checkRecentMailLogs()
	if len(lines) < 3 {
		t.Fatalf("expected >=3 error-like lines, got %d: %#v", len(lines), lines)
	}
}

func TestInspectMailLogStatus_Variants(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "mail.log")

	content := strings.Join([]string{
		"postfix/smtp[2]: QSENT: status=sent (250 ok)",
		"postfix/smtp[2]: QDEFER: status=deferred (timeout)",
		"postfix/smtp[2]: QBOUNCE: status=bounced (550 no)",
		"postfix/smtp[2]: QEXP: status=expired (delivery timed out)",
		"postfix/smtp[2]: QREJ: rejected by policy",
		"postfix/smtp[2]: QERR: connection refused",
		"postfix/smtp[2]: QUNK: some other line",
		"postfix/smtp[2]: status=sent (no queue id here)",
	}, "\n") + "\n"
	if err := os.WriteFile(logFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	origPaths := mailLogPaths
	t.Cleanup(func() { mailLogPaths = origPaths })
	mailLogPaths = []string{logFile}

	toolDir := t.TempDir()
	writeCmd(t, toolDir, "tail", "#!/bin/sh\nset -eu\ncat \"$3\"\n")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+origPath)

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	tests := []struct {
		name    string
		queueID string
		want    string
	}{
		{name: "sent", queueID: "QSENT", want: "sent"},
		{name: "deferred", queueID: "QDEFER", want: "deferred"},
		{name: "bounced", queueID: "QBOUNCE", want: "bounced"},
		{name: "expired", queueID: "QEXP", want: "expired"},
		{name: "rejected", queueID: "QREJ", want: "rejected"},
		{name: "error", queueID: "QERR", want: "error"},
		{name: "unknown", queueID: "QUNK", want: "unknown"},
		{name: "filter fallback uses whole log", queueID: "MISSING", want: "sent"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			status, matched, usedPath := notifier.inspectMailLogStatus(tt.queueID)
			if status != tt.want {
				t.Fatalf("status=%q want %q (matched=%q)", status, tt.want, matched)
			}
			if usedPath != logFile {
				t.Fatalf("logPath=%q want %q", usedPath, logFile)
			}
			if strings.TrimSpace(matched) == "" {
				t.Fatalf("expected matched line to be non-empty")
			}
		})
	}
}

func TestLogMailLogStatus_EmitsDetailsWhenNotDebug(t *testing.T) {
	t.Run("early return on empty inputs", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&buf)

		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}

		notifier.logMailLogStatus("", "", "ignored", "/var/log/mail.log")
		if buf.Len() != 0 {
			t.Fatalf("expected no output for empty queueID/status, got:\n%s", buf.String())
		}
	})

	t.Run("emits details at info for non-sent", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&buf)

		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}

		longLine := strings.Repeat("x", 260)
		notifier.logMailLogStatus("ABC123", "deferred", longLine, "/var/log/mail.log")

		out := buf.String()
		if !strings.Contains(out, "status=deferred") {
			t.Fatalf("expected output to mention deferred status, got:\n%s", out)
		}
		if !strings.Contains(out, "Details:") {
			t.Fatalf("expected output to include Details line when not debug, got:\n%s", out)
		}
		if !strings.Contains(out, "ABC123") {
			t.Fatalf("expected output to include queue ID, got:\n%s", out)
		}
	})

	t.Run("sent omits details at info", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&buf)

		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}

		notifier.logMailLogStatus("ABC123", "sent", "line", "/var/log/mail.log")
		out := buf.String()
		if !strings.Contains(out, "status=sent") {
			t.Fatalf("expected sent status message, got:\n%s", out)
		}
		if strings.Contains(out, "Details:") {
			t.Fatalf("did not expect Details for sent status, got:\n%s", out)
		}
	})

	t.Run("pending status when status empty", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&buf)

		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}

		notifier.logMailLogStatus("ABC123", "", "", "/var/log/mail.log")
		out := buf.String()
		if !strings.Contains(out, "delivery status pending") {
			t.Fatalf("expected pending status message, got:\n%s", out)
		}
	})

	t.Run("debug level emits raw log entry", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&buf)

		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}

		notifier.logMailLogStatus("ABC123", "error", "line", "/var/log/mail.log")
		out := buf.String()
		if !strings.Contains(out, "Mail log entry: line") {
			t.Fatalf("expected debug log entry output, got:\n%s", out)
		}
	})

	t.Run("unknown status falls through and still logs entry", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&buf)

		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}

		notifier.logMailLogStatus("", "weird", "line", "/var/log/mail.log")
		out := buf.String()
		if !strings.Contains(out, "Mail log entry: line") {
			t.Fatalf("expected log entry output for unknown status, got:\n%s", out)
		}
	})
}
