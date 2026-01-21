package orchestrator

import "testing"

func TestSanitizeCategoriesForClusterRecovery_RemovesEtcPVEPaths(t *testing.T) {
	categories := []Category{
		{
			ID:    "pve_jobs",
			Name:  "PVE Backup Jobs",
			Paths: []string{"./etc/pve/jobs.cfg", "./etc/pve/vzdump.cron"},
		},
		{
			ID:    "storage_pve",
			Name:  "PVE Storage Configuration",
			Paths: []string{"./etc/vzdump.conf"},
		},
		{
			ID:   "mixed",
			Name: "Mixed",
			Paths: []string{
				"./etc/pve/some.cfg",
				"./etc/other.cfg",
				"etc/pve/legacy.conf",
				"/etc/pve/abs.conf",
				"./etc/pve2/keep.conf",
			},
		},
	}

	sanitized, removed := sanitizeCategoriesForClusterRecovery(categories)

	if len(removed["pve_jobs"]) != 2 {
		t.Fatalf("expected 2 removed paths for pve_jobs, got %d", len(removed["pve_jobs"]))
	}
	if len(removed["mixed"]) != 3 {
		t.Fatalf("expected 3 removed paths for mixed, got %d", len(removed["mixed"]))
	}
	if _, ok := removed["storage_pve"]; ok {
		t.Fatalf("did not expect storage_pve to have removed paths")
	}

	if len(sanitized) != 2 {
		t.Fatalf("expected 2 categories after sanitization, got %d", len(sanitized))
	}
	if sanitized[0].ID != "storage_pve" {
		t.Fatalf("expected storage_pve first, got %s", sanitized[0].ID)
	}
	if sanitized[1].ID != "mixed" {
		t.Fatalf("expected mixed second, got %s", sanitized[1].ID)
	}

	gotPaths := sanitized[1].Paths
	if len(gotPaths) != 2 {
		t.Fatalf("expected 2 kept paths for mixed, got %d (%#v)", len(gotPaths), gotPaths)
	}
	if gotPaths[0] != "./etc/other.cfg" || gotPaths[1] != "./etc/pve2/keep.conf" {
		t.Fatalf("unexpected kept paths: %#v", gotPaths)
	}
}
