package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var safetyFS FS = osFS{}
var safetyNow = time.Now

type safetyBackupSpec struct {
	ArchivePrefix     string
	LocationFileName  string
	HumanDescription  string
	WriteLocationFile bool
}

// resolveAndCheckPath cleans and resolves symlinks for candidate extraction paths
// and verifies the resolved path is still within destRoot.
func resolveAndCheckPath(destRoot, candidate string) (string, error) {
	joined := candidate
	if !filepath.IsAbs(candidate) {
		joined = filepath.Join(destRoot, candidate)
	}

	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// If the path doesn't exist yet, EvalSymlinks will fail; fallback to the cleaned path.
		resolved = filepath.Clean(joined)
	}

	absDestRoot, err := filepath.Abs(destRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve destination root: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot resolve candidate path: %w", err)
	}

	rel, err := filepath.Rel(absDestRoot, absResolved)
	if err != nil {
		return "", fmt.Errorf("cannot compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("resolved path escapes destination: %s", absResolved)
	}

	return absResolved, nil
}

// SafetyBackupResult contains information about the safety backup
type SafetyBackupResult struct {
	BackupPath    string
	FilesBackedUp int
	TotalSize     int64
	Timestamp     time.Time
}

func createSafetyBackup(logger *logging.Logger, selectedCategories []Category, destRoot string, spec safetyBackupSpec) (result *SafetyBackupResult, err error) {
	desc := strings.TrimSpace(spec.HumanDescription)
	if desc == "" {
		desc = "Safety backup"
	}
	prefix := strings.TrimSpace(spec.ArchivePrefix)
	if prefix == "" {
		prefix = "restore_backup"
	}
	locationFileName := strings.TrimSpace(spec.LocationFileName)

	done := logging.DebugStart(logger, "create "+strings.ToLower(desc), "dest=%s categories=%d", destRoot, len(selectedCategories))
	defer func() { done(err) }()

	timestamp := safetyNow().Format("20060102_150405")
	baseDir := filepath.Join("/tmp", "proxsave")
	if err := safetyFS.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create safety backup directory: %w", err)
	}
	backupDir := filepath.Join(baseDir, fmt.Sprintf("%s_%s", prefix, timestamp))
	backupArchive := backupDir + ".tar.gz"

	logger.Info("Creating %s of current configuration...", strings.ToLower(desc))
	logger.Debug("%s will be saved to: %s", desc, backupArchive)

	file, err := safetyFS.Create(backupArchive)
	if err != nil {
		return nil, fmt.Errorf("create backup archive: %w", err)
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	result = &SafetyBackupResult{
		BackupPath: backupArchive,
		Timestamp:  safetyNow(),
	}

	pathsToBackup := GetSelectedPaths(selectedCategories)

	for _, catPath := range pathsToBackup {
		fsPath := strings.TrimPrefix(catPath, "./")
		if strings.ContainsAny(fsPath, "*?[") {
			pattern := filepath.Join(destRoot, fsPath)
			matches, err := globFS(safetyFS, pattern)
			if err != nil {
				logger.Warning("Cannot expand glob %s: %v", pattern, err)
				continue
			}
			for _, match := range matches {
				info, err := safetyFS.Stat(match)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					logger.Warning("Cannot stat %s: %v", match, err)
					continue
				}

				relPath, err := filepath.Rel(destRoot, match)
				if err != nil {
					logger.Warning("Cannot compute relative path for %s: %v", match, err)
					continue
				}
				relPath = filepath.Clean(relPath)
				if relPath == "." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) || relPath == ".." {
					logger.Warning("Skipping glob match %s: relative path escapes root (%s)", match, relPath)
					continue
				}

				if info.IsDir() {
					err = backupDirectory(tarWriter, match, relPath, result, logger)
					if err != nil {
						logger.Warning("Failed to backup directory %s: %v", match, err)
					}
				} else {
					err = backupFile(tarWriter, match, relPath, result, logger)
					if err != nil {
						logger.Warning("Failed to backup file %s: %v", match, err)
					}
				}
			}
			continue
		}

		fullPath := filepath.Join(destRoot, fsPath)

		info, err := safetyFS.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			logger.Warning("Cannot stat %s: %v", fullPath, err)
			continue
		}

		if info.IsDir() {
			err = backupDirectory(tarWriter, fullPath, fsPath, result, logger)
			if err != nil {
				logger.Warning("Failed to backup directory %s: %v", fullPath, err)
			}
		} else {
			err = backupFile(tarWriter, fullPath, fsPath, result, logger)
			if err != nil {
				logger.Warning("Failed to backup file %s: %v", fullPath, err)
			}
		}
	}

	logger.Info("%s created: %s (%d files, %.2f MB)",
		desc,
		backupArchive,
		result.FilesBackedUp,
		float64(result.TotalSize)/(1024*1024))

	if spec.WriteLocationFile && locationFileName != "" {
		locationFile := filepath.Join(baseDir, locationFileName)
		if err := safetyFS.WriteFile(locationFile, []byte(backupArchive), 0644); err != nil {
			logger.Warning("Could not write backup location file: %v", err)
		} else {
			logger.Info("Backup location saved to: %s", locationFile)
		}
	}

	return result, nil
}

// CreateSafetyBackup creates a backup of files that will be overwritten
func CreateSafetyBackup(logger *logging.Logger, selectedCategories []Category, destRoot string) (result *SafetyBackupResult, err error) {
	return createSafetyBackup(logger, selectedCategories, destRoot, safetyBackupSpec{
		ArchivePrefix:     "restore_backup",
		LocationFileName:  "restore_backup_location.txt",
		HumanDescription:  "Safety backup",
		WriteLocationFile: true,
	})
}

func CreateNetworkRollbackBackup(logger *logging.Logger, selectedCategories []Category, destRoot string) (*SafetyBackupResult, error) {
	networkCat := GetCategoryByID("network", selectedCategories)
	if networkCat == nil {
		return nil, nil
	}
	return createSafetyBackup(logger, []Category{*networkCat}, destRoot, safetyBackupSpec{
		ArchivePrefix:     "network_rollback_backup",
		LocationFileName:  "network_rollback_backup_location.txt",
		HumanDescription:  "Network rollback backup",
		WriteLocationFile: true,
	})
}

func CreateFirewallRollbackBackup(logger *logging.Logger, selectedCategories []Category, destRoot string) (*SafetyBackupResult, error) {
	firewallCat := GetCategoryByID("pve_firewall", selectedCategories)
	if firewallCat == nil {
		return nil, nil
	}
	return createSafetyBackup(logger, []Category{*firewallCat}, destRoot, safetyBackupSpec{
		ArchivePrefix:     "firewall_rollback_backup",
		LocationFileName:  "firewall_rollback_backup_location.txt",
		HumanDescription:  "Firewall rollback backup",
		WriteLocationFile: true,
	})
}

func CreateHARollbackBackup(logger *logging.Logger, selectedCategories []Category, destRoot string) (*SafetyBackupResult, error) {
	haCat := GetCategoryByID("pve_ha", selectedCategories)
	if haCat == nil {
		return nil, nil
	}
	return createSafetyBackup(logger, []Category{*haCat}, destRoot, safetyBackupSpec{
		ArchivePrefix:     "ha_rollback_backup",
		LocationFileName:  "ha_rollback_backup_location.txt",
		HumanDescription:  "HA rollback backup",
		WriteLocationFile: true,
	})
}

func CreatePVEAccessControlRollbackBackup(logger *logging.Logger, selectedCategories []Category, destRoot string) (*SafetyBackupResult, error) {
	acCat := GetCategoryByID("pve_access_control", selectedCategories)
	if acCat == nil {
		return nil, nil
	}
	return createSafetyBackup(logger, []Category{*acCat}, destRoot, safetyBackupSpec{
		ArchivePrefix:     "pve_access_control_rollback_backup",
		LocationFileName:  "pve_access_control_rollback_backup_location.txt",
		HumanDescription:  "PVE access control rollback backup",
		WriteLocationFile: true,
	})
}

// backupFile adds a single file to the tar archive
func backupFile(tw *tar.Writer, sourcePath, archivePath string, result *SafetyBackupResult, logger *logging.Logger) error {
	file, err := safetyFS.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Create tar header
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}

	// Use archive path (relative path)
	header.Name = archivePath

	// Write header
	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	// Write file content
	written, err := io.Copy(tw, file)
	if err != nil {
		return err
	}

	result.FilesBackedUp++
	result.TotalSize += written

	logger.Debug("Backed up: %s (%d bytes)", archivePath, written)

	return nil
}

// backupDirectory recursively backs up a directory
func backupDirectory(tw *tar.Writer, sourcePath, archivePath string, result *SafetyBackupResult, logger *logging.Logger) error {
	return walkFS(safetyFS, sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate relative path for archive
		relPath, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}

		archiveEntryPath := filepath.Join(archivePath, relPath)

		// Handle directories
		if info.IsDir() {
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = archiveEntryPath + "/"

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			logger.Debug("Backed up directory: %s/", archiveEntryPath)
			return nil
		}

		// Handle symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := safetyFS.Readlink(path)
			if err != nil {
				logger.Warning("Cannot read symlink %s: %v", path, err)
				return nil
			}

			header, err := tar.FileInfoHeader(info, linkTarget)
			if err != nil {
				return err
			}
			header.Name = archiveEntryPath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			logger.Debug("Backed up symlink: %s -> %s", archiveEntryPath, linkTarget)
			return nil
		}

		// Handle regular files
		return backupFile(tw, path, archiveEntryPath, result, logger)
	})
}

// RestoreSafetyBackup restores files from a safety backup (for rollback)
func RestoreSafetyBackup(logger *logging.Logger, backupPath string, destRoot string) (err error) {
	done := logging.DebugStart(logger, "restore safety backup", "backup=%s dest=%s", backupPath, destRoot)
	defer func() { done(err) }()
	logger.Info("Restoring from safety backup: %s", backupPath)

	file, err := safetyFS.Open(backupPath)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	filesRestored := 0
	absDestRoot, err := filepath.Abs(destRoot)
	if err != nil {
		return fmt.Errorf("resolve destination root: %w", err)
	}

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target, _, err := sanitizeRestoreEntryTarget(absDestRoot, header.Name)
		if err != nil {
			logger.Warning("Skipping archive entry %s: %v", header.Name, err)
			continue
		}

		relTarget, err := filepath.Rel(absDestRoot, target)
		if err != nil {
			logger.Warning("Cannot compute relative path for %s: %v", header.Name, err)
			continue
		}
		if strings.HasPrefix(relTarget, ".."+string(os.PathSeparator)) || relTarget == ".." {
			logger.Warning("Skipping archive entry %s: relative path escapes root (%s)", header.Name, relTarget)
			continue
		}

		// Create parent directories
		if err := safetyFS.MkdirAll(filepath.Dir(target), 0755); err != nil {
			logger.Warning("Cannot create directory for %s: %v", target, err)
			continue
		}

		// Handle directories
		if header.Typeflag == tar.TypeDir {
			if err := safetyFS.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				logger.Warning("Cannot create directory %s: %v", target, err)
			}
			continue
		}

		// Handle symlinks
		if header.Typeflag == tar.TypeSymlink {
			linkTarget := header.Linkname

			// Resolve intended target relative to the sanitized symlink directory inside the archive
			sanitizedDir := filepath.Dir(relTarget)
			resolvedLinkPath := linkTarget
			if !filepath.IsAbs(linkTarget) {
				resolvedLinkPath = filepath.Join(sanitizedDir, linkTarget)
			}

			if _, pathErr := resolveAndCheckPath(destRoot, resolvedLinkPath); pathErr != nil {
				logger.Warning("Skipping symlink %s -> %s: target escapes root: %v", target, linkTarget, pathErr)
				continue
			}

			// Remove existing file/symlink before creating new one
			safetyFS.Remove(target)

			// Create the symlink
			if err := safetyFS.Symlink(linkTarget, target); err != nil {
				logger.Warning("Cannot create symlink %s: %v", target, err)
				continue
			}

			// POST-CREATION VALIDATION: Verify the created symlink's target stays within destRoot
			actualTarget, err := safetyFS.Readlink(target)
			if err != nil {
				logger.Warning("Cannot read created symlink %s: %v", target, err)
				safetyFS.Remove(target) // Clean up the symlink
				continue
			}

			// Resolve the symlink target relative to the symlink's directory
			symlinkTargetDir := filepath.Dir(target)
			resolvedTarget := actualTarget
			if !filepath.IsAbs(actualTarget) {
				resolvedTarget = filepath.Join(symlinkTargetDir, actualTarget)
			}

			// Validate the resolved target stays within destRoot
			absDestRoot, err := filepath.Abs(destRoot)
			if err != nil {
				logger.Warning("Cannot resolve destination root: %v", err)
				safetyFS.Remove(target)
				continue
			}

			absResolvedTarget, err := filepath.Abs(resolvedTarget)
			if err != nil {
				logger.Warning("Cannot resolve symlink target: %v", err)
				safetyFS.Remove(target)
				continue
			}

			// Check if resolved target is within destRoot
			rel, err := filepath.Rel(absDestRoot, absResolvedTarget)
			if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
				logger.Warning("Removing symlink %s -> %s: target escapes root after creation (resolves to %s)",
					target, actualTarget, absResolvedTarget)
				safetyFS.Remove(target)
				continue
			}

			logger.Debug("Created safe symlink: %s -> %s", header.Name, linkTarget)
			continue
		}

		// Handle regular files
		outFile, err := safetyFS.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
		if err != nil {
			logger.Warning("Cannot create file %s: %v", target, err)
			continue
		}

		if _, err := io.Copy(outFile, tarReader); err != nil {
			outFile.Close()
			logger.Warning("Cannot write file %s: %v", target, err)
			continue
		}
		outFile.Close()

		filesRestored++
		logger.Debug("Restored: %s", header.Name)
	}

	logger.Info("Safety backup restored: %d files", filesRestored)
	return nil
}

// CleanupOldSafetyBackups removes safety backups older than the specified duration
func CleanupOldSafetyBackups(logger *logging.Logger, olderThan time.Duration) error {
	tmpDir := "/tmp"
	pattern := "restore_backup_*"

	matches, err := filepath.Glob(filepath.Join(tmpDir, pattern))
	if err != nil {
		return err
	}

	now := safetyNow()
	removed := 0

	for _, match := range matches {
		info, err := safetyFS.Stat(match)
		if err != nil {
			continue
		}

		if now.Sub(info.ModTime()) > olderThan {
			if err := safetyFS.Remove(match); err != nil {
				logger.Warning("Cannot remove old backup %s: %v", match, err)
			} else {
				logger.Debug("Removed old safety backup: %s", match)
				removed++
			}
		}
	}

	if removed > 0 {
		logger.Info("Cleaned up %d old safety backup(s)", removed)
	}

	return nil
}

func globFS(fs FS, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}

	clean := filepath.Clean(pattern)
	sep := string(os.PathSeparator)
	abs := filepath.IsAbs(clean)

	parts := strings.Split(clean, sep)
	if abs && len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}

	paths := []string{""}
	if abs {
		paths = []string{sep}
	}

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		isLast := i == len(parts)-1

		var next []string
		for _, base := range paths {
			dir := base
			if dir == "" {
				dir = "."
			}

			if strings.ContainsAny(part, "*?[") {
				entries, err := fs.ReadDir(dir)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return nil, err
				}
				for _, entry := range entries {
					if entry == nil {
						continue
					}
					name := strings.TrimSpace(entry.Name())
					if name == "" {
						continue
					}
					ok, err := filepath.Match(part, name)
					if err != nil || !ok {
						continue
					}
					candidate := filepath.Join(dir, name)
					if !isLast && !entry.IsDir() {
						continue
					}
					next = append(next, candidate)
				}
				continue
			}

			candidate := filepath.Join(dir, part)
			if _, err := fs.Stat(candidate); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			next = append(next, candidate)
		}

		paths = next
		if len(paths) == 0 {
			break
		}
	}

	return paths, nil
}

// walkFS recursively walks a filesystem using the provided FS implementation.
func walkFS(fs FS, root string, fn func(path string, info os.FileInfo, err error) error) error {
	info, err := fs.Stat(root)
	if err != nil {
		return fn(root, nil, err)
	}
	return walkFSRecursive(fs, root, info, fn)
}

func walkFSRecursive(fs FS, path string, info os.FileInfo, fn func(path string, info os.FileInfo, err error) error) error {
	if err := fn(path, info, nil); err != nil {
		if info != nil && info.IsDir() && err == filepath.SkipDir {
			return nil
		}
		return err
	}

	if info == nil || !info.IsDir() {
		return nil
	}

	entries, err := fs.ReadDir(path)
	if err != nil {
		if err := fn(path, info, err); err != nil && err != filepath.SkipDir {
			return err
		}
		return err
	}

	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		childInfo, err := entry.Info()
		if err != nil {
			if err := fn(childPath, nil, err); err != nil && err != filepath.SkipDir {
				return err
			}
			continue
		}

		if err := walkFSRecursive(fs, childPath, childInfo, fn); err != nil {
			if err == filepath.SkipDir {
				continue
			}
			return err
		}
	}

	return nil
}
