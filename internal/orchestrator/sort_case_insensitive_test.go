package orchestrator

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/notify"
)

// 6a52d9e's whole point (now b2087e2's history): a label starting lowercase must not
// sort below every uppercase label, because refreshLogIssuesFromFile keeps only the
// first ten and the byte order was cutting real warnings out of the notification.
// This is the test the fix shipped without: reverting strings.ToLower left every
// package green.
func TestSortLogCategoriesComparesLettersNotBytes(t *testing.T) {
	list := []notify.LogCategory{
		{Label: "Zulu warning", Type: "WARNING", Count: 1},
		{Label: "secondary storage store operation failed", Type: "WARNING", Count: 1},
	}
	sortLogCategories(list)
	// 's' sorts before 'z' as a letter; as a byte, 'Z' (0x5A) beats 's' (0x73).
	if list[0].Label != "secondary storage store operation failed" {
		t.Fatalf("byte order is back: %q sorted above %q, so a lowercase label falls below every uppercase one and the ten-category cut drops it first", list[0].Label, list[1].Label)
	}
}
