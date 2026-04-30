// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
func extractArchiveNative(ctx context.Context, opts restoreArchiveOptions) error {
	file, err := restoreFS.Open(opts.archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	reader, err := createDecompressionReader(ctx, file, opts.archivePath)
	if err != nil {
		return fmt.Errorf("create decompression reader: %w", err)
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}

	extractionLog := newRestoreExtractionLog(opts)
	defer extractionLog.close()
	extractionLog.writeHeader(opts)

	stats, err := processRestoreArchiveEntries(ctx, tar.NewReader(reader), opts, extractionLog)
	if err != nil {
		return err
	}

	extractionLog.writeSummary(stats)
	logRestoreExtractionSummary(opts, stats)
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
	file.Close()
	_ = restoreFS.Remove(file.Name())
}

func (log *restoreExtractionLog) writeHeader(opts restoreArchiveOptions) {
	if log.logFile == nil {
		return
	}
	fmt.Fprintf(log.logFile, "=== PROXMOX RESTORE LOG ===\n")
	fmt.Fprintf(log.logFile, "Date: %s\n", nowRestore().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(log.logFile, "Mode: %s\n", getModeName(opts.mode))
	if len(opts.categories) > 0 {
		fmt.Fprintf(log.logFile, "Selected categories: %d categories\n", len(opts.categories))
		for _, cat := range opts.categories {
			fmt.Fprintf(log.logFile, "  - %s (%s)\n", cat.Name, cat.ID)
		}
	} else {
		fmt.Fprintf(log.logFile, "Selected categories: ALL (full restore)\n")
	}
	fmt.Fprintf(log.logFile, "Archive: %s\n", filepath.Base(opts.archivePath))
	fmt.Fprintf(log.logFile, "\n")
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

func skipRestoreArchiveEntry(header *tar.Header, opts restoreArchiveOptions, selectiveMode bool, extractionLog *restoreExtractionLog, stats *restoreExtractionStats) bool {
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
		fmt.Fprintf(log.skippedTemp, "SKIPPED: %s (%s)\n", name, reason)
	}
}

func (log *restoreExtractionLog) recordRestored(name string) {
	if log.restoredTemp != nil {
		fmt.Fprintf(log.restoredTemp, "RESTORED: %s\n", name)
	}
}

func (log *restoreExtractionLog) writeSummary(stats restoreExtractionStats) {
	if log.logFile == nil {
		return
	}
	fmt.Fprintf(log.logFile, "=== FILES RESTORED ===\n")
	log.copyTempEntries(log.restoredTemp, "restored")
	fmt.Fprintf(log.logFile, "\n")

	fmt.Fprintf(log.logFile, "=== FILES SKIPPED ===\n")
	log.copyTempEntries(log.skippedTemp, "skipped")
	fmt.Fprintf(log.logFile, "\n")

	fmt.Fprintf(log.logFile, "=== SUMMARY ===\n")
	fmt.Fprintf(log.logFile, "Total files extracted: %d\n", stats.filesExtracted)
	fmt.Fprintf(log.logFile, "Total files skipped: %d\n", stats.filesSkipped)
	fmt.Fprintf(log.logFile, "Total files in archive: %d\n", stats.filesExtracted+stats.filesSkipped)
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
		opts.logger.Warning("Restored %d files/directories; %d item(s) failed (see detailed log)", stats.filesExtracted, stats.filesFailed)
	}

	if stats.filesSkipped > 0 {
		opts.logger.Info("%d additional archive entries (logs, diagnostics, system defaults) were left unchanged on this system; see detailed log for details", stats.filesSkipped)
	}

	if opts.logFilePath != "" {
		opts.logger.Info("Detailed restore log: %s", opts.logFilePath)
	}
}
