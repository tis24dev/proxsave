package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

const defaultNetworkRollbackTimeout = 90 * time.Second

type networkRollbackHandle struct {
	workDir    string
	markerPath string
	unitName   string
	scriptPath string
	logPath    string
	armedAt    time.Time
	timeout    time.Duration
}

func (h *networkRollbackHandle) remaining(now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	rem := h.timeout - now.Sub(h.armedAt)
	if rem < 0 {
		return 0
	}
	return rem
}

func shouldAttemptNetworkApply(plan *RestorePlan) bool {
	if plan == nil {
		return false
	}
	return hasCategoryID(plan.NormalCategories, "network")
}

func maybeApplyNetworkConfigCLI(ctx context.Context, reader *bufio.Reader, logger *logging.Logger, plan *RestorePlan, safetyBackup, networkRollbackBackup *SafetyBackupResult, archivePath string, dryRun bool) (err error) {
	if !shouldAttemptNetworkApply(plan) {
		if logger != nil {
			logger.Debug("Network safe apply (CLI): skipped (network category not selected)")
		}
		return nil
	}
	done := logging.DebugStart(logger, "network safe apply (cli)", "dryRun=%v euid=%d archive=%s", dryRun, os.Geteuid(), strings.TrimSpace(archivePath))
	defer func() { done(err) }()

	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping live network apply: non-system filesystem in use")
		return nil
	}
	if dryRun {
		logger.Info("Dry run enabled: skipping live network apply")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping live network apply: requires root privileges")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Resolve rollback backup paths")
	networkRollbackPath := ""
	if networkRollbackBackup != nil {
		networkRollbackPath = strings.TrimSpace(networkRollbackBackup.BackupPath)
	}
	fullRollbackPath := ""
	if safetyBackup != nil {
		fullRollbackPath = strings.TrimSpace(safetyBackup.BackupPath)
	}
	logging.DebugStep(logger, "network safe apply (cli)", "Rollback backup resolved: network=%q full=%q", networkRollbackPath, fullRollbackPath)
	if networkRollbackPath == "" && fullRollbackPath == "" {
		logger.Warning("Skipping live network apply: rollback backup not available")
		repairNow, err := promptYesNo(ctx, reader, "Attempt NIC name repair in restored network config files now (no reload)? (y/N): ")
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (cli)", "User choice: repairNow=%v", repairNow)
		if repairNow {
			_ = maybeRepairNICNamesCLI(ctx, reader, logger, archivePath)
		}
		logger.Info("Skipping live network apply (you can reboot or apply manually later).")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Prompt: apply network now with rollback timer")
	applyNow, err := promptYesNo(ctx, reader, "Apply restored network configuration now with automatic rollback (90s)? (y/N): ")
	if err != nil {
		return err
	}
	logging.DebugStep(logger, "network safe apply (cli)", "User choice: applyNow=%v", applyNow)
	if !applyNow {
		repairNow, err := promptYesNo(ctx, reader, "Attempt NIC name repair in restored network config files now (no reload)? (y/N): ")
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (cli)", "User choice: repairNow=%v", repairNow)
		if repairNow {
			_ = maybeRepairNICNamesCLI(ctx, reader, logger, archivePath)
		}
		logger.Info("Skipping live network apply (you can reboot or apply manually later).")
		return nil
	}

	rollbackPath := networkRollbackPath
	if rollbackPath == "" {
		if fullRollbackPath == "" {
			logger.Warning("Skipping live network apply: rollback backup not available")
			return nil
		}
		logging.DebugStep(logger, "network safe apply (cli)", "Prompt: network-only rollback missing; allow full rollback backup fallback")
		ok, err := promptYesNo(ctx, reader, "Network-only rollback backup not available. Use full safety backup for rollback instead (may revert other restored categories)? (y/N): ")
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (cli)", "User choice: allowFullRollback=%v", ok)
		if !ok {
			repairNow, err := promptYesNo(ctx, reader, "Attempt NIC name repair in restored network config files now (no reload)? (y/N): ")
			if err != nil {
				return err
			}
			logging.DebugStep(logger, "network safe apply (cli)", "User choice: repairNow=%v", repairNow)
			if repairNow {
				_ = maybeRepairNICNamesCLI(ctx, reader, logger, archivePath)
			}
			logger.Info("Skipping live network apply (you can reboot or apply manually later).")
			return nil
		}
		rollbackPath = fullRollbackPath
	}
	logging.DebugStep(logger, "network safe apply (cli)", "Selected rollback backup: %s", rollbackPath)

	systemType := SystemTypeUnknown
	if plan != nil {
		systemType = plan.SystemType
	}
	if err := applyNetworkWithRollbackCLI(ctx, reader, logger, rollbackPath, archivePath, defaultNetworkRollbackTimeout, systemType); err != nil {
		return err
	}
	return nil
}

func applyNetworkWithRollbackCLI(ctx context.Context, reader *bufio.Reader, logger *logging.Logger, backupPath, archivePath string, timeout time.Duration, systemType SystemType) (err error) {
	done := logging.DebugStart(logger, "network safe apply (cli)", "rollbackBackup=%s timeout=%s systemType=%s", strings.TrimSpace(backupPath), timeout, systemType)
	defer func() { done(err) }()

	logging.DebugStep(logger, "network safe apply (cli)", "Create diagnostics directory")
	diagnosticsDir, err := createNetworkDiagnosticsDir()
	if err != nil {
		logger.Warning("Network diagnostics disabled: %v", err)
		diagnosticsDir = ""
	} else {
		logger.Info("Network diagnostics directory: %s", diagnosticsDir)
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Detect management interface (SSH/default route)")
	iface, source := detectManagementInterface(ctx, logger)
	if iface != "" {
		logger.Info("Detected management interface: %s (%s)", iface, source)
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (cli)", "Capture network snapshot (before)")
		if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "before", 3*time.Second); err != nil {
			logger.Debug("Network snapshot before apply failed: %v", err)
		} else {
			logger.Debug("Network snapshot (before): %s", snap)
		}
	}

	logging.DebugStep(logger, "network safe apply (cli)", "NIC name repair (optional)")
	_ = maybeRepairNICNamesCLI(ctx, reader, logger, archivePath)

	logging.DebugStep(logger, "network safe apply (cli)", "Network preflight validation (ifupdown/ifupdown2)")
	preflight := runNetworkPreflightValidation(ctx, 5*time.Second, logger)
	if diagnosticsDir != "" {
		if path, err := writeNetworkPreflightReportFile(diagnosticsDir, preflight); err != nil {
			logger.Debug("Failed to write network preflight report: %v", err)
		} else {
			logger.Debug("Network preflight report: %s", path)
		}
	}
	if !preflight.Ok() {
		logger.Warning("%s", preflight.Summary())
		if details := strings.TrimSpace(preflight.Output); details != "" {
			fmt.Println("Network preflight output:")
			fmt.Println(details)
		}
		if diagnosticsDir != "" {
			fmt.Printf("Network diagnostics saved under: %s\n", diagnosticsDir)
		}
		return fmt.Errorf("network preflight validation failed; aborting live network apply")
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Arm rollback timer BEFORE applying changes")
	handle, err := armNetworkRollback(ctx, logger, backupPath, timeout, diagnosticsDir)
	if err != nil {
		return err
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Apply network configuration now")
	if err := applyNetworkConfig(ctx, logger); err != nil {
		logger.Warning("Network apply failed: %v", err)
		return err
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (cli)", "Capture network snapshot (after)")
		if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "after", 3*time.Second); err != nil {
			logger.Debug("Network snapshot after apply failed: %v", err)
		} else {
			logger.Debug("Network snapshot (after): %s", snap)
		}
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Run post-apply health checks")
	health := runNetworkHealthChecks(ctx, networkHealthOptions{
		SystemType:         systemType,
		Logger:             logger,
		CommandTimeout:     3 * time.Second,
		EnableGatewayPing:  true,
		ForceSSHRouteCheck: false,
		EnableDNSResolve:   true,
		LocalPortChecks:    defaultNetworkPortChecks(systemType),
	})
	logNetworkHealthReport(logger, health)
	fmt.Println(health.Details())
	if diagnosticsDir != "" {
		if path, err := writeNetworkHealthReportFile(diagnosticsDir, health); err != nil {
			logger.Debug("Failed to write network health report: %v", err)
		} else {
			logger.Debug("Network health report: %s", path)
		}
		fmt.Printf("Network diagnostics saved under: %s\n", diagnosticsDir)
	}
	if health.Severity == networkHealthCritical {
		fmt.Println("CRITICAL: Connectivity checks failed. Recommended action: do NOT commit and let rollback run.")
	}

	remaining := handle.remaining(time.Now())
	if remaining <= 0 {
		logger.Warning("Rollback window already expired; leaving rollback armed")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (cli)", "Wait for COMMIT (rollback in %ds)", int(remaining.Seconds()))
	committed, err := promptNetworkCommitWithCountdown(ctx, reader, logger, remaining)
	if err != nil {
		logger.Warning("Commit prompt error: %v", err)
	}
	logging.DebugStep(logger, "network safe apply (cli)", "User commit result: committed=%v", committed)
	if committed {
		disarmNetworkRollback(ctx, logger, handle)
		logger.Info("Network configuration committed successfully.")
		return nil
	}
	logger.Warning("Network configuration not committed; rollback will run automatically.")
	return nil
}

func armNetworkRollback(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (handle *networkRollbackHandle, err error) {
	done := logging.DebugStart(logger, "arm network rollback", "backup=%s timeout=%s workDir=%s", strings.TrimSpace(backupPath), timeout, strings.TrimSpace(workDir))
	defer func() { done(err) }()

	if strings.TrimSpace(backupPath) == "" {
		return nil, fmt.Errorf("empty safety backup path")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("invalid rollback timeout")
	}

	logging.DebugStep(logger, "arm network rollback", "Prepare rollback work directory")
	baseDir := strings.TrimSpace(workDir)
	perm := os.FileMode(0o755)
	if baseDir == "" {
		baseDir = "/tmp/proxsave"
	} else {
		perm = 0o700
	}
	if err := restoreFS.MkdirAll(baseDir, perm); err != nil {
		return nil, fmt.Errorf("create rollback directory: %w", err)
	}
	timestamp := nowRestore().Format("20060102_150405")
	handle = &networkRollbackHandle{
		workDir:    baseDir,
		markerPath: filepath.Join(baseDir, fmt.Sprintf("network_rollback_pending_%s", timestamp)),
		scriptPath: filepath.Join(baseDir, fmt.Sprintf("network_rollback_%s.sh", timestamp)),
		logPath:    filepath.Join(baseDir, fmt.Sprintf("network_rollback_%s.log", timestamp)),
		armedAt:    time.Now(),
		timeout:    timeout,
	}

	logging.DebugStep(logger, "arm network rollback", "Write rollback marker: %s", handle.markerPath)
	if err := restoreFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback marker: %w", err)
	}

	logging.DebugStep(logger, "arm network rollback", "Write rollback script: %s", handle.scriptPath)
	script := buildRollbackScript(handle.markerPath, backupPath, handle.logPath)
	if err := restoreFS.WriteFile(handle.scriptPath, []byte(script), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback script: %w", err)
	}

	timeoutSeconds := int(timeout.Seconds())
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	if commandAvailable("systemd-run") {
		logging.DebugStep(logger, "arm network rollback", "Arm timer via systemd-run (%ds)", timeoutSeconds)
		handle.unitName = fmt.Sprintf("proxsave-network-rollback-%s", timestamp)
		args := []string{
			"--unit=" + handle.unitName,
			"--on-active=" + fmt.Sprintf("%ds", timeoutSeconds),
			"/bin/sh",
			handle.scriptPath,
		}
		if output, err := restoreCmd.Run(ctx, "systemd-run", args...); err != nil {
			logger.Warning("systemd-run failed, falling back to background timer: %v", err)
			logger.Debug("systemd-run output: %s", strings.TrimSpace(string(output)))
			handle.unitName = ""
		} else if len(output) > 0 {
			logger.Debug("systemd-run output: %s", strings.TrimSpace(string(output)))
		}
	}

	if handle.unitName == "" {
		logging.DebugStep(logger, "arm network rollback", "Arm timer via background sleep (%ds)", timeoutSeconds)
		cmd := fmt.Sprintf("nohup sh -c 'sleep %d; /bin/sh %s' >/dev/null 2>&1 &", timeoutSeconds, handle.scriptPath)
		if output, err := restoreCmd.Run(ctx, "sh", "-c", cmd); err != nil {
			logger.Debug("Background rollback output: %s", strings.TrimSpace(string(output)))
			return nil, fmt.Errorf("failed to arm rollback timer: %w", err)
		}
	}

	logger.Info("Rollback timer armed (%ds). Work dir: %s (log: %s)", timeoutSeconds, baseDir, handle.logPath)
	return handle, nil
}

func disarmNetworkRollback(ctx context.Context, logger *logging.Logger, handle *networkRollbackHandle) {
	if handle == nil {
		return
	}
	logging.DebugStep(logger, "disarm network rollback", "Disarming rollback (marker=%s unit=%s)", strings.TrimSpace(handle.markerPath), strings.TrimSpace(handle.unitName))
	if handle.markerPath != "" {
		if err := restoreFS.Remove(handle.markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Debug("Failed to remove rollback marker %s: %v", handle.markerPath, err)
		}
	}
	if handle.unitName != "" && commandAvailable("systemctl") {
		if output, err := restoreCmd.Run(ctx, "systemctl", "stop", handle.unitName); err != nil {
			logger.Debug("Failed to stop rollback unit %s: %v (output: %s)", handle.unitName, err, strings.TrimSpace(string(output)))
		}
	}
}

func maybeRepairNICNamesCLI(ctx context.Context, reader *bufio.Reader, logger *logging.Logger, archivePath string) *nicRepairResult {
	logging.DebugStep(logger, "NIC repair", "Plan NIC name repair (archive=%s)", strings.TrimSpace(archivePath))
	plan, err := planNICNameRepair(ctx, archivePath)
	if err != nil {
		logger.Warning("NIC name repair plan failed: %v", err)
		return nil
	}
	if plan == nil {
		return nil
	}
	logging.DebugStep(logger, "NIC repair", "Plan result: mappingEntries=%d safe=%d conflicts=%d skippedReason=%q", len(plan.Mapping.Entries), len(plan.SafeMappings), len(plan.Conflicts), strings.TrimSpace(plan.SkippedReason))

	if plan.SkippedReason != "" && !plan.HasWork() {
		logger.Info("NIC name repair skipped: %s", plan.SkippedReason)
		return &nicRepairResult{AppliedAt: nowRestore(), SkippedReason: plan.SkippedReason}
	}

	if !plan.Mapping.IsEmpty() {
		logger.Debug("NIC mapping source: %s", strings.TrimSpace(plan.Mapping.BackupSourcePath))
		logger.Debug("NIC mapping details:\n%s", plan.Mapping.Details())
	}

	includeConflicts := false
	if len(plan.Conflicts) > 0 {
		fmt.Println("NIC name conflicts detected:")
		for _, conflict := range plan.Conflicts {
			fmt.Println(conflict.Details())
		}
		ok, err := promptYesNo(ctx, reader, "Apply NIC rename mapping even when conflicting interface names exist on this system? (y/N): ")
		if err != nil {
			logger.Warning("NIC conflict prompt failed: %v", err)
		} else if ok {
			includeConflicts = true
		}
	}
	logging.DebugStep(logger, "NIC repair", "Apply conflicts=%v (conflictCount=%d)", includeConflicts, len(plan.Conflicts))

	logging.DebugStep(logger, "NIC repair", "Apply NIC rename mapping to /etc/network/interfaces*")
	result, err := applyNICNameRepair(logger, plan, includeConflicts)
	if err != nil {
		logger.Warning("NIC name repair failed: %v", err)
		return nil
	}
	if len(plan.Conflicts) > 0 && !includeConflicts {
		fmt.Println("Note: conflicting NIC mappings were skipped.")
	}
	if result != nil {
		if result.Applied() {
			fmt.Println(result.Details())
		} else if result.SkippedReason != "" {
			logger.Info("%s", result.Summary())
		} else {
			logger.Debug("%s", result.Summary())
		}
	}
	return result
}

func applyNetworkConfig(ctx context.Context, logger *logging.Logger) error {
	switch {
	case commandAvailable("ifreload"):
		logging.DebugStep(logger, "network apply", "Reload networking: ifreload -a")
		return runCommandLogged(ctx, logger, "ifreload", "-a")
	case commandAvailable("systemctl"):
		logging.DebugStep(logger, "network apply", "Reload networking: systemctl restart networking")
		return runCommandLogged(ctx, logger, "systemctl", "restart", "networking")
	case commandAvailable("ifup"):
		logging.DebugStep(logger, "network apply", "Reload networking: ifup -a")
		return runCommandLogged(ctx, logger, "ifup", "-a")
	default:
		return fmt.Errorf("no supported network reload command found (ifreload/systemctl/ifup)")
	}
}

func detectManagementInterface(ctx context.Context, logger *logging.Logger) (string, string) {
	if ip := parseSSHClientIP(); ip != "" {
		if iface := routeInterfaceForIP(ctx, ip); iface != "" {
			return iface, "ssh"
		}
		logger.Debug("Unable to map SSH client %s to an interface", ip)
	}

	if iface := defaultRouteInterface(ctx); iface != "" {
		return iface, "default-route"
	}
	return "", ""
}

func parseSSHClientIP() string {
	if v := strings.TrimSpace(os.Getenv("SSH_CONNECTION")); v != "" {
		fields := strings.Fields(v)
		if len(fields) > 0 {
			return fields[0]
		}
	}
	if v := strings.TrimSpace(os.Getenv("SSH_CLIENT")); v != "" {
		fields := strings.Fields(v)
		if len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

func routeInterfaceForIP(ctx context.Context, ip string) string {
	output, err := restoreCmd.Run(ctx, "ip", "route", "get", ip)
	if err != nil {
		return ""
	}
	return parseRouteDevice(string(output))
}

func defaultRouteInterface(ctx context.Context) string {
	output, err := restoreCmd.Run(ctx, "ip", "route", "show", "default")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) == 0 {
		return ""
	}
	return parseRouteDevice(lines[0])
}

func parseRouteDevice(output string) string {
	fields := strings.Fields(output)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "dev" {
			return fields[i+1]
		}
	}
	return ""
}

func defaultNetworkPortChecks(systemType SystemType) []tcpPortCheck {
	switch systemType {
	case SystemTypePVE:
		return []tcpPortCheck{
			{Name: "PVE web UI", Address: "127.0.0.1", Port: 8006},
		}
	case SystemTypePBS:
		return []tcpPortCheck{
			{Name: "PBS web UI", Address: "127.0.0.1", Port: 8007},
		}
	default:
		return nil
	}
}

func promptNetworkCommitWithCountdown(ctx context.Context, reader *bufio.Reader, logger *logging.Logger, remaining time.Duration) (bool, error) {
	if remaining <= 0 {
		return false, context.DeadlineExceeded
	}

	fmt.Printf("Type COMMIT within %d seconds to keep the new network configuration.\n", int(remaining.Seconds()))
	deadline := time.Now().Add(remaining)
	ctxTimeout, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	inputCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		line, err := input.ReadLineWithContext(ctxTimeout, reader)
		if err != nil {
			errCh <- err
			return
		}
		inputCh <- line
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			left := time.Until(deadline)
			if left < 0 {
				left = 0
			}
			fmt.Fprintf(os.Stderr, "\rRollback in %ds... Type COMMIT to keep: ", int(left.Seconds()))
			if left <= 0 {
				fmt.Fprintln(os.Stderr)
				return false, context.DeadlineExceeded
			}
		case line := <-inputCh:
			fmt.Fprintln(os.Stderr)
			if strings.EqualFold(strings.TrimSpace(line), "commit") {
				return true, nil
			}
			return false, nil
		case err := <-errCh:
			fmt.Fprintln(os.Stderr)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return false, err
			}
			logger.Debug("Commit input error: %v", err)
			return false, err
		}
	}
}

func buildRollbackScript(markerPath, backupPath, logPath string) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		fmt.Sprintf("LOG=%s", shellQuote(logPath)),
		fmt.Sprintf("MARKER=%s", shellQuote(markerPath)),
		fmt.Sprintf("BACKUP=%s", shellQuote(backupPath)),
		`if [ ! -f "$MARKER" ]; then exit 0; fi`,
		`echo "Rollback started at $(date -Is)" >> "$LOG"`,
		`echo "Rollback backup: $BACKUP" >> "$LOG"`,
		`echo "Extract phase: restore files from rollback archive" >> "$LOG"`,
		`TAR_OK=0`,
		`if tar -xzf "$BACKUP" -C / >> "$LOG" 2>&1; then TAR_OK=1; echo "Extract phase: OK" >> "$LOG"; else echo "WARN: failed to extract rollback archive; skipping prune phase" >> "$LOG"; fi`,
		`if [ "$TAR_OK" -eq 1 ] && [ -d /etc/network ]; then`,
		`  echo "Prune phase: removing files created after backup (network-only)" >> "$LOG"`,
		`  echo "Prune scope: /etc/network (+ /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg, /etc/dnsmasq.d/lxc-vmbr1.conf)" >> "$LOG"`,
		`  (`,
		`    set +e`,
		`    MANIFEST_ALL=$(mktemp /tmp/proxsave/network_rollback_manifest_all_XXXXXX 2>/dev/null)`,
		`    MANIFEST=$(mktemp /tmp/proxsave/network_rollback_manifest_XXXXXX 2>/dev/null)`,
		`    CANDIDATES=$(mktemp /tmp/proxsave/network_rollback_candidates_XXXXXX 2>/dev/null)`,
		`    CLEANUP=$(mktemp /tmp/proxsave/network_rollback_cleanup_XXXXXX 2>/dev/null)`,
		`    if [ -z "$MANIFEST_ALL" ] || [ -z "$MANIFEST" ] || [ -z "$CANDIDATES" ] || [ -z "$CLEANUP" ]; then`,
		`      echo "WARN: mktemp failed; skipping prune"`,
		`      exit 0`,
		`    fi`,
		`    echo "Listing rollback archive contents..."`,
		`    if ! tar -tzf "$BACKUP" > "$MANIFEST_ALL"; then`,
		`      echo "WARN: failed to list rollback archive; skipping prune"`,
		`      rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`      exit 0`,
		`    fi`,
		`    echo "Normalizing manifest paths..."`,
		`    sed 's#^\./##' "$MANIFEST_ALL" > "$MANIFEST"`,
		`    if ! grep -q '^etc/network/' "$MANIFEST"; then`,
		`      echo "WARN: rollback archive does not include etc/network; skipping prune"`,
		`      rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`      exit 0`,
		`    fi`,
		`    echo "Scanning current filesystem under /etc/network..."`,
		`    find /etc/network -mindepth 1 \( -type f -o -type l \) -print > "$CANDIDATES" 2>/dev/null || true`,
		`    echo "Computing cleanup list (present on disk, absent in backup)..."`,
		`    : > "$CLEANUP"`,
		`    while IFS= read -r path; do`,
		`      rel=${path#/}`,
		`      if ! grep -Fxq "$rel" "$MANIFEST"; then`,
		`        echo "$path" >> "$CLEANUP"`,
		`      fi`,
		`    done < "$CANDIDATES"`,
		`    for extra in /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg /etc/dnsmasq.d/lxc-vmbr1.conf; do`,
		`      if [ -e "$extra" ] || [ -L "$extra" ]; then`,
		`        rel=${extra#/}`,
		`        if ! grep -Fxq "$rel" "$MANIFEST"; then`,
		`          echo "$extra" >> "$CLEANUP"`,
		`        fi`,
		`      fi`,
		`    done`,
		`    if [ -s "$CLEANUP" ]; then`,
		`      echo "Pruning extraneous network files (not present in backup):"`,
		`      cat "$CLEANUP"`,
		`      while IFS= read -r rmPath; do`,
		`        rm -f -- "$rmPath" || true`,
		`      done < "$CLEANUP"`,
		`    else`,
		`      echo "No extraneous network files to prune."`,
		`    fi`,
		`    echo "Prune phase: done"`,
		`    rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`  ) >> "$LOG" 2>&1 || true`,
		`fi`,
		`echo "Restart networking after rollback" >> "$LOG"`,
		`if command -v ifreload >/dev/null 2>&1; then ifreload -a >> "$LOG" 2>&1 || true;`,
		`elif command -v systemctl >/dev/null 2>&1; then systemctl restart networking >> "$LOG" 2>&1 || true;`,
		`elif command -v ifup >/dev/null 2>&1; then ifup -a >> "$LOG" 2>&1 || true;`,
		`fi`,
		`rm -f "$MARKER"`,
		`echo "Rollback finished at $(date -Is)" >> "$LOG"`,
	}
	return strings.Join(lines, "\n") + "\n"
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\"'\\$&;|<>") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func runCommandLogged(ctx context.Context, logger *logging.Logger, name string, args ...string) error {
	if logger != nil {
		logger.Debug("Running command: %s %s", name, strings.Join(args, " "))
	}
	output, err := restoreCmd.Run(ctx, name, args...)
	if len(output) > 0 {
		logger.Debug("%s output: %s", name, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("%s %v failed: %w", name, args, err)
	}
	return nil
}
