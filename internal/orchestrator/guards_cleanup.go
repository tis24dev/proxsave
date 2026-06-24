package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

var (
	cleanupGeteuid    = os.Geteuid
	cleanupStat       = os.Stat
	cleanupReadFile   = os.ReadFile
	cleanupRemoveAll  = os.RemoveAll
	cleanupSysUnmount = syscall.Unmount

	// cleanupChattrReadFile reads the immutable-guard index. Separate from
	// cleanupReadFile (which reads /proc) so tests can supply index bytes
	// without faking /proc.
	cleanupChattrReadFile = os.ReadFile
	// cleanupResolveTarget resolves an immutable-guard target through any symlinks
	// before it is re-validated and cleared, so a parent-component symlink cannot
	// make `chattr -i` escape the datastore-root allowlist. Injectable for tests.
	cleanupResolveTarget = filepath.EvalSymlinks
	// cleanupRunCmd runs the `chattr -i` that reverses an immutable fallback
	// guard. Injectable like the other cleanup* seams; defaults to the restore
	// command runner.
	cleanupRunCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return restoreCmd.Run(ctx, name, args...)
	}
)

// CleanupMountGuards removes ProxSave mount guards created under mountGuardBaseDir.
//
// Safety: this will only unmount guard bind mounts when they are the currently-visible
// mount on the mountpoint (i.e. the guard is the top-most mount at that mountpoint).
// If a real mount is stacked on top, the guard will be left in place.
func CleanupMountGuards(ctx context.Context, logger *logging.Logger, dryRun bool) error {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	if cleanupGeteuid() != 0 {
		return fmt.Errorf("cleanup guards requires root privileges")
	}

	if _, err := cleanupStat(mountGuardBaseDir); err != nil {
		if os.IsNotExist(err) {
			logger.Info("No guard directory found at %s — nothing to clean up. If you deleted it manually and a mountpoint is still read-only, check 'lsattr -d <mountpoint>' and clear it with 'chattr -i <mountpoint>' while the storage is unmounted.", mountGuardBaseDir)
			return nil
		}
		return fmt.Errorf("stat guards dir: %w", err)
	}

	// Reverse the chattr +i fallback guards first, independently of the bind-mount
	// state below. This runs on BOTH paths (the no-guard-mounts early return AND the
	// bind-mount loop). pending counts targets left immutable (mounted/unresolvable/
	// failed); while it is > 0 we keep the guard directory and its index so a later
	// run can finish the job.
	pending := clearImmutableGuards(ctx, logger, dryRun)

	mountinfo, err := cleanupReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}

	visibleMountpoints, hiddenMountpoints, totalGuardMounts := guardMountpointsFromMountinfo(string(mountinfo))
	if totalGuardMounts == 0 {
		if pending > 0 {
			logger.Info("Guard cleanup: %d immutable guard target(s) still pending (mounted or uncleared); keeping %s", pending, mountGuardBaseDir)
			return nil
		}
		if dryRun {
			logger.Info("DRY RUN: would remove %s", mountGuardBaseDir)
			return nil
		}
		if err := cleanupRemoveAll(mountGuardBaseDir); err != nil {
			return fmt.Errorf("remove guards dir: %w", err)
		}
		logger.Info("Removed guard directory %s", mountGuardBaseDir)
		return nil
	}

	var targets []string
	dedup := make(map[string]struct{}, len(visibleMountpoints))
	for _, mp := range visibleMountpoints {
		mp = filepath.Clean(strings.TrimSpace(mp))
		if mp == "" || mp == "." || mp == string(os.PathSeparator) {
			continue
		}
		if _, ok := dedup[mp]; ok {
			continue
		}
		dedup[mp] = struct{}{}
		targets = append(targets, mp)
	}
	sort.Strings(targets)
	for _, mp := range hiddenMountpoints {
		mp = filepath.Clean(strings.TrimSpace(mp))
		if mp == "" || mp == "." || mp == string(os.PathSeparator) {
			continue
		}
		logger.Debug("Guard cleanup: guard mount at %s is hidden under another mount; skipping unmount", mp)
	}

	unmounted := 0
	for _, mp := range targets {
		if !isConfirmableDatastoreMountRoot(mp) {
			logger.Debug("Guard cleanup: skip non-datastore mount root %s", mp)
			continue
		}

		if dryRun {
			logger.Info("DRY RUN: would unmount guard mount at %s", mp)
			continue
		}

		if err := cleanupSysUnmount(mp, 0); err != nil {
			// EINVAL: not a mountpoint (already unmounted).
			if errno, ok := err.(syscall.Errno); ok && errno == syscall.EINVAL {
				logger.Debug("Guard cleanup: %s is not a mountpoint (already unmounted)", mp)
				continue
			}
			logger.Warning("Guard cleanup: failed to unmount %s: %v", mp, err)
			continue
		}
		unmounted++
		logger.Info("Guard cleanup: unmounted guard at %s", mp)
	}

	if dryRun {
		if pending > 0 {
			logger.Info("DRY RUN: would keep %s (%d immutable guard target(s) still pending)", mountGuardBaseDir, pending)
		} else {
			logger.Info("DRY RUN: would remove %s", mountGuardBaseDir)
		}
		return nil
	}

	// If any guard mounts remain (for example hidden under a real mount), or any
	// immutable guard target is still pending, avoid removing the directory/index.
	remaining := 0
	if after, rerr := cleanupReadFile("/proc/self/mountinfo"); rerr == nil {
		_, _, remaining = guardMountpointsFromMountinfo(string(after))
	}
	if remaining > 0 || pending > 0 {
		logger.Warning("Guard cleanup: %d guard mount(s) and %d immutable target(s) still present; not removing %s", remaining, pending, mountGuardBaseDir)
		return nil
	}

	if err := cleanupRemoveAll(mountGuardBaseDir); err != nil {
		return fmt.Errorf("remove guards dir: %w", err)
	}
	logger.Info("Removed guard directory %s (unmounted=%d)", mountGuardBaseDir, unmounted)
	return nil
}

func guardMountpointsFromMountinfo(mountinfo string) (visible, hidden []string, guardMounts int) {
	prefix := mountGuardBaseDir + string(os.PathSeparator)
	type mountpointInfo struct {
		topmostID      int
		topmostIsGuard bool
		hasGuard       bool
	}

	mountpoints := make(map[string]*mountpointInfo)
	for _, line := range strings.Split(mountinfo, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		mountID, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		root := unescapeProcPath(fields[3])
		mp := unescapeProcPath(fields[4])

		isGuard := root == mountGuardBaseDir || strings.HasPrefix(root, prefix)
		if isGuard {
			guardMounts++
		}

		info := mountpoints[mp]
		if info == nil {
			info = &mountpointInfo{topmostID: -1}
			mountpoints[mp] = info
		}
		if mountID > info.topmostID {
			info.topmostID = mountID
			info.topmostIsGuard = isGuard
		}
		if isGuard {
			info.hasGuard = true
		}
	}

	for mp, info := range mountpoints {
		if info.topmostIsGuard {
			visible = append(visible, mp)
			continue
		}
		if info.hasGuard {
			hidden = append(hidden, mp)
		}
	}
	sort.Strings(visible)
	sort.Strings(hidden)
	return visible, hidden, guardMounts
}

// clearImmutableGuards reverses the `chattr +i` immutable fallback that restore
// applied when a guard bind-mount could not be created. It is the symmetric
// counterpart to the bind-mount unmount loop and processes ONLY the targets
// ProxSave itself recorded in the immutable-guard index. For each one:
//
//   - a non-datastore mount root is skipped (defense-in-depth against a tampered
//     or corrupt index — the operation can never escape /mnt, /media, /run/media);
//   - a target that is currently mounted is skipped, because the immutable flag is
//     on the shadowed underlying directory and `chattr -i` would touch the live
//     mount instead (this mirrors how a hidden bind-mount guard is left in place);
//   - dry-run only logs the intended action.
//
// Every step is best-effort and non-fatal: a missing/empty index is a no-op, and a
// failed `chattr -i` on one target does not stop the others or abort cleanup.
//
// It returns the number of targets left immutable ("pending"): those skipped because
// they are currently mounted, whose mount status or path could not be resolved, or
// whose `chattr -i` failed. The caller keeps the guard directory (and its index) on
// disk while pending > 0, so a later run — once the storage is unmounted — can still
// clear them; the index is removed (with the directory) only when nothing is pending
// and no bind-mount guards remain. In dry-run, pending reflects what a real run would
// leave behind (mounted/unresolvable targets), so the "would remove" preview is honest.
func clearImmutableGuards(ctx context.Context, logger *logging.Logger, dryRun bool) int {
	data, err := cleanupChattrReadFile(mountGuardChattrTargetsPath())
	if err != nil {
		return 0 // missing/unreadable index => nothing was recorded => no-op
	}

	pending := 0
	for _, target := range parseImmutableGuardTargets(data) {
		// Defense-in-depth against a tampered/corrupt index: only ever touch a
		// datastore mount root (/mnt, /media, /run/media). A dropped entry is not
		// pending — it is removed when the directory is finally cleaned up.
		if !isConfirmableDatastoreMountRoot(target) {
			logger.Debug("Guard cleanup: skip non-datastore immutable target %s", target)
			continue
		}

		mounted, mErr := isMounted(target)
		if mErr != nil {
			logger.Warning("Guard cleanup: cannot determine mount status of %s: %v; leaving immutable flag (clear manually with: chattr -i %s)", target, mErr, target)
			pending++
			continue
		}
		if mounted {
			// The real storage is mounted on top, so the immutable flag is on the
			// shadowed underlying directory; clearing here would touch the live mount
			// instead. Left intact (mirrors how a hidden bind-mount guard is kept).
			logger.Info("Guard cleanup: %s is currently mounted; its immutable flag is on the shadowed directory and was left intact (to clear it: unmount the storage, run --cleanup-guards again, then remount)", target)
			pending++
			continue
		}

		if dryRun {
			logger.Info("DRY RUN: would clear immutable flag (chattr -i) on %s", target)
			continue
		}

		// Resolve symlinks and re-check the allowlist so a parent-component symlink
		// cannot make chattr -i escape the datastore roots. A path that no longer
		// exists has nothing to clear (not pending). If a datastore root itself is a
		// symlink that resolves outside the allowlist (rare on Proxmox/Debian), the
		// target is refused and left pending — fail-safe: it never escapes and is
		// never data loss; the operator can clear it manually with chattr -i.
		resolved, rErr := cleanupResolveTarget(target)
		if rErr != nil {
			if os.IsNotExist(rErr) {
				logger.Debug("Guard cleanup: immutable target %s no longer exists; nothing to clear", target)
				continue
			}
			logger.Warning("Guard cleanup: cannot resolve %s: %v; leaving immutable flag (clear manually with: chattr -i %s)", target, rErr, target)
			pending++
			continue
		}
		if !isConfirmableDatastoreMountRoot(resolved) {
			logger.Warning("Guard cleanup: %s resolves outside the datastore roots (%s); refusing to clear it automatically", target, resolved)
			pending++
			continue
		}

		if _, err := cleanupRunCmd(ctx, "chattr", "-i", resolved); err != nil {
			logger.Warning("Guard cleanup: failed to clear immutable flag on %s: %v (clear manually with: chattr -i %s)", resolved, err, resolved)
			pending++
			continue
		}
		logger.Info("Guard cleanup: cleared immutable flag (chattr -i) on %s", resolved)
	}
	return pending
}
