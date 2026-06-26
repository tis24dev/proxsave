// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
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
)

// mountGuardBaseDir is the directory under which ProxSave records mount guards.
// It is a var (not a const) only so tests can redirect it to a temporary
// directory; production never reassigns it.
var mountGuardBaseDir = "/var/lib/proxsave/guards"

const mountGuardMountAttemptTimeout = 10 * time.Second

var (
	mountGuardGeteuid                = os.Geteuid
	mountGuardReadFile               = os.ReadFile
	mountGuardMkdirAll               = os.MkdirAll
	mountGuardReadDir                = os.ReadDir
	mountGuardSysMount               = syscall.Mount
	mountGuardSysUnmount             = syscall.Unmount
	mountGuardFstabMountpointsSet    = fstabMountpointsSet
	mountGuardIsPathOnRootFilesystem = isPathOnRootFilesystem
	mountGuardParsePBSDatastoreCfg   = parsePBSDatastoreCfgBlocks
)

func guardMountPoint(ctx context.Context, guardTarget string) error {
	target, err := normalizeGuardMountRequest(ctx, guardTarget)
	if err != nil {
		return err
	}
	mounted, err := isMounted(target)
	if err != nil {
		return fmt.Errorf("check mount status: %w", err)
	}
	if mounted {
		return nil
	}

	guardDir := guardDirForTarget(target)
	if err := ensureGuardDirectories(guardDir, target); err != nil {
		return err
	}
	return bindReadOnlyGuard(guardDir, target)
}

func normalizeGuardMountRequest(ctx context.Context, guardTarget string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target := filepath.Clean(strings.TrimSpace(guardTarget))
	if !isValidGuardTarget(target) {
		return "", fmt.Errorf("invalid guard target: %q", guardTarget)
	}
	return target, nil
}

func ensureGuardDirectories(guardDir, target string) error {
	if err := mountGuardMkdirAll(guardDir, 0o755); err != nil {
		return fmt.Errorf("mkdir guard dir: %w", err)
	}
	if err := mountGuardMkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	return nil
}

func bindReadOnlyGuard(guardDir, target string) error {
	if err := mountGuardSysMount(guardDir, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind mount guard: %w", err)
	}

	remountFlags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NODEV | syscall.MS_NOSUID | syscall.MS_NOEXEC)
	if err := mountGuardSysMount("", target, "", remountFlags, ""); err != nil {
		_ = mountGuardSysUnmount(target, 0)
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
	data, err := mountGuardReadFile("/proc/self/mountinfo")
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
	data, err := mountGuardReadFile("/proc/mounts")
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
	if !strings.Contains(s, "\\") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if !hasProcOctalEscapeAt(s, i) {
			_ = b.WriteByte(s[i])
			i++
			continue
		}

		_ = b.WriteByte(procOctalEscapeValue(s[i+1 : i+4]))
		i += 4
	}
	return b.String()
}

func hasProcOctalEscapeAt(s string, i int) bool {
	return i+3 < len(s) &&
		s[i] == '\\' &&
		isOctalDigit(s[i+1]) &&
		isOctalDigit(s[i+2]) &&
		isOctalDigit(s[i+3]) &&
		procOctalEscapeInt(s[i+1:i+4]) <= 255
}

func isOctalDigit(b byte) bool {
	return b >= '0' && b <= '7'
}

func procOctalEscapeValue(oct string) byte {
	v := procOctalEscapeInt(oct)
	if v < 0 || v > 0xFF {
		// Unreachable: callers gate on hasProcOctalEscapeAt (value <= 255).
		// The bound makes the byte conversion safe without that invariant.
		return 0
	}
	return byte(v)
}

func procOctalEscapeInt(oct string) int {
	return (int(oct[0]-'0') << 6) | (int(oct[1]-'0') << 3) | int(oct[2]-'0')
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
	if !isValidGuardTarget(ds) {
		return ""
	}

	for _, mp := range mountpoints {
		if mountpointContainsDatastore(mp, ds) {
			return mp
		}
	}
	return ""
}

func mountpointContainsDatastore(mountpoint, datastorePath string) bool {
	mp := filepath.Clean(strings.TrimSpace(mountpoint))
	if !isValidGuardTarget(mp) {
		return false
	}
	return datastorePath == mp || strings.HasPrefix(datastorePath, mp+string(os.PathSeparator))
}

func isValidGuardTarget(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	return path != "" && path != "." && path != string(os.PathSeparator)
}

// resolveGuardTarget resolves a guard target through any symlinks. It is the
// single injectable seam shared by the guard apply paths (bind-mount / chattr +i)
// and the cleanup path (chattr -i), so the datastore-root allowlist is always
// enforced AFTER symlink resolution and can never again be hardened on one path
// while another is left trusting an unresolved string. Defaults to
// filepath.EvalSymlinks; overridable in tests.
var resolveGuardTarget = filepath.EvalSymlinks

// resolveGuardTargetWithinAllowlist resolves target through symlinks and reports
// whether the resolved path still lands inside the datastore-root allowlist
// (/mnt, /media, /run/media). This is the shared gate that closes the
// parent-component-symlink escape: a path that textually starts with /mnt/ but
// whose parent (or leaf) is a symlink into the live OS tree resolves outside the
// allowlist and is refused.
//
// The guard apply paths frequently target a mountpoint whose leaf does not exist
// yet (the offline storage was never mounted, so the directory is created only by
// the guard step). EvalSymlinks on such a path returns ENOENT, so we instead
// resolve the deepest EXISTING ancestor and re-append the still-missing tail: a
// component that does not exist cannot itself be a symlink, so only existing
// components can redirect the path.
//
// Returns:
//   - resolved: the symlink-resolved path to act on (ancestor resolution applied
//     when the leaf is absent). Meaningful whenever err is nil.
//   - leafExists: whether the full target already existed on disk. Cleanup uses
//     this to treat a missing leaf as "nothing to clear"; apply ignores it (it
//     creates the leaf).
//   - ok: whether resolved is a confirmable datastore mount root. When false (and
//     err is nil) the caller must refuse to act (fail-safe, no allowlist escape).
//   - err: a real resolution/I/O error (never plain os.ErrNotExist, which is
//     folded into the ancestor walk / leafExists=false).
func resolveGuardTargetWithinAllowlist(target string) (resolved string, leafExists bool, ok bool, err error) {
	clean := filepath.Clean(strings.TrimSpace(target))
	if !isValidGuardTarget(clean) {
		return "", false, false, nil
	}

	// Fast path: the full target exists — resolve it end to end (this also
	// resolves a symlinked leaf), exactly like the cleanup path always has.
	if res, rErr := resolveGuardTarget(clean); rErr == nil {
		return res, true, isConfirmableDatastoreMountRoot(res), nil
	} else if !os.IsNotExist(rErr) {
		return "", false, false, rErr
	}

	// Leaf (or a tail segment) is missing: walk up to the deepest existing
	// ancestor, resolving it through symlinks, then re-append the missing tail.
	anc := clean
	var missing []string
	for {
		parent := filepath.Dir(anc)
		if parent == anc {
			// Reached the filesystem root without an existing ancestor. "/" always
			// exists, so this is unreachable in practice; treat as no symlink to
			// escape through and act on the cleaned literal path.
			return clean, false, isConfirmableDatastoreMountRoot(clean), nil
		}
		missing = append([]string{filepath.Base(anc)}, missing...)
		anc = parent
		res, rErr := resolveGuardTarget(anc)
		if rErr == nil {
			full := filepath.Clean(filepath.Join(append([]string{res}, missing...)...))
			return full, false, isConfirmableDatastoreMountRoot(full), nil
		}
		if !os.IsNotExist(rErr) {
			return "", false, false, rErr
		}
	}
}
