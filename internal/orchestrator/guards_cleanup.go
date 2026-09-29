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
	guardDirPresent bool // the guard base dir exists (false => nothing to clean up)
	bindGuards      int  // guard bind mounts found (visible + hidden)
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

// GuardCleanupReport summarizes what a guard cleanup (or a dry-run check) found and did,
// so callers can classify the outcome without parsing log lines.
type GuardCleanupReport struct {
	DryRun           bool
	GuardDirPresent  bool // the guard base dir exists (false => nothing to clean up)
	BindGuards       int  // guard bind mounts found (visible + hidden)
	ImmutableGuards  int  // immutable (chattr +i) targets found (cleared + pending)
	Unmounted        int  // bind guards actually unmounted (real run only)
	GuardsRemaining  int  // guard mounts still present after a real run (-1 = unknown)
	ImmutableCleared int  // immutable flags cleared (would-clear, in dry-run)
	ImmutablePending int  // immutable flags left pending
	DirRemoved       bool // the guard directory was removed
}

// HasGuards reports whether any guard is present to unlock (a bind mount or an immutable
// flag). It is the signal that classifies "found something to remove" vs "clean".
func (r GuardCleanupReport) HasGuards() bool {
	return r.BindGuards > 0 || r.ImmutableGuards > 0
}

// CleanupMountGuardsReport removes ProxSave mount guards created under mountGuardBaseDir
// and reports what it found and did. In dry-run it is a read-only CHECK (it reports what
// is present without changing anything); a real run reports what it removed and what is
// left pending.
//
// Safety: this will only unmount guard bind mounts when they are the currently-visible
// mount on the mountpoint (i.e. the guard is the top-most mount at that mountpoint).
// If a real mount is stacked on top, the guard will be left in place.
//
// It is the ONLY exported entry point, deliberately. An error-only twin used to sit
// beside it, and --cleanup-guards took that one: a run that removed nothing because the
// datastore was still mounted returned a nil error and the mode exited 0, telling a
// gating script the storage was unlocked when it was not. Handing every caller the
// report is what makes throwing that state away a visible choice rather than the default.
func CleanupMountGuardsReport(ctx context.Context, logger *logging.Logger, dryRun bool) (GuardCleanupReport, error) {
	return cleanupMountGuards(ctx, logger, dryRun)
}

func cleanupMountGuards(ctx context.Context, logger *logging.Logger, dryRun bool) (GuardCleanupReport, error) {
	summary := &guardCleanupSummary{dryRun: dryRun}
	report := func() GuardCleanupReport {
		return GuardCleanupReport{
			DryRun:           summary.dryRun,
			GuardDirPresent:  summary.guardDirPresent,
			BindGuards:       summary.bindGuards,
			ImmutableGuards:  summary.cleared + summary.pending,
			Unmounted:        summary.unmounted,
			GuardsRemaining:  summary.guardsRemaining,
			ImmutableCleared: summary.cleared,
			ImmutablePending: summary.pending,
			DirRemoved:       summary.dirRemoved,
		}
	}

	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	if cleanupGeteuid() != 0 {
		return report(), fmt.Errorf("cleanup guards requires root privileges")
	}

	// The current guard directory and the legacy one, each only when it exists.
	var dirs []string
	for _, dir := range mountGuardBaseDirs() {
		if _, err := cleanupStat(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return report(), fmt.Errorf("stat guards dir %s: %w", dir, err)
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		logger.Info("No guard directory found at %s, nothing to clean up.", strings.Join(mountGuardBaseDirs(), " or "))
		return report(), nil
	}
	summary.guardDirPresent = true

	// Reverse the chattr +i fallback guards first, independently of the bind-mount
	// state below. This runs on BOTH paths (the no-guard-mounts early return AND the
	// bind-mount loop). pendingIn counts, per guard directory, the targets left
	// immutable (mounted/unresolvable/failed); while it is > 0 we keep that directory
	// and its index so a later run can finish the job.
	pendingIn := make(map[string]int, len(dirs))
	for _, dir := range dirs {
		cleared, pending := clearImmutableGuards(ctx, logger, dryRun, dir)
		summary.cleared += cleared
		summary.pending += pending
		pendingIn[dir] = pending
	}

	mountinfo, err := cleanupReadFile("/proc/self/mountinfo")
	if err != nil {
		return report(), fmt.Errorf("read mountinfo: %w", err)
	}
	// Registered only after the mount state is known: a hard failure above returns
	// without emitting a misleading "summary" line.
	defer summary.emit(logger)

	matchers := guardRootMatchersFor(string(mountinfo), dirs)
	visibleMountpoints, hiddenMountpoints, mountsIn := guardMountpointsFromMountinfo(string(mountinfo), matchers)
	summary.bindGuards = sumGuardCounts(mountsIn)
	if summary.bindGuards == 0 {
		removed := 0
		for _, dir := range dirs {
			if pendingIn[dir] > 0 {
				logger.Info("Guard cleanup: %d immutable guard target(s) still pending (mounted or uncleared); keeping %s", pendingIn[dir], dir)
				continue
			}
			if dryRun {
				logger.Info("DRY RUN: would remove %s", dir)
				continue
			}
			if err := cleanupRemoveAll(dir); err != nil {
				return report(), fmt.Errorf("remove guards dir: %w", err)
			}
			removed++
			logger.Info("Removed guard directory %s", dir)
		}
		summary.dirRemoved = removed == len(dirs)
		return report(), nil
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
		for _, dir := range dirs {
			if pendingIn[dir] > 0 {
				logger.Info("DRY RUN: would keep %s (%d immutable guard target(s) still pending)", dir, pendingIn[dir])
			} else {
				logger.Info("DRY RUN: would remove %s", dir)
			}
		}
		return report(), nil
	}

	// If any guard mounts remain in a directory (for example hidden under a real
	// mount), or any of its immutable guard targets is still pending, avoid removing
	// that directory/index. Fail closed: if the verification reread of
	// /proc/self/mountinfo fails we cannot confirm the guard mounts are gone, so we
	// must NOT remove any directory and must NOT report "0 remaining". Keep the index
	// so a later run can finish the job.
	after, rerr := cleanupReadFile("/proc/self/mountinfo")
	if rerr != nil {
		// -1 records "unknown" so the summary never falsely advertises "0 remaining".
		summary.guardsRemaining = -1
		logger.Warning("Guard cleanup: could not re-read /proc/self/mountinfo to confirm guard mounts are gone (%v); keeping %s to be safe (re-run --cleanup-guards once the storage is unmounted)", rerr, strings.Join(dirs, " and "))
		return report(), nil
	}
	_, _, remainingIn := guardMountpointsFromMountinfo(string(after), matchers)
	summary.guardsRemaining = sumGuardCounts(remainingIn)

	removed := 0
	for _, dir := range dirs {
		if remainingIn[dir] > 0 || pendingIn[dir] > 0 {
			logger.Warning("Guard cleanup: %d guard mount(s) and %d immutable target(s) still present; not removing %s", remainingIn[dir], pendingIn[dir], dir)
			continue
		}
		if err := cleanupRemoveAll(dir); err != nil {
			return report(), fmt.Errorf("remove guards dir: %w", err)
		}
		removed++
		logger.Info("Removed guard directory %s (unmounted=%d)", dir, unmounted)
	}
	summary.dirRemoved = removed == len(dirs)
	return report(), nil
}

func sumGuardCounts(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// mountinfoEntry is one line of /proc/self/mountinfo: the mount id, the major:minor
// of the filesystem, the root of the mount inside that filesystem, and the mount point.
type mountinfoEntry struct {
	id         int
	device     string
	root       string
	mountpoint string
}

func parseMountinfoEntries(mountinfo string) []mountinfoEntry {
	var entries []mountinfoEntry
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 {
			continue
		}
		mountID, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		entries = append(entries, mountinfoEntry{
			id:         mountID,
			device:     fields[2],
			root:       unescapeProcPath(fields[3]),
			mountpoint: unescapeProcPath(fields[4]),
		})
	}
	return entries
}

// guardRootMatcher recognises the mountinfo entries of guard bind mounts whose
// source lies under a guard directory. The root field of /proc/self/mountinfo is
// the source path INSIDE its filesystem, not an absolute path: with the guard
// directory /opt/proxsave/guards and /opt on a filesystem of its own, a guard's
// root reads /proxsave/guards/<name>. So the matcher takes the mount that holds the
// directory (the longest mount point containing it, the topmost when several are
// stacked there) and expects that filesystem's device and the directory's path
// inside it. When no entry holds the directory it falls back to the absolute path
// on any device, which is right whenever the directory sits on the root filesystem.
type guardRootMatcher struct {
	dir    string // the guard directory, as cleanup names it
	device string // major:minor of the filesystem holding dir; "" when unknown
	root   string // dir as a path inside that filesystem
}

// cleanupEvalSymlinks resolves a guard directory the way the kernel records bind
// sources. A var only so tests can replace it.
var cleanupEvalSymlinks = filepath.EvalSymlinks

func guardRootMatchersFor(mountinfo string, dirs []string) []guardRootMatcher {
	entries := parseMountinfoEntries(mountinfo)
	matchers := make([]guardRootMatcher, 0, len(dirs))
	for _, dir := range dirs {
		matchers = append(matchers, newGuardRootMatcher(entries, dir))
	}
	return matchers
}

func newGuardRootMatcher(entries []mountinfoEntry, dir string) guardRootMatcher {
	resolved := filepath.Clean(dir)
	if r, err := cleanupEvalSymlinks(dir); err == nil && strings.TrimSpace(r) != "" {
		resolved = filepath.Clean(r)
	}
	m := guardRootMatcher{dir: dir, root: resolved}
	best := -1
	for i, e := range entries {
		if !pathWithinMountpoint(resolved, e.mountpoint) {
			continue
		}
		if best < 0 || len(e.mountpoint) > len(entries[best].mountpoint) ||
			(len(e.mountpoint) == len(entries[best].mountpoint) && e.id > entries[best].id) {
			best = i
		}
	}
	if best >= 0 {
		holder := entries[best]
		m.device = holder.device
		m.root = filepath.Join(holder.root, strings.TrimPrefix(resolved, strings.TrimSuffix(holder.mountpoint, "/")))
	}
	return m
}

func pathWithinMountpoint(path, mountpoint string) bool {
	return mountpoint == "/" || path == mountpoint || strings.HasPrefix(path, mountpoint+"/")
}

func (m guardRootMatcher) matches(e mountinfoEntry) bool {
	if m.device != "" && e.device != m.device {
		return false
	}
	return e.root == m.root || strings.HasPrefix(e.root, strings.TrimSuffix(m.root, "/")+"/")
}

// guardMountpointsFromMountinfo classifies the guard bind mounts in mountinfo: a
// mount point where a guard is the topmost mount is visible (it can be unmounted),
// one where a guard sits under a real mount is hidden. mountsIn counts the guard
// mounts per guard directory, keyed by matcher.dir.
func guardMountpointsFromMountinfo(mountinfo string, matchers []guardRootMatcher) (visible, hidden []string, mountsIn map[string]int) {
	type mountpointInfo struct {
		topmostID      int
		topmostIsGuard bool
		hasGuard       bool
	}

	mountsIn = make(map[string]int, len(matchers))
	mountpoints := make(map[string]*mountpointInfo)
	for _, e := range parseMountinfoEntries(mountinfo) {
		isGuard := false
		for _, m := range matchers {
			if m.matches(e) {
				isGuard = true
				mountsIn[m.dir]++
				break
			}
		}

		info := mountpoints[e.mountpoint]
		if info == nil {
			info = &mountpointInfo{topmostID: -1}
			mountpoints[e.mountpoint] = info
		}
		if e.id > info.topmostID {
			info.topmostID = e.id
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
	return visible, hidden, mountsIn
}

// clearImmutableGuards reverses the `chattr +i` immutable fallback that restore
// applied when a guard bind-mount could not be created. It is the symmetric
// counterpart to the bind-mount unmount loop and processes ONLY the targets
// ProxSave itself recorded in the immutable-guard index of guardDir (the current or
// the legacy guard directory). For each one it first
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
func clearImmutableGuards(ctx context.Context, logger *logging.Logger, dryRun bool, guardDir string) (cleared, pending int) {
	data, err := cleanupChattrReadFile(mountGuardChattrTargetsPathIn(guardDir))
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
			logger.Warning("Guard cleanup: cannot resolve %s: %v; leaving immutable flag", target, rErr)
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
			logger.Warning("Guard cleanup: cannot determine mount status of %s: %v; leaving immutable flag", resolved, mErr)
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
			logger.Warning("Guard cleanup: failed to clear immutable flag on %s: %v; it stays immutable", resolved, err)
			pending++
			continue
		}
		logger.Info("Guard cleanup: cleared immutable flag (chattr -i) on %s", resolved)
		cleared++
	}
	return cleared, pending
}
