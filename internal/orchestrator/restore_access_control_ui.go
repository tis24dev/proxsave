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

const defaultAccessControlRollbackTimeout = 180 * time.Second

var ErrAccessControlApplyNotCommitted = errors.New("access control changes not committed")

type AccessControlApplyNotCommittedError struct {
	RollbackLog      string
	RollbackMarker   string
	RollbackArmed    bool
	RollbackDeadline time.Time
}

func (e *AccessControlApplyNotCommittedError) Error() string {
	if e == nil {
		return ErrAccessControlApplyNotCommitted.Error()
	}
	return ErrAccessControlApplyNotCommitted.Error()
}

func (e *AccessControlApplyNotCommittedError) Unwrap() error {
	return ErrAccessControlApplyNotCommitted
}

type accessControlRollbackHandle struct {
	workDir    string
	markerPath string
	unitName   string
	scriptPath string
	logPath    string
	armedAt    time.Time
	timeout    time.Duration
}

func (h *accessControlRollbackHandle) remaining(now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	rem := h.timeout - now.Sub(h.armedAt)
	if rem < 0 {
		return 0
	}
	return rem
}

func buildAccessControlApplyNotCommittedError(handle *accessControlRollbackHandle) *AccessControlApplyNotCommittedError {
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

	return &AccessControlApplyNotCommittedError{
		RollbackLog:      rollbackLog,
		RollbackMarker:   rollbackMarker,
		RollbackArmed:    rollbackArmed,
		RollbackDeadline: rollbackDeadline,
	}
}

func stageHasPVEAccessControlConfig(stageRoot string) (bool, error) {
	stageRoot = strings.TrimSpace(stageRoot)
	if stageRoot == "" {
		return false, nil
	}

	candidates := []string{
		filepath.Join(stageRoot, "etc", "pve", "user.cfg"),
		filepath.Join(stageRoot, "etc", "pve", "domains.cfg"),
		filepath.Join(stageRoot, "etc", "pve", "priv", "shadow.cfg"),
		filepath.Join(stageRoot, "etc", "pve", "priv", "token.cfg"),
		filepath.Join(stageRoot, "etc", "pve", "priv", "tfa.cfg"),
	}

	for _, candidate := range candidates {
		info, err := restoreFS.Stat(candidate)
		if err == nil && info != nil && !info.IsDir() {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return false, nil
}

func maybeApplyAccessControlWithUI(
	ctx context.Context,
	ui RestoreWorkflowUI,
	logger *logging.Logger,
	plan *RestorePlan,
	safetyBackup, accessControlRollbackBackup *SafetyBackupResult,
	stageRoot string,
	dryRun bool,
) (err error) {
	if plan == nil {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "access control staged apply (ui)", "Skipped: staging directory not available")
		return nil
	}
	if !plan.HasCategoryID("pve_access_control") && !plan.HasCategoryID("pbs_access_control") {
		return nil
	}

	done := logging.DebugStart(logger, "access control staged apply (ui)", "dryRun=%v stage=%s", dryRun, strings.TrimSpace(stageRoot))
	defer func() { done(err) }()

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}

	// Cluster backups: PVE access control is cluster-wide. In SAFE (no config.db restore) this must be opt-in.
	if plan.SystemType == SystemTypePVE &&
		plan.HasCategoryID("pve_access_control") &&
		plan.ClusterBackup &&
		!plan.NeedsClusterRestore {
		return maybeApplyPVEAccessControlFromClusterBackupWithUI(ctx, ui, logger, plan, safetyBackup, accessControlRollbackBackup, stageRoot, dryRun)
	}

	// Default behavior for all other cases (PBS, standalone PVE, cluster RECOVERY).
	return maybeApplyAccessControlFromStage(ctx, logger, plan, stageRoot, dryRun)
}

func maybeApplyPVEAccessControlFromClusterBackupWithUI(
	ctx context.Context,
	ui RestoreWorkflowUI,
	logger *logging.Logger,
	plan *RestorePlan,
	safetyBackup, accessControlRollbackBackup *SafetyBackupResult,
	stageRoot string,
	dryRun bool,
) (err error) {
	if plan == nil || plan.SystemType != SystemTypePVE || !plan.HasCategoryID("pve_access_control") || !plan.ClusterBackup || plan.NeedsClusterRestore {
		return nil
	}

	done := logging.DebugStart(logger, "pve access control restore (cluster backup, ui)", "dryRun=%v stage=%s", dryRun, strings.TrimSpace(stageRoot))
	defer func() { done(err) }()

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping PVE access control apply (cluster backup): non-system filesystem in use")
		return nil
	}
	if dryRun {
		logger.Info("Dry run enabled: skipping PVE access control apply (cluster backup)")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping PVE access control apply (cluster backup): requires root privileges")
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "pve access control restore (cluster backup, ui)", "Skipped: staging directory not available")
		return nil
	}

	// In cluster RECOVERY mode, config.db restoration owns /etc/pve state and /etc/pve is unmounted during restore.
	if plan.NeedsClusterRestore {
		logging.DebugStep(logger, "pve access control restore (cluster backup, ui)", "Skip: cluster RECOVERY restores config.db")
		return nil
	}

	stageHasAC, err := stageHasPVEAccessControlConfig(stageRoot)
	if err != nil {
		return err
	}
	if !stageHasAC {
		logging.DebugStep(logger, "pve access control restore (cluster backup, ui)", "Skipped: no access control files in stage directory")
		return nil
	}

	etcPVE := "/etc/pve"
	mounted, mountErr := isMounted(etcPVE)
	if mountErr != nil {
		logger.Warning("PVE access control apply: unable to check pmxcfs mount (%s): %v", etcPVE, mountErr)
	}
	if !mounted {
		logger.Warning("PVE access control apply: %s is not mounted; skipping apply to avoid shadow writes on root filesystem", etcPVE)
		return nil
	}

	logger.Info("")
	message := fmt.Sprintf(
		"Cluster backup detected.\n\n"+
			"Applying PVE access control will modify users/roles/groups/ACLs and secrets cluster-wide.\n\n"+
			"WARNING: This may lock you out or break API tokens/automation.\n\n"+
			"Safety rail: root@pam is preserved from the current system and kept Administrator on /.\n\n"+
			"Recommendation: do this from local console/IPMI, not over SSH.\n\n"+
			"Apply 1:1 PVE access control now?",
	)
	applyNow, err := ui.ConfirmAction(ctx, "Apply PVE access control (cluster-wide)", message, "Apply 1:1 (expert)", "Skip apply", 90*time.Second, false)
	if err != nil {
		return err
	}
	logging.DebugStep(logger, "pve access control restore (cluster backup, ui)", "User choice: applyNow=%v", applyNow)
	if !applyNow {
		logger.Info("Skipping PVE access control apply (cluster backup).")
		return nil
	}

	rollbackPath := ""
	if accessControlRollbackBackup != nil {
		rollbackPath = strings.TrimSpace(accessControlRollbackBackup.BackupPath)
	}
	fullRollbackPath := ""
	if safetyBackup != nil {
		fullRollbackPath = strings.TrimSpace(safetyBackup.BackupPath)
	}

	if rollbackPath == "" && fullRollbackPath != "" {
		ok, err := ui.ConfirmAction(
			ctx,
			"Access control rollback backup not available",
			"Access control rollback backup is not available.\n\nIf you proceed, the rollback timer will use the full safety backup, which may revert other restored categories.\n\nProceed anyway?",
			"Proceed with full rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "pve access control restore (cluster backup, ui)", "User choice: allowFullRollback=%v", ok)
		if !ok {
			logger.Info("Skipping PVE access control apply (rollback backup not available).")
			return nil
		}
		rollbackPath = fullRollbackPath
	}

	if rollbackPath == "" {
		ok, err := ui.ConfirmAction(
			ctx,
			"No rollback available",
			"No rollback backup is available.\n\nIf you proceed and you get locked out, ProxSave cannot roll back automatically.\n\nProceed anyway?",
			"Proceed without rollback",
			"Skip apply",
			0,
			false,
		)
		if err != nil {
			return err
		}
		if !ok {
			logger.Info("Skipping PVE access control apply (no rollback available).")
			return nil
		}
	}

	var rollbackHandle *accessControlRollbackHandle
	if rollbackPath != "" {
		logger.Info("")
		logger.Info("Arming access control rollback timer (%ds)...", int(defaultAccessControlRollbackTimeout.Seconds()))
		rollbackHandle, err = armAccessControlRollback(ctx, logger, rollbackPath, defaultAccessControlRollbackTimeout, "/tmp/proxsave")
		if err != nil {
			return fmt.Errorf("arm access control rollback: %w", err)
		}
		logger.Info("Access control rollback log: %s", rollbackHandle.logPath)
	}

	if err := applyPVEAccessControlFromStage(ctx, logger, stageRoot); err != nil {
		return err
	}

	if rollbackHandle == nil {
		logger.Info("PVE access control applied (no rollback timer armed).")
		return nil
	}

	remaining := rollbackHandle.remaining(time.Now())
	if remaining <= 0 {
		return buildAccessControlApplyNotCommittedError(rollbackHandle)
	}

	logger.Info("")
	commitMessage := fmt.Sprintf(
		"PVE access control has been applied cluster-wide.\n\n"+
			"If needed, ProxSave will roll back automatically in %ds.\n\n"+
			"Keep access control changes?",
		int(remaining.Seconds()),
	)
	commit, err := ui.ConfirmAction(ctx, "Commit access control changes", commitMessage, "Keep", "Rollback", remaining, false)
	if err != nil {
		if errors.Is(err, input.ErrInputAborted) || errors.Is(err, context.Canceled) {
			return err
		}
		logger.Warning("Access control commit prompt failed: %v", err)
		return buildAccessControlApplyNotCommittedError(rollbackHandle)
	}

	if commit {
		disarmAccessControlRollback(ctx, logger, rollbackHandle)
		logger.Info("Access control changes committed.")
		return nil
	}

	return buildAccessControlApplyNotCommittedError(rollbackHandle)
}

func armAccessControlRollback(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (handle *accessControlRollbackHandle, err error) {
	done := logging.DebugStart(logger, "arm access control rollback", "backup=%s timeout=%s workDir=%s", strings.TrimSpace(backupPath), timeout, strings.TrimSpace(workDir))
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
	handle = &accessControlRollbackHandle{
		workDir:    baseDir,
		markerPath: filepath.Join(baseDir, fmt.Sprintf("access_control_rollback_pending_%s", timestamp)),
		scriptPath: filepath.Join(baseDir, fmt.Sprintf("access_control_rollback_%s.sh", timestamp)),
		logPath:    filepath.Join(baseDir, fmt.Sprintf("access_control_rollback_%s.log", timestamp)),
		armedAt:    time.Now(),
		timeout:    timeout,
	}

	if err := restoreFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback marker: %w", err)
	}

	script := buildAccessControlRollbackScript(handle.markerPath, backupPath, handle.logPath)
	if err := restoreFS.WriteFile(handle.scriptPath, []byte(script), 0o640); err != nil {
		return nil, fmt.Errorf("write rollback script: %w", err)
	}

	timeoutSeconds := int(timeout.Seconds())
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	if commandAvailable("systemd-run") {
		handle.unitName = fmt.Sprintf("proxsave-access-control-rollback-%s", timestamp)
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

func disarmAccessControlRollback(ctx context.Context, logger *logging.Logger, handle *accessControlRollbackHandle) {
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
		logger.Debug("Access control rollback disarmed (log=%s)", strings.TrimSpace(handle.logPath))
	}
}

func buildAccessControlRollbackScript(markerPath, backupPath, logPath string) string {
	targets := []string{
		"/etc/pve/user.cfg",
		"/etc/pve/domains.cfg",
		"/etc/pve/priv/shadow.cfg",
		"/etc/pve/priv/token.cfg",
		"/etc/pve/priv/tfa.cfg",
	}

	lines := []string{
		"#!/bin/sh",
		"set -eu",
		fmt.Sprintf("LOG=%s", shellQuote(logPath)),
		fmt.Sprintf("MARKER=%s", shellQuote(markerPath)),
		fmt.Sprintf("BACKUP=%s", shellQuote(backupPath)),
		`echo "[INFO] ========================================" >> "$LOG"`,
		`echo "[INFO] ACCESS CONTROL ROLLBACK SCRIPT STARTED" >> "$LOG"`,
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
		`if [ "$TAR_OK" -eq 1 ]; then`,
		`  echo "[INFO] --- PRUNE PHASE ---" >> "$LOG"`,
		`  (`,
		`    set +e`,
		`    MANIFEST_ALL=$(mktemp /tmp/proxsave/access_control_rollback_manifest_all_XXXXXX 2>/dev/null)`,
		`    MANIFEST=$(mktemp /tmp/proxsave/access_control_rollback_manifest_XXXXXX 2>/dev/null)`,
		`    if [ -z "$MANIFEST_ALL" ] || [ -z "$MANIFEST" ]; then`,
		`      echo "[WARN] mktemp failed - skipping prune phase"`,
		`      exit 0`,
		`    fi`,
		`    if ! tar -tzf "$BACKUP" > "$MANIFEST_ALL"; then`,
		`      echo "[WARN] Failed to list rollback archive - skipping prune phase"`,
		`      rm -f "$MANIFEST_ALL" "$MANIFEST"`,
		`      exit 0`,
		`    fi`,
		`    sed 's#^\\./##' "$MANIFEST_ALL" > "$MANIFEST"`,
	}

	for _, path := range targets {
		rel := strings.TrimPrefix(path, "/")
		lines = append(lines,
			fmt.Sprintf("    if [ -e %s ]; then", shellQuote(path)),
			fmt.Sprintf("      if ! grep -Fxq %s \"$MANIFEST\"; then", shellQuote(rel)),
			fmt.Sprintf("        rm -f -- %s || true", shellQuote(path)),
			`      fi`,
			`    fi`,
		)
	}

	lines = append(lines,
		`    rm -f "$MANIFEST_ALL" "$MANIFEST"`,
		`  ) >> "$LOG" 2>&1 || true`,
		`fi`,
		`rm -f "$MARKER" 2>/dev/null || true`,
	)
	return strings.Join(lines, "\n") + "\n"
}

