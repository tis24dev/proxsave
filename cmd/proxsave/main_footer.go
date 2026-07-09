// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

const rollbackCountdownDisplayDuration = 10 * time.Second

func printNetworkRollbackCountdown(abortInfo *orchestrator.RestoreAbortInfo) {
	if abortInfo == nil {
		return
	}

	color := "\033[33m" // yellow
	colorReset := "\033[0m"
	markerExists := networkRollbackMarkerExists(abortInfo.NetworkRollbackMarker)
	status := networkRollbackStatus(abortInfo, markerExists, time.Now())

	printNetworkRollbackHeader(color, colorReset)
	printNetworkRollbackStaticInfo(abortInfo, status)
	printNetworkRollbackReconnectHint(abortInfo, markerExists, time.Now())

	// Live countdown for max 10 seconds (only when rollback is still armed).
	if !markerExists || abortInfo.RollbackDeadline.IsZero() {
		fmt.Printf("%s===========================================%s\n", color, colorReset)
		return
	}

	printNetworkRollbackLiveCountdown(abortInfo.RollbackDeadline)
	fmt.Printf("%s===========================================%s\n", color, colorReset)
}

func networkRollbackMarkerExists(marker string) bool {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return false
	}
	_, err := os.Stat(marker)
	return err == nil
}

func networkRollbackStatus(abortInfo *orchestrator.RestoreAbortInfo, markerExists bool, now time.Time) string {
	switch {
	case markerExists:
		return "ARMED (will execute automatically)"
	case !abortInfo.RollbackDeadline.IsZero() && now.After(abortInfo.RollbackDeadline):
		return "EXECUTED (marker removed)"
	case strings.TrimSpace(abortInfo.NetworkRollbackMarker) != "":
		return "DISARMED/CLEARED (marker removed before deadline)"
	case abortInfo.NetworkRollbackArmed:
		return "ARMED (status from snapshot)"
	default:
		return "NOT ARMED"
	}
}

func printNetworkRollbackHeader(color, colorReset string) {
	fmt.Println()
	fmt.Printf("%s===========================================\n", color)
	fmt.Printf("NETWORK ROLLBACK%s\n", colorReset)
	fmt.Println()
}

func printNetworkRollbackStaticInfo(abortInfo *orchestrator.RestoreAbortInfo, status string) {
	fmt.Printf("  Status: %s\n", status)
	if knownValue(abortInfo.OriginalIP) {
		fmt.Printf("  Pre-apply IP (from snapshot): %s\n", strings.TrimSpace(abortInfo.OriginalIP))
	}
	if knownValue(abortInfo.CurrentIP) {
		fmt.Printf("  Post-apply IP (observed): %s\n", strings.TrimSpace(abortInfo.CurrentIP))
	}
	if strings.TrimSpace(abortInfo.NetworkRollbackLog) != "" {
		fmt.Printf("  Rollback log: %s\n", strings.TrimSpace(abortInfo.NetworkRollbackLog))
	}
	fmt.Println()
}

func printNetworkRollbackReconnectHint(abortInfo *orchestrator.RestoreAbortInfo, markerExists bool, now time.Time) {
	if printArmedRollbackReconnectHint(abortInfo, markerExists) {
		return
	}
	if printExecutedRollbackReconnectHint(abortInfo, markerExists, now) {
		return
	}
	printDisarmedRollbackReconnectHint(abortInfo, markerExists)
}

func printArmedRollbackReconnectHint(abortInfo *orchestrator.RestoreAbortInfo, markerExists bool) bool {
	if !markerExists || abortInfo.RollbackDeadline.IsZero() || time.Until(abortInfo.RollbackDeadline) <= 0 {
		return false
	}
	fmt.Println("Connection will be temporarily interrupted during restore.")
	if knownValue(abortInfo.OriginalIP) {
		fmt.Printf("Remember to reconnect using the pre-apply IP: %s\n", strings.TrimSpace(abortInfo.OriginalIP))
	}
	return true
}

func printExecutedRollbackReconnectHint(abortInfo *orchestrator.RestoreAbortInfo, markerExists bool, now time.Time) bool {
	if markerExists || abortInfo.RollbackDeadline.IsZero() || !now.After(abortInfo.RollbackDeadline) {
		return false
	}
	if knownValue(abortInfo.OriginalIP) {
		fmt.Printf("Rollback executed: reconnect using the pre-apply IP: %s\n", strings.TrimSpace(abortInfo.OriginalIP))
	}
	return true
}

func printDisarmedRollbackReconnectHint(abortInfo *orchestrator.RestoreAbortInfo, markerExists bool) {
	if markerExists || strings.TrimSpace(abortInfo.NetworkRollbackMarker) == "" || !knownValue(abortInfo.CurrentIP) {
		return
	}
	fmt.Printf("Rollback will NOT run: reconnect using the post-apply IP: %s\n", strings.TrimSpace(abortInfo.CurrentIP))
}

func knownValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "unknown"
}

func printNetworkRollbackLiveCountdown(deadline time.Time) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	displayEnd := time.Now().Add(rollbackCountdownDisplayDuration)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			fmt.Printf("\r  Remaining: Rollback executing now...          \n")
			break
		}
		if time.Now().After(displayEnd) {
			fmt.Printf("\r  Remaining: %ds (exiting, rollback will proceed)\n", int(remaining.Seconds()))
			break
		}
		fmt.Printf("\r  Remaining: %ds   ", int(remaining.Seconds()))

		<-ticker.C
	}
}

// printRunFooter is the SINGLE place that gates every end-of-run CLI footer.
// Route a footer's printing through here (as emit): it runs only for a
// non-graphical run; a graphical run (launched from the dashboard, which adopts
// the session) already shows its outcome on-screen, so any plain-scrollback CLI
// footer, with its usage-commands and sponsors block, is redundant and would leak
// after the alternate screen closes. To add a NEW suppressible footer, print it
// via printRunFooter and nothing else needs to know about the gate.
// dashboardRunWasGraphical() latches true only once a flow adopts the dashboard
// session; a plain CLI/cron run never adopts, so the footer prints there as before.
func printRunFooter(emit func()) {
	if dashboardRunWasGraphical() {
		return
	}
	emit()
}

func printFinalSummary(finalExitCode int) {
	printRunFooter(func() { finalSummaryBody(finalExitCode) })
}

func finalSummaryBody(finalExitCode int) {
	fmt.Println()

	logger := logging.GetDefaultLogger()
	printRunIssueSummary(logger)
	printFinalSummaryHeader(finalSummarySignature(), finalSummaryColor(finalExitCode, logger))
	printFinalSummaryCommands()
}

func finalSummarySignature() string {
	summarySig := buildSignature()
	if summarySig == "" {
		return "unknown"
	}
	return summarySig
}

// exitSeverity is the display-only classification of a run's exit code, shared
// by the final summary footer and the backup outcome banner so both color the
// SAME exit code identically. It carries no exit semantics of its own.
type exitSeverity int

const (
	severityOK exitSeverity = iota
	severityWarning
	severityError
	severityInterrupted
)

// exitCodeSeverity classifies an exit code into an exitSeverity, encoding the
// EXACT decision tree finalSummaryColor has always used: an interrupt (Ctrl+C)
// is its own bucket; a clean 0 is a warning if the run logged warnings, else OK;
// ExitGenericError is a non-fatal warning; everything else is an error. logger
// may be nil (treated as no warnings).
func exitCodeSeverity(exitCode int, logger *logging.Logger) exitSeverity {
	hasWarnings := logger != nil && logger.HasWarnings()

	switch {
	case exitCode == exitCodeInterrupted:
		return severityInterrupted
	case exitCode == 0 && hasWarnings:
		return severityWarning
	case exitCode == 0:
		return severityOK
	case exitCode == types.ExitGenericError.Int():
		return severityWarning
	default:
		return severityError
	}
}

func finalSummaryColor(finalExitCode int, logger *logging.Logger) string {
	switch exitCodeSeverity(finalExitCode, logger) {
	case severityInterrupted:
		return "\033[35m" // magenta for Ctrl+C
	case severityWarning:
		return "\033[33m" // yellow for success-with-warnings or non-fatal generic error
	case severityOK:
		return "\033[32m" // green for clean success
	default:
		return "\033[31m" // red for all other errors
	}
}

func printRunIssueSummary(logger *logging.Logger) {
	if logger == nil {
		return
	}
	issues := logger.IssueLines()
	if len(issues) == 0 {
		return
	}

	fmt.Println("===========================================")
	fmt.Printf("WARNINGS/ERRORS DURING RUN (warnings=%d errors=%d)\n", logger.WarningCount(), logger.ErrorCount())
	fmt.Println()
	for _, line := range issues {
		fmt.Println(line)
	}
	fmt.Println("===========================================")
	fmt.Println()
}

func printFinalSummaryHeader(summarySig, color string) {
	colorReset := "\033[0m"
	if color != "" {
		fmt.Printf("%s===========================================\n", color)
		fmt.Printf("ProxSave - Go - %s\n", summarySig)
		fmt.Printf("===========================================%s\n", colorReset)
	} else {
		fmt.Println("===========================================")
		fmt.Printf("ProxSave - Go - %s\n", summarySig)
		fmt.Println("===========================================")
	}
}

func printFinalSummaryCommands() {
	fmt.Println()
	fmt.Println("\033[31mEXTRA STEP - IF YOU FIND THIS TOOL USEFUL AND WANT TO THANK ME, A COFFEE IS ALWAYS WELCOME!\033[0m")
	fmt.Println("https://github.com/sponsors/tis24dev")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  proxsave (alias: proxmox-backup) - Open the interactive dashboard (runs the backup directly when non-interactive, e.g. cron)")
	fmt.Println("  --backup           - Run a backup now (what bare proxsave does when non-interactive)")
	fmt.Println("  --help             - Show all options")
	fmt.Println("  --dry-run          - Test without changes")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep build/env/identity) then run installer")
	fmt.Println("  --upgrade          - Update proxsave binary to latest release (also adds missing keys to backup.env)")
	fmt.Println("  --newkey           - Generate a new encryption key for backups")
	fmt.Println("  --decrypt          - Decrypt an existing backup archive")
	fmt.Println("  --restore          - Run interactive restore workflow (select bundle, decrypt if needed, apply to system)")
	fmt.Println("  --cleanup-guards   - Remove leftover restore mount guards once the storage is back online")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println("  --support          - Run in support mode (force debug log level and send email with attached log to the maintainer); available for standard backup and --restore")
	fmt.Println()
}
