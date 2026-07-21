package whatsnew

import (
	"strings"
	"testing"
)

// TestLookupNotesPlaceholderInRange: the (0.29.0, 0.30.0] range returns exactly the
// placeholder 0.30.0 entry.
func TestLookupNotesPlaceholderInRange(t *testing.T) {
	got := LookupNotes("0.29.0", "0.30.0")
	if len(got) != 1 {
		t.Fatalf("LookupNotes(0.29.0, 0.30.0) len = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].Version != "0.30.0" {
		t.Fatalf("entry version = %q, want 0.30.0", got[0].Version)
	}
}

// TestLookupNotesEqualBoundExcluded: an equal from excludes the version (open lower
// bound), so (0.30.0, 0.30.0] is empty.
func TestLookupNotesEqualBoundExcluded(t *testing.T) {
	if got := LookupNotes("0.30.0", "0.30.0"); len(got) != 0 {
		t.Fatalf("LookupNotes(0.30.0, 0.30.0) len = %d, want 0 (%+v)", len(got), got)
	}
}

// TestLookupNotesInvertedRange: an inverted / empty range returns an empty slice, never
// an error, never a panic.
func TestLookupNotesInvertedRange(t *testing.T) {
	if got := LookupNotes("0.30.0", "0.29.0"); len(got) != 0 {
		t.Fatalf("LookupNotes(0.30.0, 0.29.0) len = %d, want 0 (%+v)", len(got), got)
	}
}

// TestLookupNotesUnparseableBound: an unparseable bound fails safe with an empty slice.
func TestLookupNotesUnparseableBound(t *testing.T) {
	if got := LookupNotes("not-a-version", "0.30.0"); len(got) != 0 {
		t.Fatalf("LookupNotes(bad, 0.30.0) len = %d, want 0", len(got))
	}
	if got := LookupNotes("0.29.0", "also-bad"); len(got) != 0 {
		t.Fatalf("LookupNotes(0.29.0, bad) len = %d, want 0", len(got))
	}
}

// TestLookupNotesMultiEntryOrdering: with a temporary synthetic second entry the results
// sort ascending by version, guarding the future multi-version catch-up path.
func TestLookupNotesMultiEntryOrdering(t *testing.T) {
	orig := notes
	t.Cleanup(func() { notes = orig })
	// Insert the higher version FIRST so a correct sort must reorder it after 0.30.0.
	notes = []Note{
		{Version: "0.31.0", Lines: []string{"synthetic 0.31 note"}},
		{Version: "0.30.0", Lines: []string{"synthetic 0.30 note"}},
	}

	got := LookupNotes("0.29.0", "0.31.0")
	if len(got) != 2 {
		t.Fatalf("expected both synthetic entries, got %d (%+v)", len(got), got)
	}
	if got[0].Version != "0.30.0" || got[1].Version != "0.31.0" {
		t.Fatalf("ordering = [%s, %s], want ascending [0.30.0, 0.31.0]", got[0].Version, got[1].Version)
	}
}

// TestRenderBodyEmptyState: RenderBody(current, nil) contains the version header, the
// change-list header, and the UI-SPEC empty-state line.
func TestRenderBodyEmptyState(t *testing.T) {
	body := RenderBody("0.30.0", nil)
	for _, want := range []string{
		"ProxSave 0.30.0",
		"What changed in this version:",
		"This version has updates. See the changelog for details.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("empty-state body missing %q\n---body---\n%s", want, body)
		}
	}
}

// TestRenderBodyWithNotes: RenderBody with the placeholder notes includes each note line
// prefixed as a plain "- " bullet.
func TestRenderBodyWithNotes(t *testing.T) {
	got := LookupNotes("0.29.0", "0.30.0")
	if len(got) != 1 {
		t.Fatalf("precondition: expected 1 placeholder note, got %d", len(got))
	}
	body := RenderBody("0.30.0", got)
	if !strings.Contains(body, "ProxSave 0.30.0") {
		t.Fatalf("body missing version header\n%s", body)
	}
	for _, line := range got[0].Lines {
		want := "- " + line
		if !strings.Contains(body, want) {
			t.Fatalf("body missing bulleted line %q\n---body---\n%s", want, body)
		}
	}
}

// TestRenderBodyGlyphFree: the body must be glyph-free (no theme import), so no bullet
// glyph or em-dash/en-dash leaks into the pure state tier (W1 FIX).
func TestRenderBodyGlyphFree(t *testing.T) {
	body := RenderBody("0.30.0", LookupNotes("0.29.0", "0.30.0"))
	for _, banned := range []string{"•", "—", "–"} { // bullet, em-dash, en-dash
		if strings.Contains(body, banned) {
			t.Fatalf("body contains banned glyph %q", banned)
		}
	}
}
