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
	if plan == nil || !plan.SystemType.SupportsPVE() {
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
	// Cancellation is checked here and again before EVERY arm below. The
	// between-steps check in runStagedApplySteps guards only the boundary between
	// staged-apply steps; inside this one the arms write straight into pmxcfs, so
	// without a per-arm gate an aborted restore still applied datacenter.cfg,
	// vzdump.cron and the storage definitions cluster-wide. An abort returns
	// ctx.Err() rather than the failedItems aggregate, so the caller reads it as an
	// abort (input.IsAborted matches context.Canceled) and not as "with warnings".
	if cerr := ctx.Err(); cerr != nil {
		logger.Warning("PVE staged apply aborted: %v", cerr)
		return cerr
	}
	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping staged PVE config apply: non-system filesystem in use")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping staged PVE config apply: requires root privileges")
		return nil
	}

	// failedItems accumulates config items that ended up NOT applied, so the
	// caller reports the restore "with warnings" instead of a clean success
	// rather than silently swallowing failed applies (BH-003).
	var failedItems []string
	var aborted error

	// applyArm runs ONE arm behind the shared cancellation gate, so an arm added
	// later cannot be added without it. Per-arm wording is unchanged.
	applyArm := func(name string, run func() error) {
		if aborted != nil {
			return
		}
		if cerr := ctx.Err(); cerr != nil {
			aborted = cerr
			return
		}
		if err := run(); err != nil {
			// An arm can return the caller's cancellation now that the helpers check
			// ctx immediately before each irreversible write. That is an ABORT, not
			// an item that failed to apply. Recording it in failedItems would make
			// the LAST arm's cancellation surface as "1 PVE config item(s) failed to
			// apply": no later gate would run to set aborted, so the operator's own
			// abort would be reported back as a staged-apply failure.
			//
			// Two things are decided here and they are NOT the same question.
			//
			// Whether the restore is aborting is decided by the PARENT ctx, never by
			// the error. An arm can carry its own inner deadline
			// (maybeApplyPVEStorageMountGuardsFromStage derives mountCtx below), and
			// that timeout expiring with a live restore is an item failure, not an
			// operator abort. Only ctx.Err() tells the two apart.
			//
			// Whether THIS arm also failed on its own is decided by the error. An
			// abort landing exactly while an arm was failing for a reason of its own
			// must not erase which item that was: the tail below already prints both
			// ("aborted: X (N item(s) had already failed: ...)"), so the name is kept
			// unless the error IS the abort propagating out of the arm.
			if cerr := ctx.Err(); cerr != nil {
				if !errors.Is(err, cerr) {
					logger.Warning("PVE staged apply: %s: %v", name, err)
					failedItems = append(failedItems, name)
				}
				aborted = cerr
				return
			}
			logger.Warning("PVE staged apply: %s: %v", name, err)
			failedItems = append(failedItems, name)
		}
	}

	if plan.HasCategoryID("storage_pve") {
		applyArm("vzdump.conf", func() error { return applyPVEVzdumpConfFromStage(ctx, logger, stageRoot) })

		// In cluster RECOVERY mode, config.db restoration owns storage.cfg/datacenter.cfg.
		// Still apply mount guards because they only protect mountpoints from accidental writes.
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "pve staged apply", "Skip PVE storage/datacenter apply: cluster RECOVERY restores config.db")
		} else {
			applyArm("storage.cfg", func() error { return applyPVEStorageCfgFromStage(ctx, logger, stageRoot) })
		}

		applyArm("mount guards", func() error {
			return maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, plan, stageRoot, destRoot)
		})

		if !plan.NeedsClusterRestore {
			applyArm("datacenter.cfg", func() error { return applyPVEDatacenterCfgFromStage(ctx, logger, stageRoot) })
		}
	}

	if plan.HasCategoryID("pve_jobs") {
		if plan.NeedsClusterRestore {
			logging.DebugStep(logger, "pve staged apply", "Skip PVE backup jobs apply: cluster RECOVERY restores config.db")
		} else {
			applyArm("jobs.cfg", func() error { return applyPVEBackupJobsFromStage(ctx, logger, stageRoot) })
			applyArm("vzdump.cron", func() error { return applyPVEVzdumpCronFromStage(ctx, logger, stageRoot) })
		}
	}

	if aborted != nil {
		if len(failedItems) > 0 {
			logger.Warning("PVE staged apply aborted: %v (%d item(s) had already failed: %s)",
				aborted, len(failedItems), strings.Join(failedItems, ", "))
		} else {
			logger.Warning("PVE staged apply aborted: %v", aborted)
		}
		return aborted
	}

	if len(failedItems) > 0 {
		return fmt.Errorf("%d PVE config item(s) failed to apply: %s", len(failedItems), strings.Join(failedItems, ", "))
	}
	return nil
}

// The ctx is not decoration. applyArm gates cancellation BETWEEN arms, so without
// a check in here the window in which a cancelled restore can still write is a
// whole arm: reading the staged file, trimming it, and only then writing. The
// checks below shrink that window to the instant before each irreversible call.
// They cannot close it: cancellation can still land between the check and the
// write, and there is no atomic "write unless cancelled" to reach for.
func applyPVEVzdumpConfFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
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
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		logger.Warning("PVE staged apply: %s is empty; removing %s", rel, destPath)
		return removeIfExists(destPath)
	}

	if cerr := ctx.Err(); cerr != nil {
		return cerr
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
	if failed > 0 {
		return fmt.Errorf("storage.cfg applied with %d failure(s) (ok=%d)", failed, applied)
	}
	return nil
}

// applyPVEDatacenterCfgFromStage writes the staged datacenter.cfg into pmxcfs.
// The old arm called `pvesh set /cluster/config -conf <file>`, an endpoint a live
// PVE 9.1.9 node answers with "No 'set' handler defined for '/cluster/config'"
// (probed 2026-09-02): datacenter.cfg was therefore NEVER restored on a staged
// apply. The API has no whole-file endpoint (options live per-key under
// /cluster/options); the file write replicates cluster-wide because /etc/pve IS
// pmxcfs, and pvesh is not needed at all.
func applyPVEDatacenterCfgFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
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

	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err := pmxcfsWriteFile(logger, "datacenter.cfg", data); err != nil {
		return fmt.Errorf("apply staged datacenter.cfg: %w", err)
	}
	logger.Info("PVE staged apply: datacenter.cfg written to pmxcfs (cluster-wide)")
	return nil
}

// applyPVEVzdumpCronFromStage writes the staged vzdump.cron into pmxcfs. The
// file was collected under pve_jobs and documented as staged-applied, but no
// restore path ever read it back (fable-check bug 5): legacy cron backup jobs
// silently vanished from every staged restore. There is no pvesh endpoint for
// vzdump.cron; the file lives in pmxcfs, so the write IS the cluster-wide
// apply, the same way datacenter.cfg is handled.
func applyPVEVzdumpCronFromStage(ctx context.Context, logger *logging.Logger, stageRoot string) error {
	stagePath := filepath.Join(stageRoot, "etc/pve/vzdump.cron")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logging.DebugStep(logger, "pve staged apply vzdump.cron", "Skipped: vzdump.cron not present in staging directory")
			return nil
		}
		return fmt.Errorf("read staged vzdump.cron: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		logging.DebugStep(logger, "pve staged apply vzdump.cron", "Staged vzdump.cron is empty; skipping apply")
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err := pmxcfsWriteFile(logger, "vzdump.cron", data); err != nil {
		return fmt.Errorf("apply staged vzdump.cron: %w", err)
	}
	logger.Info("PVE staged apply: vzdump.cron written to pmxcfs (cluster-wide)")
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
	if plan == nil || !plan.SystemType.SupportsPVE() || !plan.HasCategoryID("storage_pve") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		return nil
	}
	if filepath.Clean(strings.TrimSpace(destRoot)) != string(os.PathSeparator) {
		return nil
	}
	if !isRealRestoreFS(restoreFS) || mountGuardGeteuid() != 0 {
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

		// Resolve symlinks and re-check the allowlist BEFORE any mkdir / mount /
		// activate / RO bind / chattr +i, so a parent-component symlink cannot make
		// the guard escape the datastore roots (mirrors the cleanup path; shared
		// helper). Fail-safe on an unresolvable path or an escape: skip.
		resolved, _, ok, resErr := resolveGuardTargetWithinAllowlist(guardTarget)
		if resErr != nil {
			if logger != nil {
				logger.Warning("PVE mount guard: cannot resolve %s: %v; skipping guard (fail-safe)", guardTarget, resErr)
			}
			continue
		}
		if !ok {
			if logger != nil {
				logger.Warning("PVE mount guard: %s resolves outside the datastore roots (%s); skipping guard (fail-safe)", guardTarget, resolved)
			}
			continue
		}
		guardTarget = resolved

		if err := mountGuardMkdirAll(guardTarget, 0o750); err != nil {
			if logger != nil {
				logger.Warning("PVE mount guard: unable to create mountpoint directory %s: %v", guardTarget, err)
			}
			continue
		}

		onRootFS, _, devErr := mountGuardIsPathOnRootFilesystem(guardTarget)
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
			onRootFSNow, _, devErrNow := mountGuardIsPathOnRootFilesystem(guardTarget)
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
			// Warn-only fallback: ProxSave no longer sets a persistent chattr +i here
			// (it survived reboots and re-blocked writes once the storage was later
			// unmounted). The config-only restore never extracts into datastore
			// mountpoints and ProxSave's own dir recreation is skipped by the
			// storage-mount preflight, so this only leaves EXTERNAL writers unblocked
			// while the storage is offline. Legacy flags are still cleared by
			// --cleanup-guards.
			if logger != nil {
				logger.Warning("PVE mount guard: could NOT guard offline mountpoint %s (read-only bind mount failed: %v). "+
					"Writes there while the storage is offline are not blocked and would land on the root filesystem. "+
					"Remedy: activate/mount the storage (e.g. `pvesm activate <storage>` or `mount %s` / `zpool import`) before any guest or job uses it.",
					guardTarget, err, guardTarget)
			}
			continue
		}
		if logger != nil {
			logger.Warning("PVE mount guard: %s resolves to root filesystem (mount missing?), bind-mounted a read-only guard to prevent writes until storage is available", guardTarget)
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
