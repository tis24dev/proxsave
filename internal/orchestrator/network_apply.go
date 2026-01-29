package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

const defaultNetworkRollbackTimeout = 180 * time.Second

var ErrNetworkApplyNotCommitted = errors.New("network configuration not committed")

type NetworkApplyNotCommittedError struct {
	RollbackLog      string
	RollbackMarker   string
	RestoredIP       string
	OriginalIP       string // IP from backup file (will be restored by rollback)
	RollbackArmed    bool
	RollbackDeadline time.Time // when rollback will execute
}

func (e *NetworkApplyNotCommittedError) Error() string {
	if e == nil {
		return ErrNetworkApplyNotCommitted.Error()
	}
	return ErrNetworkApplyNotCommitted.Error()
}

func (e *NetworkApplyNotCommittedError) Unwrap() error {
	return ErrNetworkApplyNotCommitted
}

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
	return plan.HasCategoryID("network")
}

// extractIPFromSnapshot reads the IP address for a given interface from a network snapshot report file.
// It searches the output section that follows the "$ ip -br addr" command written by writeNetworkSnapshot.
func extractIPFromSnapshot(path, iface string) string {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(iface) == "" {
		return "unknown"
	}
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		return "unknown"
	}

	lines := strings.Split(string(data), "\n")
	inAddrSection := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "$ ip -br addr" {
			inAddrSection = true
			continue
		}
		if strings.HasPrefix(line, "$ ") {
			if inAddrSection {
				break
			}
			continue
		}
		if !inAddrSection || line == "" || strings.HasPrefix(line, "ERROR:") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != iface {
			continue
		}

		// "ip -br addr" can print multiple addresses; prefer IPv4 when available.
		firstIPv6 := ""
		for _, token := range fields[2:] {
			ip := strings.Split(token, "/")[0]
			parsed := net.ParseIP(ip)
			if parsed == nil {
				continue
			}
			if parsed.To4() != nil {
				return ip
			}
			if firstIPv6 == "" {
				firstIPv6 = ip
			}
		}
		if firstIPv6 != "" {
			return firstIPv6
		}
		return "unknown"
	}

	return "unknown"
}

func buildNetworkApplyNotCommittedError(ctx context.Context, logger *logging.Logger, iface string, handle *networkRollbackHandle) *NetworkApplyNotCommittedError {
	logging.DebugStep(logger, "build not-committed error", "Start: iface=%s handle=%v", iface, handle != nil)

	restoredIP := "unknown"
	if strings.TrimSpace(iface) != "" {
		logging.DebugStep(logger, "build not-committed error", "Querying current IP for iface=%s", iface)
		if ep, err := currentNetworkEndpoint(ctx, iface, 1*time.Second); err == nil && len(ep.Addresses) > 0 {
			restoredIP = strings.Join(ep.Addresses, ", ")
			logging.DebugStep(logger, "build not-committed error", "Current IP: %s", restoredIP)
		} else if err != nil {
			logging.DebugStep(logger, "build not-committed error", "Failed to get IP: %v", err)
		}
	}

	rollbackArmed := true
	logging.DebugStep(logger, "build not-committed error", "Checking rollback marker status")
	if handle == nil {
		rollbackArmed = false
		logging.DebugStep(logger, "build not-committed error", "No handle: rollbackArmed=false")
	} else if strings.TrimSpace(handle.markerPath) != "" {
		if _, statErr := restoreFS.Stat(handle.markerPath); statErr != nil {
			// Marker missing => rollback likely already executed (or was manually removed).
			rollbackArmed = false
			logging.DebugStep(logger, "build not-committed error", "Marker missing (%s): rollbackArmed=false", handle.markerPath)
		} else {
			logging.DebugStep(logger, "build not-committed error", "Marker exists (%s): rollbackArmed=true", handle.markerPath)
		}
	}

	rollbackLog := ""
	rollbackMarker := ""
	originalIP := "unknown"
	var rollbackDeadline time.Time
	if handle != nil {
		rollbackLog = strings.TrimSpace(handle.logPath)
		rollbackMarker = strings.TrimSpace(handle.markerPath)
		// Read original IP from before.txt snapshot (IP that will be restored by rollback)
		if strings.TrimSpace(handle.workDir) != "" {
			beforePath := filepath.Join(handle.workDir, "before.txt")
			originalIP = extractIPFromSnapshot(beforePath, iface)
			logging.DebugStep(logger, "build not-committed error", "Original IP from %s: %s", beforePath, originalIP)
		}
		// Calculate rollback deadline
		rollbackDeadline = handle.armedAt.Add(handle.timeout)
		logging.DebugStep(logger, "build not-committed error", "Rollback deadline: %s", rollbackDeadline.Format(time.RFC3339))
	}

	logging.DebugStep(logger, "build not-committed error", "Result: ip=%s originalIP=%s armed=%v log=%s", restoredIP, originalIP, rollbackArmed, rollbackLog)
	return &NetworkApplyNotCommittedError{
		RollbackLog:      rollbackLog,
		RollbackMarker:   rollbackMarker,
		RestoredIP:       strings.TrimSpace(restoredIP),
		OriginalIP:       originalIP,
		RollbackArmed:    rollbackArmed,
		RollbackDeadline: rollbackDeadline,
	}
}

func rollbackAlreadyRunning(ctx context.Context, logger *logging.Logger, handle *networkRollbackHandle) bool {
	if handle == nil || strings.TrimSpace(handle.unitName) == "" {
		logging.DebugStep(logger, "rollback already running", "Skip check: handle=%v unitName=%q", handle != nil, "")
		return false
	}
	if !commandAvailable("systemctl") {
		logging.DebugStep(logger, "rollback already running", "Skip check: systemctl not available")
		return false
	}

	serviceUnit := strings.TrimSpace(handle.unitName) + ".service"
	logging.DebugStep(logger, "rollback already running", "Checking systemctl is-active %s", serviceUnit)

	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := restoreCmd.Run(checkCtx, "systemctl", "is-active", serviceUnit)
	if err != nil {
		logging.DebugStep(logger, "rollback already running", "systemctl is-active failed: %v (assuming not running)", err)
		return false
	}

	state := strings.TrimSpace(string(out))
	running := state == "active" || state == "activating"
	logging.DebugStep(logger, "rollback already running", "Service state=%s running=%v", state, running)
	return running
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
	logging.DebugStep(logger, "arm network rollback", "Parameters validated: backup=%s timeout=%s", backupPath, timeout)

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
	logging.DebugStep(logger, "arm network rollback", "Handle created: marker=%s script=%s log=%s", handle.markerPath, handle.scriptPath, handle.logPath)

	logging.DebugStep(logger, "arm network rollback", "Write rollback marker: %s", handle.markerPath)
	if err := restoreFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback marker: %w", err)
	}
	logging.DebugStep(logger, "arm network rollback", "Marker written successfully")

	logging.DebugStep(logger, "arm network rollback", "Write rollback script: %s", handle.scriptPath)
	script := buildRollbackScript(handle.markerPath, backupPath, handle.logPath, true)
	if err := restoreFS.WriteFile(handle.scriptPath, []byte(script), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback script: %w", err)
	}
	logging.DebugStep(logger, "arm network rollback", "Script written successfully (%d bytes)", len(script))

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
		} else {
			logging.DebugStep(logger, "arm network rollback", "Timer armed via systemd-run: unit=%s", handle.unitName)
			if len(output) > 0 {
				logger.Debug("systemd-run output: %s", strings.TrimSpace(string(output)))
			}
		}
	}

	if handle.unitName == "" {
		logging.DebugStep(logger, "arm network rollback", "Arm timer via background sleep (%ds)", timeoutSeconds)
		cmd := fmt.Sprintf("nohup sh -c 'sleep %d; /bin/sh %s' >/dev/null 2>&1 &", timeoutSeconds, handle.scriptPath)
		if output, err := restoreCmd.Run(ctx, "sh", "-c", cmd); err != nil {
			logger.Debug("Background rollback output: %s", strings.TrimSpace(string(output)))
			return nil, fmt.Errorf("failed to arm rollback timer: %w", err)
		}
		logging.DebugStep(logger, "arm network rollback", "Timer armed via nohup (fallback)")
	}

	logger.Info("Rollback timer armed (%ds). Work dir: %s (log: %s)", timeoutSeconds, baseDir, handle.logPath)
	return handle, nil
}

func disarmNetworkRollback(ctx context.Context, logger *logging.Logger, handle *networkRollbackHandle) {
	if handle == nil {
		logging.DebugStep(logger, "disarm network rollback", "Skip: handle is nil")
		return
	}

	logging.DebugStep(logger, "disarm network rollback", "Start: marker=%s unit=%s", strings.TrimSpace(handle.markerPath), strings.TrimSpace(handle.unitName))

	// Remove marker first so that even if the timer triggers concurrently the rollback script exits early.
	if strings.TrimSpace(handle.markerPath) != "" {
		logging.DebugStep(logger, "disarm network rollback", "Removing marker file: %s", handle.markerPath)
		if err := restoreFS.Remove(handle.markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warning("Failed to remove rollback marker %s: %v", handle.markerPath, err)
		} else {
			logging.DebugStep(logger, "disarm network rollback", "Marker removed successfully")
		}
	}

	if strings.TrimSpace(handle.unitName) != "" && commandAvailable("systemctl") {
		// Stop the timer only. If the service already started, let it finish.
		timerUnit := strings.TrimSpace(handle.unitName) + ".timer"
		logging.DebugStep(logger, "disarm network rollback", "Stopping timer: %s", timerUnit)
		if output, err := restoreCmd.Run(ctx, "systemctl", "stop", timerUnit); err != nil {
			logger.Warning("Failed to stop rollback timer %s: %v (output: %s)", timerUnit, err, strings.TrimSpace(string(output)))
		} else {
			logging.DebugStep(logger, "disarm network rollback", "Timer stopped successfully")
		}

		logging.DebugStep(logger, "disarm network rollback", "Resetting failed units")
		_, _ = restoreCmd.Run(ctx, "systemctl", "reset-failed", strings.TrimSpace(handle.unitName)+".service", timerUnit)
	}

	logging.DebugStep(logger, "disarm network rollback", "Disarm complete")
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

	if !plan.Mapping.IsEmpty() {
		logging.DebugStep(logger, "NIC repair", "Detect persistent NIC naming overrides (udev/systemd)")
		overrides, err := detectNICNamingOverrideRules(logger)
		if err != nil {
			logger.Debug("NIC naming override detection failed: %v", err)
		} else if overrides.Empty() {
			logging.DebugStep(logger, "NIC repair", "No persistent NIC naming overrides detected")
		} else {
			logger.Warning("%s", overrides.Summary())
			logging.DebugStep(logger, "NIC repair", "Naming override details:\n%s", overrides.Details(32))
			fmt.Println()
			fmt.Println("WARNING: Persistent NIC naming rules detected (udev/systemd).")
			fmt.Println("If you use custom rules to keep legacy interface names (e.g. enp3s0 -> eth0), ProxSave NIC repair may rewrite /etc/network/interfaces* to different names.")
			if details := strings.TrimSpace(overrides.Details(8)); details != "" {
				fmt.Println(details)
			}
			skip, err := promptYesNo(ctx, reader, "Skip NIC name repair and keep restored interface names? (y/N): ")
			if err != nil {
				logger.Warning("NIC naming override prompt failed: %v", err)
			} else if skip {
				logging.DebugStep(logger, "NIC repair", "User choice: skip NIC repair due to naming overrides")
				logger.Info("NIC name repair skipped due to persistent naming rules")
				return &nicRepairResult{AppliedAt: nowRestore(), SkippedReason: "skipped due to persistent NIC naming rules (user choice)"}
			} else {
				logging.DebugStep(logger, "NIC repair", "User choice: proceed with NIC repair despite naming overrides")
			}
		}
	}

	includeConflicts := false
	if len(plan.Conflicts) > 0 {
		logging.DebugStep(logger, "NIC repair", "Conflicts detected: %d", len(plan.Conflicts))
		for i, conflict := range plan.Conflicts {
			if i >= 32 {
				logging.DebugStep(logger, "NIC repair", "Conflict details truncated (showing first 32)")
				break
			}
			logging.DebugStep(logger, "NIC repair", "Conflict: %s", conflict.Details())
		}
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
	logging.DebugStep(logger, "prompt commit", "Start: remaining=%s", remaining)

	if remaining <= 0 {
		logging.DebugStep(logger, "prompt commit", "No time remaining, returning timeout")
		return false, context.DeadlineExceeded
	}

	deadline := time.Now().Add(remaining)
	logging.DebugStep(logger, "prompt commit", "Deadline set: %s", deadline.Format(time.RFC3339))

	fmt.Printf("Type COMMIT within %d seconds to keep the new network configuration.\n", int(remaining.Seconds()))
	ctxTimeout, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	inputCh := make(chan string, 1)
	errCh := make(chan error, 1)

	logging.DebugStep(logger, "prompt commit", "Starting input reader goroutine")
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

	logging.DebugStep(logger, "prompt commit", "Waiting for user input...")

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
				logging.DebugStep(logger, "prompt commit", "Timeout expired, returning DeadlineExceeded")
				return false, context.DeadlineExceeded
			}
		case line := <-inputCh:
			fmt.Fprintln(os.Stderr)
			trimmedLine := strings.TrimSpace(line)
			logging.DebugStep(logger, "prompt commit", "User input received: %q", trimmedLine)
			if strings.EqualFold(trimmedLine, "commit") {
				logging.DebugStep(logger, "prompt commit", "Result: COMMITTED")
				return true, nil
			}
			logging.DebugStep(logger, "prompt commit", "Result: NOT COMMITTED (input was not 'commit')")
			return false, nil
		case err := <-errCh:
			fmt.Fprintln(os.Stderr)
			logging.DebugStep(logger, "prompt commit", "Input error received: %v", err)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				logging.DebugStep(logger, "prompt commit", "Result: context deadline/canceled")
				return false, err
			}
			logging.DebugStep(logger, "prompt commit", "Result: NOT COMMITTED (input error)")
			return false, err
		}
	}
}

func rollbackNetworkFilesNow(ctx context.Context, logger *logging.Logger, backupPath, workDir string) (logPath string, err error) {
	done := logging.DebugStart(logger, "rollback network files", "backup=%s workDir=%s", strings.TrimSpace(backupPath), strings.TrimSpace(workDir))
	defer func() { done(err) }()

	if strings.TrimSpace(backupPath) == "" {
		return "", fmt.Errorf("empty rollback backup path")
	}

	baseDir := strings.TrimSpace(workDir)
	perm := os.FileMode(0o755)
	if baseDir == "" {
		baseDir = "/tmp/proxsave"
	} else {
		perm = 0o700
	}
	if err := restoreFS.MkdirAll(baseDir, perm); err != nil {
		return "", fmt.Errorf("create rollback directory: %w", err)
	}

	timestamp := nowRestore().Format("20060102_150405")
	markerPath := filepath.Join(baseDir, fmt.Sprintf("network_rollback_now_pending_%s", timestamp))
	scriptPath := filepath.Join(baseDir, fmt.Sprintf("network_rollback_now_%s.sh", timestamp))
	logPath = filepath.Join(baseDir, fmt.Sprintf("network_rollback_now_%s.log", timestamp))

	logging.DebugStep(logger, "rollback network files", "Write rollback marker: %s", markerPath)
	if err := restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640); err != nil {
		return "", fmt.Errorf("write rollback marker: %w", err)
	}

	logging.DebugStep(logger, "rollback network files", "Write rollback script: %s", scriptPath)
	script := buildRollbackScript(markerPath, backupPath, logPath, false)
	if err := restoreFS.WriteFile(scriptPath, []byte(script), 0o640); err != nil {
		_ = restoreFS.Remove(markerPath)
		return "", fmt.Errorf("write rollback script: %w", err)
	}

	logging.DebugStep(logger, "rollback network files", "Run rollback script now: %s", scriptPath)
	output, runErr := restoreCmd.Run(ctx, "sh", scriptPath)
	if len(output) > 0 {
		logger.Debug("Rollback script output: %s", strings.TrimSpace(string(output)))
	}

	if err := restoreFS.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Debug("Failed to remove rollback marker %s: %v", markerPath, err)
	}

	if runErr != nil {
		return logPath, fmt.Errorf("rollback script failed: %w", runErr)
	}
	return logPath, nil
}

func buildRollbackScript(markerPath, backupPath, logPath string, restartNetworking bool) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		fmt.Sprintf("LOG=%s", shellQuote(logPath)),
		fmt.Sprintf("MARKER=%s", shellQuote(markerPath)),
		fmt.Sprintf("BACKUP=%s", shellQuote(backupPath)),
		// Header
		`echo "[INFO] ========================================" >> "$LOG"`,
		`echo "[INFO] NETWORK ROLLBACK SCRIPT STARTED" >> "$LOG"`,
		`echo "[INFO] Timestamp: $(date -Is)" >> "$LOG"`,
		`echo "[INFO] Marker: $MARKER" >> "$LOG"`,
		`echo "[INFO] Backup: $BACKUP" >> "$LOG"`,
		`echo "[INFO] ========================================" >> "$LOG"`,
		// Check marker
		`echo "[DEBUG] Checking marker file..." >> "$LOG"`,
		`if [ ! -f "$MARKER" ]; then`,
		`  echo "[INFO] Marker not found - rollback cancelled (already disarmed)" >> "$LOG"`,
		`  echo "[INFO] ========================================" >> "$LOG"`,
		`  echo "[INFO] ROLLBACK SCRIPT EXITED (no-op)" >> "$LOG"`,
		`  echo "[INFO] ========================================" >> "$LOG"`,
		`  exit 0`,
		`fi`,
		`echo "[DEBUG] Marker exists, proceeding with rollback" >> "$LOG"`,
		// Extract phase
		`echo "[INFO] --- EXTRACT PHASE ---" >> "$LOG"`,
		`echo "[DEBUG] Executing: tar -xzf $BACKUP -C /" >> "$LOG"`,
		`TAR_OK=0`,
		`if tar -xzf "$BACKUP" -C / >> "$LOG" 2>&1; then`,
		`  TAR_OK=1`,
		`  echo "[OK] Extract phase completed successfully" >> "$LOG"`,
		`else`,
		`  RC=$?`,
		`  echo "[ERROR] Extract phase failed (exit=$RC) - skipping prune phase" >> "$LOG"`,
		`fi`,
		// Prune phase
		`if [ "$TAR_OK" -eq 1 ] && [ -d /etc/network ]; then`,
		`  echo "[INFO] --- PRUNE PHASE ---" >> "$LOG"`,
		`  echo "[DEBUG] Scope: /etc/network (+ /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg, /etc/dnsmasq.d/lxc-vmbr1.conf)" >> "$LOG"`,
		`  (`,
		`    set +e`,
		`    echo "[DEBUG] Creating temp files for prune operation..."`,
		`    MANIFEST_ALL=$(mktemp /tmp/proxsave/network_rollback_manifest_all_XXXXXX 2>/dev/null)`,
		`    MANIFEST=$(mktemp /tmp/proxsave/network_rollback_manifest_XXXXXX 2>/dev/null)`,
		`    CANDIDATES=$(mktemp /tmp/proxsave/network_rollback_candidates_XXXXXX 2>/dev/null)`,
		`    CLEANUP=$(mktemp /tmp/proxsave/network_rollback_cleanup_XXXXXX 2>/dev/null)`,
		`    if [ -z "$MANIFEST_ALL" ] || [ -z "$MANIFEST" ] || [ -z "$CANDIDATES" ] || [ -z "$CLEANUP" ]; then`,
		`      echo "[WARN] mktemp failed - skipping prune phase"`,
		`      exit 0`,
		`    fi`,
		`    echo "[DEBUG] Temp files created: manifest_all=$MANIFEST_ALL manifest=$MANIFEST candidates=$CANDIDATES cleanup=$CLEANUP"`,
		`    echo "[DEBUG] Listing rollback archive contents..."`,
		`    if ! tar -tzf "$BACKUP" > "$MANIFEST_ALL"; then`,
		`      echo "[WARN] Failed to list rollback archive - skipping prune phase"`,
		`      rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`      exit 0`,
		`    fi`,
		`    MANIFEST_COUNT=$(wc -l < "$MANIFEST_ALL")`,
		`    echo "[DEBUG] Archive contains $MANIFEST_COUNT entries"`,
		`    echo "[DEBUG] Normalizing manifest paths..."`,
		`    sed 's#^\./##' "$MANIFEST_ALL" > "$MANIFEST"`,
		`    if ! grep -q '^etc/network/' "$MANIFEST"; then`,
		`      echo "[WARN] Rollback archive does not include etc/network - skipping prune phase"`,
		`      rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`      exit 0`,
		`    fi`,
		`    echo "[DEBUG] Scanning current filesystem under /etc/network..."`,
		`    find /etc/network -mindepth 1 \( -type f -o -type l \) -print > "$CANDIDATES" 2>/dev/null || true`,
		`    CANDIDATES_COUNT=$(wc -l < "$CANDIDATES")`,
		`    echo "[DEBUG] Found $CANDIDATES_COUNT files/links on disk"`,
		`    echo "[DEBUG] Computing cleanup list (present on disk, absent in backup)..."`,
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
		`      CLEANUP_COUNT=$(wc -l < "$CLEANUP")`,
		`      echo "[DEBUG] Found $CLEANUP_COUNT extraneous files to prune:"`,
		`      cat "$CLEANUP"`,
		`      echo "[DEBUG] Removing extraneous files..."`,
		`      REMOVED=0`,
		`      while IFS= read -r rmPath; do`,
		`        if rm -f -- "$rmPath"; then`,
		`          REMOVED=$((REMOVED+1))`,
		`        else`,
		`          echo "[WARN] Failed to remove: $rmPath"`,
		`        fi`,
		`      done < "$CLEANUP"`,
		`      echo "[OK] Prune phase completed - removed $REMOVED files"`,
		`    else`,
		`      echo "[OK] Prune phase completed - no extraneous files to remove"`,
		`    fi`,
		`    rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`  ) >> "$LOG" 2>&1 || true`,
		`fi`,
	}

	if restartNetworking {
		lines = append(lines,
			`echo "[INFO] Restart networking after rollback" >> "$LOG"`,
			`echo "[INFO] Live state before reload:" >> "$LOG"`,
			`ip -br addr >> "$LOG" 2>&1 || true`,
			`ip route show >> "$LOG" 2>&1 || true`,
			`RELOAD_OK=0`,
			`if command -v ifreload >/dev/null 2>&1; then`,
			`  echo "[INFO] Executing: ifreload -a" >> "$LOG"`,
			`  if ifreload -a >> "$LOG" 2>&1; then`,
			`    RELOAD_OK=1`,
			`    echo "[OK] ifreload -a completed successfully" >> "$LOG"`,
			`  else`,
			`    RC=$?`,
			`    echo "[ERROR] ifreload -a failed (exit=$RC)" >> "$LOG"`,
			`  fi`,
			`fi`,
			`if [ "$RELOAD_OK" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then`,
			`  echo "[INFO] Executing fallback: systemctl restart networking" >> "$LOG"`,
			`  if systemctl restart networking >> "$LOG" 2>&1; then`,
			`    RELOAD_OK=1`,
			`    echo "[OK] systemctl restart networking completed successfully" >> "$LOG"`,
			`  else`,
			`    RC=$?`,
			`    echo "[ERROR] systemctl restart networking failed (exit=$RC)" >> "$LOG"`,
			`  fi`,
			`fi`,
			`if [ "$RELOAD_OK" -eq 0 ] && command -v ifup >/dev/null 2>&1; then`,
			`  echo "[INFO] Executing fallback: ifup -a" >> "$LOG"`,
			`  if ifup -a >> "$LOG" 2>&1; then`,
			`    RELOAD_OK=1`,
			`    echo "[OK] ifup -a completed successfully" >> "$LOG"`,
			`  else`,
			`    RC=$?`,
			`    echo "[ERROR] ifup -a failed (exit=$RC)" >> "$LOG"`,
			`  fi`,
			`fi`,
			`if [ "$RELOAD_OK" -eq 0 ]; then`,
			`  echo "[WARN] All network reload methods failed - network may not be properly configured" >> "$LOG"`,
			`fi`,
			`echo "[INFO] Live state after reload:" >> "$LOG"`,
			`ip -br addr >> "$LOG" 2>&1 || true`,
			`ip route show >> "$LOG" 2>&1 || true`,
		)
	} else {
		lines = append(lines, `echo "[INFO] Restart networking after rollback: skipped (manual)" >> "$LOG"`)
	}

	lines = append(lines,
		`echo "[DEBUG] Removing marker file..." >> "$LOG"`,
		`rm -f "$MARKER"`,
		`echo "[INFO] ========================================" >> "$LOG"`,
		`echo "[INFO] NETWORK ROLLBACK SCRIPT FINISHED" >> "$LOG"`,
		`echo "[INFO] Timestamp: $(date -Is)" >> "$LOG"`,
		`echo "[INFO] ========================================" >> "$LOG"`,
	)
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
