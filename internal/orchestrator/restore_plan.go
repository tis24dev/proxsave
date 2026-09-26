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
	normal, staged, export = redirectOtherProductCategoriesToExport(systemType, normal, staged, export)

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
	normal, staged, export = redirectOtherProductCategoriesToExport(plan.SystemType, normal, staged, export)
	if plan.ClusterSafeMode {
		normal, export = redirectClusterCategoryToExport(normal, export)
	}
	plan.NormalCategories = normal
	plan.StagedCategories = staged
	plan.ExportCategories = export
	plan.NeedsClusterRestore = plan.SystemType.SupportsPVE() && hasCategoryID(plan.NormalCategories, "pve_cluster")
	plan.NeedsPBSServices = plan.SystemType.SupportsPBS() && shouldStopPBSServices(append(append([]Category{}, plan.NormalCategories...), plan.StagedCategories...))
}

// redirectOtherProductCategoriesToExport moves the categories of the product this host
// does not run from the write lists to export. Nothing in export reaches the live
// system: the selective path extracts it to the export directory, the analysis-failure
// fallback skips it.
//
// The role filter used to live only in the CUSTOM category prompt
// (filterAndSortCategoriesForSystem). FULL takes every category in the archive
// (GetCategoriesForMode) and never met it, so a PVE+PBS archive restored in FULL on a
// PBS host wrote pve_cluster, corosync and ceph over the live system, and on a PVE host
// maintenance_pbs; the staged applies of the other product stop by themselves, the
// direct writes did not. That came after a compatibility notice promising only the
// categories compatible with the current system. config.db on a PBS host then made
// the production detection call it dual. The plan is where every mode, and the
// analysis-failure fallback, meet, so the filter is here. Export rather than drop:
// nothing in the archive is lost.
//
// A dual or unknown host keeps every category where splitRestoreCategories put it (see
// otherProductCategoryType). Common categories never move.
func redirectOtherProductCategoriesToExport(systemType SystemType, normal, staged, export []Category) ([]Category, []Category, []Category) {
	otherProduct, ok := otherProductCategoryType(systemType)
	if !ok {
		return normal, staged, export
	}
	keep := func(categories []Category) []Category {
		kept := make([]Category, 0, len(categories))
		for _, cat := range categories {
			if cat.Type == otherProduct {
				export = append(export, cat)
				continue
			}
			kept = append(kept, cat)
		}
		return kept
	}
	normal = keep(normal)
	staged = keep(staged)
	return normal, staged, export
}

// otherProductCategoryType returns the category type of the product this host does not
// run. Only a host known to run one product has another one: a dual host runs both, and
// an unknown host gives nothing to compare against, so both report false.
func otherProductCategoryType(systemType SystemType) (CategoryType, bool) {
	switch systemType {
	case SystemTypePVE:
		return CategoryTypePBS, true
	case SystemTypePBS:
		return CategoryTypePVE, true
	default:
		return "", false
	}
}

func (p *RestorePlan) HasCategoryID(id string) bool {
	if p == nil {
		return false
	}
	return hasCategoryID(p.NormalCategories, id) || hasCategoryID(p.StagedCategories, id) || hasCategoryID(p.ExportCategories, id)
}
