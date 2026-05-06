// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
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

type pbsMountGuardApply struct {
	ctx                  context.Context
	logger               *logging.Logger
	plan                 *RestorePlan
	stageRoot            string
	destRoot             string
	dryRun               bool
	fstabMounts          map[string]struct{}
	mountpointCandidates []string
	protected            map[string]struct{}
}

func maybeApplyPBSDatastoreMountGuards(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot, destRoot string, dryRun bool) error {
	apply := &pbsMountGuardApply{
		ctx:       ctx,
		logger:    logger,
		plan:      plan,
		stageRoot: stageRoot,
		destRoot:  destRoot,
		dryRun:    dryRun,
		protected: make(map[string]struct{}),
	}
	return apply.run()
}

func (a *pbsMountGuardApply) run() error {
	if !a.shouldRun() {
		return nil
	}

	blocks, err := a.stagedDatastoreBlocks()
	if err != nil || len(blocks) == 0 {
		return err
	}

	a.loadFstabMountpoints()
	for _, block := range blocks {
		a.applyDatastoreBlock(block)
	}
	return nil
}

func (a *pbsMountGuardApply) shouldRun() bool {
	if a.plan == nil || !a.plan.SystemType.SupportsPBS() || !a.plan.HasCategoryID("datastore_pbs") {
		return false
	}
	if strings.TrimSpace(a.stageRoot) == "" {
		return false
	}
	if filepath.Clean(strings.TrimSpace(a.destRoot)) != string(os.PathSeparator) {
		a.debug("Skipping PBS mount guards: restore destination is not system root (dest=%s)", a.destRoot)
		return false
	}
	return a.runtimeAllowsMountGuards()
}

func (a *pbsMountGuardApply) runtimeAllowsMountGuards() bool {
	if a.dryRun {
		a.info("Dry run enabled: skipping PBS mount guards")
		return false
	}
	if !isRealRestoreFS(restoreFS) {
		a.debug("Skipping PBS mount guards: non-system filesystem in use")
		return false
	}
	if mountGuardGeteuid() != 0 {
		a.warning("Skipping PBS mount guards: requires root privileges")
		return false
	}
	return true
}

func (a *pbsMountGuardApply) stagedDatastoreBlocks() ([]pbsDatastoreBlock, error) {
	stagePath := filepath.Join(a.stageRoot, "etc/proxmox-backup/datastore.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read staged datastore.cfg: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, nil
	}

	normalized, _ := normalizePBSDatastoreCfgContent(string(data))
	return mountGuardParsePBSDatastoreCfg(normalized)
}

func (a *pbsMountGuardApply) loadFstabMountpoints() {
	currentFstab := filepath.Join(a.destRoot, "etc", "fstab")
	mounts, err := mountGuardFstabMountpointsSet(currentFstab)
	if err != nil {
		a.warning("PBS mount guard: unable to parse current fstab %s: %v (continuing without fstab cross-check)", currentFstab, err)
		return
	}

	a.fstabMounts = mounts
	for mp := range mounts {
		if isValidGuardTarget(mp) && isConfirmableDatastoreMountRoot(mp) {
			a.mountpointCandidates = append(a.mountpointCandidates, mp)
		}
	}
	sortByLengthDesc(a.mountpointCandidates)
}

func (a *pbsMountGuardApply) applyDatastoreBlock(block pbsDatastoreBlock) {
	dsPath := filepath.Clean(strings.TrimSpace(block.Path))
	if !isValidGuardTarget(dsPath) {
		return
	}

	guardTarget := a.guardTargetForDatastore(dsPath)
	if !a.shouldProtectTarget(guardTarget) {
		return
	}
	if !a.prepareOfflineGuardTarget(guardTarget) {
		return
	}
	if a.mountAttemptSucceeded(guardTarget) {
		return
	}
	a.protectOfflineTarget(guardTarget)
}

func (a *pbsMountGuardApply) guardTargetForDatastore(dsPath string) string {
	guardTarget := ""
	if len(a.mountpointCandidates) > 0 {
		guardTarget = firstFstabMountpointMatch(dsPath, a.mountpointCandidates)
	}
	if guardTarget == "" {
		guardTarget = pbsMountGuardRootForDatastorePath(dsPath)
	}
	return filepath.Clean(strings.TrimSpace(guardTarget))
}

func (a *pbsMountGuardApply) shouldProtectTarget(guardTarget string) bool {
	if !isValidGuardTarget(guardTarget) {
		return false
	}
	if _, seen := a.protected[guardTarget]; seen {
		return false
	}
	if a.fstabMounts == nil {
		return true
	}
	_, ok := a.fstabMounts[guardTarget]
	return ok
}

func (a *pbsMountGuardApply) prepareOfflineGuardTarget(guardTarget string) bool {
	if err := mountGuardMkdirAll(guardTarget, 0o755); err != nil {
		a.warning("PBS mount guard: unable to create mountpoint directory %s: %v", guardTarget, err)
		return false
	}

	onRootFS, _, err := mountGuardIsPathOnRootFilesystem(guardTarget)
	if err != nil {
		a.warning("PBS mount guard: unable to determine filesystem device for %s: %v", guardTarget, err)
		return false
	}
	if !onRootFS {
		return false
	}

	mounted, err := isMounted(guardTarget)
	if err != nil {
		a.warning("PBS mount guard: unable to check mount status for %s: %v (continuing)", guardTarget, err)
		return true
	}
	if mounted {
		a.debug("PBS mount guard: mountpoint %s already mounted, skipping guard", guardTarget)
		return false
	}
	return true
}

func (a *pbsMountGuardApply) mountAttemptSucceeded(guardTarget string) bool {
	mountCtx, cancel := context.WithTimeout(a.ctx, mountGuardMountAttemptTimeout)
	out, err := restoreCmd.Run(mountCtx, "mount", guardTarget)
	cancel()
	if err != nil {
		a.logMountAttemptFailure(mountCtx, guardTarget, out, err)
		return false
	}
	if a.targetMovedOffRootFS(guardTarget) || a.targetIsMounted(guardTarget) {
		a.info("PBS mount guard: mountpoint %s is now mounted (mount attempt succeeded)", guardTarget)
		return true
	}
	return false
}

func (a *pbsMountGuardApply) targetMovedOffRootFS(guardTarget string) bool {
	onRootFS, _, err := mountGuardIsPathOnRootFilesystem(guardTarget)
	return err == nil && !onRootFS
}

func (a *pbsMountGuardApply) targetIsMounted(guardTarget string) bool {
	mounted, err := isMounted(guardTarget)
	return err == nil && mounted
}

func (a *pbsMountGuardApply) logMountAttemptFailure(mountCtx context.Context, guardTarget string, out []byte, err error) {
	if errors.Is(mountCtx.Err(), context.DeadlineExceeded) {
		a.warning("PBS mount guard: mount attempt timed out for %s after %s", guardTarget, mountGuardMountAttemptTimeout)
		return
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		a.debug("PBS mount guard: mount attempt failed for %s: %v (output=%s)", guardTarget, err, trimmed)
		return
	}
	a.debug("PBS mount guard: mount attempt failed for %s: %v", guardTarget, err)
}

func (a *pbsMountGuardApply) protectOfflineTarget(guardTarget string) {
	a.info("PBS mount guard: mountpoint %s offline, applying guard bind mount", guardTarget)
	if err := guardMountPoint(a.ctx, guardTarget); err != nil {
		a.protectOfflineTargetWithChattr(guardTarget, err)
		return
	}

	a.protected[guardTarget] = struct{}{}
	if entries, err := mountGuardReadDir(guardTarget); err == nil && len(entries) > 0 {
		a.warning("PBS mount guard: guard mount point %s is not empty (entries=%d)", guardTarget, len(entries))
	}
	a.warning("PBS mount guard: %s resolves to root filesystem (mount missing?) — bind-mounted a read-only guard to prevent writes until storage is available", guardTarget)
}

func (a *pbsMountGuardApply) protectOfflineTargetWithChattr(guardTarget string, bindErr error) {
	a.warning("PBS mount guard: failed to bind-mount guard on %s: %v; falling back to chattr +i", guardTarget, bindErr)
	if _, err := restoreCmd.Run(a.ctx, "chattr", "+i", guardTarget); err != nil {
		a.warning("PBS mount guard: failed to set immutable attribute on %s: %v", guardTarget, err)
		return
	}
	a.protected[guardTarget] = struct{}{}
	a.warning("PBS mount guard: %s resolves to root filesystem (mount missing?) — marked immutable (chattr +i) to prevent writes until storage is available", guardTarget)
}

func (a *pbsMountGuardApply) debug(format string, args ...interface{}) {
	if a.logger != nil {
		a.logger.Debug(format, args...)
	}
}

func (a *pbsMountGuardApply) info(format string, args ...interface{}) {
	if a.logger != nil {
		a.logger.Info(format, args...)
	}
}

func (a *pbsMountGuardApply) warning(format string, args ...interface{}) {
	if a.logger != nil {
		a.logger.Warning(format, args...)
	}
}
