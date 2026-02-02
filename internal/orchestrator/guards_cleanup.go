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
)

// CleanupMountGuards removes ProxSave mount guards created under mountGuardBaseDir.
//
// Safety: this will only unmount guard bind mounts when they are the currently-visible
// mount on the mountpoint (i.e. the guard is the top-most mount at that mountpoint).
// If a real mount is stacked on top, the guard will be left in place.
func CleanupMountGuards(ctx context.Context, logger *logging.Logger, dryRun bool) error {
	_ = ctx // reserved for future timeouts/cancellation hooks

	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	if cleanupGeteuid() != 0 {
		return fmt.Errorf("cleanup guards requires root privileges")
	}

	if _, err := cleanupStat(mountGuardBaseDir); err != nil {
		if os.IsNotExist(err) {
			logger.Info("No guard directory found at %s", mountGuardBaseDir)
			return nil
		}
		return fmt.Errorf("stat guards dir: %w", err)
	}

	mountinfo, err := cleanupReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}

	visibleMountpoints, hiddenMountpoints, totalGuardMounts := guardMountpointsFromMountinfo(string(mountinfo))
	if totalGuardMounts == 0 {
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
		logger.Info("DRY RUN: would remove %s", mountGuardBaseDir)
		return nil
	}

	// If any guard mounts remain (for example hidden under a real mount), avoid removing the directory.
	after, err := cleanupReadFile("/proc/self/mountinfo")
	if err == nil {
		_, _, remaining := guardMountpointsFromMountinfo(string(after))
		if remaining > 0 {
			logger.Warning("Guard cleanup: %d guard mount(s) still present; not removing %s", remaining, mountGuardBaseDir)
			return nil
		}
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
