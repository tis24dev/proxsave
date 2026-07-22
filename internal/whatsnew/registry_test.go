package whatsnew

import (
	"os"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
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

// TestLookupNotesBetaUpperBound (reviewer #1): a prerelease upper bound is finalized, so a
// beta of a line returns that line's FINAL entry instead of an empty range; a beta lower bound
// is finalized too, so a user who saw the line on an earlier beta does not re-see it.
func TestLookupNotesBetaUpperBound(t *testing.T) {
	got := LookupNotes("0.0.0", "0.30.0-beta6")
	if len(got) != 1 || got[0].Version != "0.30.0" {
		t.Fatalf("LookupNotes(0.0.0, 0.30.0-beta6) = %+v, want the 0.30.0 entry", got)
	}
	if got := LookupNotes("0.30.0-beta5", "0.30.0-beta6"); len(got) != 0 {
		t.Fatalf("LookupNotes(0.30.0-beta5, 0.30.0-beta6) = %+v, want empty (same line already seen)", got)
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

// TestRenderBodyActions verifies the optional actions section renders under the FIXED header
// "What you need to do now:" (unbulleted, under a blank line) with its action lines bulleted
// (uses a synthetic note so it does not couple to the shipped 0.30 copy).
func TestRenderBodyActions(t *testing.T) {
	body := RenderBody("0.30.0", []Note{{
		Version: "0.30.0",
		Lines:   []string{"a highlight"},
		Actions: []string{"do the thing"},
	}})
	if !strings.Contains(body, "- a highlight\n") {
		t.Fatalf("missing bulleted highlight\n%s", body)
	}
	// The double "\n\n" pins the BLANK LINE before the FIXED actions header (a single "\n"
	// would be satisfied by the preceding bullet's own newline, letting a dropped separator
	// survive); the fixed header text itself is pinned here too.
	if !strings.Contains(body, "\n\nWhat you need to do now:\n- do the thing\n") {
		t.Fatalf("actions section not rendered with the fixed header + blank line\n%s", body)
	}
	if strings.Contains(body, "- What you need to do now:") {
		t.Fatalf("actions header must not be bulleted\n%s", body)
	}
}

// TestRenderBodyNoActionsOmitsHeader pins that a note WITHOUT actions renders no actions
// section at all (guards the `len(n.Actions) > 0` branch: a mutation that always rendered the
// header would emit a dangling empty "What you need to do now:" and is caught here).
func TestRenderBodyNoActionsOmitsHeader(t *testing.T) {
	body := RenderBody("0.30.0", []Note{{
		Version: "0.30.0",
		Lines:   []string{"a highlight"},
	}})
	if strings.Contains(body, "What you need to do now:") {
		t.Fatalf("actionless note must not render the actions header\n%s", body)
	}
}

// TestRenderBodyMultiNoteSeparator pins the blank line between consecutive notes in a
// catch-up, so a later note's highlights never glue directly under the prior note's actions
// bullets (which would misattribute them to the actions header).
func TestRenderBodyMultiNoteSeparator(t *testing.T) {
	body := RenderBody("0.31.0", []Note{
		{Version: "0.30.0", Lines: []string{"first"}, Actions: []string{"do x"}},
		{Version: "0.31.0", Lines: []string{"second"}},
	})
	if !strings.Contains(body, "- do x\n\n- second\n") {
		t.Fatalf("consecutive notes not separated by a blank line\n---body---\n%s", body)
	}
}

// assertCleanNoteLine holds the house content rules for any registry copy line: non-blank,
// no leftover "Placeholder", terse (<=120 chars), and pure ASCII (so no em-dash, en-dash,
// or emoji slips into a shipped note).
func assertCleanNoteLine(t *testing.T, version, line string) {
	t.Helper()
	if strings.TrimSpace(line) == "" {
		t.Fatalf("%s: empty/blank note line", version)
	}
	if strings.Contains(strings.ToLower(line), "placeholder") {
		t.Fatalf("%s: leftover placeholder text: %q", version, line)
	}
	if len(line) > 120 {
		t.Fatalf("%s: note line too long (%d > 120): %q", version, len(line), line)
	}
	for _, r := range line {
		if r > 127 {
			t.Fatalf("%s: non-ASCII rune %q in %q (no em-dash/en-dash/emoji)", version, r, line)
		}
	}
}

// noteCopyLines flattens every human-facing string of a Note (highlights + actions) for the
// content-rule checks.
func noteCopyLines(n Note) []string {
	return append(append([]string{}, n.Lines...), n.Actions...)
}

// TestRegistryWellFormed is the ALWAYS-ON lint that runs on every PR (a plain unit test):
// the whole compiled-in registry must be non-empty, strictly ascending by unique semver
// key, and every entry must carry highlight lines and only clean copy (no placeholder, no
// forbidden glyph, terse). It catches a malformed edit at authoring time, independent of any
// release.
func TestRegistryWellFormed(t *testing.T) {
	if len(notes) == 0 {
		t.Fatal("registry is empty; at least one Note{} is required")
	}
	seen := map[string]bool{}
	var prev *semver.Version
	for i, n := range notes {
		v, err := semver.NewVersion(n.Version)
		if err != nil {
			t.Fatalf("notes[%d].Version %q is not valid semver: %v", i, n.Version, err)
		}
		if seen[v.String()] {
			t.Fatalf("notes[%d]: duplicate version %q", i, n.Version)
		}
		seen[v.String()] = true
		if prev != nil && !v.GreaterThan(prev) {
			t.Fatalf("notes[%d] version %q not strictly ascending after %q (keep the registry append-only and ordered)", i, n.Version, prev.String())
		}
		prev = v
		if len(n.Lines) == 0 {
			t.Fatalf("notes[%d] (%s) has no highlight lines", i, n.Version)
		}
		if len(n.Lines) > 8 {
			t.Fatalf("notes[%d] (%s) has %d highlights; keep Screen 0 terse (<=8)", i, n.Version, len(n.Lines))
		}
		for _, line := range noteCopyLines(n) {
			assertCleanNoteLine(t, n.Version, line)
		}
	}
}

// TestReleaseNotesPresent is the RELEASE GATE, inert unless WHATSNEW_REQUIRE_VERSION is set
// (the release CI sets it to the release tag). It is final-only: a prerelease is skipped
// (betas inherit the final's notes). When enforced it FAILS the release unless the released
// version has a real, well-formed what's-new entry in the registry.
func TestReleaseNotesPresent(t *testing.T) {
	req := strings.TrimSpace(os.Getenv("WHATSNEW_REQUIRE_VERSION"))
	if req == "" {
		t.Skip("WHATSNEW_REQUIRE_VERSION not set; release-notes gate is inert outside the release CI")
	}
	want, err := semver.NewVersion(req)
	if err != nil {
		t.Fatalf("WHATSNEW_REQUIRE_VERSION %q is not a valid release version: %v", req, err)
	}
	if want.Prerelease() != "" {
		t.Skipf("release %q is a prerelease; what's-new notes are required only for final releases", req)
	}
	for _, n := range notes {
		v, err := semver.NewVersion(n.Version)
		if err != nil {
			continue
		}
		if !v.Equal(want) {
			continue
		}
		if len(n.Lines) == 0 {
			t.Fatalf("what's-new entry for %s has no highlight lines", req)
		}
		for _, line := range noteCopyLines(n) {
			assertCleanNoteLine(t, n.Version, line)
		}
		return // found and well-formed
	}
	t.Fatalf("no what's-new entry for release %s in internal/whatsnew/registry.go; add a Note{} before releasing", req)
}
