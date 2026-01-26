package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

// CleanupMountGuards removes ProxSave mount guards created under mountGuardBaseDir.
//
// Safety: this will only unmount guard bind mounts when they are the currently-visible
// mount on the mountpoint (i.e. the mountpoint resolves to the root filesystem device).
// If a real mount is stacked on top, the guard will be left in place.
func CleanupMountGuards(ctx context.Context, logger *logging.Logger, dryRun bool) error {
	_ = ctx // reserved for future timeouts/cancellation hooks

	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("cleanup guards requires root privileges")
	}

	if _, err := os.Stat(mountGuardBaseDir); err != nil {
		if os.IsNotExist(err) {
			logger.Info("No guard directory found at %s", mountGuardBaseDir)
			return nil
		}
		return fmt.Errorf("stat guards dir: %w", err)
	}

	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}

	guardMountpoints := guardMountpointsFromMountinfo(string(mountinfo))
	if len(guardMountpoints) == 0 {
		if dryRun {
			logger.Info("DRY RUN: would remove %s", mountGuardBaseDir)
			return nil
		}
		if err := os.RemoveAll(mountGuardBaseDir); err != nil {
			return fmt.Errorf("remove guards dir: %w", err)
		}
		logger.Info("Removed guard directory %s", mountGuardBaseDir)
		return nil
	}

	dedup := make(map[string]struct{}, len(guardMountpoints))
	var targets []string
	for _, mp := range guardMountpoints {
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

	unmounted := 0
	for _, mp := range targets {
		if !isConfirmableDatastoreMountRoot(mp) {
			logger.Debug("Guard cleanup: skip non-datastore mount root %s", mp)
			continue
		}

		onRootFS, _, devErr := isPathOnRootFilesystem(mp)
		if devErr != nil {
			logger.Warning("Guard cleanup: unable to determine device for %s: %v (skipping)", mp, devErr)
			continue
		}
		if !onRootFS {
			logger.Debug("Guard cleanup: %s is not on root filesystem (guard likely hidden under a real mount); skipping unmount", mp)
			continue
		}

		if dryRun {
			logger.Info("DRY RUN: would unmount guard mount at %s", mp)
			continue
		}

		if err := syscall.Unmount(mp, 0); err != nil {
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
	after, err := os.ReadFile("/proc/self/mountinfo")
	if err == nil {
		remaining := guardMountpointsFromMountinfo(string(after))
		if len(remaining) > 0 {
			logger.Warning("Guard cleanup: %d guard mount(s) still present; not removing %s", len(remaining), mountGuardBaseDir)
			return nil
		}
	}

	if err := os.RemoveAll(mountGuardBaseDir); err != nil {
		return fmt.Errorf("remove guards dir: %w", err)
	}
	logger.Info("Removed guard directory %s (unmounted=%d)", mountGuardBaseDir, unmounted)
	return nil
}

func guardMountpointsFromMountinfo(mountinfo string) []string {
	prefix := mountGuardBaseDir + string(os.PathSeparator)
	var out []string
	for _, line := range strings.Split(mountinfo, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		root := unescapeProcPath(fields[3])
		if !strings.HasPrefix(root, prefix) {
			continue
		}
		mp := unescapeProcPath(fields[4])
		out = append(out, mp)
	}
	return out
}
