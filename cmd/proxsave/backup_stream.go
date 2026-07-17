package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/serverbot"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// backupStreamSteps is the seam runBackupStreamed drives to run the real backup.
// It points at runBackupModeSteps in production; tests override it with a stub
// that emits a couple of log lines (captured -> streamed) and returns a canned
// result, so the capture->stream plumbing can be exercised without a real backup.
var backupStreamSteps = runBackupModeSteps

// backupAdoptSession is the seam runBackupStreamed uses to adopt the altscreen
// session the dashboard stashed on the "Backup" choice. Production uses
// adoptDashboardSession; tests override it with an output-observing altscreen
// session so the emitted lines and the outcome can be asserted.
var backupAdoptSession = adoptDashboardSession

// captureRunOutput routes BOTH the loggers (default + colored bootstrap mirror)
// AND raw os.Stdout through a SINGLE pipe into emit, so everything a run prints -
// colored logger lines AND the raw fmt.Println blank spacers between sections -
// streams into the panel in the SAME order as the CLI. The bubbletea program
// renders to its own saved fd (captured at program start), so redirecting the
// os.Stdout variable here never touches the altscreen. restore() (call via defer)
// undoes the logger swap + restores os.Stdout, closes the pipe, and drains it.
// If os.Pipe fails it degrades to logger-only capture (no stdout spacers).
func captureRunOutput(bootstrap *logging.BootstrapLogger, emit func(line string)) func() {
	sink := logging.NewLineWriterRaw(emit)
	r, w, err := os.Pipe()
	if err != nil {
		return logging.CaptureConsoleWithColor(bootstrap, sink)
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(sink, r)
		close(done)
	}()
	origStdout := os.Stdout
	os.Stdout = w
	restoreLoggers := logging.CaptureConsoleWithColor(bootstrap, w)
	return func() {
		restoreLoggers()
		os.Stdout = origStdout
		_ = w.Close()
		<-done
		_ = r.Close()
	}
}

// runStreamedEndOfRunActions runs the backup side effects that must appear
// INSIDE the streamed viewport, in ONE place, so the RunStreamTask closure has a
// single call and any future in-stream end-of-run action is added here:
//   - the support email (support mode only): sent HERE inside the capture so its
//     outcome streams into the viewport instead of printing to a screen already
//     closed after Continue. The log-management phase (in backupStreamSteps) has
//     closed the log file, so the email attaches the COMPLETE log. It sets
//     res.supportEmailSent so the deferred sender skips it (no double send).
//     envInfo is always set for a real run; guarded to never deref.
//   - the daemon handoff: its debug trace streams into the viewport instead of
//     the plain scrollback after the session closes (visible in support mode,
//     where the run forces DEBUG logging).
//
// This is the in-STREAM case only. The fallback (session vanished) and the CLI
// path hand off directly, without an in-viewport email.
func runStreamedEndOfRunActions(ctx context.Context, opts backupModeOptions, res *backupModeResult) {
	if opts.support && res.supportStats != nil && opts.envInfo != nil {
		emitSupportEmail(ctx, opts.cfg, opts.logger, opts.envInfo.Type, res.supportStats, opts.supportMeta)
		res.supportEmailSent = true
	}
	maybeHandoffManualBackup(opts, *res)
}

// runBackupStreamed runs the backup INSIDE the graphical dashboard ALTSCREEN
// session, streaming its [ts] LEVEL log lines into a CONTAINED, scrollable,
// COLORED viewport panel (components.RunStreamTask) so scrolling stays within the
// box and the whole run is contained in the frame -- exactly what a long run
// needs. It adopts the session the dashboard stashed on the "Backup" choice; if
// that handoff has vanished (already adopted, or never stashed -- a CLI/cron/
// daemon backup) it falls back to the plain steps so the backup always runs.
//
// The captured console (logging.CaptureConsoleWithColor) routes the default
// logger + the COLORED bootstrap mirror into the raw sink, so both the run's
// logging.Info lines and the bootstrap finalization lines flow (colored) into the
// viewport. taskCtx is threaded into the backup so Esc cancels the run; the
// session is closed only after the user presses Continue.
func runBackupStreamed(opts backupModeOptions) backupModeResult {
	useColor := true
	if opts.cfg != nil {
		useColor = opts.cfg.UseColor
	}
	session := backupAdoptSession(shell.Config{
		AppName:  "ProxSave",
		Subtitle: "Backup",
		UseColor: useColor,
	})
	if session == nil {
		// The handoff vanished (CLI/cron/daemon): run the backup plain, then hand
		// the outcome to the daemon (plain console, like the CLI path).
		res := backupStreamSteps(opts)
		maybeHandoffManualBackup(opts, res)
		return res
	}

	var res backupModeResult
	streamErr := components.RunStreamTask(opts.ctx, session, "Running backup",
		func(taskCtx context.Context, emit func(line string)) (string, error) {
			// Route the default + COLORED bootstrap-mirror loggers AND raw os.Stdout
			// (the fmt.Println section spacers the CLI prints) through one pipe into
			// the panel; restored on return/panic. So the panel shows the SAME lines -
			// colored logs + the blank spacer rows between sections - in the same
			// order as the CLI, instead of losing them to the raw altscreen.
			defer captureRunOutput(opts.bootstrap, emit)()

			// Thread taskCtx so an Esc cancel propagates into the running backup.
			stepOpts := opts
			stepOpts.ctx = taskCtx
			res = backupStreamSteps(stepOpts)
			runStreamedEndOfRunActions(taskCtx, opts, &res)
			return buildBackupOutcomePrompt(res), nil
		})
	if streamErr != nil {
		// The stream is best-effort UI: an abort/UI-death never changes the
		// backup outcome (res already holds it), so only trace it.
		logging.DebugStepBootstrap(opts.bootstrap, "dashboard", "backup stream: %v", streamErr)
	}

	// The user pressed Continue: release the terminal so any deferred output
	// prints to the plain scrollback (the in-graphics viewport vanishes with the
	// alternate screen, matching the install finalization).
	if closeErr := session.Close(); closeErr != nil {
		logging.DebugStepBootstrap(opts.bootstrap, "dashboard", "session close: %v", closeErr)
	}
	return res
}

// renderBackupBanner renders the pre-styled backup outcome banner for a given
// display severity, mapping each severity to its (style, symbol, title). It
// classifies with the SHARED exitCodeSeverity so the banner colors res.exitCode
// EXACTLY like the CLI final summary footer: a non-fatal generic error (exit 1,
// ExitGenericError) reads yellow "completed with warnings", NOT red "failed".
// The interrupted case uses the magenta InterruptedText matching the footer's
// Ctrl+C color; there is no altscreen banner level for it.
func renderBackupBanner(sev exitSeverity) string {
	switch sev {
	case severityOK:
		return theme.SuccessText.Render(theme.SymbolSuccess + " " + "Backup completed")
	case severityWarning:
		return theme.WarningText.Render(theme.SymbolWarning + " " + "Backup completed with warnings")
	case severityInterrupted:
		return theme.InterruptedText.Render(theme.SymbolWarning + " " + "Backup interrupted")
	default: // severityError
		return theme.ErrorText.Render(theme.SymbolError + " " + "Backup failed")
	}
}

// buildBackupOutcomePrompt composes the pre-styled outcome block shown at the
// bottom of the streamed backup screen (StreamDoneMsg.Outcome), mirroring
// buildInstallOutcomePrompt. It opens with renderBackupBanner classified via the
// shared exitCodeSeverity (same logger the footer reads, so HasWarnings agrees),
// then adds themed stat lines from the run's BackupStats when present. The
// display classification only colors the banner - res.exitCode is untouched and
// still drives finalize, byte-identical to the CLI. Missing/nil data is skipped
// (never panics).
func buildBackupOutcomePrompt(res backupModeResult) string {
	var b strings.Builder

	logger := logging.GetDefaultLogger()
	sev := exitCodeSeverity(res.exitCode, logger)
	b.WriteString(renderBackupBanner(sev))

	if st := res.supportStats; st != nil {
		// The backup-statistics block (headerless); the log block is now debug-only.
		b.WriteString("\n")
		appendBackupStatsBlock(&b, st)

		// Secondary/Cloud storage status, only when they carry a meaningful
		// (non-disabled) status, so a single-destination run stays terse.
		if s := strings.TrimSpace(st.SecondaryStatus); s != "" && s != "disabled" {
			appendBackupStatusLine(&b, "Secondary", st.SecondaryStatus)
		}
		if s := strings.TrimSpace(st.CloudStatus); s != "" && s != "disabled" {
			appendBackupStatusLine(&b, "Cloud", st.CloudStatus)
		}

		// Centralized-mode identity: the Telegram/relay pairing id and the sanitized
		// Healthchecks portal link, each shown only when present. The link mirrors the
		// sole-display discipline of logMonitoringPortalLink: sanitized once with
		// serverbot.SanitizeLoginURL, printed ONLY if it survives, NEVER raw and never
		// registered as a secret.
		if id := strings.TrimSpace(st.ServerID); id != "" {
			b.WriteString("\n")
			b.WriteString(theme.Text.Render("Server ID Telegram: " + id))
		}
		if link := serverbot.SanitizeLoginURL(st.HealthcheckLink); link != "" {
			b.WriteString("\n")
			b.WriteString(theme.Text.Render("Healthchecks link: " + link))
		}
	}

	// The run log path, shown regardless of supportStats so an early failure (nil
	// stats) still points the operator at the diagnostic log.
	//
	// The run log file is CLOSED during the log-management phase before this
	// outcome is built, so GetLogFilePath may be "" by now; runLogPath falls back
	// to the LOG_FILE the runtime exports at startup (empty is guarded).
	if lp := runLogPath(); lp != "" {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Log: " + lp))
	}

	// Warnings/errors recap - the theme counterpart of the CLI footer's
	// printRunIssueSummary - shown whenever the run logged issues (even a failed run
	// with no stats), so the graphical outcome states them as clearly as the CLI.
	appendRunIssueSummary(&b, logger)

	return b.String()
}

// appendBackupStatsBlock renders the backup-statistics block into the graphical
// outcome recap. Its first line is the enriched "Files: N collected - K missing
// (M failed)" moved down from the upper recap; the remaining lines mirror the
// debug-only log block in logBackupStatistics (same lines, conditionals and
// formatters - formatBytes/formatDuration and the shared compressionRatioText),
// just THEME-styled instead of logged.
func appendBackupStatsBlock(b *strings.Builder, st *orchestrator.BackupStats) {
	// Files: N collected - K missing (M failed) - moved down from the upper recap.
	// "missing" reuses st.FilesMissing (the field the notifications report), always
	// shown (yellow when >0); the failed count only when non-zero.
	b.WriteString("\n")
	b.WriteString(theme.Text.Render(fmt.Sprintf("Files: %d collected - ", st.FilesCollected)))
	missingStyle := theme.Text
	if st.FilesMissing > 0 {
		missingStyle = theme.WarningText
	}
	b.WriteString(missingStyle.Render(fmt.Sprintf("%d missing", st.FilesMissing)))
	if st.FilesFailed > 0 {
		b.WriteString(theme.WarningText.Render(fmt.Sprintf(" (%d failed)", st.FilesFailed)))
	}
	b.WriteString("\n")
	b.WriteString(theme.Text.Render(fmt.Sprintf("Directories created: %d", st.DirsCreated)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Data collected: " + formatBytes(st.BytesCollected)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Archive size: " + formatBytes(st.ArchiveSize)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Compression ratio: " + compressionRatioText(st)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render(fmt.Sprintf("Compression used: %s (level %d, mode %s)", st.Compression, st.CompressionLevel, st.CompressionMode)))
	if st.RequestedCompression != st.Compression {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render(fmt.Sprintf("Requested compression: %s", st.RequestedCompression)))
	}
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Duration: " + formatDuration(st.Duration)))

	// Mirror logBackupArtifactPaths (bundle case shown contents-then-path). A base
	// (non-bundle) run has no "Bundle contents" line: it shows "Archive path" plus
	// the manifest/checksum, so this line adapts to the configured mode.
	if st.BundleCreated {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Bundle contents: archive + checksum + metadata"))
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Bundle path: " + st.ArchivePath))
		return
	}
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Archive path: " + st.ArchivePath))
	if st.ManifestPath != "" {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Manifest path: " + st.ManifestPath))
	}
	if st.Checksum != "" {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Archive checksum (SHA256): " + st.Checksum))
	}
}

// runLogPath returns the path of the run's log file for the outcome "Log:" line.
// It prefers the live default logger's path, but that logger's file is CLOSED
// during the backup log-management phase before the outcome is built, so
// GetLogFilePath may be "" by then; it then falls back to the LOG_FILE env var the
// runtime exports at startup (main_runtime.go os.Setenv("LOG_FILE", logFilePath)).
func runLogPath() string {
	if p := logging.GetDefaultLogger().GetLogFilePath(); p != "" {
		return p
	}
	return strings.TrimSpace(os.Getenv("LOG_FILE"))
}

// appendRunIssueSummary appends a warnings/errors recap to the backup outcome: a
// colored count header (yellow when only warnings, red once any error was logged)
// followed by the captured "[ts] LEVEL msg" issue lines. Nothing is emitted for a
// clean run. The list is capped so a noisy run cannot overflow the outcome block;
// the full list stays scrollable in the panel above. It reads the SAME logger the
// CLI footer's printRunIssueSummary reads, so the counts and lines match exactly.
func appendRunIssueSummary(b *strings.Builder, logger *logging.Logger) {
	if logger == nil {
		return
	}
	issues := logger.IssueLines()
	if len(issues) == 0 {
		return
	}

	// Notify/communication failures are warning-weight for the run status but are
	// shown as errors in the recap, so count them under errors here.
	errCount := logger.ErrorCount() + logger.NotifyCount()
	headerStyle := theme.WarningText
	if errCount > 0 {
		headerStyle = theme.ErrorText
	}
	b.WriteString("\n\n")
	b.WriteString(headerStyle.Render(fmt.Sprintf("%s Warnings/errors during run: %d warning(s), %d error(s)",
		theme.SymbolWarning, logger.WarningCount(), errCount)))

	const maxLines = 10
	shown := issues
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	for _, line := range shown {
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render("  " + logging.NormalizeNotifyErrorToken(line)))
	}
	if extra := len(issues) - len(shown); extra > 0 {
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render(fmt.Sprintf("  ... and %d more (scroll up to review)", extra)))
	}
}

// appendBackupStatusLine writes a themed "<label>: <status>" storage line, colored
// by the orchestrator's status vocabulary (ok/error/warning/disabled). An empty
// status is skipped so no blank line is emitted.
func appendBackupStatusLine(b *strings.Builder, label, status string) {
	status = strings.TrimSpace(status)
	if status == "" {
		return
	}
	b.WriteString("\n")
	b.WriteString(theme.Text.Render(label + ": "))
	switch status {
	case "ok":
		b.WriteString(theme.SuccessText.Render(status))
	case "error":
		b.WriteString(theme.ErrorText.Render(status))
	case "warning":
		b.WriteString(theme.WarningText.Render(status))
	default: // disabled / unknown: neutral
		b.WriteString(theme.Subtle.Render(status))
	}
}
