// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

const restoreTempPattern = ".proxsave-tmp-*"

// extractTarEntry extracts a single TAR entry, preserving all attributes including atime/ctime
func extractTarEntry(tarReader *tar.Reader, header *tar.Header, destRoot string, logger *logging.Logger) error {
	target, cleanDestRoot, err := sanitizeRestoreEntryTargetWithFS(restoreFS, destRoot, header.Name)
	if err != nil {
		return err
	}

	skip, err := shouldSkipRestoreEntryTarget(header, target, cleanDestRoot, logger)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}

	// Create parent directories
	if err := restoreFS.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	return extractTypedTarEntry(tarReader, header, target, cleanDestRoot, logger)
}

func shouldSkipRestoreEntryTarget(header *tar.Header, target, cleanDestRoot string, logger *logging.Logger) (bool, error) {
	if cleanDestRoot != string(os.PathSeparator) {
		return false, nil
	}
	// Hard guard: never write directly into /etc/pve when restoring to system root
	if target == "/etc/pve" || strings.HasPrefix(target, "/etc/pve/") {
		logger.Warning("Skipping restore to %s (writes to /etc/pve are prohibited)", target)
		return true, nil
	}
	relTarget, err := filepath.Rel(cleanDestRoot, target)
	if err != nil {
		return false, fmt.Errorf("determine restore target for %s: %w", header.Name, err)
	}
	if skip, reason := shouldSkipProxmoxSystemRestore(relTarget); skip {
		logger.Warning("Skipping restore to %s (%s)", target, reason)
		return true, nil
	}
	return false, nil
}

func extractTypedTarEntry(tarReader *tar.Reader, header *tar.Header, target, cleanDestRoot string, logger *logging.Logger) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return extractDirectory(target, header, logger)
	case tar.TypeReg:
		return extractRegularFile(tarReader, target, header, logger)
	case tar.TypeSymlink:
		return extractSymlink(target, header, cleanDestRoot, logger)
	case tar.TypeLink:
		return extractHardlink(target, header, cleanDestRoot)
	default:
		logger.Debug("Skipping unsupported file type %d: %s", header.Typeflag, header.Name)
		return nil
	}
}

// extractDirectory creates a directory with proper permissions and timestamps
func extractDirectory(target string, header *tar.Header, logger *logging.Logger) (retErr error) {
	// Create with an owner-accessible mode first so the directory can be opened
	// before applying restrictive archive permissions.
	if err := restoreFS.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	dirFile, err := restoreFS.Open(target)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	defer func() {
		if dirFile == nil {
			return
		}
		if err := dirFile.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close directory: %w", err)
		}
	}()

	// Apply metadata on the opened directory handle so logical FS paths
	// (e.g. FakeFS-backed test roots) do not leak through to host paths.
	// Ownership remains best-effort to match the previous restore behavior on
	// unprivileged runs and filesystems that do not support chown.
	if err := atomicFileChown(dirFile, header.Uid, header.Gid); err != nil {
		logger.Debug("Failed to chown directory %s: %v", target, err)
	}
	if err := atomicFileChmod(dirFile, os.FileMode(header.Mode)); err != nil {
		return fmt.Errorf("chmod directory: %w", err)
	}

	// Set timestamps (mtime, atime)
	if err := setTimestamps(target, header); err != nil {
		logger.Debug("Failed to set timestamps on directory %s: %v", target, err)
	}

	return nil
}

// extractRegularFile extracts a regular file with content and timestamps
func extractRegularFile(tarReader *tar.Reader, target string, header *tar.Header, logger *logging.Logger) (retErr error) {
	tmpPath := ""
	var outFile *os.File
	appendDeferredErr := func(prefix string, err error) {
		if err == nil {
			return
		}
		wrapped := fmt.Errorf("%s: %w", prefix, err)
		if retErr == nil {
			retErr = wrapped
			return
		}
		retErr = errors.Join(retErr, wrapped)
	}
	closeOutFile := func() error {
		if outFile == nil {
			return nil
		}
		err := outFile.Close()
		outFile = nil
		return err
	}

	// Write to a sibling temp file first so a truncated archive entry cannot clobber
	// an existing target before the content is fully copied and closed.
	outFile, err := restoreFS.CreateTemp(filepath.Dir(target), restoreTempPattern)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	tmpPath = outFile.Name()
	defer func() {
		appendDeferredErr("close file", closeOutFile())
		if tmpPath != "" {
			if err := restoreFS.Remove(tmpPath); err != nil && logger != nil {
				logger.Debug("Failed to remove temp file %s: %v", tmpPath, err)
			}
		}
	}()

	// Copy content
	if _, err := io.Copy(outFile, tarReader); err != nil {
		return fmt.Errorf("write file content: %w", err)
	}

	// Set metadata on the temp file before replacing the target so failures do not
	// leave the final path in a partially restored state.
	// Ownership remains best-effort to match the previous restore behavior on
	// unprivileged runs and filesystems that do not support chown.
	if err := atomicFileChown(outFile, header.Uid, header.Gid); err != nil {
		logger.Debug("Failed to chown file %s: %v", target, err)
	}
	if err := atomicFileChmod(outFile, os.FileMode(header.Mode)); err != nil {
		return fmt.Errorf("chmod file: %w", err)
	}

	// Close before renaming into place.
	if err := closeOutFile(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}

	if err := restoreFS.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	tmpPath = ""

	// Set timestamps (mtime, atime, ctime via syscall)
	if err := setTimestamps(target, header); err != nil {
		logger.Debug("Failed to set timestamps on file %s: %v", target, err)
	}

	return nil
}

// extractSymlink creates a symbolic link
func extractSymlink(target string, header *tar.Header, destRoot string, logger *logging.Logger) error {
	linkTarget := header.Linkname

	// Pre-validation: ensure the symlink target resolves within destRoot before creation.
	if _, err := resolvePathRelativeToBaseWithinRootFS(restoreFS, destRoot, filepath.Dir(target), linkTarget); err != nil {
		return fmt.Errorf("symlink target escapes root before creation: %s -> %s: %w", header.Name, linkTarget, err)
	}

	// Remove existing file/link if it exists
	_ = restoreFS.Remove(target)

	// Create symlink
	if err := restoreFS.Symlink(linkTarget, target); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}

	// POST-CREATION VALIDATION: Verify the created symlink's target stays within destRoot
	actualTarget, err := restoreFS.Readlink(target)
	if err != nil {
		restoreFS.Remove(target) // Clean up
		return fmt.Errorf("read created symlink %s: %w", target, err)
	}

	if _, err := resolvePathRelativeToBaseWithinRootFS(restoreFS, destRoot, filepath.Dir(target), actualTarget); err != nil {
		restoreFS.Remove(target)
		return fmt.Errorf("symlink target escapes root after creation: %s -> %s: %w", header.Name, actualTarget, err)
	}

	// Set ownership (on the symlink itself, not the target)
	if err := os.Lchown(target, header.Uid, header.Gid); err != nil {
		logger.Debug("Failed to lchown symlink %s: %v", target, err)
	}

	// Note: timestamps on symlinks are not typically preserved
	return nil
}

// extractHardlink creates a hard link
func extractHardlink(target string, header *tar.Header, destRoot string) error {
	// Validate hard link target
	linkName := filepath.FromSlash(header.Linkname)
	if linkName == "" || filepath.Clean(linkName) == "." {
		return fmt.Errorf("empty hardlink target not allowed")
	}

	// Reject absolute hard link targets immediately
	if filepath.IsAbs(linkName) {
		return fmt.Errorf("absolute hardlink target not allowed: %s", linkName)
	}

	// Resolve and validate the hard link target stays within extraction root.
	linkTarget, err := resolvePathWithinRootFS(restoreFS, destRoot, linkName)
	if err != nil {
		return fmt.Errorf("hardlink target escapes root: %s -> %s: %w", header.Name, linkName, err)
	}

	// Remove existing file/link if it exists
	_ = restoreFS.Remove(target)

	// Create hard link
	if err := restoreFS.Link(linkTarget, target); err != nil {
		return fmt.Errorf("create hardlink: %w", err)
	}

	return nil
}

// setTimestamps sets atime, mtime, and attempts to set ctime via syscall
func setTimestamps(target string, header *tar.Header) error {
	// Convert times to Unix format
	atime := header.AccessTime
	mtime := header.ModTime

	// Use syscall.UtimesNano to set atime and mtime with nanosecond precision
	times := []syscall.Timespec{
		{Sec: atime.Unix(), Nsec: int64(atime.Nanosecond())},
		{Sec: mtime.Unix(), Nsec: int64(mtime.Nanosecond())},
	}

	if err := syscall.UtimesNano(target, times); err != nil {
		return fmt.Errorf("set atime/mtime: %w", err)
	}

	// Note: ctime (change time) cannot be set directly by user-space programs
	// It is automatically updated by the kernel when file metadata changes
	// The header.ChangeTime is stored in PAX but cannot be restored

	return nil
}
