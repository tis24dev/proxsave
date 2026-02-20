package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
)

var restoreStageSequence uint64

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

func stageDestRoot() string {
	base := "/tmp/proxsave"
	seq := atomic.AddUint64(&restoreStageSequence, 1)
	return filepath.Join(base, fmt.Sprintf("restore-stage-%s_%d", nowRestore().Format("20060102-150405"), seq))
}
