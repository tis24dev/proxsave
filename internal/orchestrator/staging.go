package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

func isStagedCategoryID(id string) bool {
	switch strings.TrimSpace(id) {
	case "network",
		"datastore_pbs",
		"pbs_jobs",
		"pbs_remotes",
		"pbs_host",
		"pbs_tape",
		"storage_pve",
		"pve_jobs",
		"pve_notifications",
		"pbs_notifications",
		"pve_access_control",
		"pbs_access_control",
		"pve_firewall",
		"pve_ha",
		"pve_sdn":
		return true
	default:
		return false
	}
}

func splitRestoreCategories(categories []Category) (normal []Category, staged []Category, export []Category) {
	for _, cat := range categories {
		if cat.ExportOnly {
			export = append(export, cat)
			continue
		}
		if isStagedCategoryID(cat.ID) {
			staged = append(staged, cat)
			continue
		}
		normal = append(normal, cat)
	}
	return normal, staged, export
}

func createRestoreStageDir() (string, error) {
	base := "/tmp/proxsave"
	if err := restoreFS.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("ensure staging base directory %s: %w", base, err)
	}

	pattern := fmt.Sprintf("restore-stage-%s_pid%d-", nowRestore().Format("20060102-150405"), os.Getpid())
	dir, err := restoreFS.MkdirTemp(base, pattern)
	if err != nil {
		return "", fmt.Errorf("create staging directory under %s: %w", base, err)
	}
	return dir, nil
}

func preserveRestoreStagingFromEnv() bool {
	v := strings.TrimSpace(os.Getenv("PROXSAVE_PRESERVE_RESTORE_STAGING"))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func cleanupOldRestoreStageDirs(fs FS, logger *logging.Logger, now time.Time, maxAge time.Duration) (removed int, failed int) {
	base := "/tmp/proxsave"
	entries, err := fs.ReadDir(base)
	if err != nil {
		return 0, 0
	}

	cutoff := now.Add(-maxAge)
	for _, entry := range entries {
		if entry == nil || !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || !strings.HasPrefix(name, "restore-stage-") {
			continue
		}
		fullPath := filepath.Join(base, name)
		info, err := fs.Stat(fullPath)
		if err != nil || info == nil || !info.IsDir() {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}

		if err := fs.RemoveAll(fullPath); err != nil {
			failed++
			if logger != nil {
				logger.Debug("Failed to cleanup restore staging directory %s: %v", fullPath, err)
			}
			continue
		}
		removed++
		if logger != nil {
			logger.Debug("Cleaned old restore staging directory: %s", fullPath)
		}
	}

	return removed, failed
}
