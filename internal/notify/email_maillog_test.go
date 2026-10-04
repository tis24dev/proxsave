package notify

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNewMessageID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := newMessageID("no-reply@proxmox.example.com")
		local, domain, ok := strings.Cut(id, "@")
		if !ok || domain != "proxmox.example.com" || len(local) != len("proxsave-")+32 || !strings.HasPrefix(local, "proxsave-") {
			t.Fatalf("Message-ID %q, want proxsave-<32 hex>@proxmox.example.com", id)
		}
		if seen[id] {
			t.Fatalf("Message-ID %q repeated", id)
		}
		seen[id] = true
	}
	for from, want := range map[string]string{
		"no-reply@proxmox.example.com":         "proxmox.example.com",
		"ProxSave <alerts@backup.example.org>": "backup.example.org",
		"root":                                 "localhost",
		"broken@":                              "localhost",
		"":                                     "localhost",
	} {
		if got := messageIDDomain(from); got != want {
			t.Fatalf("messageIDDomain(%q) = %q, want %q", from, got, want)
		}
	}
}

// The three log forms: postfix (the pve-test lines), and sendmail and exim lines in
// their documented formats.
func TestQueueIDForMessageID(t *testing.T) {
	const id = "proxsave-0123abcd@pve.example.lan"
	for _, tc := range []struct {
		name, line, want string
	}{
		{"postfix journal", "2026-10-04T14:22:38+02:00 pve postfix/cleanup[445657]: 8D8944D348: message-id=<" + id + ">", "8D8944D348"},
		{"postfix syslog", "Oct  4 14:22:38 pve postfix/cleanup[445657]: 8D8944D348: message-id=<" + id + ">", "8D8944D348"},
		{"sendmail", "Oct  4 14:22:38 pve sm-mta[1234]: 494CMcXQ001234: from=<root@pve.example.lan>, size=15201, class=0, nrcpts=1, msgid=<" + id + ">, proto=ESMTP, relay=localhost [127.0.0.1]", "494CMcXQ001234"},
		{"exim", "2026-10-04 14:22:38 1rAbCd-000Xyz-Ef <= root@pve.example.lan U=root P=local S=15201 id=" + id, "1rAbCd-000Xyz-Ef"},
		{"exim with pid", "2026-10-04 14:22:38 [4321] 1rAbCd-000Xyz-Ef <= root@pve.example.lan U=root P=local S=15201 id=" + id, "1rAbCd-000Xyz-Ef"},
		{"another message-id", "2026-10-04T14:22:38+02:00 pve postfix/cleanup[445657]: 8D8944D348: message-id=<other@pve.example.lan>", ""},
		{"a longer id", "2026-10-04T14:22:38+02:00 pve postfix/cleanup[445657]: 8D8944D348: message-id=<x" + id + ">", ""},
		{"no syslog tag", "8D8944D348: message-id=<" + id + ">", ""},
	} {
		if got := queueIDForMessageID(tc.line, id); got != tc.want {
			t.Fatalf("%s: queue ID %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := queueIDForMessageID("pve postfix/cleanup[1]: 8D8944D348: message-id=<>", ""); got != "" {
		t.Fatalf("empty Message-ID matched %q", got)
	}
}

// The status of the message's own lines, in the three forms; a mention never counts.
func TestMailLogLineStatusForms(t *testing.T) {
	for _, tc := range []struct {
		name, line, queueID, want string
		names                     bool
	}{
		{"postfix sent", "pve postfix/smtp[1]: 2A91F4D329: to=<admin@example.com>, relay=mx.example.com[192.0.2.25]:25, dsn=2.0.0, status=sent (250 OK)", "2A91F4D329", "sent", true},
		{"postfix bounced", "pve postfix/smtp[1]: 8D8944D348: to=<admin@example.com>, dsn=5.0.0, status=bounced (550)", "8D8944D348", "bounced", true},
		{"postfix mention", "pve postfix/bounce[1]: 8D8944D348: sender non-delivery notification: 256914D34B", "256914D34B", "", false},
		{"sendmail sent", "pve sm-mta[1236]: 494CMcXQ001234: to=<admin@example.com>, delay=00:00:01, mailer=esmtp, dsn=2.0.0, stat=Sent (OK id=1xDLsO)", "494CMcXQ001234", "sent", true},
		{"sendmail deferred", "pve sm-mta[1236]: 494CMcXQ001234: to=<admin@example.com>, dsn=4.0.0, stat=Deferred: Connection refused by mx.example.com.", "494CMcXQ001234", "deferred", true},
		{"sendmail bounced", "pve sm-mta[1236]: 494CMcXQ001234: to=<admin@example.com>, dsn=5.1.1, stat=User unknown", "494CMcXQ001234", "bounced", true},
		{"exim delivered", "2026-10-04 14:22:39 1rAbCd-000Xyz-Ef => admin@example.com R=dnslookup T=remote_smtp H=mx.example.com [192.0.2.25] C=\"250 OK\"", "1rAbCd-000Xyz-Ef", "sent", true},
		{"exim bounced", "2026-10-04 14:22:39 1rAbCd-000Xyz-Ef ** admin@example.com R=dnslookup T=remote_smtp: SMTP error from remote mail server after RCPT TO: 550 Sender verify failed", "1rAbCd-000Xyz-Ef", "bounced", true},
		{"exim deferred", "2026-10-04 14:22:39 1rAbCd-000Xyz-Ef == admin@example.com R=dnslookup T=remote_smtp defer (-44): SMTP error", "1rAbCd-000Xyz-Ef", "deferred", true},
		{"exim completed", "2026-10-04 14:22:39 1rAbCd-000Xyz-Ef Completed", "1rAbCd-000Xyz-Ef", "", true},
		{"exim mention", "2026-10-04 14:22:39 1rZzZz-000Aaa-Bb <= <> R=1rAbCd-000Xyz-Ef U=Debian-exim", "1rAbCd-000Xyz-Ef", "", false},
	} {
		if got := mailLogLineNamesQueueID(tc.line, tc.queueID); got != tc.names {
			t.Fatalf("%s: names the queue ID = %v, want %v", tc.name, got, tc.names)
		}
		if !tc.names {
			continue
		}
		if got := mailLogLineStatus(tc.line, tc.queueID); got != tc.want {
			t.Fatalf("%s: status %q, want %q", tc.name, got, tc.want)
		}
	}
}

// installMailLogFixture makes the pve-test mail log (anonymised) the mail log the
// notifier reads.
func installMailLogFixture(t *testing.T, fixture string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "mail_log", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	logFile := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(logFile, raw, 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	origPaths := mailLogPaths
	t.Cleanup(func() { mailLogPaths = origPaths })
	mailLogPaths = []string{logFile}
	toolDir := t.TempDir()
	writeCmd(t, toolDir, "tail", "#!/bin/sh\nset -eu\ncat \"$3\"\n")
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The pve-test runs (2026-10-04): the Message-ID postfix gave ProxSave's email in each
// run (ProxSave set none then) finds that email's queue ID and status, never those of
// the bounce notices proxmox-mail-forward resubmitted to the same recipient.
func TestFindQueueIDByMessageIDOnTheLiveMailLogs(t *testing.T) {
	for _, tc := range []struct {
		fixture, messageID, queueID, status string
	}{
		{"pve-test-case2.log", "20261004122238.8D8944D348@pve.example.lan", "8D8944D348", "bounced"},
		{"pve-test-case3.log", "20261004122344.1D5074D35E@pve.example.lan", "1D5074D35E", "bounced"},
		{"pve-test-case4.log", "20261004122447.72FB64D36E@pve.example.lan", "72FB64D36E", "bounced"},
		// The notice D4E174D371 resubmitted by proxmox-mail-forward keeps its
		// Message-ID: the later hop, E43EB4D372, is where it went next.
		{"pve-test-case4.log", "20261004122447.D4E174D371@pve.example.lan", "E43EB4D372", "bounced"},
		{"pve-test-sent-case1.log", "20261004130335.2A91F4D329@pve.example.lan", "2A91F4D329", "sent"},
		{"pve-test-sent-case2.log", "20261004130534.8AE014D338@pve.example.lan", "8AE014D338", "sent"},
		{"pve-test-sent-case3.log", "20261004130706.EC2224D34B@pve.example.lan", "EC2224D34B", "sent"},
		{"pve-test-case2.log", "proxsave-0123abcd@pve.example.lan", "", ""},
	} {
		t.Run(tc.fixture+" "+tc.messageID, func(t *testing.T) {
			installMailLogFixture(t, tc.fixture)
			var out bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&out)
			notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxVE, logger)
			if err != nil {
				t.Fatalf("NewEmailNotifier() error=%v", err)
			}
			queueID, readable := notifier.findQueueIDByMessageID(context.Background(), tc.messageID)
			if queueID != tc.queueID || !readable {
				t.Fatalf("queue ID %q readable=%v, want %q\n%s", queueID, readable, tc.queueID, out.String())
			}
			if tc.queueID == "" {
				if !strings.Contains(out.String(), "mail log: message-id not found in 29 lines") {
					t.Fatalf("missing the not-found line:\n%s", out.String())
				}
				return
			}
			if !strings.Contains(out.String(), "mail log: source=") || !strings.Contains(out.String(), "message_id=<"+tc.messageID+">") ||
				!strings.Contains(out.String(), "mail log: message-id found queue_id="+tc.queueID+" line=") {
				t.Fatalf("missing the approved DEBUG lines:\n%s", out.String())
			}
			status, _, _ := notifier.inspectMailLogStatus(context.Background(), queueID)
			if status != tc.status {
				t.Fatalf("status %q, want %q", status, tc.status)
			}
		})
	}
}

// The whole sendmail flow on the case 2 mail log: ProxSave's email is found by its own
// Message-ID and reported bounced; the bounce notice proxmox-mail-forward resubmitted to
// the same recipient, the one entry left in the mail queue, is never printed.
func TestSendViaSendmailReportsItsOwnMessageOnTheLiveMailLog(t *testing.T) {
	installMailLogFixture(t, "pve-test-case2.log")
	origMessageID := newMessageID
	newMessageID = func(string) string { return "20261004122238.8D8944D348@pve.example.lan" }
	t.Cleanup(func() { newMessageID = origMessageID })

	toolsDir := t.TempDir()
	sendmailPath := writeCmd(t, toolsDir, "sendmail", "#!/bin/sh\ncat >/dev/null\necho 'Mail Delivery Status Report will be mailed to <root>.'\nexit 0\n")
	countFile := filepath.Join(toolsDir, "mailq.count")
	t.Setenv("MAILQ_COUNT_FILE", countFile)
	writeCmd(t, toolsDir, "mailq", `#!/bin/sh
n=0
[ -f "$MAILQ_COUNT_FILE" ] && n=$(cat "$MAILQ_COUNT_FILE")
n=$((n+1))
echo "$n" > "$MAILQ_COUNT_FILE"
if [ "$n" -eq 1 ]; then echo "Mail queue is empty"; exit 0; fi
cat <<'EOF'
-Queue ID-  --Size-- ----Arrival Time---- -Sender/Recipient-------
444804D348    17916 Sun Oct  4 14:22:39  root@pve.example.lan
                                         admin@example.com

-- 17 Kbytes in 1 Request.
EOF
`)
	writeCmd(t, toolsDir, "journalctl", "#!/bin/sh\nexit 0\n")
	writeCmd(t, toolsDir, "systemctl", "#!/bin/sh\nexit 3\n")
	t.Setenv("PATH", toolsDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	origSendmailPath := sendmailBinaryPath
	sendmailBinaryPath = sendmailPath
	t.Cleanup(func() { sendmailBinaryPath = origSendmailPath })

	for _, level := range []types.LogLevel{types.LogLevelInfo, types.LogLevelDebug} {
		if err := os.Remove(countFile); err != nil && !os.IsNotExist(err) {
			t.Fatalf("reset mailq count: %v", err)
		}
		var out bytes.Buffer
		logger := logging.New(level, false)
		logger.SetOutput(&out)
		notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail, Recipient: "admin@example.com"}, types.ProxmoxVE, logger)
		if err != nil {
			t.Fatalf("NewEmailNotifier() error=%v", err)
		}
		result, err := notifier.Send(context.Background(), createTestNotificationData())
		if err != nil || !result.Success {
			t.Fatalf("Send: %+v %v", result, err)
		}
		text := out.String()
		if !strings.Contains(text, "reports status=bounced for queue ID 8D8944D348") {
			t.Fatalf("level %v: ProxSave's message not reported:\n%s", level, text)
		}
		// No line attributes another message to this email. (The DEBUG dump of recent
		// error-like mail log lines, any message's, is not an attribution.)
		if strings.Contains(text, "Detected queue ID") {
			t.Fatalf("level %v: a mail queue ID was printed:\n%s", level, text)
		}
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "queue ID") && !strings.Contains(line, "queue_id=") {
				continue
			}
			for _, other := range []string{"444804D348", "29C324D347", "256914D34B", "459A54D34A", "47E924D348"} {
				if strings.Contains(line, other) {
					t.Fatalf("level %v: %q attributed to this email: %s", level, other, line)
				}
			}
		}
		if got, _ := result.Metadata["mail_queue_id"].(string); got != "8D8944D348" {
			t.Fatalf("mail_queue_id = %q, want 8D8944D348", got)
		}
		if level == types.LogLevelDebug {
			for _, want := range []string{
				"email message-id: <20261004122238.8D8944D348@pve.example.lan>",
				"mail log: message-id found queue_id=8D8944D348 line=",
				"mail log: queue_id=8D8944D348 status=bounced line=",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing DEBUG %q:\n%s", want, text)
				}
			}
		}
	}
}

// While the mail log has no line of the email yet, the one new mail queue entry for the
// recipient is it; two new entries, or no listing before sending, and no ID is given.
func TestNewQueueEntryFor(t *testing.T) {
	queue := `-Queue ID-  --Size-- ----Arrival Time---- -Sender/Recipient-------
AAAAA11111    15201 Sun Oct  4 14:22:38  root@pve.example.lan
                                         admin@example.com

BBBBB22222    15100 Sun Oct  4 14:22:38  root@pve.example.lan
                                         admin@example.com

-- 30 Kbytes in 2 Requests.
`
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxVE, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}
	mockCmdEnv(t, "mailq", queue, 0)
	for _, tc := range []struct {
		name   string
		before map[string]bool
		want   string
	}{
		{"one new entry", map[string]bool{"AAAAA11111": true}, "BBBBB22222"},
		{"two new entries", map[string]bool{}, ""},
		{"no listing before", nil, ""},
		{"nothing new", map[string]bool{"AAAAA11111": true, "BBBBB22222": true}, ""},
	} {
		if got := notifier.newQueueEntryFor(context.Background(), "admin@example.com", tc.before); got != tc.want {
			t.Fatalf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
