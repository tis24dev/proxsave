package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestBuildBackupOutcomePromptSuccess asserts a successful run renders the green
// "Backup completed" banner plus themed stat lines from the run's BackupStats.
func TestBuildBackupOutcomePromptSuccess(t *testing.T) {
	// A clean (no-warnings) default logger so the exit-0 run classifies OK, not
	// Warning (buildBackupOutcomePrompt reads GetDefaultLogger().HasWarnings()).
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{
		exitCode: types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{
			FilesCollected:  42,
			FilesFailed:     3,
			ArchiveSize:     4096,
			Duration:        90 * time.Second,
			ArchivePath:     "/var/backup/proxsave-2026.tar.zst",
			LocalStatus:     "ok",
			SecondaryStatus: "warning",
			CloudStatus:     "disabled",
		},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))

	if !strings.Contains(out, "Backup completed") {
		t.Fatalf("missing completion banner:\n%s", out)
	}
	if strings.Contains(out, "Backup failed") {
		t.Fatalf("a successful run must not say 'failed':\n%s", out)
	}
	if !strings.Contains(out, "Files: 42 collected") || !strings.Contains(out, "(3 failed)") {
		t.Fatalf("missing files line:\n%s", out)
	}
	if !strings.Contains(out, "Size: 4.0 KiB") {
		t.Fatalf("missing size line:\n%s", out)
	}
	if !strings.Contains(out, "Duration: 1m30s") {
		t.Fatalf("missing duration line:\n%s", out)
	}
	if !strings.Contains(out, "Archive: /var/backup/proxsave-2026.tar.zst") {
		t.Fatalf("missing archive line:\n%s", out)
	}
	if !strings.Contains(out, "Local: ok") {
		t.Fatalf("missing local status line:\n%s", out)
	}
	if !strings.Contains(out, "Secondary: warning") {
		t.Fatalf("missing secondary status line:\n%s", out)
	}
	// A disabled cloud destination is skipped to keep the block terse.
	if strings.Contains(out, "Cloud:") {
		t.Fatalf("disabled cloud must be skipped:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptWarning is the visible proof of fix #4: exit 1
// (ExitGenericError, a NON-FATAL generic error) must classify as a WARNING and
// read "Backup completed with warnings", NOT the old red "Backup failed" - the
// same yellow the CLI final summary gives that exit code.
func TestBuildBackupOutcomePromptWarning(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{
		exitCode:     types.ExitGenericError.Int(),
		supportStats: &orchestrator.BackupStats{FilesCollected: 1, LocalStatus: "error"},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))

	if !strings.Contains(out, "Backup completed with warnings") {
		t.Fatalf("exit 1 (non-fatal) must read as completed-with-warnings:\n%s", out)
	}
	if strings.Contains(out, "Backup failed") {
		t.Fatalf("exit 1 must NOT read as failed (that is the fix #4 flip):\n%s", out)
	}
	if !strings.Contains(out, "Local: error") {
		t.Fatalf("missing local error status line:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptFailure asserts a genuinely fatal exit code
// (ExitConfigError) renders the red "Backup failed" banner and never reads as
// completed.
func TestBuildBackupOutcomePromptFailure(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{
		exitCode:     types.ExitConfigError.Int(),
		supportStats: &orchestrator.BackupStats{FilesCollected: 1, LocalStatus: "error"},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))

	if !strings.Contains(out, "Backup failed") {
		t.Fatalf("missing failure banner:\n%s", out)
	}
	if strings.Contains(out, "Backup completed") {
		t.Fatalf("a failed run must NOT read as completed:\n%s", out)
	}
	if !strings.Contains(out, "Local: error") {
		t.Fatalf("missing local error status line:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptInterrupted asserts an interrupted run (Ctrl+C,
// exit 130) renders the magenta "Backup interrupted" banner.
func TestBuildBackupOutcomePromptInterrupted(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{exitCode: exitCodeInterrupted}
	out := ansi.Strip(buildBackupOutcomePrompt(res))

	if !strings.Contains(out, "Backup interrupted") {
		t.Fatalf("missing interrupted banner:\n%s", out)
	}
	if strings.Contains(out, "Backup failed") || strings.Contains(out, "Backup completed") {
		t.Fatalf("an interrupted run must read as interrupted, not failed/completed:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptNoStats asserts a missing BackupStats renders just the
// banner without panicking.
func TestBuildBackupOutcomePromptNoStats(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	out := ansi.Strip(buildBackupOutcomePrompt(backupModeResult{exitCode: types.ExitSuccess.Int()}))
	if !strings.Contains(out, "Backup completed") {
		t.Fatalf("missing banner:\n%s", out)
	}
	if strings.Contains(out, "Files:") {
		t.Fatalf("no stats -> no stat lines:\n%s", out)
	}
}

// TestRunBackupStreamedDriver drives runBackupStreamed the same way the dashboard
// handoff does: it stashes a session (so teardownDashboardSessionForInline succeeds
// and tears the altscreen session down), overrides backupInlineSession with an
// output-observing INLINE session (so the tea.Println lines are asserted), and
// overrides backupStreamSteps with a stub that emits [ts] LEVEL lines via
// logging.Info (captured -> streamed) and returns a canned result. It then asserts
// the streamed lines + the outcome + the Continue hint render, and that pressing
// Enter lets runBackupStreamed close the session and return the result.
func TestRunBackupStreamedDriver(t *testing.T) {
	// Clean, Info-level default logger so the captured logging.Info lines emit.
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	prevSteps := backupStreamSteps
	backupStreamSteps = func(opts backupModeOptions) backupModeResult {
		logging.Info("collecting cluster configuration")
		logging.Info("archive written to disk")
		return backupModeResult{
			exitCode: types.ExitSuccess.Int(),
			supportStats: &orchestrator.BackupStats{
				FilesCollected: 7,
				ArchiveSize:    4096,
				LocalStatus:    "ok",
			},
		}
	}
	t.Cleanup(func() { backupStreamSteps = prevSteps })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The INLINE session runBackupStreamed streams into: the emitted lines land in
	// buf via tea.Println (a no-op in altscreen), so they can be asserted.
	var buf shell.SyncBuffer
	var inlineSession *shell.Session
	prevInline := backupInlineSession
	backupInlineSession = func(c context.Context, cfg shell.Config, _ ...tea.ProgramOption) *shell.Session {
		inlineSession = shell.StartInlineForTestWithOutput(c, cfg, &buf)
		return inlineSession
	}
	t.Cleanup(func() { backupInlineSession = prevInline })

	// Stash a session so teardownDashboardSessionForInline finds one to tear down
	// (mirrors the dashboard "Run backup now" handoff); teardown closes it. A
	// renderless session is enough - it is only closed, never rendered.
	stashed := shell.StartForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	bootstrap := logging.NewBootstrapLogger()
	stashDashboardSession(stashed, bootstrap)
	t.Cleanup(releaseDashboardLeftovers)

	resCh := make(chan backupModeResult, 1)
	go func() {
		resCh <- runBackupStreamed(backupModeOptions{ctx: ctx, bootstrap: bootstrap, cfg: &config.Config{}})
	}()

	waitFor(t, &buf, "collecting cluster configuration")
	waitFor(t, &buf, "archive written to disk")
	waitFor(t, &buf, "Backup completed")
	waitFor(t, &buf, "Files: 7 collected")
	waitFor(t, &buf, "enter continue")

	// inlineSession is set by backupInlineSession by the time the streamed lines
	// above have appeared in buf; Enter on it resolves the done screen.
	res := pumpEnterBackup(t, inlineSession, resCh)
	if res.exitCode != types.ExitSuccess.Int() {
		t.Fatalf("expected success exit, got %d", res.exitCode)
	}
	if res.supportStats == nil || res.supportStats.FilesCollected != 7 {
		t.Fatalf("expected the stub's canned result, got %+v", res.supportStats)
	}
}

// pumpEnterBackup sends Enter until runBackupStreamed returns its result.
func pumpEnterBackup(t *testing.T, s *shell.Session, done <-chan backupModeResult) backupModeResult {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(uitest.Deadline(5 * time.Second))
	for {
		select {
		case res := <-done:
			return res
		case <-ticker.C:
			s.Send(shell.KeyMsg("enter"))
		case <-deadline:
			t.Fatal("runBackupStreamed did not return")
			return backupModeResult{}
		}
	}
}
