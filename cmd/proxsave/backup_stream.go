package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// backupStreamSteps is the seam runBackupStreamed drives to run the real backup.
// It points at runBackupModeSteps in production; tests override it with a stub
// that emits a couple of log lines (captured -> streamed) and returns a canned
// result, so the capture->stream plumbing can be exercised without a real backup.
var backupStreamSteps = runBackupModeSteps

// runBackupStreamed runs the backup INSIDE the graphical dashboard session,
// streaming its [ts] LEVEL log lines into a StreamTask exactly like the install
// finalization (runInstallTUI). It adopts the session the dashboard stashed on
// the "Run backup now" choice; if that handoff has vanished (already adopted, or
// never stashed) it falls back to the plain steps so the backup always runs.
//
// The captured console (logging.CaptureConsole) routes the default logger + the
// bootstrap mirror into the stream sink, so both the run's logging.Info lines and
// the bootstrap finalization lines appear in-graphics instead of corrupting the
// alternate screen. taskCtx is threaded into the backup so Esc cancels the run;
// the session is closed only after the user presses Continue, matching the
// install finalization's teardown.
func runBackupStreamed(opts backupModeOptions) backupModeResult {
	useColor := true
	if opts.cfg != nil {
		useColor = opts.cfg.UseColor
	}
	session := adoptDashboardSession(shell.Config{
		AppName:  "ProxSave",
		Subtitle: "Backup",
		UseColor: useColor,
	})
	if session == nil {
		// The handoff vanished: run the backup plain (no graphical session).
		return backupStreamSteps(opts)
	}

	var res backupModeResult
	streamErr := components.RunStreamTask(opts.ctx, session, "Running backup",
		func(taskCtx context.Context, emit func(line string)) (string, error) {
			// Capture the default + bootstrap-mirror loggers into the UI stream;
			// restore on return/panic. The real backup logs via both, so its
			// [ts] LEVEL lines flow into the growing list instead of the altscreen.
			sink := logging.NewLineWriter(emit)
			defer logging.CaptureConsole(opts.bootstrap, sink)()

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
	// prints to the plain scrollback (the in-graphics lines vanish with the
	// alternate screen, matching the install finalization).
	if closeErr := session.Close(); closeErr != nil {
		logging.DebugStepBootstrap(opts.bootstrap, "dashboard", "session close: %v", closeErr)
	}
	return res
}

// buildBackupOutcomePrompt composes the pre-styled outcome block shown at the
// bottom of the streamed backup screen (StreamDoneMsg.Outcome), mirroring
// buildInstallOutcomePrompt. It opens with the shared themed banner
// (renderInstallBanner) - green "Backup completed" on success (exitCode ==
// ExitSuccess), red "Backup failed" otherwise, so a context-cancelled run never
// reads as completed - then adds themed stat lines from the run's BackupStats
// when present. Missing/nil data is skipped (never panics).
func buildBackupOutcomePrompt(res backupModeResult) string {
	var b strings.Builder

	if res.exitCode == types.ExitSuccess.Int() {
		b.WriteString(renderInstallBanner(installBannerCompleted, "Backup completed"))
	} else {
		b.WriteString(renderInstallBanner(installBannerFailed, "Backup failed"))
	}

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
