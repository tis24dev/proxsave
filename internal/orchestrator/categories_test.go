package orchestrator

import "testing"

// TestPVEAccessControlCategoryMatchesRestoreConstants locks the authoritative
// pve_access_control file set. The same list is duplicated as exclusion patterns
// in internal/backup/collector_pve.go (pveACLPrivExcludePatterns) because the
// backup package cannot import this one (import cycle). If this test fails after
// adding/removing a PVE access-control file, update the backup-side exclusion too.
func TestPVEAccessControlCategoryMatchesRestoreConstants(t *testing.T) {
	cat := GetCategoryByID("pve_access_control", GetAllCategories())
	if cat == nil {
		t.Fatal("pve_access_control category not found")
	}

	want := []string{
		"." + pveUserCfgPath,
		"." + pveDomainsCfgPath,
		"." + pveShadowCfgPath,
		"." + pveTokenCfgPath,
		"." + pveTFACfgPath,
	}

	have := make(map[string]bool, len(cat.Paths))
	for _, p := range cat.Paths {
		have[p] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("pve_access_control category missing %q; keep it in sync with restore_access_control.go constants and internal/backup/collector_pve.go pveACLPrivExcludePatterns", w)
		}
	}
	if len(cat.Paths) != len(want) {
		t.Errorf("pve_access_control has %d paths, want %d (%v); a new access-control file must also be added to the backup-side exclusion", len(cat.Paths), len(want), cat.Paths)
	}
}
