package orchestrator

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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

// The restore plan is shown in a non-wrapping Pager, so an over-width line is
// truncated (only reachable via horizontal scroll). Every static plan line must
// fit a conventional 80-column terminal. The TFA/WebAuthn advisory is the tightest
// line; a config that arms it plus short paths must keep every line within 80.
func TestBuildRestorePlanTextLinesFit80Columns(t *testing.T) {
	config := &SelectiveRestoreConfig{
		Mode:       RestoreModeCustom,
		SystemType: SystemTypePVE,
		SelectedCategories: []Category{
			// pve_access_control without network+ssl arms the TFA/WebAuthn advisory.
			{ID: "pve_access_control", Name: "Access control", Description: "Users, roles, TFA", Paths: []string{"./etc/pve/user.cfg"}},
		},
	}

	text := buildRestorePlanText(config)

	if !strings.Contains(text, "TFA/WebAuthn") {
		t.Fatalf("expected the TFA/WebAuthn advisory to be present:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("plan line exceeds 80 columns (%d): %q", w, line)
		}
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
