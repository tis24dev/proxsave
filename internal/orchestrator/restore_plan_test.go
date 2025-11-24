package orchestrator

import (
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/backup"
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
	pbsCat := Category{ID: "pbs_config", Type: CategoryTypePBS}
	normalCat := Category{ID: "network", Type: CategoryTypeCommon}

	plan := PlanRestore(nil, []Category{pbsCat, normalCat}, SystemTypePBS, RestoreModeCustom)
	if len(plan.ExportCategories) != 0 {
		t.Fatalf("expected no export categories, got %d", len(plan.ExportCategories))
	}
	if !plan.NeedsPBSServices {
		t.Fatalf("expected PBS services to be required")
	}
	if plan.NeedsClusterRestore {
		t.Fatalf("unexpected cluster restore requirement")
	}
}
