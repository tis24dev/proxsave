package support

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

type fakeNotifier struct {
	enabled bool
	sent    int
	last    *notify.NotificationData
	result  *notify.NotificationResult
	err     error
}

func (f *fakeNotifier) Name() string                          { return "fake-email" }
func (f *fakeNotifier) IsEnabled() bool                       { return f.enabled }
func (f *fakeNotifier) IsCritical() bool                      { return false }
func (f *fakeNotifier) Send(ctx context.Context, data *notify.NotificationData) (*notify.NotificationResult, error) {
	f.sent++
	f.last = data
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &notify.NotificationResult{Success: true, Method: "fake", Duration: time.Millisecond}, nil
}

func withStdinFile(t *testing.T, content string) {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "stdin.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = orig })
}

func TestPromptYesNoSupport_InvalidThenYes(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("maybe\ny\n"))
	ok, err := promptYesNoSupport(context.Background(), reader, "prompt: ")
	if err != nil {
		t.Fatalf("promptYesNoSupport error: %v", err)
	}
	if !ok {
		t.Fatalf("ok=%v; want true", ok)
	}
}

func TestRunIntro_DeclinedConsent(t *testing.T) {
	withStdinFile(t, "n\n")
	bootstrap := logging.NewBootstrapLogger()

	meta, ok, interrupted := RunIntro(context.Background(), bootstrap)
	if ok || interrupted {
		t.Fatalf("ok=%v interrupted=%v; want false/false", ok, interrupted)
	}
	if meta.GitHubUser != "" || meta.IssueID != "" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestRunIntro_FullFlowWithRetries(t *testing.T) {
	withStdinFile(t, strings.Join([]string{
		"y",   // accept
		"y",   // has issue
		"",    // empty nickname -> retry
		"user", // nickname
		"abc",  // invalid issue (missing #)
		"#no",  // invalid issue (non-numeric)
		"#123", // valid
		"",
	}, "\n"))
	bootstrap := logging.NewBootstrapLogger()

	meta, ok, interrupted := RunIntro(context.Background(), bootstrap)
	if !ok || interrupted {
		t.Fatalf("ok=%v interrupted=%v; want true/false", ok, interrupted)
	}
	if meta.GitHubUser != "user" {
		t.Fatalf("GitHubUser=%q; want %q", meta.GitHubUser, "user")
	}
	if meta.IssueID != "#123" {
		t.Fatalf("IssueID=%q; want %q", meta.IssueID, "#123")
	}
}

func TestRunIntro_CanceledContextInterrupts(t *testing.T) {
	// Provide at least one line so the read goroutine can complete and exit.
	withStdinFile(t, "y\n")
	bootstrap := logging.NewBootstrapLogger()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ok, interrupted := RunIntro(ctx, bootstrap)
	if ok || !interrupted {
		t.Fatalf("ok=%v interrupted=%v; want false/true", ok, interrupted)
	}
}

func TestBuildSupportStats(t *testing.T) {
	if got := BuildSupportStats(nil, "h", types.ProxmoxVE, "v", "t", time.Time{}, time.Time{}, 0, ""); got != nil {
		t.Fatalf("expected nil when logger is nil")
	}

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "backup.log")
	logger := logging.New(types.LogLevelDebug, false)
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() { _ = logger.CloseLogFile() })

	start := time.Unix(1700000000, 0)
	end := start.Add(10 * time.Second)

	stats := BuildSupportStats(logger, "host", types.ProxmoxBS, "8.0", "1.2.3", start, end, 0, "restore")
	if stats == nil {
		t.Fatalf("expected stats")
	}
	if stats.LocalStatus != "ok" {
		t.Fatalf("LocalStatus=%q; want %q", stats.LocalStatus, "ok")
	}
	if stats.Duration != 10*time.Second {
		t.Fatalf("Duration=%v; want %v", stats.Duration, 10*time.Second)
	}
	if stats.LocalStatusSummary != "Support wrapper mode=restore" {
		t.Fatalf("LocalStatusSummary=%q", stats.LocalStatusSummary)
	}
	if stats.LogFilePath != logPath {
		t.Fatalf("LogFilePath=%q; want %q", stats.LogFilePath, logPath)
	}

	statsErr := BuildSupportStats(logger, "host", types.ProxmoxBS, "8.0", "1.2.3", start, end, 2, "")
	if statsErr.LocalStatus != "error" {
		t.Fatalf("LocalStatus=%q; want %q", statsErr.LocalStatus, "error")
	}
	if statsErr.LocalStatusSummary != "Support wrapper" {
		t.Fatalf("LocalStatusSummary=%q; want %q", statsErr.LocalStatusSummary, "Support wrapper")
	}
}

func TestSendEmail_StatsNilNoop(t *testing.T) {
	SendEmail(context.Background(), &config.Config{}, nil, types.ProxmoxVE, nil, Meta{}, "sig")
}

func TestSendEmail_NewNotifierErrorHandled(t *testing.T) {
	orig := newEmailNotifier
	t.Cleanup(func() { newEmailNotifier = orig })
	newEmailNotifier = func(cfg notify.EmailConfig, proxmoxType types.ProxmoxType, logger *logging.Logger) (notify.Notifier, error) {
		return nil, errors.New("boom")
	}

	logger := logging.New(types.LogLevelDebug, false)
	stats := &orchestrator.BackupStats{ExitCode: 0}
	SendEmail(context.Background(), &config.Config{}, logger, types.ProxmoxVE, stats, Meta{}, "")
}

func TestSendEmail_SubjectCompositionAndSend(t *testing.T) {
	orig := newEmailNotifier
	t.Cleanup(func() { newEmailNotifier = orig })

	var captured notify.EmailConfig
	fake := &fakeNotifier{enabled: true}
	newEmailNotifier = func(cfg notify.EmailConfig, proxmoxType types.ProxmoxType, logger *logging.Logger) (notify.Notifier, error) {
		captured = cfg
		return fake, nil
	}

	logger := logging.New(types.LogLevelDebug, false)
	stats := &orchestrator.BackupStats{
		ExitCode:    0,
		Hostname:    "host",
		ArchivePath: "/tmp/a.tar",
	}
	cfg := &config.Config{EmailFrom: "from@example.com"}

	SendEmail(context.Background(), cfg, logger, types.ProxmoxVE, stats, Meta{GitHubUser: " alice ", IssueID: " #123 "}, " sig ")

	if captured.Recipient != "github-support@tis24.it" {
		t.Fatalf("Recipient=%q", captured.Recipient)
	}
	if captured.From != "from@example.com" {
		t.Fatalf("From=%q", captured.From)
	}
	wantSubject := "SUPPORT REQUEST - Nickname: alice - Issue: #123 - Build: sig"
	if captured.SubjectOverride != wantSubject {
		t.Fatalf("SubjectOverride=%q; want %q", captured.SubjectOverride, wantSubject)
	}
	if !captured.AttachLogFile || !captured.Enabled {
		t.Fatalf("expected AttachLogFile and Enabled true")
	}
	if fake.sent != 1 || fake.last == nil {
		t.Fatalf("expected fake notifier to be called once")
	}
}
