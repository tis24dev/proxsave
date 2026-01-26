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

func maybeApplyPVEConfigsFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot, destRoot string, dryRun bool) (err error) {
	if plan == nil || plan.SystemType != SystemTypePVE {
		return nil
	}
	if !plan.HasCategoryID("storage_pve") && !plan.HasCategoryID("pve_jobs") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		logging.DebugStep(logger, "pve staged apply", "Skipped: staging directory not available")
		return nil
	}
	if filepath.Clean(strings.TrimSpace(destRoot)) != string(os.PathSeparator) {
		logging.DebugStep(logger, "pve staged apply", "Skipped: restore destination is not system root (dest=%s)", destRoot)
		return nil
	}

	done := logging.DebugStart(logger, "pve staged apply", "dryRun=%v stage=%s", dryRun, stageRoot)
	defer func() { done(err) }()

	if dryRun {
		logger.Info("Dry run enabled: skipping staged PVE config apply")
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged PVE config apply: non-system filesystem in use")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping staged PVE config apply: requires root privileges")
		return nil
	}

	if plan.HasCategoryID("storage_pve") {
		if err := applyPVEVzdumpConfFromStage(logger, stageRoot); err != nil {
			logger.Warning("PVE staged apply: vzdump.conf: %v", err)
		}

		// In cluster RECOVERY mode, config.db restoration owns storage.cfg/datacenter.cfg.
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "pve staged apply", "Skip PVE storage/datacenter apply: cluster RECOVERY restores config.db")
		} else {
			if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, destRoot); err != nil {
				logger.Warning("PVE staged apply: mount guards: %v", err)
			}
			if err := applyPVEStorageCfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PVE staged apply: storage.cfg: %v", err)
			}
			if err := applyPVEDatacenterCfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PVE staged apply: datacenter.cfg: %v", err)
			}
		}
	}

	if plan.HasCategoryID("pve_jobs") {
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "pve staged apply", "Skip PVE backup jobs apply: cluster RECOVERY restores config.db")
		} else {
			if err := applyPVEBackupJobsFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PVE staged apply: jobs.cfg: %v", err)
			}
		}
	}

	return nil
}

func applyPVEVzdumpConfFromStage(logger *logging.Logger, stageRoot string) error {
	rel := "etc/vzdump.conf"
	stagePath := filepath.Join(stageRoot, rel)
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve staged apply file", "Skip %s: not present in staging directory", rel)
			return nil
		}
		return fmt.Errorf("read staged %s: %w", rel, err)
	}

	trimmed := strings.TrimSpace(string(data))
	destPath := "/etc/vzdump.conf"
	if trimmed == "" {
		logger.Warning("PVE staged apply: %s is empty; removing %s", rel, destPath)
		return removeIfExists(destPath)
	}

	if err := restoreFS.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", filepath.Dir(destPath), err)
	}
	if err := restoreFS.WriteFile(destPath, []byte(trimmed+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", destPath, err)
	}

	logging.DebugStep(logger, "pve staged apply file", "Applied %s -> %s", rel, destPath)
	return nil
}

func applyPVEStorageCfgFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	if _, err := restoreCmd.Run(ctx, "which", "pvesh"); err != nil {
		logger.Warning("pvesh not found; skipping PVE storage.cfg apply")
		return nil
	}

	stagePath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve staged apply storage.cfg", "Skipped: storage.cfg not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged storage.cfg: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		logging.DebugStep(logger, "pve staged apply storage.cfg", "Staged storage.cfg is empty; skipping apply")
		return nil
	}

	applied, failed, err := applyStorageCfg(ctx, stagePath, logger)
	if err != nil {
		return err
	}
	logger.Info("PVE staged apply: storage.cfg applied (ok=%d failed=%d)", applied, failed)
	return nil
}

func applyPVEDatacenterCfgFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	if _, err := restoreCmd.Run(ctx, "which", "pvesh"); err != nil {
		logger.Warning("pvesh not found; skipping PVE datacenter.cfg apply")
		return nil
	}

	stagePath := filepath.Join(stageRoot, "etc/pve/datacenter.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve staged apply datacenter.cfg", "Skipped: datacenter.cfg not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged datacenter.cfg: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		logging.DebugStep(logger, "pve staged apply datacenter.cfg", "Staged datacenter.cfg is empty; skipping apply")
		return nil
	}

	if err := runPvesh(ctx, logger, []string{"set", "/cluster/config", "-conf", stagePath}); err != nil {
		return err
	}
	logger.Info("PVE staged apply: datacenter.cfg applied")
	return nil
}

func applyPVEBackupJobsFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	if _, err := restoreCmd.Run(ctx, "which", "pvesh"); err != nil {
		logger.Warning("pvesh not found; skipping PVE jobs apply")
		return nil
	}

	stagePath := filepath.Join(stageRoot, "etc/pve/jobs.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve staged apply jobs.cfg", "Skipped: jobs.cfg not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged jobs.cfg: %w", err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		logging.DebugStep(logger, "pve staged apply jobs.cfg", "Staged jobs.cfg is empty; skipping apply")
		return nil
	}

	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse jobs.cfg: %w", err)
	}

	var jobs []proxmoxNotificationSection
	for _, s := range sections {
		if strings.EqualFold(strings.TrimSpace(s.Type), "vzdump") && strings.TrimSpace(s.Name) != "" {
			jobs = append(jobs, s)
		}
	}
	if len(jobs) == 0 {
		logging.DebugStep(logger, "pve staged apply jobs.cfg", "No vzdump jobs detected; skipping")
		return nil
	}

	applied := 0
	failed := 0
	for _, job := range jobs {
		jobID := strings.TrimSpace(job.Name)
		if jobID == "" {
			continue
		}

		args := []string{"create", "/cluster/backup", "--id", jobID}
		for _, kv := range job.Entries {
			key := strings.TrimSpace(kv.Key)
			value := strings.TrimSpace(kv.Value)
			if key == "" || value == "" {
				continue
			}
			args = append(args, "--"+key, value)
		}

		if err := runPvesh(ctx, logger, args); err != nil {
			// Fallback: if job exists, try updating it.
			updateArgs := []string{"set", fmt.Sprintf("/cluster/backup/%s", jobID)}
			for _, kv := range job.Entries {
				key := strings.TrimSpace(kv.Key)
				value := strings.TrimSpace(kv.Value)
				if key == "" || value == "" {
					continue
				}
				updateArgs = append(updateArgs, "--"+key, value)
			}
			if err2 := runPvesh(ctx, logger, updateArgs); err2 != nil {
				logger.Warning("Failed to apply PVE backup job %s: %v", jobID, err2)
				failed++
				continue
			}
		}

		applied++
		logger.Info("Applied PVE backup job %s", jobID)
	}

	if failed > 0 {
		return fmt.Errorf("applied=%d failed=%d", applied, failed)
	}
	return nil
}

func maybeApplyPVEStorageMountGuardsFromStage(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot, destRoot string) error {
	if plan == nil || plan.SystemType != SystemTypePVE || !plan.HasCategoryID("storage_pve") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		return nil
	}
	if filepath.Clean(strings.TrimSpace(destRoot)) != string(os.PathSeparator) {
		return nil
	}
	if !isRealRestoreFS(restoreFS) || os.Geteuid() != 0 {
		return nil
	}

	stagePath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read staged storage.cfg: %w", err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}

	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return fmt.Errorf("parse storage.cfg: %w", err)
	}
	storagePaths := pveDirStoragePathsFromSections(sections)
	if len(storagePaths) == 0 {
		return nil
	}

	currentFstab := filepath.Join(destRoot, "etc", "fstab")
	mounts, err := fstabMountpointsSet(currentFstab)
	if err != nil {
		if logger != nil {
			logger.Warning("PVE mount guard: unable to parse current fstab %s: %v (skipping guards)", currentFstab, err)
		}
		return nil
	}
	var mountCandidates []string
	for mp := range mounts {
		if mp == "" || mp == "." || mp == string(os.PathSeparator) {
			continue
		}
		if !isConfirmableDatastoreMountRoot(mp) {
			continue
		}
		mountCandidates = append(mountCandidates, mp)
	}
	sortByLengthDesc(mountCandidates)

	guardTargets := pveGuardTargetsForStoragePaths(storagePaths, mountCandidates)
	if len(guardTargets) == 0 {
		return nil
	}

	protected := make(map[string]struct{})
	for _, guardTarget := range guardTargets {
		guardTarget = filepath.Clean(strings.TrimSpace(guardTarget))
		if guardTarget == "" || guardTarget == "." || guardTarget == string(os.PathSeparator) {
			continue
		}
		if _, ok := protected[guardTarget]; ok {
			continue
		}
		protected[guardTarget] = struct{}{}

		if err := os.MkdirAll(guardTarget, 0o755); err != nil {
			if logger != nil {
				logger.Warning("PVE mount guard: unable to create mountpoint directory %s: %v", guardTarget, err)
			}
			continue
		}

		onRootFS, _, devErr := isPathOnRootFilesystem(guardTarget)
		if devErr != nil {
			if logger != nil {
				logger.Warning("PVE mount guard: unable to determine filesystem device for %s: %v", guardTarget, devErr)
			}
			continue
		}
		if !onRootFS {
			continue
		}

		mounted, mountErr := isMounted(guardTarget)
		if mountErr == nil && mounted {
			continue
		}

		mountCtx, cancel := context.WithTimeout(ctx, mountGuardMountAttemptTimeout)
		_, attemptErr := restoreCmd.Run(mountCtx, "mount", guardTarget)
		cancel()
		if attemptErr == nil {
			onRootFSNow, _, devErrNow := isPathOnRootFilesystem(guardTarget)
			if devErrNow == nil && !onRootFSNow {
				continue
			}
			if mountedNow, mountErrNow := isMounted(guardTarget); mountErrNow == nil && mountedNow {
				continue
			}
		}

		if logger != nil {
			logger.Info("PVE mount guard: mountpoint %s offline, applying guard bind mount", guardTarget)
		}
		if err := guardMountPoint(ctx, guardTarget); err != nil {
			if logger != nil {
				logger.Warning("PVE mount guard: failed to bind-mount guard on %s: %v; falling back to chattr +i", guardTarget, err)
			}
			if _, fallbackErr := restoreCmd.Run(ctx, "chattr", "+i", guardTarget); fallbackErr != nil {
				if logger != nil {
					logger.Warning("PVE mount guard: failed to set immutable attribute on %s: %v", guardTarget, fallbackErr)
				}
				continue
			}
			if logger != nil {
				logger.Warning("PVE mount guard: %s resolves to root filesystem (mount missing?) — marked immutable (chattr +i) to prevent writes until storage is available", guardTarget)
			}
			continue
		}
		if logger != nil {
			logger.Warning("PVE mount guard: %s resolves to root filesystem (mount missing?) — bind-mounted a read-only guard to prevent writes until storage is available", guardTarget)
		}
	}

	return nil
}

func pveDirStoragePathsFromSections(sections []proxmoxNotificationSection) []string {
	var out []string
	for _, s := range sections {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "storage") {
			continue
		}
		typ := ""
		path := ""
		for _, kv := range s.Entries {
			key := strings.TrimSpace(kv.Key)
			val := strings.TrimSpace(kv.Value)
			switch key {
			case "type":
				typ = val
			case "path":
				path = val
			}
		}
		if strings.EqualFold(strings.TrimSpace(typ), "dir") && strings.TrimSpace(path) != "" {
			out = append(out, filepath.Clean(path))
		}
	}
	return out
}

func pveGuardTargetsForStoragePaths(storagePaths, mountCandidates []string) []string {
	out := make([]string, 0, len(storagePaths))
	seen := make(map[string]struct{}, len(storagePaths))
	for _, sp := range storagePaths {
		sp = filepath.Clean(strings.TrimSpace(sp))
		if sp == "" || sp == "." || sp == string(os.PathSeparator) {
			continue
		}
		target := firstFstabMountpointMatch(sp, mountCandidates)
		if target == "" {
			target = pbsMountGuardRootForDatastorePath(sp)
		}
		target = filepath.Clean(strings.TrimSpace(target))
		if target == "" || target == "." || target == string(os.PathSeparator) {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	sortByLengthDesc(out)
	return out
}
