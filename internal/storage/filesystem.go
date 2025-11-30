package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// FilesystemDetector provides methods to detect and validate filesystem types
type FilesystemDetector struct {
	logger *logging.Logger
}

// NewFilesystemDetector creates a new filesystem detector
func NewFilesystemDetector(logger *logging.Logger) *FilesystemDetector {
	return &FilesystemDetector{
		logger: logger,
	}
}

// DetectFilesystem detects the filesystem type for a given path
// This function logs the detected filesystem type in real-time
func (d *FilesystemDetector) DetectFilesystem(ctx context.Context, path string) (*FilesystemInfo, error) {
	// Ensure path exists
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("path does not exist: %s: %w", path, err)
	}

	// Get mount point for this path
	mountPoint, err := d.getMountPoint(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get mount point for %s: %w", path, err)
	}

	// Get filesystem type using df command
	fsType, device, err := d.getFilesystemType(ctx, mountPoint)
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

	// Check if we need to test ownership support for network filesystems
	if info.IsNetworkFS {
		supportsOwnership := d.testOwnershipSupport(ctx, path)
		info.SupportsOwnership = supportsOwnership
		if supportsOwnership {
			d.logger.Info("Network filesystem %s supports Unix ownership", fsType)
		} else {
			d.logger.Warning("Network filesystem %s does NOT support Unix ownership", fsType)
		}
	}

	// Auto-exclude incompatible filesystems
	if fsType.ShouldAutoExclude() {
		d.logger.Warning("Filesystem %s is incompatible with Unix ownership - will skip chown/chmod", fsType)
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
	// Use stat syscall to get filesystem information
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mountPoint, &stat); err != nil {
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
	// Create a temporary test file
	testFile := filepath.Join(path, ".ownership_test_"+utils.GenerateRandomString(8))

	// Create the test file
	f, err := os.Create(testFile)
	if err != nil {
		d.logger.Debug("Cannot create test file for ownership check: %v", err)
		return false
	}
	f.Close()
	defer os.Remove(testFile)

	// Try to change ownership to current user (should be safe)
	uid := os.Getuid()
	gid := os.Getgid()

	if err := os.Chown(testFile, uid, gid); err != nil {
		d.logger.Debug("Chown test failed: %v", err)
		return false
	}

	// Try to change permissions
	if err := os.Chmod(testFile, 0600); err != nil {
		d.logger.Debug("Chmod test failed: %v", err)
		return false
	}

	// Verify the changes took effect
	stat, err := os.Stat(testFile)
	if err != nil {
		return false
	}

	// Check if permissions were actually set
	if stat.Mode().Perm() != 0600 {
		d.logger.Debug("Permissions not set correctly: expected 0600, got %o", stat.Mode().Perm())
		return false
	}

	return true
}

// parseFilesystemType converts a filesystem type string to FilesystemType
func parseFilesystemType(fsTypeStr string) FilesystemType {
	fsTypeStr = strings.ToLower(fsTypeStr)

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
	case "vfat", "fat32":
		return FilesystemFAT32
	case "fat", "fat16":
		return FilesystemFAT
	case "exfat":
		return FilesystemExFAT
	case "ntfs", "ntfs-3g":
		return FilesystemNTFS
	case "fuse", "fuse.sshfs":
		return FilesystemFUSE
	case "nfs":
		return FilesystemNFS
	case "nfs4":
		return FilesystemNFS4
	case "cifs", "smb", "smbfs":
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
			// Try to parse octal sequence
			octal := s[i+1 : i+4]
			var val int
			if _, err := fmt.Sscanf(octal, "%o", &val); err == nil {
				result.WriteByte(byte(val))
				i += 4
				continue
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
