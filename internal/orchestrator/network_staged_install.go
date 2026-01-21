package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// maybeInstallNetworkConfigFromStage installs staged network files to system paths without reloading networking.
// It is designed to be prevention-first: if preflight validation fails, network files are rolled back automatically.
func maybeInstallNetworkConfigFromStage(
	ctx context.Context,
	logger *logging.Logger,
	plan *RestorePlan,
	stageRoot string,
	archivePath string,
	networkRollbackBackup *SafetyBackupResult,
	dryRun bool,
) (installed bool, err error) {
	if plan == nil || !plan.HasCategoryID("network") {
		return false, nil
	}
	stageRoot = strings.TrimSpace(stageRoot)
	if stageRoot == "" {
		return false, nil
	}

	done := logging.DebugStart(logger, "network staged install", "dryRun=%v stage=%s", dryRun, stageRoot)
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping staged network install")
		return false, nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged network install: non-system filesystem in use")
		return false, nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping staged network install: requires root privileges")
		return false, nil
	}

	rollbackPath := ""
	if networkRollbackBackup != nil {
		rollbackPath = strings.TrimSpace(networkRollbackBackup.BackupPath)
	}
	if rollbackPath == "" {
		logger.Warning("Network staged install skipped: network rollback backup not available")
		logger.Info("Network files remain staged under: %s", stageRoot)
		return false, nil
	}

	logger.Info("Network restore: validating staged configuration before writing to /etc (no live reload)")

	logging.DebugStep(logger, "network staged install", "Apply staged network files to system paths (no reload)")
	applied, err := applyNetworkFilesFromStage(logger, stageRoot)
	if err != nil {
		return false, err
	}
	logging.DebugStep(logger, "network staged install", "Staged network files applied: %d", len(applied))

	logging.DebugStep(logger, "network staged install", "Attempt automatic NIC name repair (safe mappings only)")
	if repair := maybeRepairNICNamesAuto(ctx, logger, archivePath); repair != nil {
		if repair.Applied() || repair.SkippedReason != "" {
			logger.Info("%s", repair.Summary())
		} else {
			logger.Debug("%s", repair.Summary())
		}
	}

	logging.DebugStep(logger, "network staged install", "Run network preflight validation (no reload)")
	preflight := runNetworkPreflightValidation(ctx, 5*time.Second, logger)
	if preflight.Ok() {
		logger.Info("Network restore: staged configuration installed successfully (preflight OK).")
		return true, nil
	}

	logger.Warning("%s", preflight.Summary())
	if out := strings.TrimSpace(preflight.Output); out != "" {
		logger.Debug("Network preflight output:\n%s", out)
	}

	logging.DebugStep(logger, "network staged install", "Preflight failed: rolling back network files automatically (backup=%s)", rollbackPath)
	rollbackLog, rbErr := rollbackNetworkFilesNow(ctx, logger, rollbackPath, "")
	if strings.TrimSpace(rollbackLog) != "" {
		logger.Info("Network rollback log: %s", rollbackLog)
	}
	if rbErr != nil {
		logger.Error("Network restore aborted: staged configuration failed validation (%s) and rollback failed: %v", preflight.CommandLine(), rbErr)
		return false, fmt.Errorf("network staged install preflight failed; rollback attempt failed: %w", rbErr)
	}

	logger.Warning(
		"Network restore aborted: staged configuration failed validation (%s). Rolled back /etc/network/*, /etc/hosts, /etc/hostname, /etc/resolv.conf to the pre-restore state (rollback=%s).",
		preflight.CommandLine(),
		rollbackPath,
	)
	logger.Info("Staged network files remain available under: %s", stageRoot)
	return false, fmt.Errorf("network staged install preflight failed; network files rolled back")
}

func maybeRepairNICNamesAuto(ctx context.Context, logger *logging.Logger, archivePath string) *nicRepairResult {
	done := logging.DebugStart(logger, "NIC repair auto", "archive=%s", strings.TrimSpace(archivePath))
	defer func() { done(nil) }()

	plan, err := planNICNameRepair(ctx, archivePath)
	if err != nil {
		logger.Warning("NIC name repair failed: %v", err)
		return nil
	}

	overrides, err := detectNICNamingOverrideRules(logger)
	if err != nil {
		logger.Debug("NIC naming override detection failed: %v", err)
	} else if !overrides.Empty() {
		logger.Warning("%s", overrides.Summary())
		return &nicRepairResult{AppliedAt: nowRestore(), SkippedReason: "skipped due to persistent NIC naming rules (auto-safe)"}
	}

	if plan != nil && len(plan.Conflicts) > 0 {
		logger.Warning("NIC name repair: %d conflict(s) detected; applying only non-conflicting mappings (auto-safe)", len(plan.Conflicts))
		for i, conflict := range plan.Conflicts {
			if i >= 8 {
				logger.Debug("NIC conflict details truncated (showing first 8)")
				break
			}
			logger.Debug("NIC conflict: %s", conflict.Details())
		}
	}

	result, err := applyNICNameRepair(logger, plan, false)
	if err != nil {
		logger.Warning("NIC name repair failed: %v", err)
		return nil
	}
	return result
}
