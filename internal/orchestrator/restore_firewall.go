package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

const defaultFirewallRollbackTimeout = 180 * time.Second

var ErrFirewallApplyNotCommitted = errors.New("firewall configuration not committed")

type FirewallApplyNotCommittedError struct {
	RollbackLog      string
	RollbackMarker   string
	RollbackArmed    bool
	RollbackDeadline time.Time
}

func (e *FirewallApplyNotCommittedError) Error() string {
	if e == nil {
		return ErrFirewallApplyNotCommitted.Error()
	}
	return ErrFirewallApplyNotCommitted.Error()
}

func (e *FirewallApplyNotCommittedError) Unwrap() error {
	return ErrFirewallApplyNotCommitted
}

type firewallRollbackHandle struct {
	workDir    string
	markerPath string
	unitName   string
	scriptPath string
	logPath    string
	armedAt    time.Time
	timeout    time.Duration
}

func (h *firewallRollbackHandle) remaining(now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	rem := h.timeout - now.Sub(h.armedAt)
	if rem < 0 {
		return 0
	}
	return rem
}

func buildFirewallApplyNotCommittedError(handle *firewallRollbackHandle) *FirewallApplyNotCommittedError {
	rollbackArmed := false
	rollbackMarker := ""
	rollbackLog := ""
	var rollbackDeadline time.Time
	if handle != nil {
		rollbackMarker = strings.TrimSpace(handle.markerPath)
		rollbackLog = strings.TrimSpace(handle.logPath)
		if rollbackMarker != "" {
			if _, err := restoreFS.Stat(rollbackMarker); err == nil {
				rollbackArmed = true
			}
		}
		rollbackDeadline = handle.armedAt.Add(handle.timeout)
	}

	return &FirewallApplyNotCommittedError{
		RollbackLog:      rollbackLog,
		RollbackMarker:   rollbackMarker,
		RollbackArmed:    rollbackArmed,
		RollbackDeadline: rollbackDeadline,
	}
}

func maybeApplyPVEFirewallWithUI(
	ctx context.Context,
	ui RestoreWorkflowUI,
	logger *logging.Logger,
	plan *RestorePlan,
	safetyBackup, firewallRollbackBackup *SafetyBackupResult,
	stageRoot string,
	dryRun bool,
) (err error) {
	if plan == nil || plan.SystemType != SystemTypePVE || !plan.HasCategoryID("pve_firewall") {
		return nil
	}

	done := logging.DebugStart(logger, "pve firewall restore (ui)", "dryRun=%v stage=%s", dryRun, strings.TrimSpace(stageRoot))
	defer func() { done(err) }()

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping PVE firewall restore: non-system filesystem in use")
		return nil
	}
	if dryRun {
		logger.Info("Dry run enabled: skipping PVE firewall restore")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping PVE firewall restore: requires root privileges")
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "pve firewall restore (ui)", "Skipped: staging directory not available")
		return nil
	}

	// In cluster RECOVERY mode, config.db restoration owns /etc/pve state and /etc/pve is unmounted during restore.
	if plan.NeedsClusterRestore {
		logging.DebugStep(logger, "pve firewall restore (ui)", "Skip: cluster RECOVERY restores config.db")
		return nil
	}

	etcPVE := "/etc/pve"
	mounted, mountErr := isMounted(etcPVE)
	if mountErr != nil {
		logger.Warning("PVE firewall restore: unable to check pmxcfs mount (%s): %v", etcPVE, mountErr)
	}
	if !mounted {
		logger.Warning("PVE firewall restore: %s is not mounted; skipping firewall apply to avoid shadow writes on root filesystem", etcPVE)
		return nil
	}

	stageFirewall := filepath.Join(stageRoot, "etc", "pve", "firewall")
	stageNodes := filepath.Join(stageRoot, "etc", "pve", "nodes")
	if _, err := restoreFS.Stat(stageFirewall); err != nil && errors.Is(err, os.ErrNotExist) {
		if _, err := restoreFS.Stat(stageNodes); err != nil && errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve firewall restore (ui)", "Skipped: no firewall data in stage directory")
			return nil
		}
	}

	rollbackPath := ""
	if firewallRollbackBackup != nil {
		rollbackPath = strings.TrimSpace(firewallRollbackBackup.BackupPath)
	}
	fullRollbackPath := ""
	if safetyBackup != nil {
		fullRollbackPath = strings.TrimSpace(safetyBackup.BackupPath)
	}

	logger.Info("")
	message := fmt.Sprintf(
		"PVE firewall restore: configuration is ready to apply.\nSource: %s\n\n"+
			"WARNING: This may immediately change firewall rules and disconnect SSH/Web sessions.\n\n"+
			"After applying, confirm within %ds or ProxSave will roll back automatically.\n\n"+
			"Recommendation: run this step from the local console/IPMI, not over SSH.\n\n"+
			"Apply PVE firewall configuration now?",
		strings.TrimSpace(stageRoot),
		int(defaultFirewallRollbackTimeout.Seconds()),
	)
	applyNow, err := ui.ConfirmAction(ctx, "Apply PVE firewall configuration", message, "Apply now", "Skip apply", 90*time.Second, false)
	if err != nil {
		return err
	}
	logging.DebugStep(logger, "pve firewall restore (ui)", "User choice: applyNow=%v", applyNow)
	if !applyNow {
		logger.Info("Skipping PVE firewall apply (you can apply manually later).")
		return nil
	}

	if rollbackPath == "" && fullRollbackPath != "" {
		ok, err := ui.ConfirmAction(
			ctx,
			"Firewall rollback backup not available",
			"Firewall rollback backup is not available.\n\nIf you proceed, the rollback timer will use the full safety backup, which may revert other restored categories.\n\nProceed anyway?",
			"Proceed with full rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "pve firewall restore (ui)", "User choice: allowFullRollback=%v", ok)
		if !ok {
			logger.Info("Skipping PVE firewall apply (rollback backup not available).")
			return nil
		}
		rollbackPath = fullRollbackPath
	}

	if rollbackPath == "" {
		ok, err := ui.ConfirmAction(
			ctx,
			"No rollback available",
			"No rollback backup is available.\n\nIf you proceed and the firewall locks you out, ProxSave cannot roll back automatically.\n\nProceed anyway?",
			"Proceed without rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		if !ok {
			logger.Info("Skipping PVE firewall apply (no rollback available).")
			return nil
		}
	}

	var rollbackHandle *firewallRollbackHandle
	if rollbackPath != "" {
		logger.Info("")
		logger.Info("Arming firewall rollback timer (%ds)...", int(defaultFirewallRollbackTimeout.Seconds()))
		rollbackHandle, err = armFirewallRollback(ctx, logger, rollbackPath, defaultFirewallRollbackTimeout, "/tmp/proxsave")
		if err != nil {
			return fmt.Errorf("arm firewall rollback: %w", err)
		}
		logger.Info("Firewall rollback log: %s", rollbackHandle.logPath)
	}

	applied, err := applyPVEFirewallFromStage(logger, stageRoot)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		logger.Info("PVE firewall restore: no changes applied (stage contained no firewall entries)")
		if rollbackHandle != nil {
			disarmFirewallRollback(ctx, logger, rollbackHandle)
		}
		return nil
	}

	if err := restartPVEFirewallService(ctx, logger); err != nil {
		logger.Warning("PVE firewall restore: reload/restart failed: %v", err)
	}

	if rollbackHandle == nil {
		logger.Info("PVE firewall restore applied (no rollback timer armed).")
		return nil
	}

	remaining := rollbackHandle.remaining(time.Now())
	if remaining <= 0 {
		return buildFirewallApplyNotCommittedError(rollbackHandle)
	}

	logger.Info("")
	commitMessage := fmt.Sprintf(
		"PVE firewall configuration has been applied.\n\n"+
			"If you lose access, ProxSave will roll back automatically in %ds.\n\n"+
			"Keep firewall changes?",
		int(remaining.Seconds()),
	)
	commit, err := ui.ConfirmAction(ctx, "Commit firewall changes", commitMessage, "Keep", "Rollback", remaining, false)
	if err != nil {
		if errors.Is(err, input.ErrInputAborted) || errors.Is(err, context.Canceled) {
			return err
		}
		logger.Warning("Firewall commit prompt failed: %v", err)
		return buildFirewallApplyNotCommittedError(rollbackHandle)
	}

	if commit {
		disarmFirewallRollback(ctx, logger, rollbackHandle)
		logger.Info("Firewall changes committed.")
		return nil
	}

	return buildFirewallApplyNotCommittedError(rollbackHandle)
}

func applyPVEFirewallFromStage(logger *logging.Logger, stageRoot string) (applied []string, err error) {
	stageRoot = strings.TrimSpace(stageRoot)
	done := logging.DebugStart(logger, "pve firewall apply", "stage=%s", stageRoot)
	defer func() { done(err) }()

	if stageRoot == "" {
		return nil, nil
	}

	stageFirewall := filepath.Join(stageRoot, "etc", "pve", "firewall")
	destFirewall := "/etc/pve/firewall"

	if info, err := restoreFS.Stat(stageFirewall); err == nil {
		if info.IsDir() {
			paths, err := syncDirExact(stageFirewall, destFirewall)
			if err != nil {
				return applied, err
			}
			applied = append(applied, paths...)
		} else {
			ok, err := copyFileExact(stageFirewall, destFirewall)
			if err != nil {
				return applied, err
			}
			if ok {
				applied = append(applied, destFirewall)
			}
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return applied, fmt.Errorf("stat staged firewall config %s: %w", stageFirewall, err)
	}

	srcHostFW, srcNode, ok, err := selectStageHostFirewall(logger, stageRoot)
	if err != nil {
		return applied, err
	}
	if ok {
		currentNode, _ := os.Hostname()
		currentNode = shortHost(currentNode)
		if strings.TrimSpace(currentNode) == "" {
			currentNode = "localhost"
		}
		destHostFW := filepath.Join("/etc/pve/nodes", currentNode, "host.fw")
		ok, err := copyFileExact(srcHostFW, destHostFW)
		if err != nil {
			return applied, err
		}
		if ok {
			applied = append(applied, destHostFW)
		}
		if srcNode != "" && !strings.EqualFold(srcNode, currentNode) && logger != nil {
			logger.Warning("PVE firewall: applied host.fw from staged node %s onto current node %s", srcNode, currentNode)
		}
	}

	return applied, nil
}

func selectStageHostFirewall(logger *logging.Logger, stageRoot string) (path string, sourceNode string, ok bool, err error) {
	currentNode, _ := os.Hostname()
	currentNode = shortHost(currentNode)
	if strings.TrimSpace(currentNode) == "" {
		currentNode = "localhost"
	}

	stageNodes := filepath.Join(stageRoot, "etc", "pve", "nodes")
	entries, err := restoreFS.ReadDir(stageNodes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("readdir %s: %w", stageNodes, err)
	}

	var candidates []string
	for _, entry := range entries {
		if entry == nil || !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		hostFW := filepath.Join(stageNodes, name, "host.fw")
		if info, err := restoreFS.Stat(hostFW); err == nil && !info.IsDir() {
			candidates = append(candidates, name)
		}
	}

	if len(candidates) == 0 {
		return "", "", false, nil
	}

	for _, node := range candidates {
		if strings.EqualFold(node, currentNode) {
			return filepath.Join(stageNodes, node, "host.fw"), node, true, nil
		}
	}

	if len(candidates) == 1 {
		node := candidates[0]
		return filepath.Join(stageNodes, node, "host.fw"), node, true, nil
	}

	if logger != nil {
		logger.Warning("PVE firewall: multiple staged host.fw candidates found (%s) but none matches current node %s; skipping host.fw apply", strings.Join(candidates, ", "), currentNode)
	}
	return "", "", false, nil
}

func restartPVEFirewallService(ctx context.Context, logger *logging.Logger) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if commandAvailable("systemctl") {
		if _, err := restoreCmd.Run(timeoutCtx, "systemctl", "try-restart", "pve-firewall"); err == nil {
			return nil
		}
		if _, err := restoreCmd.Run(timeoutCtx, "systemctl", "restart", "pve-firewall"); err == nil {
			return nil
		}
	}
	if commandAvailable("pve-firewall") {
		if _, err := restoreCmd.Run(timeoutCtx, "pve-firewall", "restart"); err == nil {
			return nil
		}
	}
	return fmt.Errorf("pve-firewall reload not available")
}

func armFirewallRollback(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (handle *firewallRollbackHandle, err error) {
	done := logging.DebugStart(logger, "arm firewall rollback", "backup=%s timeout=%s workDir=%s", strings.TrimSpace(backupPath), timeout, strings.TrimSpace(workDir))
	defer func() { done(err) }()

	if strings.TrimSpace(backupPath) == "" {
		return nil, fmt.Errorf("empty safety backup path")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("invalid rollback timeout")
	}

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
	handle = &firewallRollbackHandle{
		workDir:    baseDir,
		markerPath: filepath.Join(baseDir, fmt.Sprintf("firewall_rollback_pending_%s", timestamp)),
		scriptPath: filepath.Join(baseDir, fmt.Sprintf("firewall_rollback_%s.sh", timestamp)),
		logPath:    filepath.Join(baseDir, fmt.Sprintf("firewall_rollback_%s.log", timestamp)),
		armedAt:    time.Now(),
		timeout:    timeout,
	}

	if err := restoreFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback marker: %w", err)
	}

	script := buildFirewallRollbackScript(handle.markerPath, backupPath, handle.logPath)
	if err := restoreFS.WriteFile(handle.scriptPath, []byte(script), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback script: %w", err)
	}

	timeoutSeconds := int(timeout.Seconds())
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	if commandAvailable("systemd-run") {
		handle.unitName = fmt.Sprintf("proxsave-firewall-rollback-%s", timestamp)
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
		}
	}

	if handle.unitName == "" {
		cmd := fmt.Sprintf("nohup sh -c 'sleep %d; /bin/sh %s' >/dev/null 2>&1 &", timeoutSeconds, handle.scriptPath)
		if output, err := restoreCmd.Run(ctx, "sh", "-c", cmd); err != nil {
			logger.Debug("Background rollback output: %s", strings.TrimSpace(string(output)))
			return nil, fmt.Errorf("failed to arm rollback timer: %w", err)
		}
	}

	logger.Info("Firewall rollback timer armed (%ds). Work dir: %s (log: %s)", timeoutSeconds, baseDir, handle.logPath)
	return handle, nil
}

func disarmFirewallRollback(ctx context.Context, logger *logging.Logger, handle *firewallRollbackHandle) {
	if handle == nil {
		return
	}

	if strings.TrimSpace(handle.markerPath) != "" {
		if err := restoreFS.Remove(handle.markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warning("Failed to remove firewall rollback marker %s: %v", handle.markerPath, err)
		}
	}

	if strings.TrimSpace(handle.unitName) != "" && commandAvailable("systemctl") {
		timerUnit := strings.TrimSpace(handle.unitName) + ".timer"
		_, _ = restoreCmd.Run(ctx, "systemctl", "stop", timerUnit)
		_, _ = restoreCmd.Run(ctx, "systemctl", "reset-failed", strings.TrimSpace(handle.unitName)+".service", timerUnit)
	}
}

func buildFirewallRollbackScript(markerPath, backupPath, logPath string) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		fmt.Sprintf("LOG=%s", shellQuote(logPath)),
		fmt.Sprintf("MARKER=%s", shellQuote(markerPath)),
		fmt.Sprintf("BACKUP=%s", shellQuote(backupPath)),
		`echo "[INFO] ========================================" >> "$LOG"`,
		`echo "[INFO] FIREWALL ROLLBACK SCRIPT STARTED" >> "$LOG"`,
		`echo "[INFO] Timestamp: $(date -Is)" >> "$LOG"`,
		`echo "[INFO] Marker: $MARKER" >> "$LOG"`,
		`echo "[INFO] Backup: $BACKUP" >> "$LOG"`,
		`echo "[INFO] ========================================" >> "$LOG"`,
		`if [ ! -f "$MARKER" ]; then`,
		`  echo "[INFO] Marker not found - rollback cancelled (already disarmed)" >> "$LOG"`,
		`  exit 0`,
		`fi`,
		`echo "[INFO] --- EXTRACT PHASE ---" >> "$LOG"`,
		`TAR_OK=0`,
		`if tar -xzf "$BACKUP" -C / >> "$LOG" 2>&1; then`,
		`  TAR_OK=1`,
		`  echo "[OK] Extract phase completed successfully" >> "$LOG"`,
		`else`,
		`  RC=$?`,
		`  echo "[ERROR] Extract phase failed (exit=$RC) - skipping prune phase" >> "$LOG"`,
		`fi`,
		`if [ "$TAR_OK" -eq 1 ] && [ -d /etc/pve/firewall ]; then`,
		`  echo "[INFO] --- PRUNE PHASE ---" >> "$LOG"`,
		`  (`,
		`    set +e`,
		`    MANIFEST_ALL=$(mktemp /tmp/proxsave/firewall_rollback_manifest_all_XXXXXX 2>/dev/null)`,
		`    MANIFEST=$(mktemp /tmp/proxsave/firewall_rollback_manifest_XXXXXX 2>/dev/null)`,
		`    CANDIDATES=$(mktemp /tmp/proxsave/firewall_rollback_candidates_XXXXXX 2>/dev/null)`,
		`    CLEANUP=$(mktemp /tmp/proxsave/firewall_rollback_cleanup_XXXXXX 2>/dev/null)`,
		`    if [ -z "$MANIFEST_ALL" ] || [ -z "$MANIFEST" ] || [ -z "$CANDIDATES" ] || [ -z "$CLEANUP" ]; then`,
		`      echo "[WARN] mktemp failed - skipping prune phase"`,
		`      exit 0`,
		`    fi`,
		`    if ! tar -tzf "$BACKUP" > "$MANIFEST_ALL"; then`,
		`      echo "[WARN] Failed to list rollback archive - skipping prune phase"`,
		`      rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`      exit 0`,
		`    fi`,
		`    sed 's#^\\./##' "$MANIFEST_ALL" > "$MANIFEST"`,
		`    find /etc/pve/firewall -mindepth 1 \( -type f -o -type l \) -print > "$CANDIDATES" 2>/dev/null || true`,
		`    : > "$CLEANUP"`,
		`    while IFS= read -r path; do`,
		`      rel=${path#/}`,
		`      if ! grep -Fxq "$rel" "$MANIFEST"; then`,
		`        echo "$path" >> "$CLEANUP"`,
		`      fi`,
		`    done < "$CANDIDATES"`,
		`    if [ -s "$CLEANUP" ]; then`,
		`      while IFS= read -r rmPath; do`,
		`        rm -f -- "$rmPath" || true`,
		`      done < "$CLEANUP"`,
		`    fi`,
		`    find /etc/pve/nodes -maxdepth 2 -type f -name host.fw -print 2>/dev/null | while IFS= read -r hostfw; do`,
		`      rel=${hostfw#/}`,
		`      if ! grep -Fxq "$rel" "$MANIFEST"; then`,
		`        rm -f -- "$hostfw" || true`,
		`      fi`,
		`    done`,
		`    rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`  ) >> "$LOG" 2>&1 || true`,
		`fi`,
		`echo "[INFO] Restart firewall service after rollback" >> "$LOG"`,
		`if command -v systemctl >/dev/null 2>&1; then`,
		`  systemctl restart pve-firewall >> "$LOG" 2>&1 || true`,
		`fi`,
		`if command -v pve-firewall >/dev/null 2>&1; then`,
		`  pve-firewall restart >> "$LOG" 2>&1 || true`,
		`fi`,
		`rm -f "$MARKER" 2>/dev/null || true`,
	}
	return strings.Join(lines, "\n") + "\n"
}

func copyFileExact(src, dest string) (bool, error) {
	info, err := restoreFS.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return false, nil
	}

	data, err := restoreFS.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", src, err)
	}

	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	if err := writeFileAtomic(dest, data, mode); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}
	return true, nil
}

func syncDirExact(srcDir, destDir string) ([]string, error) {
	info, err := restoreFS.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	if err := restoreFS.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	stageFiles := make(map[string]struct{})
	stageDirs := make(map[string]struct{})

	var applied []string

	var walkStage func(path string) error
	walkStage = func(path string) error {
		entries, err := restoreFS.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("readdir %s: %w", path, err)
		}
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" {
				continue
			}
			src := filepath.Join(path, name)
			rel, relErr := filepath.Rel(srcDir, src)
			if relErr != nil {
				return fmt.Errorf("rel %s: %w", src, relErr)
			}
			rel = filepath.ToSlash(filepath.Clean(rel))
			if rel == "." || strings.HasPrefix(rel, "../") {
				continue
			}
			dest := filepath.Join(destDir, filepath.FromSlash(rel))

			info, infoErr := entry.Info()
			if infoErr != nil {
				return fmt.Errorf("stat %s: %w", src, infoErr)
			}

			if info.Mode()&os.ModeSymlink != 0 {
				stageFiles[rel] = struct{}{}
				target, err := restoreFS.Readlink(src)
				if err != nil {
					return fmt.Errorf("readlink %s: %w", src, err)
				}
				if err := restoreFS.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
				}
				_ = restoreFS.Remove(dest)
				if err := restoreFS.Symlink(target, dest); err != nil {
					return fmt.Errorf("symlink %s -> %s: %w", dest, target, err)
				}
				applied = append(applied, dest)
				continue
			}

			if info.IsDir() {
				stageDirs[rel] = struct{}{}
				if err := restoreFS.MkdirAll(dest, 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", dest, err)
				}
				if err := walkStage(src); err != nil {
					return err
				}
				continue
			}

			stageFiles[rel] = struct{}{}
			ok, err := copyFileExact(src, dest)
			if err != nil {
				return err
			}
			if ok {
				applied = append(applied, dest)
			}
		}
		return nil
	}

	if err := walkStage(srcDir); err != nil {
		return applied, err
	}

	var pruneDest func(path string) error
	pruneDest = func(path string) error {
		entries, err := restoreFS.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("readdir %s: %w", path, err)
		}
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" {
				continue
			}
			dest := filepath.Join(path, name)
			rel, relErr := filepath.Rel(destDir, dest)
			if relErr != nil {
				return fmt.Errorf("rel %s: %w", dest, relErr)
			}
			rel = filepath.ToSlash(filepath.Clean(rel))
			if rel == "." || strings.HasPrefix(rel, "../") {
				continue
			}

			info, infoErr := entry.Info()
			if infoErr != nil {
				return fmt.Errorf("stat %s: %w", dest, infoErr)
			}

			if info.IsDir() {
				if err := pruneDest(dest); err != nil {
					return err
				}
				// Best-effort: remove empty dirs that are not present in stage.
				if _, keep := stageDirs[rel]; !keep {
					_ = restoreFS.Remove(dest)
				}
				continue
			}

			if _, keep := stageFiles[rel]; keep {
				continue
			}
			if err := restoreFS.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", dest, err)
			}
		}
		return nil
	}

	if err := pruneDest(destDir); err != nil {
		return applied, err
	}

	return applied, nil
}
