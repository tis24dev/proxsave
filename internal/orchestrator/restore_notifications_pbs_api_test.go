package orchestrator

import (
	"slices"
	"testing"
)

// The missing names come back sorted, both when nothing was found and when only some
// names are missing.
func TestMissingNameSetSorted(t *testing.T) {
	expected := map[string]struct{}{"zeta": {}, "alpha": {}, "mid": {}, "beta": {}}

	if got, want := missingNameSet(expected, nil), []string{"alpha", "beta", "mid", "zeta"}; !slices.Equal(got, want) {
		t.Fatalf("missingNameSet(expected, nil) = %v, want %v", got, want)
	}
	actual := map[string]struct{}{"beta": {}}
	if got, want := missingNameSet(expected, actual), []string{"alpha", "mid", "zeta"}; !slices.Equal(got, want) {
		t.Fatalf("missingNameSet(expected, actual) = %v, want %v", got, want)
	}
}
