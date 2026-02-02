package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

const mountGuardBaseDir = "/var/lib/proxsave/guards"
const mountGuardMountAttemptTimeout = 10 * time.Second

func maybeApplyPBSDatastoreMountGuards(ctx context.Context, logger *logging.Logger, plan *RestorePlan, stageRoot, destRoot string, dryRun bool) error {
	if plan == nil || plan.SystemType != SystemTypePBS || !plan.HasCategoryID("datastore_pbs") {
		return nil
	}
	if strings.TrimSpace(stageRoot) == "" {
		return nil
	}
	if filepath.Clean(strings.TrimSpace(destRoot)) != string(os.PathSeparator) {
		if logger != nil {
			logger.Debug("Skipping PBS mount guards: restore destination is not system root (dest=%s)", destRoot)
		}
		return nil
	}

	if dryRun {
		if logger != nil {
			logger.Info("Dry run enabled: skipping PBS mount guards")
		}
		return nil
	}
	if !isRealRestoreFS(restoreFS) {
		if logger != nil {
			logger.Debug("Skipping PBS mount guards: non-system filesystem in use")
		}
		return nil
	}
	if os.Geteuid() != 0 {
		if logger != nil {
			logger.Warning("Skipping PBS mount guards: requires root privileges")
		}
		return nil
	}

	stagePath := filepath.Join(stageRoot, "etc/proxmox-backup/datastore.cfg")
	data, err := restoreFS.ReadFile(stagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read staged datastore.cfg: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}

	normalized, _ := normalizePBSDatastoreCfgContent(string(data))
	blocks, err := parsePBSDatastoreCfgBlocks(normalized)
	if err != nil {
		return err
	}
	if len(blocks) == 0 {
		return nil
	}

	var fstabMounts map[string]struct{}
	var mountpointCandidates []string
	currentFstab := filepath.Join(destRoot, "etc", "fstab")
	if mounts, err := fstabMountpointsSet(currentFstab); err != nil {
		if logger != nil {
			logger.Warning("PBS mount guard: unable to parse current fstab %s: %v (continuing without fstab cross-check)", currentFstab, err)
		}
	} else {
		fstabMounts = mounts
		for mp := range mounts {
			if mp == "" || mp == "." || mp == string(os.PathSeparator) {
				continue
			}
			if !isConfirmableDatastoreMountRoot(mp) {
				continue
			}
			mountpointCandidates = append(mountpointCandidates, mp)
		}
		sortByLengthDesc(mountpointCandidates)
	}

	protected := make(map[string]struct{})
	for _, block := range blocks {
		dsPath := filepath.Clean(strings.TrimSpace(block.Path))
		if dsPath == "" || dsPath == "." || dsPath == string(os.PathSeparator) {
			continue
		}

		guardTarget := ""
		if len(mountpointCandidates) > 0 {
			guardTarget = firstFstabMountpointMatch(dsPath, mountpointCandidates)
		}
		if guardTarget == "" {
			guardTarget = pbsMountGuardRootForDatastorePath(dsPath)
		}
		guardTarget = filepath.Clean(strings.TrimSpace(guardTarget))
		if guardTarget == "" || guardTarget == "." || guardTarget == string(os.PathSeparator) {
			continue
		}
		if _, seen := protected[guardTarget]; seen {
			continue
		}

		// If we can parse /etc/fstab, only guard mountpoints that exist there.
		// This avoids making local (rootfs) datastores immutable by mistake.
		if fstabMounts != nil {
			if _, ok := fstabMounts[guardTarget]; !ok {
				continue
			}
		}

		if err := os.MkdirAll(guardTarget, 0o755); err != nil {
			if logger != nil {
				logger.Warning("PBS mount guard: unable to create mountpoint directory %s: %v", guardTarget, err)
			}
			continue
		}

		onRootFS, _, devErr := isPathOnRootFilesystem(guardTarget)
		if devErr != nil {
			if logger != nil {
				logger.Warning("PBS mount guard: unable to determine filesystem device for %s: %v", guardTarget, devErr)
			}
			continue
		}
		if !onRootFS {
			continue
		}

		mounted, mountErr := isMounted(guardTarget)
		if mountErr != nil && logger != nil {
			logger.Warning("PBS mount guard: unable to check mount status for %s: %v (continuing)", guardTarget, mountErr)
		}
		if mountErr == nil && mounted {
			if logger != nil {
				logger.Debug("PBS mount guard: mountpoint %s already mounted, skipping guard", guardTarget)
			}
			continue
		}

		// Best-effort attempt to mount now (the entry may have just been restored to /etc/fstab).
		// If the storage is online, this avoids applying guards on mountpoints that would mount cleanly.
		mountCtx, cancel := context.WithTimeout(ctx, mountGuardMountAttemptTimeout)
		out, attemptErr := restoreCmd.Run(mountCtx, "mount", guardTarget)
		cancel()
		if attemptErr == nil {
			onRootFSNow, _, devErrNow := isPathOnRootFilesystem(guardTarget)
			if devErrNow == nil && !onRootFSNow {
				if logger != nil {
					logger.Info("PBS mount guard: mountpoint %s is now mounted (mount attempt succeeded)", guardTarget)
				}
				continue
			}
			if mountedNow, mountErrNow := isMounted(guardTarget); mountErrNow == nil && mountedNow {
				if logger != nil {
					logger.Info("PBS mount guard: mountpoint %s is now mounted (mount attempt succeeded)", guardTarget)
				}
				continue
			}
		} else {
			if logger != nil {
				if errors.Is(mountCtx.Err(), context.DeadlineExceeded) {
					logger.Warning("PBS mount guard: mount attempt timed out for %s after %s", guardTarget, mountGuardMountAttemptTimeout)
				} else {
					trimmed := strings.TrimSpace(string(out))
					if trimmed != "" {
						logger.Debug("PBS mount guard: mount attempt failed for %s: %v (output=%s)", guardTarget, attemptErr, trimmed)
					} else {
						logger.Debug("PBS mount guard: mount attempt failed for %s: %v", guardTarget, attemptErr)
					}
				}
			}
		}

		if logger != nil {
			logger.Info("PBS mount guard: mountpoint %s offline, applying guard bind mount", guardTarget)
		}

		if err := guardMountPoint(ctx, guardTarget); err != nil {
			if logger != nil {
				logger.Warning("PBS mount guard: failed to bind-mount guard on %s: %v; falling back to chattr +i", guardTarget, err)
			}
			if _, fallbackErr := restoreCmd.Run(ctx, "chattr", "+i", guardTarget); fallbackErr != nil {
				if logger != nil {
					logger.Warning("PBS mount guard: failed to set immutable attribute on %s: %v", guardTarget, fallbackErr)
				}
				continue
			}
			protected[guardTarget] = struct{}{}
			if logger != nil {
				logger.Warning("PBS mount guard: %s resolves to root filesystem (mount missing?) — marked immutable (chattr +i) to prevent writes until storage is available", guardTarget)
			}
			continue
		}

		protected[guardTarget] = struct{}{}
		if logger != nil {
			if entries, err := os.ReadDir(guardTarget); err == nil && len(entries) > 0 {
				logger.Warning("PBS mount guard: guard mount point %s is not empty (entries=%d)", guardTarget, len(entries))
			}
			logger.Warning("PBS mount guard: %s resolves to root filesystem (mount missing?) — bind-mounted a read-only guard to prevent writes until storage is available", guardTarget)
		}
	}

	return nil
}

func guardMountPoint(ctx context.Context, guardTarget string) error {
	target := filepath.Clean(strings.TrimSpace(guardTarget))
	if target == "" || target == "." || target == string(os.PathSeparator) {
		return fmt.Errorf("invalid guard target: %q", guardTarget)
	}

	mounted, err := isMounted(target)
	if err != nil {
		return fmt.Errorf("check mount status: %w", err)
	}
	if mounted {
		return nil
	}

	guardDir := guardDirForTarget(target)
	if err := os.MkdirAll(guardDir, 0o755); err != nil {
		return fmt.Errorf("mkdir guard dir: %w", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}

	// Bind mount guard directory over the mountpoint to avoid writes to the underlying rootfs path.
	if err := syscall.Mount(guardDir, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind mount guard: %w", err)
	}

	// Make the bind mount read-only to ensure PBS cannot write backup data to the guard directory.
	remountFlags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NODEV | syscall.MS_NOSUID | syscall.MS_NOEXEC)
	if err := syscall.Mount("", target, "", remountFlags, ""); err != nil {
		_ = syscall.Unmount(target, 0)
		return fmt.Errorf("remount guard read-only: %w", err)
	}

	return nil
}

func guardDirForTarget(target string) string {
	sum := sha256.Sum256([]byte(target))
	id := fmt.Sprintf("%x", sum[:8])
	base := filepath.Base(target)
	if base == "" || base == "." || base == string(os.PathSeparator) {
		base = "guard"
	}
	return filepath.Join(mountGuardBaseDir, fmt.Sprintf("%s-%s", base, id))
}

func isMounted(path string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err == nil {
		return isMountedFromMountinfo(string(data), path), nil
	}

	mounted, mountsErr := isMountedFromProcMounts(path)
	if mountsErr == nil {
		return mounted, nil
	}

	// Prefer reporting the mountinfo error, but keep the /proc/mounts error context too.
	if errors.Is(err, os.ErrNotExist) {
		return false, mountsErr
	}
	return false, fmt.Errorf("mountinfo=%v mounts=%v", err, mountsErr)
}

func isMountedFromMountinfo(mountinfo, path string) bool {
	target := filepath.Clean(strings.TrimSpace(path))
	if target == "" || target == "." {
		return false
	}

	for _, line := range strings.Split(mountinfo, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mp := unescapeProcPath(fields[4])
		if filepath.Clean(mp) == target {
			return true
		}
	}
	return false
}

func isMountedFromProcMounts(path string) (bool, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false, err
	}

	target := filepath.Clean(strings.TrimSpace(path))
	if target == "" || target == "." {
		return false, nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mp := unescapeProcPath(fields[1])
		if filepath.Clean(mp) == target {
			return true, nil
		}
	}
	return false, nil
}

func unescapeProcPath(s string) string {
	// /proc/self/mountinfo uses octal escapes: \040, \011, \012, \134.
	// Keep it minimal: decode any \XYZ sequence where XYZ are octal digits and the value fits into a byte (0-255).
	if !strings.Contains(s, "\\") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' || i+3 >= len(s) {
			_ = b.WriteByte(s[i])
			i++
			continue
		}

		oct := s[i+1 : i+4]
		if oct[0] < '0' || oct[0] > '7' || oct[1] < '0' || oct[1] > '7' || oct[2] < '0' || oct[2] > '7' {
			_ = b.WriteByte(s[i])
			i++
			continue
		}

		val := (int(oct[0]-'0') << 6) | (int(oct[1]-'0') << 3) | int(oct[2]-'0')
		if val > 255 {
			_ = b.WriteByte(s[i])
			i++
			continue
		}
		_ = b.WriteByte(byte(val))
		i += 4
	}
	return b.String()
}

func fstabMountpointsSet(path string) (map[string]struct{}, error) {
	entries, _, err := parseFstab(path)
	if err != nil {
		return nil, err
	}

	out := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		mp := filepath.Clean(strings.TrimSpace(entry.MountPoint))
		if mp == "" || mp == "." {
			continue
		}
		out[mp] = struct{}{}
	}
	return out, nil
}

func pbsMountGuardRootForDatastorePath(path string) string {
	p := filepath.Clean(strings.TrimSpace(path))
	if p == "" || p == "." || p == string(os.PathSeparator) {
		return ""
	}

	switch {
	case strings.HasPrefix(p, "/mnt/"):
		return mountRootWithPrefix(p, "/mnt/")
	case strings.HasPrefix(p, "/media/"):
		return mountRootWithPrefix(p, "/media/")
	case strings.HasPrefix(p, "/run/media/"):
		rest := strings.TrimPrefix(p, "/run/media/")
		parts := splitPath(rest)
		if len(parts) == 0 {
			return ""
		}
		if len(parts) == 1 {
			return filepath.Join("/run/media", parts[0])
		}
		return filepath.Join("/run/media", parts[0], parts[1])
	default:
		return ""
	}
}

func mountRootWithPrefix(path, prefix string) string {
	rest := strings.TrimPrefix(path, prefix)
	parts := splitPath(rest)
	if len(parts) == 0 {
		return ""
	}
	return filepath.Join(strings.TrimSuffix(prefix, "/"), parts[0])
}

func splitPath(rest string) []string {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil
	}
	var parts []string
	for _, p := range strings.Split(rest, "/") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		parts = append(parts, p)
	}
	return parts
}

func sortByLengthDesc(items []string) {
	if len(items) < 2 {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		return len(items[i]) > len(items[j])
	})
}

func firstFstabMountpointMatch(datastorePath string, mountpoints []string) string {
	ds := filepath.Clean(strings.TrimSpace(datastorePath))
	if ds == "" || ds == "." || ds == string(os.PathSeparator) {
		return ""
	}

	for _, mp := range mountpoints {
		if mp == "" || mp == "." || mp == string(os.PathSeparator) {
			continue
		}
		if ds == mp || strings.HasPrefix(ds, mp+string(os.PathSeparator)) {
			return mp
		}
	}
	return ""
}
