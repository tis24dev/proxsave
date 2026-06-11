// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
)

type restoreArchiveOptions struct {
	archivePath string
	destRoot    string
	logger      *logging.Logger
	categories  []Category
	mode        RestoreMode
	logFile     *os.File
	logFilePath string
	skipFn      func(entryName string) bool
	// failOnPartialExtraction makes extractArchiveNative return an error when one
	// or more entries fail to extract. Best-effort callers leave it false (a
	// partial extraction is reported as a warning); the staged restore path sets
	// it true so an incomplete stage is never applied to the live system (BH-002).
	failOnPartialExtraction bool
}

type restoreExtractionStats struct {
	filesExtracted int
	filesSkipped   int
	filesFailed    int
}

type restoreExtractionLog struct {
	logger       *logging.Logger
	logFile      *os.File
	logFilePath  string
	restoredTemp *os.File
	skippedTemp  *os.File
}

// extractArchiveNative extracts TAR archives natively in Go, preserving all timestamps.
func extractArchiveNative(ctx context.Context, opts restoreArchiveOptions) (err error) {
	file, err := restoreFS.Open(opts.archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer closeIntoErr(&err, file, "close archive")

	reader, err := createDecompressionReader(ctx, file, opts.archivePath)
	if err != nil {
		return fmt.Errorf("create decompression reader: %w", err)
	}
	defer closeDecompressionReader(reader, &err, "close decompression reader")

	extractionLog := newRestoreExtractionLog(opts)
	defer extractionLog.close()
	extractionLog.writeHeader(opts)

	stats, err := processRestoreArchiveEntries(ctx, tar.NewReader(reader), opts, extractionLog)
	if err != nil {
		return err
	}

	extractionLog.writeSummary(stats)
	logRestoreExtractionSummary(opts, stats)

	// Turn deduplicated symlinks back into regular files using the dedup manifest,
	// so selective restore never leaves a dangling link and full restore preserves
	// the original file type (issue #70).
	materializeDedupSymlinks(opts.destRoot, opts.logger)

	// When the caller cannot safely act on a partial result (the staged restore
	// path, which would otherwise apply an incomplete tree of PVE/PBS/network/
	// secret config to the live system), surface per-entry extraction failures as
	// an error instead of silently succeeding (BH-002).
	if opts.failOnPartialExtraction && stats.filesFailed > 0 {
		return fmt.Errorf("incomplete extraction: %d entr(ies) failed to extract (%d extracted); refusing to apply a partial result", stats.filesFailed, stats.filesExtracted)
	}
	return nil
}

func newRestoreExtractionLog(opts restoreArchiveOptions) *restoreExtractionLog {
	extractionLog := &restoreExtractionLog{
		logger:      opts.logger,
		logFile:     opts.logFile,
		logFilePath: opts.logFilePath,
	}
	if opts.logFile == nil {
		return extractionLog
	}

	if tmp, err := restoreFS.CreateTemp("", "restored_entries_*.log"); err == nil {
		extractionLog.restoredTemp = tmp
	} else {
		opts.logger.Warning("Could not create temporary file for restored entries: %v", err)
	}
	if tmp, err := restoreFS.CreateTemp("", "skipped_entries_*.log"); err == nil {
		extractionLog.skippedTemp = tmp
	} else {
		opts.logger.Warning("Could not create temporary file for skipped entries: %v", err)
	}
	return extractionLog
}

func (log *restoreExtractionLog) close() {
	closeAndRemoveRestoreTemp(log.restoredTemp)
	closeAndRemoveRestoreTemp(log.skippedTemp)
}

func closeAndRemoveRestoreTemp(file *os.File) {
	if file == nil {
		return
	}
	_ = file.Close()
	_ = restoreFS.Remove(file.Name())
}

func (log *restoreExtractionLog) writeHeader(opts restoreArchiveOptions) {
	if log.logFile == nil {
		return
	}
	_, _ = fmt.Fprintf(log.logFile, "=== PROXMOX RESTORE LOG ===\n")
	_, _ = fmt.Fprintf(log.logFile, "Date: %s\n", nowRestore().Format("2006-01-02 15:04:05"))
	_, _ = fmt.Fprintf(log.logFile, "Mode: %s\n", getModeName(opts.mode))
	if len(opts.categories) > 0 {
		_, _ = fmt.Fprintf(log.logFile, "Selected categories: %d categories\n", len(opts.categories))
		for _, cat := range opts.categories {
			_, _ = fmt.Fprintf(log.logFile, "  - %s (%s)\n", cat.Name, cat.ID)
		}
	} else {
		_, _ = fmt.Fprintf(log.logFile, "Selected categories: ALL (full restore)\n")
	}
	_, _ = fmt.Fprintf(log.logFile, "Archive: %s\n", filepath.Base(opts.archivePath))
	_, _ = fmt.Fprintf(log.logFile, "\n")
}

func processRestoreArchiveEntries(ctx context.Context, tarReader *tar.Reader, opts restoreArchiveOptions, extractionLog *restoreExtractionLog) (restoreExtractionStats, error) {
	var stats restoreExtractionStats
	selectiveMode := len(opts.categories) > 0
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}

		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("read tar header: %w", err)
		}

		if skipRestoreArchiveEntry(header, opts, selectiveMode, extractionLog, &stats) {
			continue
		}
		if err := extractTarEntry(tarReader, header, opts.destRoot, opts.logger); err != nil {
			opts.logger.Warning("Failed to extract %s: %v", header.Name, err)
			stats.filesFailed++
			continue
		}

		stats.filesExtracted++
		extractionLog.recordRestored(header.Name)
		if stats.filesExtracted%100 == 0 {
			opts.logger.Debug("Extracted %d files...", stats.filesExtracted)
		}
	}
	return stats, nil
}

func isDedupManifestEntry(name string) bool {
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "/")
	return clean == backup.DedupManifestRelPath
}

func skipRestoreArchiveEntry(header *tar.Header, opts restoreArchiveOptions, selectiveMode bool, extractionLog *restoreExtractionLog, stats *restoreExtractionStats) bool {
	// The dedup manifest is always extracted, regardless of selected categories, so
	// the post-extraction pass can materialize deduplicated symlinks (issue #70).
	if isDedupManifestEntry(header.Name) {
		return false
	}
	if opts.skipFn != nil && opts.skipFn(header.Name) {
		stats.filesSkipped++
		extractionLog.recordSkipped(header.Name, "skipped by restore policy")
		return true
	}
	if !selectiveMode || restoreEntryMatchesCategories(header.Name, opts.categories) {
		return false
	}
	stats.filesSkipped++
	extractionLog.recordSkipped(header.Name, "does not match any selected category")
	return true
}

func restoreEntryMatchesCategories(entryName string, categories []Category) bool {
	for _, cat := range categories {
		if PathMatchesCategory(entryName, cat) {
			return true
		}
	}
	return false
}

func (log *restoreExtractionLog) recordSkipped(name, reason string) {
	if log.skippedTemp != nil {
		_, _ = fmt.Fprintf(log.skippedTemp, "SKIPPED: %s (%s)\n", name, reason)
	}
}

func (log *restoreExtractionLog) recordRestored(name string) {
	if log.restoredTemp != nil {
		_, _ = fmt.Fprintf(log.restoredTemp, "RESTORED: %s\n", name)
	}
}

func (log *restoreExtractionLog) writeSummary(stats restoreExtractionStats) {
	if log.logFile == nil {
		return
	}
	_, _ = fmt.Fprintf(log.logFile, "=== FILES RESTORED ===\n")
	log.copyTempEntries(log.restoredTemp, "restored")
	_, _ = fmt.Fprintf(log.logFile, "\n")

	_, _ = fmt.Fprintf(log.logFile, "=== FILES SKIPPED ===\n")
	log.copyTempEntries(log.skippedTemp, "skipped")
	_, _ = fmt.Fprintf(log.logFile, "\n")

	_, _ = fmt.Fprintf(log.logFile, "=== SUMMARY ===\n")
	_, _ = fmt.Fprintf(log.logFile, "Total files extracted: %d\n", stats.filesExtracted)
	_, _ = fmt.Fprintf(log.logFile, "Total files skipped: %d\n", stats.filesSkipped)
	_, _ = fmt.Fprintf(log.logFile, "Total files failed: %d\n", stats.filesFailed)
	_, _ = fmt.Fprintf(log.logFile, "Total files in archive: %d\n", stats.filesExtracted+stats.filesSkipped+stats.filesFailed)
}

func (log *restoreExtractionLog) copyTempEntries(tempFile *os.File, label string) {
	if tempFile == nil {
		return
	}
	if _, err := tempFile.Seek(0, 0); err == nil {
		if _, err := io.Copy(log.logFile, tempFile); err != nil {
			log.logger.Warning("Could not write %s entries to log: %v", label, err)
		}
	}
}

func logRestoreExtractionSummary(opts restoreArchiveOptions, stats restoreExtractionStats) {
	if stats.filesFailed == 0 {
		if len(opts.categories) > 0 {
			opts.logger.Info("Successfully restored all %d configuration files/directories", stats.filesExtracted)
		} else {
			opts.logger.Info("Successfully restored all %d files/directories", stats.filesExtracted)
		}
	} else {
		if opts.logFilePath != "" {
			opts.logger.Warning("Restored %d files/directories; %d item(s) failed (see detailed log)", stats.filesExtracted, stats.filesFailed)
		} else {
			opts.logger.Warning("Restored %d files/directories; %d item(s) failed", stats.filesExtracted, stats.filesFailed)
		}
	}

	if stats.filesSkipped > 0 {
		if opts.logFilePath != "" {
			opts.logger.Info("%d additional archive entries (logs, diagnostics, system defaults) were left unchanged on this system; see detailed log for details", stats.filesSkipped)
		} else {
			opts.logger.Info("%d additional archive entries (logs, diagnostics, system defaults) were left unchanged on this system", stats.filesSkipped)
		}
	}

	if opts.logFilePath != "" {
		opts.logger.Info("Detailed restore log: %s", opts.logFilePath)
	}
}

// materializeDedupSymlinks reads the dedup manifest written at backup time and
// replaces each recorded symlink with a regular copy of its target. This undoes
// the backup-side deduplication so a restored file is a real file (not a symlink)
// and a selectively-restored file is never left as a dangling link (issue #70).
// It is a no-op when no manifest is present (deduplication was off or found no
// duplicates).
func materializeDedupSymlinks(destRoot string, logger *logging.Logger) {
	manifestTarget, cleanDestRoot, err := sanitizeRestoreEntryTargetWithFS(restoreFS, destRoot, backup.DedupManifestRelPath)
	if err != nil {
		return
	}
	data, err := restoreFS.ReadFile(manifestTarget)
	if err != nil {
		return // no dedup manifest: nothing to materialize
	}
	var entries []backup.DedupManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		logger.Warning("Dedup manifest unreadable; skipping symlink materialization: %v", err)
		return
	}

	var materialized, dangling int
	for _, entry := range entries {
		if strings.TrimSpace(entry.Path) == "" {
			continue
		}
		target, _, err := sanitizeRestoreEntryTargetWithFS(restoreFS, destRoot, entry.Path)
		if err != nil {
			continue
		}
		info, err := restoreFS.Lstat(target)
		if err != nil {
			continue // not extracted: its category was not selected
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue // already a regular file
		}
		linkTarget, err := restoreFS.Readlink(target)
		if err != nil {
			continue
		}
		resolved, err := resolvePathRelativeToBaseWithinRootFS(restoreFS, cleanDestRoot, filepath.Dir(target), linkTarget)
		if err != nil {
			continue
		}
		content, err := restoreFS.ReadFile(resolved)
		if err != nil {
			// Dangling: the dedup target's file was not restored (its category was
			// not selected). Remove the broken link rather than leaving it.
			_ = restoreFS.Remove(target)
			logger.Warning("Dedup: %s could not be restored as a regular file (its content lives in %s, whose category was not selected); removed dangling link", entry.Path, linkTarget)
			dangling++
			continue
		}
		mode := os.FileMode(entry.Mode).Perm()
		if mode == 0 {
			mode = 0o600
		}
		if err := restoreFS.Remove(target); err != nil {
			logger.Warning("Dedup: failed to replace symlink %s: %v", entry.Path, err)
			continue
		}
		if err := restoreFS.WriteFile(target, content, mode); err != nil {
			logger.Warning("Dedup: failed to materialize %s: %v", entry.Path, err)
			continue
		}
		materialized++
	}

	// Drop the manifest so it does not linger on the restored system.
	_ = restoreFS.Remove(manifestTarget)

	if materialized > 0 || dangling > 0 {
		logger.Info("Dedup: materialized %d deduplicated file(s) as regular files, removed %d dangling link(s)", materialized, dangling)
	}
}
