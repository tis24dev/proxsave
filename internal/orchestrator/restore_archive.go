// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/tis24dev/proxsave/internal/input"
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

// runFullRestore performs a full restore without selective options (fallback)
func runFullRestore(ctx context.Context, reader *bufio.Reader, candidate *backupCandidate, prepared *preparedBundle, destRoot string, logger *logging.Logger, dryRun bool) error {
	if err := confirmRestoreAction(ctx, reader, candidate, destRoot); err != nil {
		return err
	}

	safeFstabMerge := destRoot == "/" && isRealRestoreFS(restoreFS)
	if safeFstabMerge {
		logger.Warning("Full restore safety: /etc/fstab will not be overwritten; Smart Merge will be applied after extraction.")
	}

	if err := extractPlainArchive(ctx, prepared.ArchivePath, destRoot, logger, fullRestoreSkipFn(safeFstabMerge)); err != nil {
		return err
	}

	if safeFstabMerge {
		if err := runFullRestoreFstabMerge(ctx, reader, prepared.ArchivePath, destRoot, logger, dryRun); err != nil {
			return err
		}
	}

	logger.Info("Restore completed successfully.")
	return nil
}

func fullRestoreSkipFn(safeFstabMerge bool) func(name string) bool {
	return func(name string) bool {
		if !safeFstabMerge {
			return false
		}
		clean := strings.TrimPrefix(strings.TrimSpace(name), "./")
		clean = strings.TrimPrefix(clean, "/")
		return clean == "etc/fstab"
	}
}

func runFullRestoreFstabMerge(ctx context.Context, reader *bufio.Reader, archivePath, destRoot string, logger *logging.Logger, dryRun bool) error {
	logger.Info("")
	fsTempDir, err := restoreFS.MkdirTemp("", "proxsave-fstab-")
	if err != nil {
		logger.Warning("Failed to create temp dir for fstab merge: %v", err)
		return nil
	}
	defer func() {
		if err := restoreFS.RemoveAll(fsTempDir); err != nil {
			logger.Debug("Failed to remove temporary fstab merge directory %s: %v", fsTempDir, err)
		}
	}()

	if err := extractFullRestoreFstab(ctx, archivePath, fsTempDir, logger); err != nil {
		logger.Warning("Failed to extract filesystem config for merge: %v", err)
		return nil
	}
	extractFullRestoreFstabInventory(ctx, archivePath, fsTempDir, logger)
	currentFstab := filepath.Join(destRoot, "etc", "fstab")
	backupFstab := filepath.Join(fsTempDir, "etc", "fstab")
	if err := SmartMergeFstab(ctx, logger, reader, currentFstab, backupFstab, dryRun); err != nil {
		if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
			logger.Info("Restore aborted by user during Smart Filesystem Configuration Merge.")
			return err
		}
		logger.Warning("Smart Fstab Merge failed: %v", err)
	}
	return nil
}

func extractFullRestoreFstab(ctx context.Context, archivePath, fsTempDir string, logger *logging.Logger) error {
	return extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    fsTempDir,
		logger:      logger,
		categories: []Category{{
			ID:    "filesystem",
			Name:  "Filesystem Configuration",
			Paths: []string{"./etc/fstab"},
		}},
		mode: RestoreModeCustom,
	})
}

func extractFullRestoreFstabInventory(ctx context.Context, archivePath, fsTempDir string, logger *logging.Logger) {
	invCategory := []Category{{
		ID:   "fstab_inventory",
		Name: "Fstab inventory (device mapping)",
		Paths: []string{
			"./var/lib/proxsave-info/commands/system/blkid.txt",
			"./var/lib/proxsave-info/commands/system/lsblk_json.json",
			"./var/lib/proxsave-info/commands/system/lsblk.txt",
			"./var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json",
		},
	}}
	if err := extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    fsTempDir,
		logger:      logger,
		categories:  invCategory,
		mode:        RestoreModeCustom,
	}); err != nil {
		logger.Debug("Failed to extract fstab inventory data (continuing): %v", err)
	}
}

func confirmRestoreAction(ctx context.Context, reader *bufio.Reader, cand *backupCandidate, dest string) error {
	manifest := cand.Manifest
	fmt.Println()
	fmt.Printf("Selected backup: %s (%s)\n", cand.DisplayBase, manifest.CreatedAt.Format("2006-01-02 15:04:05"))
	cleanDest := filepath.Clean(strings.TrimSpace(dest))
	if cleanDest == "" || cleanDest == "." {
		cleanDest = string(os.PathSeparator)
	}
	if cleanDest == string(os.PathSeparator) {
		fmt.Println("Restore destination: / (system root; original paths will be preserved)")
		fmt.Println("WARNING: This operation will overwrite configuration files on this system.")
	} else {
		fmt.Printf("Restore destination: %s (original paths will be preserved under this directory)\n", cleanDest)
		fmt.Printf("WARNING: This operation will overwrite existing files under %s.\n", cleanDest)
	}
	fmt.Println("Type RESTORE to proceed or 0 to cancel.")

	for {
		fmt.Print("Confirmation: ")
		line, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return err
		}
		switch strings.TrimSpace(line) {
		case "RESTORE":
			return nil
		case "0":
			return ErrRestoreAborted
		default:
			fmt.Println("Please type RESTORE to confirm or 0 to cancel.")
		}
	}
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
func extractSelectiveArchive(ctx context.Context, archivePath, destRoot string, categories []Category, mode RestoreMode, logger *logging.Logger) (logPath string, err error) {
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
	if err := restoreFS.MkdirAll(logDir, 0o755); err != nil {
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
		defer closeIntoErr(&err, logFile, "close detailed restore log")
		logger.Info("Detailed restore log: %s", logPath)
		logging.DebugStep(logger, "extract selective archive", "log file=%s", logPath)
	}

	logger.Info("Extracting selected categories from archive %s into %s", filepath.Base(archivePath), destRoot)

	// Use native Go extraction with category filter
	if err := extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    destRoot,
		logger:      logger,
		categories:  categories,
		mode:        mode,
		logFile:     logFile,
		logFilePath: logPath,
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
