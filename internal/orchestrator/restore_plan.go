package orchestrator

import "github.com/tis24dev/proxmox-backup/internal/backup"

// RestorePlan contains a pure, side-effect-free description of a restore run.
type RestorePlan struct {
	Mode                RestoreMode
	SystemType          SystemType
	NormalCategories    []Category
	ExportCategories    []Category
	ClusterSafeMode     bool
	NeedsClusterRestore bool
	NeedsPBSServices    bool
}

// PlanRestore computes the restore plan without performing any I/O or prompts.
func PlanRestore(
	manifest *backup.Manifest,
	selectedCategories []Category,
	systemType SystemType,
	mode RestoreMode,
) *RestorePlan {
	normal, export := splitExportCategories(selectedCategories)

	plan := &RestorePlan{
		Mode:             mode,
		SystemType:       systemType,
		NormalCategories: normal,
		ExportCategories: export,
	}

	plan.NeedsClusterRestore = systemType == SystemTypePVE && hasCategoryID(normal, "pve_cluster")
	plan.NeedsPBSServices = systemType == SystemTypePBS && shouldStopPBSServices(normal)

	applyClusterSafety(plan)

	return plan
}

// ApplyClusterSafeMode toggles SAFE cluster handling and recomputes derived fields.
func (p *RestorePlan) ApplyClusterSafeMode(enable bool) {
	if p == nil {
		return
	}
	p.ClusterSafeMode = enable
	applyClusterSafety(p)
}

func applyClusterSafety(plan *RestorePlan) {
	if plan == nil {
		return
	}

	// Rebuild from current selections to allow toggling both ways.
	all := append([]Category{}, plan.NormalCategories...)
	all = append(all, plan.ExportCategories...)
	normal, export := splitExportCategories(all)
	if plan.ClusterSafeMode {
		normal, export = redirectClusterCategoryToExport(normal, export)
	}
	plan.NormalCategories = normal
	plan.ExportCategories = export
	plan.NeedsClusterRestore = plan.SystemType == SystemTypePVE && hasCategoryID(plan.NormalCategories, "pve_cluster")
	plan.NeedsPBSServices = plan.SystemType == SystemTypePBS && shouldStopPBSServices(plan.NormalCategories)
}
