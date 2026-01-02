package orchestrator

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
)

func TestPlanRestoreClusterSafeToggle(t *testing.T) {
	clusterCat := Category{ID: "pve_cluster", Type: CategoryTypePVE}
	storageCat := Category{ID: "storage_pve", Type: CategoryTypePVE}
	manifest := &backup.Manifest{ClusterMode: "cluster"}

	plan := PlanRestore(manifest, []Category{clusterCat, storageCat}, SystemTypePVE, RestoreModeCustom)

	if !plan.NeedsClusterRestore {
		t.Fatalf("expected NeedsClusterRestore true")
	}
	if plan.ClusterSafeMode {
		t.Fatalf("expected ClusterSafeMode default false")
	}

	plan.ApplyClusterSafeMode(true)
	if !plan.ClusterSafeMode {
		t.Fatalf("ClusterSafeMode should be true after apply")
	}
	if hasCategoryID(plan.NormalCategories, "pve_cluster") {
		t.Fatalf("pve_cluster should be moved to export in SAFE mode")
	}
	if !hasCategoryID(plan.ExportCategories, "pve_cluster") {
		t.Fatalf("pve_cluster should be exported in SAFE mode")
	}
	if plan.NeedsClusterRestore {
		t.Fatalf("NeedsClusterRestore should be false in SAFE mode")
	}

	plan.ApplyClusterSafeMode(false)
	if plan.ClusterSafeMode {
		t.Fatalf("ClusterSafeMode should be false after disable")
	}
	if !hasCategoryID(plan.NormalCategories, "pve_cluster") {
		t.Fatalf("pve_cluster should return to normal restore on disable")
	}
	if plan.NeedsClusterRestore == false {
		t.Fatalf("NeedsClusterRestore should be true when SAFE mode disabled")
	}
}

func TestPlanRestorePBSCategories(t *testing.T) {
	pbsCat := Category{ID: "pbs_config", Type: CategoryTypePBS, ExportOnly: true}
	normalCat := Category{ID: "network", Type: CategoryTypeCommon}

	plan := PlanRestore(nil, []Category{pbsCat, normalCat}, SystemTypePBS, RestoreModeCustom)
	if len(plan.ExportCategories) != 1 || !hasCategoryID(plan.ExportCategories, "pbs_config") {
		t.Fatalf("expected pbs_config to be exported, got %+v", plan.ExportCategories)
	}
	if plan.NeedsPBSServices {
		t.Fatalf("expected PBS services not to be required when only exporting")
	}
	if plan.NeedsClusterRestore {
		t.Fatalf("unexpected cluster restore requirement")
	}
}

func TestPlanRestoreKeepsExportCategoriesFromFullSelection(t *testing.T) {
	exportCat := Category{ID: "pve_config_export", ExportOnly: true}
	normalCat := Category{ID: "network"}

	plan := PlanRestore(nil, []Category{normalCat, exportCat}, SystemTypePVE, RestoreModeFull)
	if len(plan.NormalCategories) != 1 || plan.NormalCategories[0].ID != "network" {
		t.Fatalf("expected normal categories to keep network, got %+v", plan.NormalCategories)
	}
	if len(plan.ExportCategories) != 1 || plan.ExportCategories[0].ID != "pve_config_export" {
		t.Fatalf("expected export categories to include pve_config_export, got %+v", plan.ExportCategories)
	}
	if plan.NeedsClusterRestore {
		t.Fatalf("should not require cluster restore without cluster category")
	}
}
