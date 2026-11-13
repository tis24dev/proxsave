package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// FilesystemDetector provides methods to detect and validate filesystem types
type FilesystemDetector struct {
	logger *logging.Logger

	// ioTimeout bounds each raw filesystem syscall the detector performs (the
	// initial existence stat, the statfs on the mount point, and the network-FS
	// ownership probe). Zero means unbounded (legacy behaviour); callers that may
	// touch dead/stale network mounts should opt in via WithIOTimeout.
	ioTimeout time.Duration

	// dryRun, when true, suppresses the network-FS ownership write-probe
	// (testOwnershipSupport creates/chowns/chmods a temp file). A dry run must not
	// mutate the filesystem, so detection falls back to the type-based default.
	dryRun bool

	// Test hooks (nil in production).
	mountPointLookup     func(path string) (string, error)
	filesystemTypeLookup func(ctx context.Context, mountPoint string) (FilesystemType, string, error)
	ownershipSupportTest func(ctx context.Context, path string) bool
}

// DetectorOption configures a FilesystemDetector at construction time.
type DetectorOption func(*FilesystemDetector)

// WithIOTimeout bounds every blocking filesystem syscall the detector performs
// with the supplied per-operation timeout. A non-positive value keeps the legacy
// unbounded behaviour. Use it on user-configured storage paths that may be
// dead/stale network mounts, where a raw syscall would otherwise block forever
// in uninterruptible (D) state.
func WithIOTimeout(timeout time.Duration) DetectorOption {
	return func(d *FilesystemDetector) {
		if timeout < 0 {
			timeout = 0
		}
		d.ioTimeout = timeout
	}
}

// WithDryRun suppresses the detector's network-FS ownership write-probe so a dry
// run performs no filesystem mutations; SupportsOwnership then falls back to the
// filesystem type's default.
func WithDryRun(dryRun bool) DetectorOption {
	return func(d *FilesystemDetector) {
		d.dryRun = dryRun
	}
}

// NewFilesystemDetector creates a new filesystem detector. With no options the
// detector is unbounded (legacy behaviour); pass WithIOTimeout to bound syscalls.
func NewFilesystemDetector(logger *logging.Logger, opts ...DetectorOption) *FilesystemDetector {
	d := &FilesystemDetector{
		logger: logger,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// DetectFilesystem detects the filesystem type for a given path
// This function logs the detected filesystem type in real-time
func (d *FilesystemDetector) DetectFilesystem(ctx context.Context, path string) (*FilesystemInfo, error) {
	// Ensure path exists. Bounded so a dead/stale mount does not wedge here.
	pathInfo, err := safefs.Stat(ctx, path, d.ioTimeout)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %s: %w", path, err)
	}

	// Get mount point for this path
	var mountPoint, linkTarget string
	if d.mountPointLookup != nil {
		mountPoint, err = d.mountPointLookup(path)
	} else {
		mountPoint, linkTarget, err = d.getMountPoint(ctx, path, pathInfo)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get mount point for %s: %w", path, err)
	}

	// Get filesystem type using df command
	var fsType FilesystemType
	var device string
	if d.filesystemTypeLookup != nil {
		fsType, device, err = d.filesystemTypeLookup(ctx, mountPoint)
	} else {
		fsType, device, err = d.getFilesystemType(ctx, mountPoint)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to detect filesystem type for %s: %w", path, err)
	}

	info := &FilesystemInfo{
		Path:              path,
		Type:              fsType,
		SupportsOwnership: fsType.SupportsUnixOwnership(),
		IsNetworkFS:       fsType.IsNetworkFilesystem(),
		MountPoint:        mountPoint,
		Device:            device,
		LinkTarget:        linkTarget,
	}

	// Log the detected filesystem in real-time (live output)
	d.logFilesystemInfo(info)

	// Check if we need to test ownership support for network filesystems. The
	// probe writes a temp file, so it is skipped in dry-run (which must not mutate
	// the filesystem); SupportsOwnership keeps the type-based default.
	if info.IsNetworkFS {
		if d.dryRun {
			d.logger.Debug("DRY RUN: skipping network-FS ownership write-probe for %s; assuming type default (%v)", path, info.SupportsOwnership)
		} else {
			testFn := d.testOwnershipSupport
			if d.ownershipSupportTest != nil {
				testFn = d.ownershipSupportTest
			}
			supportsOwnership := testFn(ctx, path)
			info.SupportsOwnership = supportsOwnership
			if supportsOwnership {
				d.logger.Debug("Network filesystem %s supports Unix ownership", fsType)
			} else {
				d.logger.Debug("Network filesystem %s does NOT support Unix ownership", fsType)
			}
		}
	}

	// Auto-exclude incompatible filesystems
	if fsType.ShouldAutoExclude() {
		d.logger.Debug("Filesystem %s is incompatible with Unix ownership - will skip chown/chmod", fsType)
	}

	return info, nil
}

// logFilesystemInfo logs filesystem information in real-time next to the path
func (d *FilesystemDetector) logFilesystemInfo(info *FilesystemInfo) {
	ownership := "no ownership"
	if info.SupportsOwnership {
		ownership = "supports ownership"
	}

	network := ""
	if info.IsNetworkFS {
		network = " [network]"
	}

	link := ""
	if info.LinkTarget != "" {
		link = " [link to: " + info.LinkTarget + "]"
	}

	// Log in format: "Path: /path/to/backup -> Filesystem: ext4 (supports ownership)" (debug-level, detailed summary printed elsewhere)
	d.logger.Debug("Path: %s -> Filesystem: %s (%s)%s [mount: %s]%s",
		info.Path,
		info.Type,
		ownership,
		network,
		info.MountPoint,
		link,
	)
}

// getMountPoint finds the mount point that holds path, and where path resolves when a
// symlink is on the way ("" when none is). The mount is found by device: the st_dev of
// path (the stat follows links) against /proc/self/mountinfo. Matching the path string
// against /proc/mounts gave a BACKUP_PATH that is a link into an NFS share the type of
// the filesystem holding the link: ext4 with ownership, so the security check tried to
// chown the root_squash share and the run stopped with exit 14. A device no mountinfo
// entry carries (btrfs subvolumes report their own) falls back to the deepest mount
// whose path holds the resolved path.
func (d *FilesystemDetector) getMountPoint(ctx context.Context, path string, pathInfo os.FileInfo) (mountPoint, linkTarget string, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	resolved, rerr := safefs.Run(ctx, "resolve symlinks", absPath, d.ioTimeout, func() (string, error) {
		return filepath.EvalSymlinks(absPath)
	})
	if rerr != nil {
		d.logger.Debug("Filesystem detection: cannot resolve %s: %v; matching it as written", absPath, rerr)
		resolved = absPath
	}
	if resolved != absPath {
		linkTarget = resolved
	}

	if st, ok := pathInfo.Sys().(*syscall.Stat_t); ok {
		if data, merr := os.ReadFile("/proc/self/mountinfo"); merr == nil {
			devID := deviceID(uint64(st.Dev))
			if mp, found := mountPointForDevice(strings.Split(string(data), "\n"), devID, resolved); found {
				d.logger.Debug("Filesystem detection: %s is on device %s, mounted at %s", path, devID, mp)
				return mp, linkTarget, nil
			}
			d.logger.Debug("Filesystem detection: no mountinfo entry for device %s of %s; matching %s by path", devID, path, resolved)
		} else {
			d.logger.Debug("Filesystem detection: cannot read /proc/self/mountinfo: %v; matching %s by path", merr, resolved)
		}
	}

	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", "", err
	}
	return bestMountPointFor(strings.Split(string(data), "\n"), resolved), linkTarget, nil
}

// deviceID renders a device number as mountinfo's "major:minor", with glibc's
// major()/minor() split of the Linux dev_t.
func deviceID(dev uint64) string {
	major := (dev>>8)&0xfff | (dev>>32)&^uint64(0xfff)
	minor := dev&0xff | (dev>>12)&^uint64(0xff)
	return fmt.Sprintf("%d:%d", major, minor)
}

// mountPointForDevice picks, among the /proc/self/mountinfo lines of device devID, the
// deepest mount point that holds resolved; when none does (a bind mount reached through
// another path), the last entry of the device. found is false when no line has devID.
func mountPointForDevice(lines []string, devID, resolved string) (mountPoint string, found bool) {
	best, last := "", ""
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[2] != devID {
			continue
		}
		mp := unescapeOctal(fields[4])
		last = mp
		if (mp == "/" || resolved == mp || strings.HasPrefix(resolved, mp+"/")) && len(mp) > len(best) {
			best = mp
		}
	}
	if best != "" {
		return best, true
	}
	return last, last != ""
}

// bestMountPointFor picks the deepest mount point owning absPath from /proc/mounts
// lines, defaulting to "/".
func bestMountPointFor(lines []string, absPath string) string {
	bestMatch := "/"
	bestMatchLen := 0

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		mountPoint := fields[1]
		// Unescape octal sequences like \040 (space)
		mountPoint = unescapeOctal(mountPoint)

		// Under this mount point by PATH BOUNDARY: a bare prefix check let a
		// sibling mount whose path is a string prefix win (/mnt/nas vs /mnt/nas2),
		// and a dead sibling then aborted the backup of a healthy path.
		if mountPoint == "/" || absPath == mountPoint || strings.HasPrefix(absPath, mountPoint+"/") {
			if len(mountPoint) > bestMatchLen {
				bestMatch = mountPoint
				bestMatchLen = len(mountPoint)
			}
		}
	}

	return bestMatch
}

// getFilesystemType gets the filesystem type for a mount point using df
func (d *FilesystemDetector) getFilesystemType(ctx context.Context, mountPoint string) (FilesystemType, string, error) {
	// Use statfs to confirm the mount point is reachable. Bounded so a dead/stale
	// mount does not wedge here in an uninterruptible syscall.
	if _, err := safefs.Statfs(ctx, mountPoint, d.ioTimeout); err != nil {
		return FilesystemUnknown, "", err
	}

	// Read /proc/mounts to get the actual filesystem type string
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return FilesystemUnknown, "", err
	}

	if fsTypeStr, device, ok := lastMountEntryFor(strings.Split(string(data), "\n"), mountPoint); ok {
		return parseFilesystemType(fsTypeStr), device, nil
	}

	return FilesystemUnknown, "", fmt.Errorf("filesystem type not found in /proc/mounts")
}

// lastMountEntryFor returns the type and device of the LAST /proc/mounts entry for
// mountPoint. The kernel appends mounts in order, so with stacked entries the last
// one is the filesystem a path actually resolves to - under a systemd automount the
// autofs placeholder comes first and the triggered real filesystem after it, and
// answering with the first entry made the backup path look like an Unknown autofs:
// no network ownership probe, chown/chmod on stored backups silently skipped.
func lastMountEntryFor(lines []string, mountPoint string) (fsTypeStr, device string, ok bool) {
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if unescapeOctal(fields[1]) == mountPoint {
			fsTypeStr, device, ok = fields[2], fields[0], true
		}
	}
	return fsTypeStr, device, ok
}

// testOwnershipSupport tests if a filesystem actually supports Unix ownership
// This is necessary for network filesystems (NFS/CIFS) which may or may not support it
func (d *FilesystemDetector) testOwnershipSupport(ctx context.Context, path string) bool {
	// The probe creates a temp file and exercises chown/chmod/stat on it. On a
	// dead/stale network mount any of these can block in an uninterruptible
	// syscall, so the whole sequence is bounded as a single unit; on timeout the
	// worker goroutine is abandoned and we conservatively report "no ownership".
	ok, err := safefs.Run(ctx, "ownership-probe", path, d.ioTimeout, func() (bool, error) {
		// Confine every probe op to `path` via os.Root: the create-by-name carries
		// no tainted absolute path, so this is structurally free of gosec G304 (file
		// inclusion via a variable) without a #nosec, and the probe file can never
		// escape the directory under test.
		root, rerr := os.OpenRoot(path)
		if rerr != nil {
			d.logger.Debug("Cannot open root for ownership check: %v", rerr)
			return false, nil
		}
		defer func() { _ = root.Close() }()

		name := ".ownership_test_" + utils.GenerateRandomString(8)

		// Create the test file
		f, cerr := root.Create(name)
		if cerr != nil {
			d.logger.Debug("Cannot create test file for ownership check: %v", cerr)
			return false, nil
		}
		defer func() { _ = root.Remove(name) }()

		if cerr := f.Close(); cerr != nil {
			d.logger.Debug("Cannot close test file for ownership check: %v", cerr)
			return false, nil
		}

		// Try to change ownership to current user (should be safe)
		uid := os.Getuid()
		gid := os.Getgid()

		if cerr := root.Chown(name, uid, gid); cerr != nil {
			d.logger.Debug("Chown test failed: %v", cerr)
			return false, nil
		}

		// Try to change permissions
		if cerr := root.Chmod(name, 0600); cerr != nil {
			d.logger.Debug("Chmod test failed: %v", cerr)
			return false, nil
		}

		// Verify the changes took effect
		stat, serr := root.Stat(name)
		if serr != nil {
			return false, nil
		}

		// Check if permissions were actually set
		if stat.Mode().Perm() != 0600 {
			d.logger.Debug("Permissions not set correctly: expected 0600, got %o", stat.Mode().Perm())
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		d.logger.Debug("Ownership probe on %s did not complete: %v", path, err)
		return false
	}
	return ok
}

// parseFilesystemType converts a filesystem type string to FilesystemType
func parseFilesystemType(fsTypeStr string) FilesystemType {
	fsTypeStr = strings.ToLower(fsTypeStr)
	// CephFS had no case: the kernel client ("ceph") fell to unknown and ceph-fuse to
	// the FUSE rule, both "no ownership", so backup permissions were never applied on a
	// common PVE destination that takes POSIX ownership.
	switch fsTypeStr {
	case "ceph", "fuse.ceph-fuse", "fuse.ceph":
		return FilesystemCeph
	}
	if strings.HasPrefix(fsTypeStr, "fuse.") {
		return FilesystemFUSE
	}

	switch fsTypeStr {
	case "ext4":
		return FilesystemExt4
	case "ext3":
		return FilesystemExt3
	case "ext2":
		return FilesystemExt2
	case "xfs":
		return FilesystemXFS
	case "btrfs":
		return FilesystemBtrfs
	case "zfs":
		return FilesystemZFS
	case "jfs":
		return FilesystemJFS
	case "reiserfs":
		return FilesystemReiserFS
	case "overlay":
		return FilesystemOverlay
	case "tmpfs":
		return FilesystemTmpfs
	case "vfat", "fat32":
		return FilesystemFAT32
	case "fat", "fat16":
		return FilesystemFAT
	case "msdos":
		return FilesystemMSDOS
	case "exfat":
		return FilesystemExFAT
	case "ntfs", "ntfs-3g":
		return FilesystemNTFS
	case "fuse":
		return FilesystemFUSE
	case "nfs":
		return FilesystemNFS
	case "nfs4":
		return FilesystemNFS4
	case "cifs", "smb", "smbfs", "smb2", "smb3":
		return FilesystemCIFS
	default:
		return FilesystemUnknown
	}
}

// unescapeOctal unescapes octal sequences in mount point strings
// /proc/mounts represents spaces as \040, etc.
func unescapeOctal(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+3 < len(s) {
			// Try to parse octal sequence (exactly 3 octal digits)
			octal := s[i+1 : i+4]
			valid := true
			for j := range 3 {
				if octal[j] < '0' || octal[j] > '7' {
					valid = false
					break
				}
			}
			if valid {
				val, err := strconv.ParseUint(octal, 8, 8)
				if err == nil {
					result.WriteByte(byte(val))
					i += 4
					continue
				}
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

// PermissionsError is what SetPermissions could not apply to one file: the owner
// (chown), the mode (chmod), or both. A nil field is an operation that succeeded. The
// storage blocks print one fact per failed operation, in the order they run
// ("  Owner failed: ...", then "  Permissions failed: ...").
type PermissionsError struct {
	Owner error
	Mode  error
}

func (e *PermissionsError) Error() string {
	var parts []string
	for _, err := range []error{e.Owner, e.Mode} {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, "; ")
}

func (e *PermissionsError) Unwrap() []error {
	var errs []error
	for _, err := range []error{e.Owner, e.Mode} {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// SetPermissions sets file permissions and ownership, respecting filesystem
// capabilities. A failed chown no longer stops the chmod, and neither is dropped: the
// result is a *PermissionsError carrying each operation that failed, nil when both
// succeeded.
func (d *FilesystemDetector) SetPermissions(ctx context.Context, path string, uid, gid int, mode os.FileMode, fsInfo *FilesystemInfo) error {
	// If filesystem doesn't support ownership, skip
	if fsInfo != nil && !fsInfo.SupportsOwnership {
		d.logger.Debug("Skipping chown/chmod for %s (filesystem %s doesn't support ownership)", path, fsInfo.Type)
		return nil
	}

	var failed PermissionsError

	// Try to set ownership (bounded against a dead/stale mount).
	if _, err := safefs.Run(ctx, "setperm-chown", path, d.ioTimeout, func() (struct{}, error) {
		return struct{}{}, os.Chown(path, uid, gid)
	}); err != nil {
		d.logger.Debug("Failed to set ownership for %s (filesystem %s): %v", path, fsTypeLabel(fsInfo), err)
		// Continue with chmod: the mode is worth setting even when the owner is not.
		failed.Owner = err
	}

	// Try to set permissions (bounded against a dead/stale mount).
	if err := safefs.Chmod(ctx, path, mode, d.ioTimeout); err != nil {
		d.logger.Debug("Failed to set permissions for %s (filesystem %s): %v", path, fsTypeLabel(fsInfo), err)
		failed.Mode = err
	}

	if failed.Owner == nil && failed.Mode == nil {
		return nil
	}
	return &failed
}

// fsTypeLabel is the filesystem type a DEBUG line names, "unknown" without a detection.
func fsTypeLabel(fsInfo *FilesystemInfo) string {
	if fsInfo == nil || fsInfo.Type == "" {
		return "unknown"
	}
	return string(fsInfo.Type)
}
