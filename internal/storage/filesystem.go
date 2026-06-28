package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if _, err := safefs.Stat(ctx, path, d.ioTimeout); err != nil {
		return nil, fmt.Errorf("path does not exist: %s: %w", path, err)
	}

	// Get mount point for this path
	var mountPoint string
	var err error
	if d.mountPointLookup != nil {
		mountPoint, err = d.mountPointLookup(path)
	} else {
		mountPoint, err = d.getMountPoint(path)
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
				d.logger.Info("Network filesystem %s supports Unix ownership", fsType)
			} else {
				d.logger.Info("Network filesystem %s does NOT support Unix ownership", fsType)
			}
		}
	}

	// Auto-exclude incompatible filesystems
	if fsType.ShouldAutoExclude() {
		d.logger.Info("Filesystem %s is incompatible with Unix ownership - will skip chown/chmod", fsType)
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

	// Log in format: "Path: /path/to/backup -> Filesystem: ext4 (supports ownership)" (debug-level, detailed summary printed elsewhere)
	d.logger.Debug("Path: %s -> Filesystem: %s (%s)%s [mount: %s]",
		info.Path,
		info.Type,
		ownership,
		network,
		info.MountPoint,
	)
}

// getMountPoint finds the mount point for a given path
func (d *FilesystemDetector) getMountPoint(path string) (string, error) {
	// Read /proc/mounts to find mount points
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	bestMatch := "/"
	bestMatchLen := 0

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		mountPoint := fields[1]
		// Unescape octal sequences like \040 (space)
		mountPoint = unescapeOctal(mountPoint)

		// Check if this path is under this mount point
		if strings.HasPrefix(absPath, mountPoint) {
			if len(mountPoint) > bestMatchLen {
				bestMatch = mountPoint
				bestMatchLen = len(mountPoint)
			}
		}
	}

	return bestMatch, nil
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

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		mount := unescapeOctal(fields[1])
		if mount == mountPoint {
			fsTypeStr := fields[2]
			device := fields[0]
			return parseFilesystemType(fsTypeStr), device, nil
		}
	}

	return FilesystemUnknown, "", fmt.Errorf("filesystem type not found in /proc/mounts")
}

// testOwnershipSupport tests if a filesystem actually supports Unix ownership
// This is necessary for network filesystems (NFS/CIFS) which may or may not support it
func (d *FilesystemDetector) testOwnershipSupport(ctx context.Context, path string) bool {
	// The probe creates a temp file and exercises chown/chmod/stat on it. On a
	// dead/stale network mount any of these can block in an uninterruptible
	// syscall, so the whole sequence is bounded as a single unit; on timeout the
	// worker goroutine is abandoned and we conservatively report "no ownership".
	ok, err := safefs.Run(ctx, "ownership-probe", path, d.ioTimeout, func() (bool, error) {
		testFile := filepath.Join(path, ".ownership_test_"+utils.GenerateRandomString(8))

		// Create the test file
		f, cerr := os.Create(testFile)
		if cerr != nil {
			d.logger.Debug("Cannot create test file for ownership check: %v", cerr)
			return false, nil
		}
		defer func() { _ = os.Remove(testFile) }()

		if cerr := f.Close(); cerr != nil {
			d.logger.Debug("Cannot close test file for ownership check: %v", cerr)
			return false, nil
		}

		// Try to change ownership to current user (should be safe)
		uid := os.Getuid()
		gid := os.Getgid()

		if cerr := os.Chown(testFile, uid, gid); cerr != nil {
			d.logger.Debug("Chown test failed: %v", cerr)
			return false, nil
		}

		// Try to change permissions
		if cerr := os.Chmod(testFile, 0600); cerr != nil {
			d.logger.Debug("Chmod test failed: %v", cerr)
			return false, nil
		}

		// Verify the changes took effect
		stat, serr := os.Stat(testFile)
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
			for j := 0; j < 3; j++ {
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

// SetPermissions sets file permissions and ownership, respecting filesystem capabilities
func (d *FilesystemDetector) SetPermissions(ctx context.Context, path string, uid, gid int, mode os.FileMode, fsInfo *FilesystemInfo) error {
	// If filesystem doesn't support ownership, skip
	if fsInfo != nil && !fsInfo.SupportsOwnership {
		d.logger.Debug("Skipping chown/chmod for %s (filesystem %s doesn't support ownership)", path, fsInfo.Type)
		return nil
	}

	// Try to set ownership
	if err := os.Chown(path, uid, gid); err != nil {
		// On compatible filesystem, this is a warning (not an error)
		if fsInfo != nil && !fsInfo.Type.ShouldAutoExclude() {
			d.logger.Warning("Failed to set ownership for %s (filesystem %s): %v", path, fsInfo.Type, err)
		}
		// Don't return error - continue with chmod
	}

	// Try to set permissions
	if err := os.Chmod(path, mode); err != nil {
		// On compatible filesystem, this is a warning (not an error)
		if fsInfo != nil && !fsInfo.Type.ShouldAutoExclude() {
			d.logger.Warning("Failed to set permissions for %s (filesystem %s): %v", path, fsInfo.Type, err)
		}
		return err
	}

	return nil
}
