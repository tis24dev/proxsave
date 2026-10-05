package orchestrator

import (
	"strings"
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

// Within a type, ✗ labels come first, then ⚠ ones (⚠️ too), then the others; then
// count, descending, and label, case-insensitively.
func TestSortLogCategoriesBySymbol(t *testing.T) {
	list := []notify.LogCategory{
		{Label: "apple", Type: "WARNING", Count: 5},
		{Label: "⚠️ Email: sent via fallback", Type: "WARNING", Count: 1},
		{Label: "✗ Cloud Storage (rclone): backup not saved", Type: "WARNING", Count: 1},
		{Label: "⚠ Retention not applied", Type: "WARNING", Count: 2},
		{Label: "Banana", Type: "WARNING", Count: 5},
		{Label: "✗ PBS Storage: backup not saved", Type: "WARNING", Count: 3},
		{Label: "plain error", Type: "ERROR", Count: 1},
		{Label: "✗ Local Storage: backup not accessible", Type: "ERROR", Count: 1},
	}
	sortLogCategories(list)
	var got []string
	for _, c := range list {
		got = append(got, c.Type+" "+c.Label)
	}
	want := []string{
		"ERROR ✗ Local Storage: backup not accessible",
		"ERROR plain error",
		"WARNING ✗ PBS Storage: backup not saved",
		"WARNING ✗ Cloud Storage (rclone): backup not saved",
		"WARNING ⚠ Retention not applied",
		"WARNING ⚠️ Email: sent via fallback",
		"WARNING apple",
		"WARNING Banana",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("order:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
