package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
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
		// The handoff vanished (CLI/cron/daemon): run the backup plain.
		return backupStreamSteps(opts)
	}

	var res backupModeResult
	streamErr := components.RunStreamTask(opts.ctx, session, "Running backup",
		func(taskCtx context.Context, emit func(line string)) (string, error) {
			// Capture the default + COLORED bootstrap-mirror loggers into the raw
			// sink; restore on return/panic. The real backup logs via both, so its
			// [ts] LEVEL lines flow (colored) into the contained viewport instead
			// of the raw altscreen.
			sink := logging.NewLineWriterRaw(emit)
			defer logging.CaptureConsoleWithColor(opts.bootstrap, sink)()

			// Thread taskCtx so an Esc cancel propagates into the running backup.
			stepOpts := opts
			stepOpts.ctx = taskCtx
			res = backupStreamSteps(stepOpts)
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

	sev := exitCodeSeverity(res.exitCode, logging.GetDefaultLogger())
	b.WriteString(renderBackupBanner(sev))

	st := res.supportStats
	if st == nil {
		return b.String()
	}

	b.WriteString("\n")

	// Files: N collected (M failed) - the failed count is only shown (in yellow)
	// when non-zero, so a clean run reads simply "Files: N collected".
	b.WriteString("\n")
	b.WriteString(theme.Text.Render(fmt.Sprintf("Files: %d collected", st.FilesCollected)))
	if st.FilesFailed > 0 {
		b.WriteString(theme.WarningText.Render(fmt.Sprintf(" (%d failed)", st.FilesFailed)))
	}

	if st.ArchiveSize > 0 {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Size: " + backup.FormatBytes(st.ArchiveSize)))
	}
	if st.Duration > 0 {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Duration: " + st.Duration.Round(time.Second).String()))
	}
	if p := strings.TrimSpace(st.ArchivePath); p != "" {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Archive: " + p))
	}

	// Local is always shown when known; Secondary/Cloud only when they carry a
	// meaningful (non-disabled) status, so a single-destination run stays terse.
	appendBackupStatusLine(&b, "Local", st.LocalStatus)
	if s := strings.TrimSpace(st.SecondaryStatus); s != "" && s != "disabled" {
		appendBackupStatusLine(&b, "Secondary", st.SecondaryStatus)
	}
	if s := strings.TrimSpace(st.CloudStatus); s != "" && s != "disabled" {
		appendBackupStatusLine(&b, "Cloud", st.CloudStatus)
	}

	return b.String()
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
