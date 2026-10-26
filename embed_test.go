package proxmoxbackup

import (
	"slices"
	"strings"
	"testing"
)

// The embedded docs come back in name order.
func TestCollectEmbeddedDocsReturnsNameOrder(t *testing.T) {
	assets, err := collectEmbeddedDocs()
	if err != nil {
		t.Fatalf("collectEmbeddedDocs: %v", err)
	}
	if len(assets) < 2 {
		t.Fatalf("want at least 2 embedded docs to compare, got %d", len(assets))
	}
	if !slices.IsSortedFunc(assets, func(a, b DocAsset) int { return strings.Compare(a.Name, b.Name) }) {
		names := make([]string, 0, len(assets))
		for _, a := range assets {
			names = append(names, a.Name)
		}
		t.Fatalf("embedded docs not in name order: %v", names)
	}
}
