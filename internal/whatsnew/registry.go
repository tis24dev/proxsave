package whatsnew

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Note is one release's what's-new entry: a semver key, its highlight body lines, and an
// optional call-to-action block (a title plus its own detail lines). All copy is plain
// terse English (no em-dash, no en-dash, no emoji); the Pager sanitizes and wraps it, so
// it is never hand-styled here and carries no glyphs.
type Note struct {
	Version  string
	Lines    []string // highlights, one plain "- " bullet each
	CTATitle string   // optional call-to-action header, rendered under a blank line
	CTALines []string // optional call-to-action detail bullets, shown under CTATitle
}

// notes is the compiled-in, version-keyed registry. Keep entries semver-keyed and
// append-only; a release PR is gated (release-guard) on the released version carrying a
// real, well-formed entry here. A white-box test may temporarily swap this var to
// exercise the multi-version catch-up ordering.
var notes = []Note{
	{
		Version: "0.30.0",
		Lines: []string{
			"New: a one-time what's new screen after each upgrade (you are looking at it)",
			"Host backup mode for Proxmox HA and LXC clusters",
			"Safer confined file reads (safefs), hardened path handling",
			"Clearer Telegram pairing status with distinct states and errors",
		},
		CTATitle: "Recommended: enable backup monitoring",
		CTALines: []string{
			"Run proxsave --install and enable Backup monitoring (daemon scheduler) for alerts when a backup stops running",
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
// UI-SPEC empty-state line (so continue is always reachable and the flag can clear) or,
// per note, each highlight line prefixed with a plain ASCII "- " bullet followed by an
// optional call-to-action block (a blank line, the CTATitle, then its "- " bullets). No
// theme import, no glyph constant; the pure state tier stays stdlib-plus-semver only.
func RenderBody(current string, notes []Note) string {
	var b strings.Builder
	b.WriteString("ProxSave " + current + "\n\n")
	b.WriteString("What changed in this version:\n")
	if len(notes) == 0 {
		b.WriteString("This version has updates. See the changelog for details.\n")
		return b.String()
	}
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n") // blank line between consecutive notes in a multi-version catch-up
		}
		for _, line := range n.Lines {
			b.WriteString("- " + line + "\n")
		}
		if n.CTATitle != "" || len(n.CTALines) > 0 {
			b.WriteString("\n")
			if n.CTATitle != "" {
				b.WriteString(n.CTATitle + "\n")
			}
			for _, line := range n.CTALines {
				b.WriteString("- " + line + "\n")
			}
		}
	}
	return b.String()
}
