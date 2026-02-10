package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		// Still apply mount guards because they only protect mountpoints from accidental writes.
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "pve staged apply", "Skip PVE storage/datacenter apply: cluster RECOVERY restores config.db")
		} else {
			if err := applyPVEStorageCfgFromStage(ctx, logger, stageRoot); err != nil {
				logger.Warning("PVE staged apply: storage.cfg: %v", err)
			}
		}

		if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, destRoot); err != nil {
			logger.Warning("PVE staged apply: mount guards: %v", err)
		}

		if !plan.NeedsClusterRestore {
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

	if err := writeFileAtomic(destPath, []byte(trimmed+"\n"), 0o644); err != nil {
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

	candidates := pveStorageMountGuardCandidatesFromSections(sections)
	if len(candidates) == 0 {
		return nil
	}

	currentFstab := filepath.Join(destRoot, "etc", "fstab")
	mounts, err := fstabMountpointsSet(currentFstab)
	if err != nil {
		if logger != nil {
			logger.Warning("PVE mount guard: unable to parse current fstab %s: %v (continuing without fstab cross-check)", currentFstab, err)
		}
	}
	var mountCandidates []string
	if len(mounts) > 0 {
		for mp := range mounts {
			if mp == "" || mp == "." || mp == string(os.PathSeparator) {
				continue
			}
			mountCandidates = append(mountCandidates, mp)
		}
		sortByLengthDesc(mountCandidates)
	}

	pvesmAvailable := false
	if _, err := restoreCmd.Run(ctx, "which", "pvesm"); err == nil {
		pvesmAvailable = true
	}

	protected := make(map[string]struct{})
	for _, item := range pveStorageMountGuardItems(candidates, mountCandidates, mounts) {
		guardTarget := filepath.Clean(strings.TrimSpace(item.GuardTarget))
		if guardTarget == "" || guardTarget == "." || guardTarget == string(os.PathSeparator) {
			continue
		}
		if _, ok := protected[guardTarget]; ok {
			continue
		}
		protected[guardTarget] = struct{}{}

		// Safety: only guard typical mount roots (prevent accidental rootfs directory shadowing).
		if !isConfirmableDatastoreMountRoot(guardTarget) {
			if logger != nil {
				logger.Debug("PVE mount guard: skip unsafe mount root %s (storage=%s type=%s)", guardTarget, item.StorageID, item.StorageType)
			}
			continue
		}

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

		// Best-effort mount/activate attempt (avoid guarding mountpoints that would mount cleanly).
		mountCtx, cancel := context.WithTimeout(ctx, mountGuardMountAttemptTimeout)
		var attemptErr error
		if item.IsNetwork && pvesmAvailable && item.StorageID != "" {
			_, attemptErr = restoreCmd.Run(mountCtx, "pvesm", "activate", item.StorageID)
		} else {
			_, attemptErr = restoreCmd.Run(mountCtx, "mount", guardTarget)
		}
		cancel()

		if attemptErr == nil {
			onRootFSNow, _, devErrNow := isPathOnRootFilesystem(guardTarget)
			if devErrNow == nil && !onRootFSNow {
				if logger != nil {
					logger.Info("PVE mount guard: mountpoint %s is now mounted (activation/mount attempt succeeded)", guardTarget)
				}
				continue
			}
			if mountedNow, mountErrNow := isMounted(guardTarget); mountErrNow == nil && mountedNow {
				if logger != nil {
					logger.Info("PVE mount guard: mountpoint %s is now mounted (activation/mount attempt succeeded)", guardTarget)
				}
				continue
			}
		}

		if logger != nil {
			if item.IsNetwork {
				logger.Info("PVE mount guard: storage %s (%s) offline, applying guard bind mount on %s", item.StorageID, item.StorageType, guardTarget)
			} else {
				logger.Info("PVE mount guard: mountpoint %s offline, applying guard bind mount", guardTarget)
			}
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

type pveStorageMountGuardCandidate struct {
	StorageID   string
	StorageType string
	Path        string
}

func pveStorageMountGuardCandidatesFromSections(sections []proxmoxNotificationSection) []pveStorageMountGuardCandidate {
	out := make([]pveStorageMountGuardCandidate, 0, len(sections))
	for _, s := range sections {
		storageType := strings.ToLower(strings.TrimSpace(s.Type))
		storageID := strings.TrimSpace(s.Name)
		if storageType == "" || storageID == "" {
			continue
		}

		c := pveStorageMountGuardCandidate{
			StorageID:   storageID,
			StorageType: storageType,
		}
		if storageType == "dir" {
			for _, kv := range s.Entries {
				if strings.EqualFold(strings.TrimSpace(kv.Key), "path") {
					c.Path = filepath.Clean(strings.TrimSpace(kv.Value))
					break
				}
			}
		}
		out = append(out, c)
	}
	return out
}

type pveStorageMountGuardItem struct {
	GuardTarget   string
	StorageID     string
	StorageType   string
	IsNetwork     bool
	RequiresFstab bool
}

func pveStorageMountGuardItems(candidates []pveStorageMountGuardCandidate, mountCandidates []string, fstabMounts map[string]struct{}) []pveStorageMountGuardItem {
	out := make([]pveStorageMountGuardItem, 0, len(candidates))
	for _, c := range candidates {
		storageType := strings.ToLower(strings.TrimSpace(c.StorageType))
		storageID := strings.TrimSpace(c.StorageID)
		if storageType == "" || storageID == "" {
			continue
		}

		switch storageType {
		case "nfs", "cifs", "cephfs", "glusterfs":
			out = append(out, pveStorageMountGuardItem{
				GuardTarget:   filepath.Join("/mnt/pve", storageID),
				StorageID:     storageID,
				StorageType:   storageType,
				IsNetwork:     true,
				RequiresFstab: false,
			})

		case "dir":
			path := filepath.Clean(strings.TrimSpace(c.Path))
			if path == "" || path == "." || path == string(os.PathSeparator) {
				continue
			}
			target := firstFstabMountpointMatch(path, mountCandidates)
			if target == "" {
				target = pbsMountGuardRootForDatastorePath(path)
			}
			target = filepath.Clean(strings.TrimSpace(target))
			if target == "" || target == "." || target == string(os.PathSeparator) {
				continue
			}
			// Only guard dir-backed storage if the mountpoint is present in fstab (avoid making rootfs dirs immutable).
			if fstabMounts == nil {
				continue
			}
			if _, ok := fstabMounts[target]; !ok {
				continue
			}
			out = append(out, pveStorageMountGuardItem{
				GuardTarget:   target,
				StorageID:     storageID,
				StorageType:   storageType,
				IsNetwork:     false,
				RequiresFstab: true,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return len(out[i].GuardTarget) > len(out[j].GuardTarget)
	})
	return out
}
