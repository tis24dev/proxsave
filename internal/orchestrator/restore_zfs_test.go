package orchestrator

import (
	"slices"
	"testing"
)

// The configured pool names come back sorted.
func TestSortedPoolNames(t *testing.T) {
	got := sortedPoolNames(map[string]struct{}{"tank": {}, "backup": {}, "rpool": {}})
	if want := []string{"backup", "rpool", "tank"}; !slices.Equal(got, want) {
		t.Fatalf("sortedPoolNames = %v, want %v", got, want)
	}
}
