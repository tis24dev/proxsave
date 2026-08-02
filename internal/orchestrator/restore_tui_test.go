package orchestrator

import (
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
