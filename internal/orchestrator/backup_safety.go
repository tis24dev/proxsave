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

	"github.com/tis24dev/proxmox-backup/internal/logging"
)

var safetyFS FS = osFS{}
var safetyNow = time.Now

// SafetyBackupResult contains information about the safety backup
type SafetyBackupResult struct {
	BackupPath    string
	FilesBackedUp int
	TotalSize     int64
	Timestamp     time.Time
}

// CreateSafetyBackup creates a backup of files that will be overwritten
func CreateSafetyBackup(logger *logging.Logger, selectedCategories []Category, destRoot string) (*SafetyBackupResult, error) {
	timestamp := safetyNow().Format("20060102_150405")
	baseDir := filepath.Join("/tmp", "proxmox-backup")
	if err := safetyFS.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create safety backup directory: %w", err)
	}
	backupDir := filepath.Join(baseDir, fmt.Sprintf("restore_backup_%s", timestamp))
	backupArchive := backupDir + ".tar.gz"

	logger.Info("Creating safety backup of current configuration...")
	logger.Debug("Safety backup will be saved to: %s", backupArchive)

	// Create backup archive
	file, err := safetyFS.Create(backupArchive)
	if err != nil {
		return nil, fmt.Errorf("create backup archive: %w", err)
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	result := &SafetyBackupResult{
		BackupPath: backupArchive,
		Timestamp:  safetyNow(),
	}

	// Collect all paths to backup
	pathsToBackup := GetSelectedPaths(selectedCategories)

	for _, catPath := range pathsToBackup {
		// Convert archive path to filesystem path
		fsPath := strings.TrimPrefix(catPath, "./")
		fullPath := filepath.Join(destRoot, fsPath)

		// Check if path exists
		info, err := safetyFS.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				// Path doesn't exist, skip
				continue
			}
			logger.Warning("Cannot stat %s: %v", fullPath, err)
			continue
		}

		// Backup the path
		if info.IsDir() {
			// Backup directory recursively
			err = backupDirectory(tarWriter, fullPath, fsPath, result, logger)
			if err != nil {
				logger.Warning("Failed to backup directory %s: %v", fullPath, err)
			}
		} else {
			// Backup single file
			err = backupFile(tarWriter, fullPath, fsPath, result, logger)
			if err != nil {
				logger.Warning("Failed to backup file %s: %v", fullPath, err)
			}
		}
	}

	logger.Info("Safety backup created: %s (%d files, %.2f MB)",
		backupArchive,
		result.FilesBackedUp,
		float64(result.TotalSize)/(1024*1024))

	// Write backup location to a file for easy reference
	locationFile := filepath.Join(baseDir, "restore_backup_location.txt")
	if err := safetyFS.WriteFile(locationFile, []byte(backupArchive), 0644); err != nil {
		logger.Warning("Could not write backup location file: %v", err)
	} else {
		logger.Info("Backup location saved to: %s", locationFile)
	}

	return result, nil
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
func RestoreSafetyBackup(logger *logging.Logger, backupPath string, destRoot string) error {
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

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destRoot, header.Name)

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
			// Remove existing file/symlink
			safetyFS.Remove(target)
			if err := safetyFS.Symlink(header.Linkname, target); err != nil {
				logger.Warning("Cannot create symlink %s: %v", target, err)
			}
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
