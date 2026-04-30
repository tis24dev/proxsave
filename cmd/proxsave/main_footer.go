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

func printFinalSummary(finalExitCode int) {
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

func finalSummaryColor(finalExitCode int, logger *logging.Logger) string {
	hasWarnings := logger != nil && logger.HasWarnings()

	switch {
	case finalExitCode == exitCodeInterrupted:
		return "\033[35m" // magenta for Ctrl+C
	case finalExitCode == 0 && hasWarnings:
		return "\033[33m" // yellow for success with warnings
	case finalExitCode == 0:
		return "\033[32m" // green for clean success
	case finalExitCode == types.ExitGenericError.Int():
		return "\033[33m" // yellow for generic error (non-fatal)
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
	fmt.Println("  proxsave (alias: proxmox-backup) - Start backup")
	fmt.Println("  --help             - Show all options")
	fmt.Println("  --dry-run          - Test without changes")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep build/env/identity) then run installer")
	fmt.Println("  --env-migration    - Run installer and migrate legacy Bash backup.env to Go template")
	fmt.Println("  --env-migration-dry-run - Preview installer/migration without writing files")
	fmt.Println("  --upgrade          - Update proxsave binary to latest release (also adds missing keys to backup.env)")
	fmt.Println("  --newkey           - Generate a new encryption key for backups")
	fmt.Println("  --decrypt          - Decrypt an existing backup archive")
	fmt.Println("  --restore          - Run interactive restore workflow (select bundle, decrypt if needed, apply to system)")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println("  --support          - Run in support mode (force debug log level and send email with attached log to github-support@tis24.it); available for standard backup and --restore")
	fmt.Println()
}
