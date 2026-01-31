package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func maybeApplyPVESDNFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot string, dryRun bool) (err error) {
	if plan == nil || plan.SystemType != SystemTypePVE || !plan.HasCategoryID("pve_sdn") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "pve sdn staged apply", "Skipped: staging directory not available")
		return nil
	}

	done := logging.DebugStart(logger, "pve sdn staged apply", "dryRun=%v stage=%s", dryRun, strings.TrimSpace(stageRoot))
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping PVE SDN apply")
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping PVE SDN apply: non-system filesystem in use")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping PVE SDN apply: requires root privileges")
		return nil
	}

	// In cluster RECOVERY mode, config.db restoration owns /etc/pve state and /etc/pve is unmounted during restore.
	if plan.NeedsClusterRestore {
		logging.DebugStep(logger, "pve sdn staged apply", "Skip: cluster RECOVERY restores config.db")
		return nil
	}

	etcPVE := "/etc/pve"
	mounted, mountErr := isMounted(etcPVE)
	if mountErr != nil {
		logger.Warning("PVE SDN apply: unable to check pmxcfs mount (%s): %v", etcPVE, mountErr)
	}
	if !mounted {
		logger.Warning("PVE SDN apply: %s is not mounted; skipping SDN apply to avoid shadow writes on root filesystem", etcPVE)
		return nil
	}

	applied, err := applyPVESDNFromStage(logger, stageRoot)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		logging.DebugStep(logger, "pve sdn staged apply", "No changes applied (no SDN data in staging directory)")
		return nil
	}

	logger.Info("PVE SDN staged apply: applied %d item(s)", len(applied))
	logger.Warning("PVE SDN note: this restores SDN definitions only; you may still need to apply SDN changes via the Proxmox UI/CLI")
	return nil
}

func applyPVESDNFromStage(logger *logging.Logger, stageRoot string) (applied []string, err error) {
	stageRoot = strings.TrimSpace(stageRoot)
	done := logging.DebugStart(logger, "pve sdn apply", "stage=%s", stageRoot)
	defer func() { done(err) }()

	if stageRoot == "" {
		return nil, nil
	}

	stageSDN := filepath.Join(stageRoot, "etc", "pve", "sdn")
	destSDN := "/etc/pve/sdn"

	if info, err := restoreFS.Stat(stageSDN); err == nil {
		if info.IsDir() {
			paths, err := syncDirExact(stageSDN, destSDN)
			if err != nil {
				return applied, err
			}
			applied = append(applied, paths...)
		} else {
			ok, err := copyFileExact(stageSDN, destSDN)
			if err != nil {
				return applied, err
			}
			if ok {
				applied = append(applied, destSDN)
			}
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return applied, fmt.Errorf("stat staged sdn %s: %w", stageSDN, err)
	}

	// Legacy/alternate config layout (best-effort).
	stageSDNCfg := filepath.Join(stageRoot, "etc", "pve", "sdn.cfg")
	destSDNCfg := "/etc/pve/sdn.cfg"
	ok, err := copyFileExact(stageSDNCfg, destSDNCfg)
	if err != nil {
		return applied, err
	}
	if ok {
		applied = append(applied, destSDNCfg)
	}

	return applied, nil
}
