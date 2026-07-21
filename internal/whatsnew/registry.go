package whatsnew

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Note is one release's what's-new entry: a semver key and its body lines. Lines are
// plain terse English (no em-dash, no en-dash, no emoji); the Pager sanitizes and wraps
// them, so they are never hand-styled here and carry no glyphs.
type Note struct {
	Version string
	Lines   []string
}

// notes is the compiled-in, version-keyed registry. Phase 1 ships ONE placeholder 0.30.0
// entry; the real 0.30 content is a separate milestone, so this copy is intentionally
// generic. Keep entries semver-keyed and append-only. A white-box test may temporarily
// swap this var to exercise the multi-version catch-up ordering.
var notes = []Note{
	{
		Version: "0.30.0",
		Lines: []string{
			"Placeholder release note. Real 0.30 content lands in a later milestone.",
			"See the changelog at https://github.com/tis24dev/proxsave for details.",
		},
	},
}

// LookupNotes returns the notes for versions in the half-open range (from, to],
// ascending by parsed semver. The lower bound is exclusive (an equal from never
// re-shows a version the user already saw) and the upper bound is inclusive. An
// unparseable bound, an inverted range, or simply no matching entry yields an empty
// slice, never an error and never the whole registry (fail toward silence).
func LookupNotes(from, to string) []Note {
	lo, errLo := semver.NewVersion(from)
	hi, errHi := semver.NewVersion(to)
	if errLo != nil || errHi != nil {
		return nil
	}
	var out []Note
	for _, n := range notes {
		v, err := semver.NewVersion(n.Version)
		if err != nil {
			continue // a malformed registry key is skipped, never surfaced as show-all
		}
		if v.GreaterThan(lo) && !v.GreaterThan(hi) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := semver.NewVersion(out[i].Version)
		b, _ := semver.NewVersion(out[j].Version)
		return a.LessThan(b)
	})
	return out
}

// RenderBody builds the plain, \n-separated Pager body. It is NOT hand-styled or
// colored (the Pager sanitizes and wraps it): the first line is the version header
// "ProxSave <current>", a blank line, then the change-list header, then either the
// UI-SPEC empty-state line (so continue is always reachable and the flag can clear) or
// each note line prefixed with a plain ASCII "- " bullet. No theme import, no glyph
// constant; the pure state tier stays stdlib-plus-semver only.
func RenderBody(current string, notes []Note) string {
	var b strings.Builder
	b.WriteString("ProxSave " + current + "\n\n")
	b.WriteString("What changed in this version:\n")
	if len(notes) == 0 {
		b.WriteString("This version has updates. See the changelog for details.\n")
		return b.String()
	}
	for _, n := range notes {
		for _, line := range n.Lines {
			b.WriteString("- " + line + "\n")
		}
	}
	return b.String()
}
