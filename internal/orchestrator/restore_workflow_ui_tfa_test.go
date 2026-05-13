package orchestrator

import (
	"context"
	"testing"
)

func TestMaybeAddRecommendedCategoriesForTFA_AddsNetworkAndSSLWhenConfirmed(t *testing.T) {
	ui := &fakeRestoreWorkflowUI{confirmAction: true}

	available := []Category{
		{ID: "pve_access_control", Name: "PVE Access Control", IsAvailable: true},
		{ID: "network", Name: "Network", IsAvailable: true},
		{ID: "ssl", Name: "SSL", IsAvailable: true},
	}
	selected := []Category{
		{ID: "pve_access_control", Name: "PVE Access Control", IsAvailable: true},
	}

	got, err := maybeAddRecommendedCategoriesForTFA(context.Background(), ui, newTestLogger(), selected, available)
	if err != nil {
		t.Fatalf("maybeAddRecommendedCategoriesForTFA error: %v", err)
	}
	if !hasCategoryID(got, "network") {
		t.Fatalf("expected network category to be added, got=%v", got)
	}
	if !hasCategoryID(got, "ssl") {
		t.Fatalf("expected ssl category to be added, got=%v", got)
	}
}

func TestMaybeAddRecommendedCategoriesForTFA_DoesNotAddWhenDeclined(t *testing.T) {
	ui := &fakeRestoreWorkflowUI{confirmAction: false}

	available := []Category{
		{ID: "pbs_access_control", Name: "PBS Access Control", IsAvailable: true},
		{ID: "network", Name: "Network", IsAvailable: true},
		{ID: "ssl", Name: "SSL", IsAvailable: true},
	}
	selected := []Category{
		{ID: "pbs_access_control", Name: "PBS Access Control", IsAvailable: true},
	}

	got, err := maybeAddRecommendedCategoriesForTFA(context.Background(), ui, newTestLogger(), selected, available)
	if err != nil {
		t.Fatalf("maybeAddRecommendedCategoriesForTFA error: %v", err)
	}
	if hasCategoryID(got, "network") || hasCategoryID(got, "ssl") {
		t.Fatalf("expected no categories to be added, got=%v", got)
	}
}

func TestMaybeAddRecommendedCategoriesForTFA_NilLoggerStillPrompts(t *testing.T) {
	tests := []struct {
		name          string
		confirmAction bool
		wantAdded     bool
	}{
		{name: "confirmed", confirmAction: true, wantAdded: true},
		{name: "declined", confirmAction: false, wantAdded: false},
	}

	available := []Category{
		{ID: "pve_access_control", Name: "PVE Access Control", IsAvailable: true},
		{ID: "network", Name: "Network", IsAvailable: true},
		{ID: "ssl", Name: "SSL", IsAvailable: true},
	}
	selected := []Category{
		{ID: "pve_access_control", Name: "PVE Access Control", IsAvailable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ui := &fakeRestoreWorkflowUI{confirmAction: tt.confirmAction}

			got, err := maybeAddRecommendedCategoriesForTFA(context.Background(), ui, nil, selected, available)
			if err != nil {
				t.Fatalf("maybeAddRecommendedCategoriesForTFA error: %v", err)
			}

			gotAdded := hasCategoryID(got, "network") && hasCategoryID(got, "ssl")
			if gotAdded != tt.wantAdded {
				t.Fatalf("got added=%v; want %v; categories=%v", gotAdded, tt.wantAdded, got)
			}
		})
	}
}
