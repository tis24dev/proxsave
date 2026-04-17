package orchestrator

// RestorePlan contains a pure, side-effect-free description of a restore run.
type RestorePlan struct {
	Mode                RestoreMode
	SystemType          SystemType
	NormalCategories    []Category
	StagedCategories    []Category
	ExportCategories    []Category
	PBSRestoreBehavior  PBSRestoreBehavior
	ClusterBackup       bool
	ClusterSafeMode     bool
	NeedsClusterRestore bool
	NeedsPBSServices    bool
}

// PlanRestore computes the restore plan without performing any I/O or prompts.
func PlanRestore(
	clusterBackup bool,
	selectedCategories []Category,
	systemType SystemType,
	mode RestoreMode,
) *RestorePlan {
	normal, staged, export := splitRestoreCategories(selectedCategories)

	plan := &RestorePlan{
		Mode:             mode,
		SystemType:       systemType,
		NormalCategories: normal,
		StagedCategories: staged,
		ExportCategories: export,
		ClusterBackup:    clusterBackup,
	}

	plan.NeedsClusterRestore = systemType.SupportsPVE() && hasCategoryID(normal, "pve_cluster")
	plan.NeedsPBSServices = systemType.SupportsPBS() && shouldStopPBSServices(append(append([]Category{}, normal...), staged...))

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
	all = append(all, plan.StagedCategories...)
	all = append(all, plan.ExportCategories...)
	normal, staged, export := splitRestoreCategories(all)
	if plan.ClusterSafeMode {
		normal, export = redirectClusterCategoryToExport(normal, export)
	}
	plan.NormalCategories = normal
	plan.StagedCategories = staged
	plan.ExportCategories = export
	plan.NeedsClusterRestore = plan.SystemType.SupportsPVE() && hasCategoryID(plan.NormalCategories, "pve_cluster")
	plan.NeedsPBSServices = plan.SystemType.SupportsPBS() && shouldStopPBSServices(append(append([]Category{}, plan.NormalCategories...), plan.StagedCategories...))
}

func (p *RestorePlan) HasCategoryID(id string) bool {
	if p == nil {
		return false
	}
	return hasCategoryID(p.NormalCategories, id) || hasCategoryID(p.StagedCategories, id) || hasCategoryID(p.ExportCategories, id)
}
