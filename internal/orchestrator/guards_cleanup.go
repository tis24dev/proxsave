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
	// cleanupRunCmd runs the `chattr -i` that reverses an immutable fallback
	// guard. Injectable like the other cleanup* seams; defaults to the restore
	// command runner.
	cleanupRunCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return restoreCmd.Run(ctx, name, args...)
	}
)

// guardCleanupSummary accumulates what a cleanup run did so a single, uniform
// SUMMARY line (plus actionable warnings) can be emitted once the guard directory
// is known to exist and the mount state has been read.
type guardCleanupSummary struct {
	dryRun          bool
	cleared         int  // immutable flags cleared (or would-clear, in dry-run)
	pending         int  // immutable flags left (mounted/unresolvable/failed)
	unmounted       int  // bind guards unmounted
	guardsRemaining int  // bind guard mounts still present after this run (-1 = unknown: verification reread failed)
	dirRemoved      bool // the guard directory was removed
}

func (s *guardCleanupSummary) emit(logger *logging.Logger) {
	if logger == nil {
		return
	}
	prefix := "Guard cleanup summary:"
	if s.dryRun {
		prefix = "Guard cleanup summary (DRY RUN):"
	}
	dirState := "kept"
	if s.dirRemoved {
		dirState = "removed"
	}
	// guardsRemaining == -1 is the fail-closed sentinel: the verification reread of
	// /proc/self/mountinfo failed, so the count is unknown. Render it as "unknown"
	// rather than a misleading "-1" or "0".
	remainingStr := strconv.Itoa(s.guardsRemaining)
	if s.guardsRemaining < 0 {
		remainingStr = "unknown"
	}
	logger.Info("%s bind-unmounted=%d guards-remaining=%s immutable-cleared=%d immutable-pending=%d guard-dir=%s",
		prefix, s.unmounted, remainingStr, s.cleared, s.pending, dirState)
	if s.guardsRemaining > 0 {
		logger.Warning("Guard cleanup: %d bind guard(s) still present (hidden under a real mount, or an unmount failed); they are discarded on reboot, and re-running --cleanup-guards once the storage is unmounted will retry removing them", s.guardsRemaining)
	}
	if s.pending > 0 {
		logger.Warning("Guard cleanup: %d immutable (chattr +i) flag(s) still pending; to clear, unmount the datastore, run 'proxsave --cleanup-guards', then remount", s.pending)
	}
}

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

	summary := &guardCleanupSummary{dryRun: dryRun}

	// Reverse the chattr +i fallback guards first, independently of the bind-mount
	// state below. This runs on BOTH paths (the no-guard-mounts early return AND the
	// bind-mount loop). pending counts targets left immutable (mounted/unresolvable/
	// failed); while it is > 0 we keep the guard directory and its index so a later
	// run can finish the job.
	cleared, pending := clearImmutableGuards(ctx, logger, dryRun)
	summary.cleared, summary.pending = cleared, pending

	mountinfo, err := cleanupReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}
	// Registered only after the mount state is known: a hard failure above returns
	// without emitting a misleading "summary" line.
	defer summary.emit(logger)

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
		summary.dirRemoved = true
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
	summary.unmounted = unmounted

	if dryRun {
		summary.guardsRemaining = len(hiddenMountpoints)
		if pending > 0 {
			logger.Info("DRY RUN: would keep %s (%d immutable guard target(s) still pending)", mountGuardBaseDir, pending)
		} else {
			logger.Info("DRY RUN: would remove %s", mountGuardBaseDir)
		}
		return nil
	}

	// If any guard mounts remain (for example hidden under a real mount), or any
	// immutable guard target is still pending, avoid removing the directory/index.
	// Fail closed: if the verification reread of /proc/self/mountinfo fails we cannot
	// confirm the guard mounts are gone, so we must NOT remove the directory and must
	// NOT report "0 remaining". Keep the index so a later run can finish the job.
	after, rerr := cleanupReadFile("/proc/self/mountinfo")
	if rerr != nil {
		// -1 records "unknown" so the summary never falsely advertises "0 remaining".
		summary.guardsRemaining = -1
		logger.Warning("Guard cleanup: could not re-read /proc/self/mountinfo to confirm guard mounts are gone (%v); keeping %s to be safe (re-run --cleanup-guards once the storage is unmounted)", rerr, mountGuardBaseDir)
		return nil
	}
	_, _, remaining := guardMountpointsFromMountinfo(string(after))
	summary.guardsRemaining = remaining
	if remaining > 0 || pending > 0 {
		logger.Warning("Guard cleanup: %d guard mount(s) and %d immutable target(s) still present; not removing %s", remaining, pending, mountGuardBaseDir)
		return nil
	}

	if err := cleanupRemoveAll(mountGuardBaseDir); err != nil {
		return fmt.Errorf("remove guards dir: %w", err)
	}
	summary.dirRemoved = true
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
// ProxSave itself recorded in the immutable-guard index. For each one it first
// resolves the recorded path through symlinks (re-checking the datastore-root
// allowlist on the RESOLVED path), then decides what to do:
//
//   - a non-datastore mount root (textually, before resolution) is skipped
//     (defense-in-depth against a tampered or corrupt index — the operation can
//     never escape /mnt, /media, /run/media);
//   - a target whose leaf no longer exists has nothing to clear and is skipped (not
//     pending — it is removed when the directory is finally cleaned up);
//   - a target that resolves OUTSIDE the datastore roots (a parent/leaf symlink into
//     the live OS tree) is refused and left pending — fail-safe, it is never chattr'd;
//   - a target whose RESOLVED path is currently mounted is skipped, because the
//     immutable flag is on the shadowed underlying directory and `chattr -i` would
//     touch the live mount instead (this mirrors how a hidden bind-mount guard is
//     left in place);
//   - dry-run only logs the intended action.
//
// Ordering matters for correctness: the mount probe and the eventual `chattr -i` are
// BOTH performed on the same resolved path returned by
// resolveGuardTargetWithinAllowlist. Probing the raw index path instead could read
// "not mounted" for a symlinked mountpoint (mountinfo records the kernel-resolved
// mountpoint) and then run `chattr -i` on the live mount root. By resolving first,
// the "is this path mounted?" question and the "what will I chattr?" answer can never
// diverge.
//
// Every step is best-effort and non-fatal: a missing/empty index is a no-op, and a
// failed `chattr -i` on one target does not stop the others or abort cleanup.
//
// It returns the number of targets left immutable ("pending"): those skipped because
// they are currently mounted, whose mount status or path could not be resolved, that
// resolve outside the datastore roots, or whose `chattr -i` failed. The caller keeps
// the guard directory (and its index) on disk while pending > 0, so a later run — once
// the storage is unmounted — can still clear them; the index is removed (with the
// directory) only when nothing is pending and no bind-mount guards remain. In dry-run,
// pending reflects what a real run would leave behind (mounted/unresolvable/escaping
// targets), so the "would remove" preview is honest.
func clearImmutableGuards(ctx context.Context, logger *logging.Logger, dryRun bool) (cleared, pending int) {
	data, err := cleanupChattrReadFile(mountGuardChattrTargetsPath())
	if err != nil {
		return 0, 0 // missing/unreadable index => nothing was recorded => no-op
	}

	for _, target := range parseImmutableGuardTargets(data) {
		// Defense-in-depth against a tampered/corrupt index: only ever touch a
		// datastore mount root (/mnt, /media, /run/media). A dropped entry is not
		// pending — it is removed when the directory is finally cleaned up.
		if !isConfirmableDatastoreMountRoot(target) {
			logger.Debug("Guard cleanup: skip non-datastore immutable target %s", target)
			continue
		}

		// Resolve symlinks and re-check the allowlist BEFORE probing mount status, so
		// the mount check and the eventual chattr -i act on the SAME path the kernel
		// sees (shared with the apply paths via resolveGuardTargetWithinAllowlist).
		// A path that no longer exists has nothing to clear (not pending). If a
		// datastore root itself is a symlink that resolves outside the allowlist (rare
		// on Proxmox/Debian), the target is refused and left pending — fail-safe: it
		// never escapes and is never data loss; the operator can clear it manually
		// with chattr -i.
		resolved, leafExists, ok, rErr := resolveGuardTargetWithinAllowlist(target)
		if rErr != nil {
			logger.Warning("Guard cleanup: cannot resolve %s: %v; leaving immutable flag (clear manually with: chattr -i %s)", target, rErr, target)
			pending++
			continue
		}
		if !leafExists {
			logger.Debug("Guard cleanup: immutable target %s no longer exists; nothing to clear", target)
			continue
		}
		if !ok {
			logger.Warning("Guard cleanup: %s resolves outside the datastore roots (%s); refusing to clear it automatically", target, resolved)
			pending++
			continue
		}

		// Probe mount status on the RESOLVED path. /proc/self/mountinfo records the
		// kernel-resolved mountpoint, so probing the raw index path could miss a mount
		// when the recorded path is a symlink to the real mountpoint, and chattr -i
		// would then hit the live mount root below.
		mounted, mErr := isMounted(resolved)
		if mErr != nil {
			logger.Warning("Guard cleanup: cannot determine mount status of %s: %v; leaving immutable flag (clear manually with: chattr -i %s)", resolved, mErr, resolved)
			pending++
			continue
		}
		if mounted {
			// The real storage is mounted on top, so the immutable flag is on the
			// shadowed underlying directory; clearing here would touch the live mount
			// instead. Left intact (mirrors how a hidden bind-mount guard is kept).
			logger.Info("Guard cleanup: %s is currently mounted; its immutable flag is on the shadowed directory and was left intact (to clear it: unmount the storage, run --cleanup-guards again, then remount)", resolved)
			pending++
			continue
		}

		if dryRun {
			logger.Info("DRY RUN: would clear immutable flag (chattr -i) on %s", resolved)
			cleared++
			continue
		}

		if _, err := cleanupRunCmd(ctx, "chattr", "-i", resolved); err != nil {
			logger.Warning("Guard cleanup: failed to clear immutable flag on %s: %v (clear manually with: chattr -i %s)", resolved, err, resolved)
			pending++
			continue
		}
		logger.Info("Guard cleanup: cleared immutable flag (chattr -i) on %s", resolved)
		cleared++
	}
	return cleared, pending
}
