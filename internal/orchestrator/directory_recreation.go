package orchestrator

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

var (
	storageCfgPath   = "/etc/pve/storage.cfg"
	datastoreCfgPath = "/etc/proxmox-backup/datastore.cfg"
	zpoolCachePath   = "/etc/zfs/zpool.cache"
)

// RecreateStorageDirectories parses storage.cfg and recreates storage directories (PVE)
func RecreateStorageDirectories(logger *logging.Logger) error {
	// Check if file exists
	if _, err := os.Stat(storageCfgPath); err != nil {
		if os.IsNotExist(err) {
			logger.Debug("No storage.cfg found, skipping storage directory recreation")
			return nil
		}
		return fmt.Errorf("stat storage.cfg: %w", err)
	}

	logger.Info("Parsing storage.cfg to recreate storage directories...")

	file, err := os.Open(storageCfgPath)
	if err != nil {
		return fmt.Errorf("open storage.cfg: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var currentStorage string
	var currentPath string
	var currentType string

	directoriesCreated := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for storage definition start (e.g., "dir: local")
		if strings.Contains(line, ":") && !strings.Contains(line, "=") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentType = strings.TrimSuffix(parts[0], ":")
				currentStorage = strings.TrimSuffix(parts[1], ":")
				currentPath = ""
			}
			continue
		}

		// Parse path directive
		if strings.HasPrefix(line, "path ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentPath = parts[1]
			}
		}

		// When we have both storage name and path, create the directory structure
		if currentStorage != "" && currentPath != "" && currentType != "" {
			if err := createPVEStorageStructure(currentPath, currentType, logger); err != nil {
				logger.Warning("Failed to create storage structure for %s: %v", currentStorage, err)
			} else {
				directoriesCreated++
				logger.Debug("Created storage structure: %s (%s) at %s", currentStorage, currentType, currentPath)
			}

			// Reset for next storage
			currentStorage = ""
			currentPath = ""
			currentType = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read storage.cfg: %w", err)
	}

	if directoriesCreated > 0 {
		logger.Info("Recreated %d storage directory structures", directoriesCreated)
	}

	return nil
}

// createPVEStorageStructure creates the directory structure for a PVE storage
func createPVEStorageStructure(basePath, storageType string, logger *logging.Logger) error {
	// Create base directory
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return fmt.Errorf("create base directory: %w", err)
	}

	// Create subdirectories based on storage type
	switch storageType {
	case "dir":
		// Standard directory storage needs these subdirectories
		subdirs := []string{"dump", "images", "template", "snippets", "private"}
		for _, subdir := range subdirs {
			path := filepath.Join(basePath, subdir)
			if err := os.MkdirAll(path, 0750); err != nil {
				logger.Warning("Failed to create %s: %v", path, err)
			}
		}

	case "nfs", "cifs":
		// Network storage
		subdirs := []string{"dump", "images", "template"}
		for _, subdir := range subdirs {
			path := filepath.Join(basePath, subdir)
			if err := os.MkdirAll(path, 0750); err != nil {
				logger.Warning("Failed to create %s: %v", path, err)
			}
		}

	default:
		// For other storage types, just ensure base path exists
		logger.Debug("Storage type %s does not require subdirectories", storageType)
	}

	// Set ownership to root:root (already the case when running as root)
	// PVE typically uses root:root for storage directories

	return nil
}

// RecreateDatastoreDirectories parses datastore.cfg and recreates datastore directories (PBS)
func RecreateDatastoreDirectories(logger *logging.Logger) error {
	// Check if file exists
	if _, err := os.Stat(datastoreCfgPath); err != nil {
		if os.IsNotExist(err) {
			logger.Debug("No datastore.cfg found, skipping datastore directory recreation")
			return nil
		}
		return fmt.Errorf("stat datastore.cfg: %w", err)
	}

	if err := normalizePBSDatastoreCfg(datastoreCfgPath, logger); err != nil {
		logger.Warning("PBS datastore.cfg normalization failed: %v", err)
	}

	logger.Info("Parsing datastore.cfg to recreate datastore directories...")

	file, err := os.Open(datastoreCfgPath)
	if err != nil {
		return fmt.Errorf("open datastore.cfg: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var currentDatastore string
	var currentPath string

	directoriesCreated := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for datastore definition start (e.g., "datastore: backup")
		if strings.HasPrefix(line, "datastore:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentDatastore = strings.TrimSuffix(parts[1], ":")
				currentPath = ""
			}
			continue
		}

		// Parse path directive
		if strings.HasPrefix(line, "path ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentPath = parts[1]
			}
		}

		// When we have both datastore name and path, create the directory
		if currentDatastore != "" && currentPath != "" {
			created, err := createPBSDatastoreStructure(currentPath, currentDatastore, logger)
			if err != nil {
				logger.Warning("Failed to create datastore structure for %s: %v", currentDatastore, err)
			} else if created {
				directoriesCreated++
				logger.Debug("Created datastore structure: %s at %s", currentDatastore, currentPath)
			}

			// Reset for next datastore
			currentDatastore = ""
			currentPath = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read datastore.cfg: %w", err)
	}

	if directoriesCreated > 0 {
		logger.Info("Recreated %d datastore directory structures", directoriesCreated)
	}

	return nil
}

// createPBSDatastoreStructure creates the directory structure for a PBS datastore.
// It returns true when ProxSave made filesystem changes for this datastore path.
func createPBSDatastoreStructure(basePath, datastoreName string, logger *logging.Logger) (bool, error) {
	done := logging.DebugStart(logger, "pbs datastore directory recreation", "datastore=%s path=%s", datastoreName, basePath)
	var err error
	defer func() { done(err) }()

	changed := false

	// ZFS SAFETY: if ZFS is detected and this path looks like a ZFS mountpoint, avoid creating the datastore directory
	// when it does not exist yet. On ZFS systems the directory is typically created by mounting/importing the pool;
	// creating it ourselves can "shadow" the intended mountpoint and leads to confusing restore outcomes.
	if isLikelyZFSMountPoint(basePath, logger) {
		if _, statErr := os.Stat(basePath); statErr != nil {
			if os.IsNotExist(statErr) {
				logger.Warning("PBS datastore preflight: %s looks like a ZFS mountpoint and does not exist yet; skipping directory creation to avoid shadowing a not-yet-imported pool", basePath)
				err = nil
				return false, nil
			}
			logger.Warning("PBS datastore preflight: unable to stat potential ZFS mountpoint %s: %v; skipping any datastore filesystem changes", basePath, statErr)
			err = nil
			return false, nil
		}
	}

	dataUnknown := false
	hasData, dataErr := pbsDatastoreHasData(basePath)
	if dataErr != nil {
		dataUnknown = true
		logger.Warning("PBS datastore preflight: unable to determine whether %s contains datastore data: %v", basePath, dataErr)
	}

	onRootFS, existingPath, devErr := isPathOnRootFilesystem(basePath)
	if devErr != nil {
		logger.Warning("PBS datastore preflight: unable to determine filesystem device for %s: %v", basePath, devErr)
	}
	logging.DebugStep(
		logger,
		"pbs datastore preflight",
		"path=%s existing=%s on_rootfs=%t has_data=%t data_unknown=%t",
		basePath,
		existingPath,
		onRootFS,
		hasData,
		dataUnknown,
	)

	// IMPORTANT SAFETY GUARD:
	// If the datastore path looks like a mountpoint location (e.g. under /mnt) but resolves to the root filesystem
	// and contains no datastore data, we assume the disk/pool is not mounted and refuse to write. This prevents
	// accidentally creating datastore scaffolding on "/" during restore.
	if onRootFS && (isSuspiciousDatastoreMountLocation(basePath) || isLikelyZFSMountPoint(basePath, logger)) && (dataUnknown || !hasData) {
		logger.Warning("PBS datastore preflight: %s resolves to the root filesystem (mount missing?) — skipping datastore directory initialization to avoid writing to the wrong disk", basePath)
		logger.Info("Mount/import the datastore disk/pool first, then restart PBS services.")
		if _, zfsErr := os.Stat(zpoolCachePath); zfsErr == nil {
			logger.Info("ZFS detected: if this datastore was on ZFS, you may need to import the pool first (e.g. `zpool import` then `zpool import <pool-name>`).")
		}
		err = nil
		return false, nil
	}

	// If we cannot reliably inspect the datastore path, we refuse to mutate it to avoid risking real datastore data.
	if dataUnknown {
		logger.Warning("PBS datastore preflight: datastore path inspection failed — skipping any datastore filesystem changes to avoid risking existing data")
		err = nil
		return false, nil
	}

	// If the datastore already contains chunk/index data, avoid any modifications to prevent touching real backup data.
	// We only validate and report issues.
	if hasData {
		if warn := validatePBSDatastoreReadOnly(basePath, logger); warn != "" {
			logger.Warning("PBS datastore preflight: %s", warn)
		}
		logger.Info("PBS datastore preflight: datastore %s appears to contain data; skipping directory/permission changes to avoid risking datastore contents", datastoreName)
		err = nil
		return false, nil
	}

	// If the datastore root contains any entries outside of the expected PBS scaffolding, do not touch it.
	// This keeps ProxSave conservative: only initialize truly empty/uninitialized datastore directories.
	unexpected, unexpectedErr := pbsDatastoreHasUnexpectedEntries(basePath)
	if unexpectedErr != nil {
		logger.Warning("PBS datastore preflight: unable to inspect %s contents: %v; skipping any datastore filesystem changes to avoid risking unrelated data", basePath, unexpectedErr)
		err = nil
		return false, nil
	}
	if unexpected {
		logger.Warning("PBS datastore preflight: %s is not empty (unexpected entries present); skipping any datastore filesystem changes to avoid risking unrelated data", basePath)
		err = nil
		return false, nil
	}

	dirsToFix, err := computeMissingDirs(basePath)
	if err != nil {
		return false, fmt.Errorf("compute missing dirs: %w", err)
	}

	// Create base directory
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return false, fmt.Errorf("create base directory: %w", err)
	}
	if len(dirsToFix) > 0 {
		changed = true
	}

	// PBS datastores need these subdirectories
	subdirs := []string{".chunks", ".index"}
	for _, subdir := range subdirs {
		path := filepath.Join(basePath, subdir)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				changed = true
				dirsToFix = append(dirsToFix, path)
			}
		}
		if err := os.MkdirAll(path, 0750); err != nil {
			logger.Warning("Failed to create %s: %v", path, err)
		}
	}

	// Set ownership to backup:backup when possible for directory components created by ProxSave.
	// This avoids a common failure mode where parent directories created by MkdirAll remain root-only
	// and prevent PBS (backup user) from accessing the datastore path.
	if len(dirsToFix) > 0 {
		logger.Debug("PBS datastore permissions: applying ownership to %d created path(s) (datastore=%s path=%s)", len(dirsToFix), datastoreName, basePath)
	}
	for _, dir := range dirsToFix {
		if err := setDatastoreOwnership(dir, logger); err != nil {
			logger.Warning("Could not set datastore ownership for %s: %v", dir, err)
		}
	}

	// Always attempt to fix the datastore root itself (even if it pre-existed), since PBS requires
	// backup:backup ownership and accessible permissions to function.
	if err := setDatastoreOwnership(basePath, logger); err != nil {
		logger.Warning("Could not set datastore ownership for %s: %v", basePath, err)
	}

	lockChanged, lockErr := ensurePBSDatastoreLockFile(basePath, logger)
	if lockErr != nil {
		logger.Warning("PBS datastore lock file: %v", lockErr)
	}
	changed = changed || lockChanged

	return changed, nil
}

func validatePBSDatastoreReadOnly(datastorePath string, logger *logging.Logger) string {
	if datastorePath == "" {
		return "datastore path is empty"
	}

	info, err := os.Stat(datastorePath)
	if err != nil {
		return fmt.Sprintf("datastore path %s cannot be stat'd: %v", datastorePath, err)
	}
	if !info.IsDir() {
		return fmt.Sprintf("datastore path %s is not a directory (type=%s)", datastorePath, info.Mode())
	}

	chunksPath := filepath.Join(datastorePath, ".chunks")
	chunksInfo, err := os.Stat(chunksPath)
	if err != nil {
		return fmt.Sprintf("datastore %s missing .chunks directory: %v", datastorePath, err)
	}
	if !chunksInfo.IsDir() {
		return fmt.Sprintf("datastore %s .chunks is not a directory (type=%s)", datastorePath, chunksInfo.Mode())
	}

	indexPath := filepath.Join(datastorePath, ".index")
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return fmt.Sprintf("datastore %s missing .index directory: %v", datastorePath, err)
	}
	if !indexInfo.IsDir() {
		return fmt.Sprintf("datastore %s .index is not a directory (type=%s)", datastorePath, indexInfo.Mode())
	}

	lockPath := filepath.Join(datastorePath, ".lock")
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		return fmt.Sprintf("datastore %s missing .lock file: %v", datastorePath, err)
	}
	if !lockInfo.Mode().IsRegular() {
		return fmt.Sprintf("datastore %s .lock is not a regular file (type=%s)", datastorePath, lockInfo.Mode())
	}

	return ""
}

func ensurePBSDatastoreLockFile(datastorePath string, logger *logging.Logger) (bool, error) {
	lockPath := filepath.Join(datastorePath, ".lock")

	info, err := os.Lstat(lockPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("stat %s: %w", lockPath, err)
		}

		logger.Debug("PBS datastore lock: creating %s", lockPath)
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return false, fmt.Errorf("create %s: %w", lockPath, err)
		}
		_ = file.Close()

		if err := setDatastoreOwnership(lockPath, logger); err != nil {
			return true, fmt.Errorf("chown %s: %w", lockPath, err)
		}
		return true, nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s is a symlink; refusing to manage lock file", lockPath)
	}

	if info.IsDir() {
		changed := false
		entries, err := os.ReadDir(lockPath)
		if err != nil {
			return false, fmt.Errorf("lock path %s is a directory and cannot be read: %w", lockPath, err)
		}

		if len(entries) == 0 {
			logger.Warning("PBS datastore lock: %s is a directory (invalid); removing and recreating as file", lockPath)
			if err := os.Remove(lockPath); err != nil {
				return false, fmt.Errorf("remove invalid lock dir %s: %w", lockPath, err)
			}
			changed = true
		} else {
			backupPath := fmt.Sprintf("%s.proxsave-dir.%s", lockPath, nowRestore().Format("20060102-150405"))
			logger.Warning("PBS datastore lock: %s is a non-empty directory (invalid); renaming to %s and creating lock file", lockPath, backupPath)
			if err := os.Rename(lockPath, backupPath); err != nil {
				return false, fmt.Errorf("rename invalid lock dir %s -> %s: %w", lockPath, backupPath, err)
			}
			changed = true
		}

		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return changed, fmt.Errorf("create %s: %w", lockPath, err)
		}
		_ = file.Close()
		changed = true

		if err := setDatastoreOwnership(lockPath, logger); err != nil {
			return changed, fmt.Errorf("chown %s: %w", lockPath, err)
		}

		return changed, nil
	}

	if err := setDatastoreOwnership(lockPath, logger); err != nil {
		return false, fmt.Errorf("chown %s: %w", lockPath, err)
	}

	return false, nil
}

func normalizePBSDatastoreCfg(path string, logger *logging.Logger) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read datastore.cfg: %w", err)
	}

	normalized, fixed := normalizePBSDatastoreCfgContent(string(raw))
	if fixed == 0 {
		logger.Debug("PBS datastore.cfg: formatting looks OK (no normalization needed)")
		return nil
	}

	if err := os.MkdirAll("/tmp/proxsave", 0o755); err != nil {
		return fmt.Errorf("ensure /tmp/proxsave exists: %w", err)
	}

	backupPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("datastore.cfg.pre-normalize.%s", nowRestore().Format("20060102-150405")))
	if err := os.WriteFile(backupPath, raw, 0o600); err != nil {
		return fmt.Errorf("write backup copy: %w", err)
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmpPath := fmt.Sprintf("%s.proxsave.tmp", path)
	if err := os.WriteFile(tmpPath, []byte(normalized), mode); err != nil {
		return fmt.Errorf("write normalized datastore.cfg: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace datastore.cfg: %w", err)
	}

	logger.Warning("PBS datastore.cfg: fixed %d malformed line(s) (properties must be indented); backup saved to %s", fixed, backupPath)
	return nil
}

func normalizePBSDatastoreCfgContent(content string) (string, int) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return content, 0
	}

	inDatastoreBlock := false
	fixed := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasPrefix(trimmed, "datastore:") {
			inDatastoreBlock = true
			continue
		}

		if !inDatastoreBlock {
			continue
		}

		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}

		lines[i] = "    " + line
		fixed++
	}

	return strings.Join(lines, "\n"), fixed
}

func computeMissingDirs(target string) ([]string, error) {
	path := filepath.Clean(target)
	if path == "" || path == "." || path == "/" {
		return nil, nil
	}

	var missing []string
	for {
		if path == "" || path == "." || path == "/" {
			break
		}
		_, err := os.Stat(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		missing = append(missing, path)
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}

	// Reverse so parents come first (top-down), making logs more readable.
	for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
		missing[i], missing[j] = missing[j], missing[i]
	}
	return missing, nil
}

func pbsDatastoreHasData(datastorePath string) (bool, error) {
	if strings.TrimSpace(datastorePath) == "" {
		return false, fmt.Errorf("path is empty")
	}
	info, err := os.Stat(datastorePath)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}

	for _, subdir := range []string{".chunks", ".index"} {
		has, err := dirHasAnyEntry(filepath.Join(datastorePath, subdir))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		if has {
			return true, nil
		}
	}

	return false, nil
}

func pbsDatastoreHasUnexpectedEntries(datastorePath string) (bool, error) {
	if strings.TrimSpace(datastorePath) == "" {
		return false, nil
	}

	info, err := os.Stat(datastorePath)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}

	allowed := map[string]struct{}{
		".chunks": {},
		".index":  {},
		".lock":   {},
	}

	f, err := os.Open(datastorePath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	for {
		names, err := f.Readdirnames(64)
		if err == nil {
			for _, name := range names {
				if _, ok := allowed[name]; ok {
					continue
				}
				return true, nil
			}
			continue
		}

		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
}

func dirHasAnyEntry(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	return false, err
}

func isConfirmableDatastoreMountRoot(path string) bool {
	path = filepath.Clean(path)
	switch {
	case strings.HasPrefix(path, "/mnt/"):
		return true
	case strings.HasPrefix(path, "/media/"):
		return true
	case strings.HasPrefix(path, "/run/media/"):
		return true
	default:
		return false
	}
}

func isSuspiciousDatastoreMountLocation(path string) bool {
	// Conservative: only treat typical mount roots as "must be mounted".
	// This prevents accidental writes to "/" when a disk/pool wasn't mounted yet.
	return isConfirmableDatastoreMountRoot(path)
}

func isPathOnRootFilesystem(path string) (bool, string, error) {
	rootDev, err := deviceID("/")
	if err != nil {
		return false, "/", err
	}

	existing, err := nearestExistingPath(path)
	if err != nil {
		return false, "", err
	}
	targetDev, err := deviceID(existing)
	if err != nil {
		return false, existing, err
	}
	return rootDev == targetDev, existing, nil
}

func nearestExistingPath(target string) (string, error) {
	path := filepath.Clean(target)
	if path == "" || path == "." {
		return "", fmt.Errorf("invalid path")
	}

	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(path)
		if parent == path {
			return path, nil
		}
		path = parent
	}
}

func deviceID(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, fmt.Errorf("unsupported stat type for %s", path)
	}
	return uint64(stat.Dev), nil
}

// isLikelyZFSMountPoint checks if a path is likely a ZFS mount point
func isLikelyZFSMountPoint(path string, logger *logging.Logger) bool {
	// Check if /etc/zfs/zpool.cache exists (indicates ZFS is used on this system)
	if _, err := os.Stat(zpoolCachePath); err != nil {
		// No ZFS on this system
		return false
	}

	// Common ZFS mount point patterns
	// PBS datastores on ZFS are typically under /mnt/ or use "backup" in the name
	pathLower := strings.ToLower(path)
	if strings.HasPrefix(pathLower, "/mnt/") ||
		strings.Contains(pathLower, "backup") ||
		strings.Contains(pathLower, "datastore") {
		logger.Debug("Path %s matches ZFS mount point pattern", path)
		return true
	}

	return false
}

// setDatastoreOwnership sets ownership to backup:backup for PBS datastores
func setDatastoreOwnership(path string, logger *logging.Logger) error {
	backupUser, err := user.Lookup("backup")
	if err != nil {
		// On non-PBS systems the user may not exist; treat as non-fatal.
		logger.Debug("PBS datastore ownership: user 'backup' not found; skipping chown for %s", path)
		return nil
	}
	uid, err := strconv.Atoi(backupUser.Uid)
	if err != nil {
		return fmt.Errorf("parse backup uid: %w", err)
	}
	gid, err := strconv.Atoi(backupUser.Gid)
	if err != nil {
		return fmt.Errorf("parse backup gid: %w", err)
	}

	logger.Debug("PBS datastore ownership: chown %s to backup:backup (uid=%d gid=%d)", path, uid, gid)
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		// Ownership was already applied; ignore stat errors for further chmod adjustments.
		return nil
	}
	if info.IsDir() {
		current := info.Mode().Perm()
		required := os.FileMode(0o750)
		desired := current | required
		if desired != current {
			logger.Debug("PBS datastore permissions: chmod %s from %o to %o", path, current, desired)
			if err := os.Chmod(path, desired); err != nil {
				return fmt.Errorf("chmod %s: %w", path, err)
			}
		}
	}

	return nil
}

// RecreateDirectoriesFromConfig recreates storage/datastore directories based on system type
func RecreateDirectoriesFromConfig(systemType SystemType, logger *logging.Logger) error {
	logger.Info("Recreating directory structures from configuration...")

	if systemType == SystemTypePVE {
		if err := RecreateStorageDirectories(logger); err != nil {
			return fmt.Errorf("recreate PVE storage directories: %w", err)
		}
	} else if systemType == SystemTypePBS {
		if err := RecreateDatastoreDirectories(logger); err != nil {
			return fmt.Errorf("recreate PBS datastore directories: %w", err)
		}
	} else {
		logger.Debug("Unknown system type, skipping directory recreation")
	}

	return nil
}
