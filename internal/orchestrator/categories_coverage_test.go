package orchestrator

import "testing"

// TestOrphanPathsNowCovered locks the #66/#67 coverage: previously-orphaned
// collected files now match a system-writable (non-export-only) category.
func TestOrphanPathsNowCovered(t *testing.T) {
	all := GetAllCategories()
	cases := map[string]string{
		"./etc/passwd":              "accounts",
		"./etc/group":               "accounts",
		"./etc/shadow":              "accounts",
		"./etc/gshadow":             "accounts",
		"./etc/sudoers":             "accounts",
		"./etc/cron.daily/logrot":   "crontabs",
		"./etc/cron.hourly/x":       "crontabs",
		"./etc/cron.weekly/x":       "crontabs",
		"./etc/cron.monthly/x":      "crontabs",
		"./etc/keys/luks.key":       "storage_stack",
		"./etc/luks-keys/disk":      "storage_stack",
		"./etc/cryptsetup-keys.d/k": "storage_stack",
	}
	for p, wantID := range cases {
		cat := GetCategoryByID(wantID, all)
		if cat == nil {
			t.Fatalf("category %q not found", wantID)
		}
		if !PathMatchesCategory(p, *cat) {
			t.Errorf("path %q should match category %q", p, wantID)
		}
		if cat.ExportOnly {
			t.Errorf("category %q for %q must be system-writable (not ExportOnly)", wantID, p)
		}
	}
}

// TestAccountsCategoryClassification verifies the sensitive accounts category is
// staged (safe merge apply), not export-only, and confined to Full/Custom modes.
func TestAccountsCategoryClassification(t *testing.T) {
	all := GetAllCategories()
	cat := GetCategoryByID("accounts", all)
	if cat == nil {
		t.Fatal("accounts category missing")
	}
	if cat.ExportOnly {
		t.Error("accounts must not be ExportOnly")
	}
	if !isStagedCategoryID("accounts") {
		t.Error("accounts must be staged so it applies via the safe merge step")
	}
	for _, c := range GetBaseModeCategories() {
		if c.ID == "accounts" {
			t.Error("accounts must NOT be in Base mode")
		}
	}
	for _, c := range GetStorageModeCategories("dual") {
		if c.ID == "accounts" {
			t.Error("accounts must NOT be in Storage mode")
		}
	}
}
