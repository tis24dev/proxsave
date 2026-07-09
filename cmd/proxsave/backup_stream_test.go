package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/support"
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
			FilesCollected:   42,
			FilesFailed:      3,
			DirsCreated:      7,
			BytesCollected:   8192,
			ArchiveSize:      4096,
			Compression:      "zstd",
			CompressionLevel: 3,
			CompressionMode:  "standard",
			BundleCreated:    true,
			Duration:         90 * time.Second,
			ArchivePath:      "/var/backup/proxsave-2026.tar.zst",
			LocalStatus:      "ok",
			SecondaryStatus:  "warning",
			CloudStatus:      "disabled",
		},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))

	if !strings.Contains(out, "Backup completed") {
		t.Fatalf("missing completion banner:\n%s", out)
	}
	if strings.Contains(out, "Backup failed") {
		t.Fatalf("a successful run must not say 'failed':\n%s", out)
	}
	// The enriched Files line (collected + missing + failed) now lives in the lower
	// stats block, not the upper recap.
	if !strings.Contains(out, "Files: 42 collected - 0 missing") || !strings.Contains(out, "(3 failed)") {
		t.Fatalf("missing files line:\n%s", out)
	}
	// PARTE ALTA dropped Size / Duration / Archive / Local: Size/Duration/Archive
	// now live only in the stats block below, Local is removed entirely. Secondary
	// stays; a disabled Cloud is skipped.
	if !strings.Contains(out, "Secondary: warning") {
		t.Fatalf("missing secondary status line:\n%s", out)
	}
	if strings.Contains(out, "Local:") {
		t.Fatalf("the Local status line must be removed:\n%s", out)
	}
	if strings.Contains(out, "Cloud:") {
		t.Fatalf("disabled cloud must be skipped:\n%s", out)
	}

	// The recap carries the backup-statistics lines (mirroring the debug-only log
	// block) WITHOUT the "=== Backup Statistics ===" header.
	if strings.Contains(out, "=== Backup Statistics ===") {
		t.Fatalf("the stats header must be removed:\n%s", out)
	}
	for _, want := range []string{
		"Directories created: 7",
		"Data collected: 8.0 KiB",
		"Archive size: 4.0 KiB",
		"Compression ratio: 50.0%",
		"Compression used: zstd (level 3, mode standard)",
		"Bundle path: /var/backup/proxsave-2026.tar.zst",
		"Bundle contents: archive + checksum + metadata",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats block missing %q:\n%s", want, out)
		}
	}
}

// TestBuildBackupOutcomePromptFilesMissing asserts the Files line renders the
// "- N missing" count from st.FilesMissing (the SAME field the notifications
// report) when non-zero.
func TestBuildBackupOutcomePromptFilesMissing(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{
		exitCode: types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{
			FilesCollected: 40,
			FilesMissing:   5,
			LocalStatus:    "ok",
		},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))
	if !strings.Contains(out, "Files: 40 collected - 5 missing") {
		t.Fatalf("missing files-missing count:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptLogLine asserts the "Log:" line falls back to the
// LOG_FILE env var when the default logger has no file path (as in tests).
func TestBuildBackupOutcomePromptLogLine(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	t.Setenv("LOG_FILE", "/tmp/run.log")

	res := backupModeResult{
		exitCode:     types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{FilesCollected: 1, LocalStatus: "ok"},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))
	if !strings.Contains(out, "Log: /tmp/run.log") {
		t.Fatalf("missing log line (env fallback):\n%s", out)
	}
}

// TestBuildBackupOutcomePromptCentralizedIdentity asserts the centralized-mode
// lines: the Telegram/relay ServerID and the SANITIZED Healthchecks link. A
// hostile link is stripped by serverbot.SanitizeLoginURL (no line printed), and
// empty ServerID/link produce no lines.
func TestBuildBackupOutcomePromptCentralizedIdentity(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	// Present + clean: both lines shown, link passed through verbatim (sanitized OK).
	res := backupModeResult{
		exitCode: types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{
			FilesCollected:  1,
			LocalStatus:     "ok",
			ServerID:        "srv-42",
			HealthcheckLink: "https://hc/accounts/check_token/u/CAP/",
		},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))
	if !strings.Contains(out, "Server ID Telegram: srv-42") {
		t.Fatalf("missing server id line:\n%s", out)
	}
	if !strings.Contains(out, "Healthchecks link: https://hc/accounts/check_token/u/CAP/") {
		t.Fatalf("missing sanitized healthchecks link line:\n%s", out)
	}

	// Hostile link (space injection): sanitized away -> NO link line, raw never shown.
	resHostile := backupModeResult{
		exitCode: types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{
			FilesCollected:  1,
			LocalStatus:     "ok",
			HealthcheckLink: "https://hc/ x",
		},
	}
	outHostile := ansi.Strip(buildBackupOutcomePrompt(resHostile))
	if strings.Contains(outHostile, "Healthchecks link:") {
		t.Fatalf("hostile link must be sanitized away:\n%s", outHostile)
	}
	if strings.Contains(outHostile, "https://hc/ x") {
		t.Fatalf("raw hostile link must never be shown:\n%s", outHostile)
	}

	// javascript: scheme is not http(s) -> stripped.
	resJS := backupModeResult{
		exitCode: types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{
			FilesCollected:  1,
			LocalStatus:     "ok",
			HealthcheckLink: "javascript:alert(1)",
		},
	}
	if strings.Contains(ansi.Strip(buildBackupOutcomePrompt(resJS)), "Healthchecks link:") {
		t.Fatalf("javascript: scheme must be sanitized away")
	}

	// Empty ServerID/link: neither line present.
	resEmpty := backupModeResult{
		exitCode:     types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{FilesCollected: 1, LocalStatus: "ok"},
	}
	outEmpty := ansi.Strip(buildBackupOutcomePrompt(resEmpty))
	if strings.Contains(outEmpty, "Server ID Telegram:") {
		t.Fatalf("empty server id must produce no line:\n%s", outEmpty)
	}
	if strings.Contains(outEmpty, "Healthchecks link:") {
		t.Fatalf("empty link must produce no line:\n%s", outEmpty)
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
	if strings.Contains(out, "Local:") {
		t.Fatalf("the Local status line must be removed:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptIncludesIssueSummary asserts the graphical outcome
// carries the same warnings/errors recap the CLI footer prints: a count header plus
// the captured issue lines, from the same default logger.
func TestBuildBackupOutcomePromptIncludesIssueSummary(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	lg := logging.New(types.LogLevelInfo, false)
	lg.SetOutput(io.Discard)
	lg.Warning("disk almost full")
	lg.Error("failed to upload chunk 7")
	logging.SetDefaultLogger(lg)
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{
		exitCode:     types.ExitGenericError.Int(),
		supportStats: &orchestrator.BackupStats{FilesCollected: 1, LocalStatus: "ok"},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))

	if !strings.Contains(out, "Warnings/errors during run: 1 warning(s), 1 error(s)") {
		t.Fatalf("missing warnings/errors recap header:\n%s", out)
	}
	if !strings.Contains(out, "disk almost full") || !strings.Contains(out, "failed to upload chunk 7") {
		t.Fatalf("recap must list the captured issue lines:\n%s", out)
	}
}

// TestBuildBackupOutcomePromptNoIssueSummaryWhenClean asserts a clean run (no
// warnings/errors logged) shows no recap at all.
func TestBuildBackupOutcomePromptNoIssueSummaryWhenClean(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	lg := logging.New(types.LogLevelInfo, false)
	lg.SetOutput(io.Discard)
	logging.SetDefaultLogger(lg)
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	res := backupModeResult{
		exitCode:     types.ExitSuccess.Int(),
		supportStats: &orchestrator.BackupStats{FilesCollected: 1, LocalStatus: "ok"},
	}
	out := ansi.Strip(buildBackupOutcomePrompt(res))
	if strings.Contains(out, "Warnings/errors during run") {
		t.Fatalf("a clean run must not show the issue recap:\n%s", out)
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
	if strings.Contains(out, "Local:") {
		t.Fatalf("the Local status line must be removed:\n%s", out)
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

// TestRunBackupStreamedDriver drives runBackupStreamed on an observed altscreen
// session the same way the dashboard handoff does: it stashes the session (so
// runBackupStreamed adopts it), overrides backupStreamSteps with a stub that emits
// [ts] LEVEL lines via logging.Info (captured -> streamed into the contained
// viewport) and returns a canned result, then asserts the streamed lines + the
// outcome + the Continue hint render, and that pressing Enter lets runBackupStreamed
// close the session and return the result.
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

	var buf shell.SyncBuffer
	session := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, &buf)

	// Stash the observed session so runBackupStreamed adopts it (mirrors the
	// dashboard "Backup" handoff). releaseDashboardLeftovers cleans up if the run
	// never adopts (guards other tests against a dangling stash).
	bootstrap := logging.NewBootstrapLogger()
	stashDashboardSession(session, bootstrap)
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

	res := pumpEnterBackup(t, session, resCh)
	if res.exitCode != types.ExitSuccess.Int() {
		t.Fatalf("expected success exit, got %d", res.exitCode)
	}
	if res.supportStats == nil || res.supportStats.FilesCollected != 7 {
		t.Fatalf("expected the stub's canned result, got %+v", res.supportStats)
	}
}

// TestRunBackupStreamedSupportEmailInStream asserts that in support mode the
// maintainer-email step runs INSIDE the streamed viewport (its announcement is
// captured -> streamed) and that runBackupStreamed marks it sent, so the deferred
// CLI sender skips it (no double send). This is the whole point of the change: the
// send's outcome must be visible in the stream, not lost to a closed screen.
func TestRunBackupStreamedSupportEmailInStream(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	prevSteps := backupStreamSteps
	backupStreamSteps = func(opts backupModeOptions) backupModeResult {
		logging.Info("archive written to disk")
		return backupModeResult{
			exitCode:     types.ExitSuccess.Int(),
			supportStats: &orchestrator.BackupStats{FilesCollected: 3, LocalStatus: "ok"},
		}
	}
	t.Cleanup(func() { backupStreamSteps = prevSteps })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf shell.SyncBuffer
	session := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, &buf)

	bootstrap := logging.NewBootstrapLogger()
	stashDashboardSession(session, bootstrap)
	t.Cleanup(releaseDashboardLeftovers)

	resCh := make(chan backupModeResult, 1)
	go func() {
		resCh <- runBackupStreamed(backupModeOptions{
			ctx:         ctx,
			bootstrap:   bootstrap,
			cfg:         &config.Config{},
			envInfo:     &environment.EnvironmentInfo{},
			support:     true,
			supportMeta: support.Meta{GitHubUser: "alice", IssueID: "#42"},
		})
	}()

	waitFor(t, &buf, "archive written to disk")
	// The support-email step runs after the backup steps but still inside the
	// capture, so its announcement streams into the viewport.
	waitFor(t, &buf, "sending support email")
	waitFor(t, &buf, "enter continue")

	res := pumpEnterBackup(t, session, resCh)
	if !res.supportEmailSent {
		t.Fatalf("streamed support run must mark the email as sent (so the deferred sender skips): %+v", res)
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
