package orchestrator

import "testing"

func TestFilterAndSortCategoriesForSystem_PVE(t *testing.T) {
	cats := []Category{
		{Name: "Common B", Type: CategoryTypeCommon},
		{Name: "Zeta PVE", Type: CategoryTypePVE},
		{Name: "Alpha PVE", Type: CategoryTypePVE},
		{Name: "PBS Only", Type: CategoryTypePBS},
	}

	got := filterAndSortCategoriesForSystem(cats, SystemTypePVE)
	if len(got) != 3 {
		t.Fatalf("expected 3 categories for PVE, got %d", len(got))
	}
	if got[0].Name != "Alpha PVE" || got[1].Name != "Zeta PVE" || got[2].Type != CategoryTypeCommon {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func TestFilterAndSortCategoriesForSystem_PBS(t *testing.T) {
	cats := []Category{
		{Name: "Common A", Type: CategoryTypeCommon},
		{Name: "Zeta PBS", Type: CategoryTypePBS},
		{Name: "Beta PBS", Type: CategoryTypePBS},
		{Name: "PVE Only", Type: CategoryTypePVE},
	}

	got := filterAndSortCategoriesForSystem(cats, SystemTypePBS)
	if len(got) != 3 {
		t.Fatalf("expected 3 categories for PBS, got %d", len(got))
	}
	if got[0].Name != "Beta PBS" || got[1].Name != "Zeta PBS" || got[2].Type != CategoryTypeCommon {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func TestFilterAndSortCategoriesForSystem_CommonAfterSpecific(t *testing.T) {
	cats := []Category{
		{Name: "Storage", Type: CategoryTypePVE},
		{Name: "General", Type: CategoryTypeCommon},
	}

	got := filterAndSortCategoriesForSystem(cats, SystemTypePVE)
	if len(got) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(got))
	}
	if got[0].Type != CategoryTypePVE || got[1].Type != CategoryTypeCommon {
		t.Fatalf("unexpected ordering when common follows specific: %+v", got)
	}
}
