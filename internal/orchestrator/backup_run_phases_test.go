package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCleanupBackupWorkspaceRemovesAndDeregisters(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	orch := New(logger, false)

	reg, err := NewTempDirRegistry(logger, filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}

	tempDir := t.TempDir()
	// Represent plaintext staged secrets that must not survive a finished run.
	if err := os.WriteFile(filepath.Join(tempDir, "shadow"), []byte("hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tempDir); err != nil {
		t.Fatalf("register: %v", err)
	}

	orch.cleanupBackupWorkspace(&backupWorkspace{registry: reg, fs: osFS{}, tempDir: tempDir})

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("workspace must be removed when the run finishes (issue #53), stat err=%v", err)
	}
	entries, err := reg.loadEntries()
	if err != nil {
		t.Fatalf("loadEntries: %v", err)
	}
	for _, e := range entries {
		if e.Path == tempDir {
			t.Fatalf("workspace must be deregistered after removal; still present in %+v", entries)
		}
	}
}

func TestCreateBackupArchiveClassifiesAgeRecipientFailureAsEncryption(t *testing.T) {
	orch := New(newTestLogger(), false)
	orch.SetConfig(&config.Config{
		EncryptArchive: true,
		BaseDir:        t.TempDir(),
	})
	orch.SetBackupConfig(t.TempDir(), t.TempDir(), types.CompressionNone, 0, 0, "standard", nil)

	run := orch.newBackupRunContext(context.Background(), nil, "test-host")
	_, err := orch.createBackupArchive(run, &backupWorkspace{tempDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected createBackupArchive error")
	}

	var backupErr *BackupError
	if !errors.As(err, &backupErr) {
		t.Fatalf("expected BackupError, got %T: %v", err, err)
	}
	if backupErr.Phase != "encryption" {
		t.Fatalf("Phase=%q; want encryption", backupErr.Phase)
	}
	if backupErr.Code != types.ExitEncryptionError {
		t.Fatalf("Code=%v; want %v", backupErr.Code, types.ExitEncryptionError)
	}
}

// F11-02: finalizeFailedBackupStats marks a genuine backup failure (runErr != nil) as
// Failed, so the status gauge reports error even when only warnings were counted. A nil
// runErr (a run whose only issue was a non-fatal notification/communication error, which
// never sets runErr) must NOT set Failed, so such a run is never escalated to error.
func TestFinalizeFailedBackupStats_SetsFailedOnlyOnGenuineFailure(t *testing.T) {
	orch := New(newTestLogger(), false)

	failRun := &backupRunContext{stats: &BackupStats{}}
	orch.finalizeFailedBackupStats(failRun, errors.New("archive phase failed"))
	if !failRun.stats.Failed {
		t.Fatal("a non-nil runErr must set stats.Failed")
	}

	okRun := &backupRunContext{stats: &BackupStats{}}
	orch.finalizeFailedBackupStats(okRun, nil)
	if okRun.stats.Failed {
		t.Fatal("a nil runErr must NOT set stats.Failed (notification errors must not escalate)")
	}
}

// F5: a failed run is exported by a defer inside RunGoBackup, BEFORE the cmd layer
// (handleBackupRunError) writes the ERROR line that reports the failure, so
// errors_total read 0 next to status 2 while the notifications, which re-read the log
// after that line, said "Errors: 1". The chain below runs in the production order:
// the two defers, then the line the caller writes, then FinalizeAfterRun.
func TestFailedRunCountsTheStoppingErrorBeforeExport(t *testing.T) {
	type want struct {
		metricErrors, metricWarnings, metricStatus string
		notifyErrors, notifyErrorLines             int
	}
	cases := []struct {
		name     string
		canceled bool
		noLog    bool
		pre      func(l *logging.Logger)
		want     want
	}{
		{
			name: "failed run, no error logged before the failure",
			pre: func(l *logging.Logger) {
				l.Warning("Collector - one warning")
				l.Warning("Collector - another warning")
			},
			want: want{metricErrors: "1", metricWarnings: "2", metricStatus: "2", notifyErrors: 1, notifyErrorLines: 1},
		},
		{
			name: "failed run, one error logged before the failure",
			pre:  func(l *logging.Logger) { l.Warning("Collector - one warning"); l.Error("Collector - an earlier error") },
			want: want{metricErrors: "2", metricWarnings: "1", metricStatus: "2", notifyErrors: 2, notifyErrorLines: 2},
		},
		{
			name:     "canceled run is unchanged: the caller writes a WARNING, not an ERROR",
			canceled: true,
			pre:      func(l *logging.Logger) { l.Warning("Collector - one warning") },
			want:     want{metricErrors: "0", metricWarnings: "1", metricStatus: "2", notifyErrors: 0, notifyErrorLines: 0},
		},
		{
			name:  "failed run without a log file: nothing re-reads, the count is what notifications show",
			noLog: true,
			pre:   func(l *logging.Logger) {},
			want:  want{metricErrors: "1", metricWarnings: "0", metricStatus: "2", notifyErrors: 1, notifyErrorLines: 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(io.Discard)
			logPath := ""
			if !tc.noLog {
				logPath = filepath.Join(t.TempDir(), "run.log")
				if err := logger.OpenLogFile(logPath); err != nil {
					t.Fatalf("OpenLogFile: %v", err)
				}
				t.Cleanup(func() { _ = logger.CloseLogFile() })
			}
			tc.pre(logger)

			metricsDir := t.TempDir()
			o := New(logger, false)
			o.SetConfig(&config.Config{MetricsEnabled: true, MetricsPath: metricsDir, EmailEnabled: true})
			email := &stubNotifierChannel{name: "Email"}
			o.RegisterNotificationChannel(email)

			ctx := context.Background()
			backupErr := &BackupError{Phase: "compression", Err: errors.New("xz compression failed: exit status 1"), Code: types.ExitCompressionError}
			runErr := error(backupErr)
			if tc.canceled {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
				runErr = canceled.Err()
			}
			stats := &BackupStats{LogFilePath: logPath, StartTime: o.now()}
			run := &backupRunContext{ctx: ctx, stats: stats}

			// RunGoBackup's defers, in their LIFO order.
			o.finalizeFailedBackupStats(run, runErr)
			o.exportBackupMetrics(run, runErr)

			prom, err := os.ReadFile(filepath.Join(metricsDir, "proxmox_backup.prom"))
			if err != nil {
				t.Fatalf("read metrics: %v", err)
			}
			metric := func(name string) string {
				for _, line := range strings.Split(string(prom), "\n") {
					if v, ok := strings.CutPrefix(line, name+" "); ok {
						return v
					}
				}
				return "<absent>"
			}
			if got := metric("proxmox_backup_errors_total"); got != tc.want.metricErrors {
				t.Errorf("errors_total = %s, want %s", got, tc.want.metricErrors)
			}
			if got := metric("proxmox_backup_warnings_total"); got != tc.want.metricWarnings {
				t.Errorf("warnings_total = %s, want %s", got, tc.want.metricWarnings)
			}
			if got := metric("proxmox_backup_status"); got != tc.want.metricStatus {
				t.Errorf("status = %s, want %s", got, tc.want.metricStatus)
			}

			// What handleBackupRunError (cmd/proxsave/backup_execution.go) does next.
			if ctx.Err() == context.Canceled {
				logger.Warning("Backup was canceled")
			} else {
				logger.Error("Backup %s failed: %v", backupErr.Phase, backupErr.Err)
			}
			o.FinalizeAfterRun(ctx, stats)

			if !email.called {
				t.Fatal("the notification channel was not called")
			}
			if email.errorCount != tc.want.notifyErrors {
				t.Errorf("notification Errors = %d, want %d", email.errorCount, tc.want.notifyErrors)
			}
			errorLines := 0
			for _, c := range stats.LogCategories {
				if c.Type == "ERROR" {
					errorLines += c.Count
				}
			}
			if errorLines != tc.want.notifyErrorLines {
				t.Errorf("ERROR lines in the notification issue list = %d, want %d (%+v)", errorLines, tc.want.notifyErrorLines, stats.LogCategories)
			}
		})
	}
}

// F5, the other half: a run that did not fail (success, warnings only) never gets the
// extra count, and its metrics are what they were.
func TestNonFailedRunMetricsUnchangedByStoppingErrorCount(t *testing.T) {
	cases := []struct {
		name                   string
		lines                  []string
		wantErrors, wantStatus string
	}{
		{name: "success", wantErrors: "0", wantStatus: "0"},
		{name: "warnings only", lines: []string{"[2026-10-02 10:00:00] WARNING  Collector - a warning"}, wantErrors: "0", wantStatus: "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "run.log")
			if err := os.WriteFile(logPath, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			metricsDir := t.TempDir()
			o := New(newTestLogger(), false)
			o.SetConfig(&config.Config{MetricsEnabled: true, MetricsPath: metricsDir})
			stats := &BackupStats{LogFilePath: logPath, ExitCode: types.ExitSuccess.Int(), StartTime: o.now()}
			run := &backupRunContext{ctx: context.Background(), stats: stats}

			o.finalizeFailedBackupStats(run, nil)
			if stats.ErrorCount != 0 || stats.Failed {
				t.Fatalf("a nil runErr must leave the stats alone: ErrorCount=%d Failed=%v", stats.ErrorCount, stats.Failed)
			}
			o.exportBackupMetrics(run, nil)

			prom, err := os.ReadFile(filepath.Join(metricsDir, "proxmox_backup.prom"))
			if err != nil {
				t.Fatalf("read metrics: %v", err)
			}
			for _, want := range []string{"proxmox_backup_errors_total " + tc.wantErrors, "proxmox_backup_status " + tc.wantStatus} {
				if !strings.Contains(string(prom), want+"\n") {
					t.Errorf("metrics missing %q:\n%s", want, prom)
				}
			}
		})
	}
}

func TestWriteArchiveChecksumPropagatesWriteError(t *testing.T) {
	orch := New(newTestLogger(), false)
	checksumPath := "/backups/test.tar.sha256"
	writeErr := errors.New("disk full")
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = fakeFS.Cleanup() })

	err := orch.writeArchiveChecksum(
		&backupWorkspace{fs: writeFileFailFS{FS: fakeFS, failPath: checksumPath, err: writeErr}},
		&backupArtifacts{archivePath: "/backups/test.tar", checksumPath: checksumPath},
		"abc123",
	)
	if err == nil {
		t.Fatal("expected writeArchiveChecksum error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
	if !strings.Contains(err.Error(), checksumPath) {
		t.Fatalf("expected checksum path in error, got %q", err.Error())
	}
}

func TestFinalizeSuccessIssueStats_NotifyIssueBecomesWarning(t *testing.T) {
	// A notify/communication failure is logged DURING dispatch, after the
	// pre-notification snapshot. On a successful run the final re-parse must pick it
	// up so it surfaces as a warning (status 1) instead of vanishing to success (0).
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "run.log")
	content := "[2026-07-16 10:00:00] INFO     Backup completed\n" +
		"[2026-07-16 10:00:01] NOTIFY-ERR Telegram: failed: connection refused\n"
	if err := os.WriteFile(logFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	o := &Orchestrator{}
	stats := &BackupStats{LogFilePath: logFile, ExitCode: types.ExitSuccess.Int()}
	o.finalizeSuccessIssueStats(stats)

	if stats.NotifyCount != 1 {
		t.Fatalf("NotifyCount = %d, want 1", stats.NotifyCount)
	}
	if stats.ExitCode != types.ExitGenericError.Int() {
		t.Fatalf("ExitCode = %d, want %d (notify issue must surface as warning, never 0)",
			stats.ExitCode, types.ExitGenericError.Int())
	}
}

func TestFinalizeSuccessIssueStats_CleanRunStaysSuccess(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "clean.log")
	content := "[2026-07-16 10:00:00] INFO     Backup completed\n"
	if err := os.WriteFile(logFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	o := &Orchestrator{}
	stats := &BackupStats{LogFilePath: logFile, ExitCode: types.ExitSuccess.Int()}
	o.finalizeSuccessIssueStats(stats)

	if stats.ExitCode != types.ExitSuccess.Int() {
		t.Fatalf("ExitCode = %d, want %d (a clean run must stay success)",
			stats.ExitCode, types.ExitSuccess.Int())
	}
}
