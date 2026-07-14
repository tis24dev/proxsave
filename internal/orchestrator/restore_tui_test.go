package orchestrator

import (
	"strings"
	"testing"
)

func TestFilterAndSortCategoriesForSystem(t *testing.T) {
	categories := []Category{
		{Name: "Common", Type: CategoryTypeCommon},
		{Name: "PBS", Type: CategoryTypePBS},
		{Name: "Alpha", Type: CategoryTypePVE},
		{Name: "Beta", Type: CategoryTypePVE},
	}

	for _, tc := range []struct {
		name       string
		systemType SystemType
		wantNames  []string
	}{
		{name: "pve", systemType: SystemTypePVE, wantNames: []string{"Alpha", "Beta", "Common"}},
		{name: "pbs", systemType: SystemTypePBS, wantNames: []string{"PBS", "Common"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := filterAndSortCategoriesForSystem(categories, tc.systemType)
			if len(got) != len(tc.wantNames) {
				t.Fatalf("unexpected count: %d", len(got))
			}
			for i, want := range tc.wantNames {
				if got[i].Name != want {
					t.Fatalf("position %d: got %q, want %q", i, got[i].Name, want)
				}
			}
		})
	}
}

func TestBuildRestorePlanText(t *testing.T) {
	config := &SelectiveRestoreConfig{
		Mode:       RestoreModeCustom,
		SystemType: SystemTypePVE,
		SelectedCategories: []Category{
			{Name: "Alpha", Description: "First", Paths: []string{"./etc/alpha"}},
			{Name: "Beta", Description: "Second", Paths: []string{"./var/beta"}},
		},
	}

	text := buildRestorePlanText(config)

	if !strings.Contains(text, "CUSTOM selection (2 categories)") {
		t.Fatalf("missing mode line: %s", text)
	}
	if !strings.Contains(text, "System type:  Proxmox Virtual Environment (PVE)") {
		t.Fatalf("missing system type line: %s", text)
	}
	if !strings.Contains(text, "1. Alpha") || !strings.Contains(text, "2. Beta") {
		t.Fatalf("missing category entries: %s", text)
	}
	alphaIndex := strings.Index(text, "/etc/alpha")
	betaIndex := strings.Index(text, "/var/beta")
	if alphaIndex == -1 || betaIndex == -1 {
		t.Fatalf("missing paths: %s", text)
	}
	if alphaIndex > betaIndex {
		t.Fatalf("paths not sorted: %d vs %d", alphaIndex, betaIndex)
	}
	if !strings.Contains(text, "Existing files at these locations will be OVERWRITTEN") {
		t.Fatalf("missing warning text")
	}
}
