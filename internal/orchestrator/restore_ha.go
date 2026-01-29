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

const defaultHARollbackTimeout = 180 * time.Second

var ErrHAApplyNotCommitted = errors.New("HA configuration not committed")

type HAApplyNotCommittedError struct {
	RollbackLog      string
	RollbackMarker   string
	RollbackArmed    bool
	RollbackDeadline time.Time
}

func (e *HAApplyNotCommittedError) Error() string {
	if e == nil {
		return ErrHAApplyNotCommitted.Error()
	}
	return ErrHAApplyNotCommitted.Error()
}

func (e *HAApplyNotCommittedError) Unwrap() error {
	return ErrHAApplyNotCommitted
}

type haRollbackHandle struct {
	workDir    string
	markerPath string
	unitName   string
	scriptPath string
	logPath    string
	armedAt    time.Time
	timeout    time.Duration
}

func (h *haRollbackHandle) remaining(now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	rem := h.timeout - now.Sub(h.armedAt)
	if rem < 0 {
		return 0
	}
	return rem
}

func buildHAApplyNotCommittedError(handle *haRollbackHandle) *HAApplyNotCommittedError {
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

	return &HAApplyNotCommittedError{
		RollbackLog:      rollbackLog,
		RollbackMarker:   rollbackMarker,
		RollbackArmed:    rollbackArmed,
		RollbackDeadline: rollbackDeadline,
	}
}

func maybeApplyPVEHAWithUI(
	ctx context.Context,
	ui RestoreWorkflowUI,
	logger *logging.Logger,
	plan *RestorePlan,
	safetyBackup, haRollbackBackup *SafetyBackupResult,
	stageRoot string,
	dryRun bool,
) (err error) {
	if plan == nil || plan.SystemType != SystemTypePVE || !plan.HasCategoryID("pve_ha") {
		return nil
	}

	done := logging.DebugStart(logger, "pve ha restore (ui)", "dryRun=%v stage=%s", dryRun, strings.TrimSpace(stageRoot))
	defer func() { done(err) }()

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping PVE HA restore: non-system filesystem in use")
		return nil
	}
	if dryRun {
		logger.Info("Dry run enabled: skipping PVE HA restore")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping PVE HA restore: requires root privileges")
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "pve ha restore (ui)", "Skipped: staging directory not available")
		return nil
	}

	// In cluster RECOVERY mode, config.db restoration owns /etc/pve state and /etc/pve is unmounted during restore.
	if plan.NeedsClusterRestore {
		logging.DebugStep(logger, "pve ha restore (ui)", "Skip: cluster RECOVERY restores config.db")
		return nil
	}

	stageHasHA, err := stageHasPVEHAConfig(stageRoot)
	if err != nil {
		return err
	}
	if !stageHasHA {
		logging.DebugStep(logger, "pve ha restore (ui)", "Skipped: no HA config files in stage directory")
		return nil
	}

	etcPVE := "/etc/pve"
	mounted, mountErr := isMounted(etcPVE)
	if mountErr != nil {
		logger.Warning("PVE HA restore: unable to check pmxcfs mount (%s): %v", etcPVE, mountErr)
	}
	if !mounted {
		logger.Warning("PVE HA restore: %s is not mounted; skipping HA apply to avoid shadow writes on root filesystem", etcPVE)
		return nil
	}

	rollbackPath := ""
	if haRollbackBackup != nil {
		rollbackPath = strings.TrimSpace(haRollbackBackup.BackupPath)
	}
	fullRollbackPath := ""
	if safetyBackup != nil {
		fullRollbackPath = strings.TrimSpace(safetyBackup.BackupPath)
	}

	logger.Info("")
	message := fmt.Sprintf(
		"PVE HA restore: configuration is ready to apply.\nSource: %s\n\n"+
			"WARNING: This may immediately affect HA-managed VMs/CTs (start/stop/migrate) cluster-wide.\n\n"+
			"Rollback will restore HA config files, but cannot undo actions already taken by the HA manager.\n\n"+
			"After applying, confirm within %ds or ProxSave will roll back automatically.\n\n"+
			"Apply PVE HA configuration now?",
		strings.TrimSpace(stageRoot),
		int(defaultHARollbackTimeout.Seconds()),
	)
	applyNow, err := ui.ConfirmAction(ctx, "Apply PVE HA configuration", message, "Apply now", "Skip apply", 90*time.Second, false)
	if err != nil {
		return err
	}
	logging.DebugStep(logger, "pve ha restore (ui)", "User choice: applyNow=%v", applyNow)
	if !applyNow {
		logger.Info("Skipping PVE HA apply (you can apply manually later).")
		return nil
	}

	if rollbackPath == "" && fullRollbackPath != "" {
		ok, err := ui.ConfirmAction(
			ctx,
			"HA rollback backup not available",
			"HA rollback backup is not available.\n\nIf you proceed, the rollback timer will use the full safety backup, which may revert other restored categories.\n\nProceed anyway?",
			"Proceed with full rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "pve ha restore (ui)", "User choice: allowFullRollback=%v", ok)
		if !ok {
			logger.Info("Skipping PVE HA apply (rollback backup not available).")
			return nil
		}
		rollbackPath = fullRollbackPath
	}

	if rollbackPath == "" {
		ok, err := ui.ConfirmAction(
			ctx,
			"No rollback available",
			"No rollback backup is available.\n\nIf you proceed and the HA configuration causes disruption, ProxSave cannot roll back automatically.\n\nProceed anyway?",
			"Proceed without rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		if !ok {
			logger.Info("Skipping PVE HA apply (no rollback available).")
			return nil
		}
	}

	var rollbackHandle *haRollbackHandle
	if rollbackPath != "" {
		logger.Info("")
		logger.Info("Arming HA rollback timer (%ds)...", int(defaultHARollbackTimeout.Seconds()))
		rollbackHandle, err = armHARollback(ctx, logger, rollbackPath, defaultHARollbackTimeout, "/tmp/proxsave")
		if err != nil {
			return fmt.Errorf("arm HA rollback: %w", err)
		}
		logger.Info("HA rollback log: %s", rollbackHandle.logPath)
	}

	applied, err := applyPVEHAFromStage(logger, stageRoot)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		logger.Info("PVE HA restore: no changes applied (stage contained no HA config entries)")
		if rollbackHandle != nil {
			disarmHARollback(ctx, logger, rollbackHandle)
		}
		return nil
	}

	if rollbackHandle == nil {
		logger.Info("PVE HA restore applied (no rollback timer armed).")
		return nil
	}

	remaining := rollbackHandle.remaining(time.Now())
	if remaining <= 0 {
		return buildHAApplyNotCommittedError(rollbackHandle)
	}

	logger.Info("")
	commitMessage := fmt.Sprintf(
		"PVE HA configuration has been applied.\n\n"+
			"If needed, ProxSave will roll back automatically in %ds.\n\n"+
			"Keep HA changes?",
		int(remaining.Seconds()),
	)
	commit, err := ui.ConfirmAction(ctx, "Commit HA changes", commitMessage, "Keep", "Rollback", remaining, false)
	if err != nil {
		if errors.Is(err, input.ErrInputAborted) || errors.Is(err, context.Canceled) {
			return err
		}
		logger.Warning("HA commit prompt failed: %v", err)
		return buildHAApplyNotCommittedError(rollbackHandle)
	}

	if commit {
		disarmHARollback(ctx, logger, rollbackHandle)
		logger.Info("HA changes committed.")
		return nil
	}

	return buildHAApplyNotCommittedError(rollbackHandle)
}

func stageHasPVEHAConfig(stageRoot string) (bool, error) {
	stageHA := filepath.Join(strings.TrimSpace(stageRoot), "etc", "pve", "ha")
	candidates := []string{
		filepath.Join(stageHA, "resources.cfg"),
		filepath.Join(stageHA, "groups.cfg"),
		filepath.Join(stageHA, "rules.cfg"),
	}
	for _, candidate := range candidates {
		if _, err := restoreFS.Stat(candidate); err == nil {
			return true, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", candidate, err)
		}
	}
	return false, nil
}

func applyPVEHAFromStage(logger *logging.Logger, stageRoot string) (applied []string, err error) {
	stageRoot = strings.TrimSpace(stageRoot)
	done := logging.DebugStart(logger, "pve ha apply", "stage=%s", stageRoot)
	defer func() { done(err) }()

	if stageRoot == "" {
		return nil, nil
	}

	stageHA := filepath.Join(stageRoot, "etc", "pve", "ha")
	destHA := "/etc/pve/ha"
	if err := restoreFS.MkdirAll(destHA, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", destHA, err)
	}

	// Only prune config files if the stage actually contains HA config.
	hasAny, err := stageHasPVEHAConfig(stageRoot)
	if err != nil {
		return nil, err
	}
	if !hasAny {
		return nil, nil
	}

	configFiles := []string{"resources.cfg", "groups.cfg", "rules.cfg"}
	for _, name := range configFiles {
		src := filepath.Join(stageHA, name)
		dest := filepath.Join(destHA, name)

		ok, err := copyFileExact(src, dest)
		if err != nil {
			return applied, err
		}
		if ok {
			applied = append(applied, dest)
			continue
		}

		// Not present in stage -> remove from destination to maintain 1:1 semantics.
		if err := removeIfExists(dest); err != nil {
			return applied, fmt.Errorf("remove %s: %w", dest, err)
		}
	}

	return applied, nil
}

func armHARollback(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (handle *haRollbackHandle, err error) {
	done := logging.DebugStart(logger, "arm ha rollback", "backup=%s timeout=%s workDir=%s", strings.TrimSpace(backupPath), timeout, strings.TrimSpace(workDir))
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
	handle = &haRollbackHandle{
		workDir:    baseDir,
		markerPath: filepath.Join(baseDir, fmt.Sprintf("ha_rollback_pending_%s", timestamp)),
		scriptPath: filepath.Join(baseDir, fmt.Sprintf("ha_rollback_%s.sh", timestamp)),
		logPath:    filepath.Join(baseDir, fmt.Sprintf("ha_rollback_%s.log", timestamp)),
		armedAt:    time.Now(),
		timeout:    timeout,
	}

	if err := restoreFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback marker: %w", err)
	}

	script := buildHARollbackScript(handle.markerPath, backupPath, handle.logPath)
	if err := restoreFS.WriteFile(handle.scriptPath, []byte(script), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback script: %w", err)
	}

	timeoutSeconds := int(timeout.Seconds())
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	if commandAvailable("systemd-run") {
		handle.unitName = fmt.Sprintf("proxsave-ha-rollback-%s", timestamp)
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
			return nil, fmt.Errorf("failed to schedule rollback timer: %w", err)
		}
	}

	return handle, nil
}

func disarmHARollback(ctx context.Context, logger *logging.Logger, handle *haRollbackHandle) {
	if handle == nil {
		return
	}
	if strings.TrimSpace(handle.markerPath) != "" {
		_ = restoreFS.Remove(handle.markerPath)
	}
	if strings.TrimSpace(handle.unitName) != "" && commandAvailable("systemctl") {
		timerUnit := strings.TrimSpace(handle.unitName) + ".timer"
		_, _ = restoreCmd.Run(ctx, "systemctl", "stop", timerUnit)
		_, _ = restoreCmd.Run(ctx, "systemctl", "reset-failed", strings.TrimSpace(handle.unitName)+".service", timerUnit)
	}
	if strings.TrimSpace(handle.scriptPath) != "" {
		_ = restoreFS.Remove(handle.scriptPath)
	}
	if strings.TrimSpace(handle.logPath) != "" && logger != nil {
		logger.Debug("HA rollback disarmed (log=%s)", strings.TrimSpace(handle.logPath))
	}
}

func buildHARollbackScript(markerPath, backupPath, logPath string) string {
	lines := []string{
		"#!/bin/sh",
		"set -eu",
		fmt.Sprintf("LOG=%s", shellQuote(logPath)),
		fmt.Sprintf("MARKER=%s", shellQuote(markerPath)),
		fmt.Sprintf("BACKUP=%s", shellQuote(backupPath)),
		`echo "[INFO] ========================================" >> "$LOG"`,
		`echo "[INFO] HA ROLLBACK SCRIPT STARTED" >> "$LOG"`,
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
		`if [ "$TAR_OK" -eq 1 ] && [ -d /etc/pve/ha ]; then`,
		`  echo "[INFO] --- PRUNE PHASE ---" >> "$LOG"`,
		`  (`,
		`    set +e`,
		`    MANIFEST_ALL=$(mktemp /tmp/proxsave/ha_rollback_manifest_all_XXXXXX 2>/dev/null)`,
		`    MANIFEST=$(mktemp /tmp/proxsave/ha_rollback_manifest_XXXXXX 2>/dev/null)`,
		`    CANDIDATES=$(mktemp /tmp/proxsave/ha_rollback_candidates_XXXXXX 2>/dev/null)`,
		`    CLEANUP=$(mktemp /tmp/proxsave/ha_rollback_cleanup_XXXXXX 2>/dev/null)`,
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
		`    find /etc/pve/ha -maxdepth 1 -type f -name '*.cfg' -print > "$CANDIDATES" 2>/dev/null || true`,
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
		`    rm -f "$MANIFEST_ALL" "$MANIFEST" "$CANDIDATES" "$CLEANUP"`,
		`  ) >> "$LOG" 2>&1 || true`,
		`fi`,
		`rm -f "$MARKER" 2>/dev/null || true`,
	}
	return strings.Join(lines, "\n") + "\n"
}

