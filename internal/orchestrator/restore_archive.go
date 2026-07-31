// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/tis24dev/proxsave/internal/logging"
)

var restoreLogSequence uint64

func shouldRecreateDirectories(systemType SystemType, categories []Category) bool {
	return (systemType.SupportsPVE() && hasCategoryID(categories, "storage_pve")) ||
		(systemType.SupportsPBS() && hasCategoryID(categories, "datastore_pbs"))
}

func hasCategoryID(categories []Category, id string) bool {
	for _, cat := range categories {
		if cat.ID == id {
			return true
		}
	}
	return false
}

// shouldStopPBSServices reports whether any selected categories belong to PBS-specific configuration
// and therefore require stopping PBS services before restore.
func shouldStopPBSServices(categories []Category) bool {
	for _, cat := range categories {
		if cat.Type == CategoryTypePBS {
			return true
		}
		// Some common categories (e.g. SSL) include PBS paths that require restarting PBS services.
		for _, p := range cat.Paths {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "./etc/proxmox-backup/") || strings.HasPrefix(p, "./var/lib/proxmox-backup/") {
				return true
			}
		}
	}
	return false
}

func splitExportCategories(categories []Category) (normal []Category, export []Category) {
	for _, cat := range categories {
		if cat.ExportOnly {
			export = append(export, cat)
			continue
		}
		normal = append(normal, cat)
	}
	return normal, export
}

// redirectClusterCategoryToExport removes pve_cluster from normal categories and adds it to export-only list.
func redirectClusterCategoryToExport(normal []Category, export []Category) ([]Category, []Category) {
	filtered := make([]Category, 0, len(normal))
	for _, cat := range normal {
		if cat.ID == "pve_cluster" {
			export = append(export, cat)
			continue
		}
		filtered = append(filtered, cat)
	}
	return filtered, export
}

func exportDestRoot(baseDir string) string {
	base := strings.TrimSpace(baseDir)
	if base == "" {
		base = "/opt/proxsave"
	}
	return filepath.Join(base, fmt.Sprintf("proxmox-config-export-%s", nowRestore().Format("20060102-150405")))
}

func extractPlainArchive(ctx context.Context, archivePath, destRoot string, logger *logging.Logger, skipFn func(entryName string) bool) error {
	if err := restoreFS.MkdirAll(destRoot, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	// Only enforce root privileges when writing to the real system root.
	if destRoot == "/" && isRealRestoreFS(restoreFS) && os.Geteuid() != 0 {
		return fmt.Errorf("restore to %s requires root privileges", destRoot)
	}

	logger.Info("Extracting archive %s into %s", filepath.Base(archivePath), destRoot)

	// Use native Go extraction to preserve atime/ctime from PAX headers
	if err := extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    destRoot,
		logger:      logger,
		mode:        RestoreModeFull,
		skipFn:      skipFn,
	}); err != nil {
		return fmt.Errorf("archive extraction failed: %w", err)
	}

	return nil
}

// extractSelectiveArchive extracts only files matching selected categories
// (best-effort: per-entry extraction failures are logged but do not fail the call).
func extractSelectiveArchive(ctx context.Context, archivePath, destRoot string, categories []Category, mode RestoreMode, logger *logging.Logger) (logPath string, err error) {
	return extractSelectiveArchiveStrict(ctx, archivePath, destRoot, categories, mode, logger, false)
}

// extractSelectiveArchiveStrict is extractSelectiveArchive with control over
// whether a partial extraction (one or more entries failed) is reported as an
// error. The staged restore path passes failOnPartial=true so an incomplete
// stage is never applied to the live system; best-effort callers pass false.
func extractSelectiveArchiveStrict(ctx context.Context, archivePath, destRoot string, categories []Category, mode RestoreMode, logger *logging.Logger, failOnPartial bool) (logPath string, err error) {
	done := logging.DebugStart(logger, "extract selective archive", "archive=%s dest=%s categories=%d mode=%s", archivePath, destRoot, len(categories), mode)
	defer func() { done(err) }()
	if err := restoreFS.MkdirAll(destRoot, 0o755); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}

	// Only enforce root privileges when writing to the real system root.
	if destRoot == "/" && isRealRestoreFS(restoreFS) && os.Geteuid() != 0 {
		return "", fmt.Errorf("restore to %s requires root privileges", destRoot)
	}

	// Create detailed log directory
	logDir := "/tmp/proxsave"
	if err := restoreFS.MkdirAll(logDir, 0o700); err != nil {
		logger.Warning("Could not create log directory: %v", err)
	}

	// Create detailed log file
	timestamp := nowRestore().Format("20060102_150405")
	logSeq := atomic.AddUint64(&restoreLogSequence, 1)
	logPath = filepath.Join(logDir, fmt.Sprintf("restore_%s_%d.log", timestamp, logSeq))
	logFile, err := restoreFS.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		logger.Warning("Could not create detailed log file: %v", err)
		logFile = nil
	} else {
		defer func() {
			if closeErr := logFile.Close(); closeErr != nil {
				logger.Warning("close detailed restore log: %v", closeErr)
			}
		}()
		logger.Info("Detailed restore log: %s", logPath)
		logging.DebugStep(logger, "extract selective archive", "log file=%s", logPath)
	}

	logger.Info("Extracting selected categories from archive %s into %s", filepath.Base(archivePath), destRoot)

	// Use native Go extraction with category filter
	if err := extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath:             archivePath,
		destRoot:                destRoot,
		logger:                  logger,
		categories:              categories,
		mode:                    mode,
		logFile:                 logFile,
		logFilePath:             logPath,
		failOnPartialExtraction: failOnPartial,
	}); err != nil {
		return logPath, err
	}

	return logPath, nil
}

func isRealRestoreFS(fs FS) bool {
	switch fs.(type) {
	case osFS, *osFS:
		return true
	default:
		return false
	}
}

// getModeName returns a human-readable name for the restore mode
func getModeName(mode RestoreMode) string {
	switch mode {
	case RestoreModeFull:
		return "FULL restore (all files)"
	case RestoreModeStorage:
		return "STORAGE/DATASTORE only"
	case RestoreModeBase:
		return "SYSTEM BASE only"
	case RestoreModeCustom:
		return "CUSTOM selection"
	default:
		return "Unknown mode"
	}
}
