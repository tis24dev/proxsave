package whatsnew

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Fixed Screen 0 section headers. The template is fixed: these headers are always the same
// across every release; a Note never carries its own title, only the content under them.
const (
	headerChanges = "What changed in this version:"
	headerActions = "What you need to do now:"
)

// Note is one release's what's-new entry: a semver key, its highlight lines, and an optional
// list of actions the user must still take. All copy is plain terse English (no em-dash,
// en-dash, or emoji); the Pager sanitizes and wraps it, so it is never hand-styled here and
// carries no glyphs. The two section HEADERS are fixed (see the consts above), never
// per-release.
type Note struct {
	Version string
	Lines   []string // highlights, one plain "- " bullet each (under headerChanges)
	Actions []string // optional user TODOs, one "- " bullet each (under headerActions)
}

// HOW TO ADD A RELEASE ENTRY (Screen 0 population guide)
//
// Screen 0 has a FIXED template with FIXED section headers (see headerChanges/headerActions
// and RenderBody). Never invent a per-release title; only fill the two content slices:
//
//	Lines   -> shown under "What changed in this version:"
//	Actions -> shown under "What you need to do now:" (the whole section is omitted when empty)
//
// Append one Note per FINAL release only (no beta/rc: the release gate is final-only and
// betas inherit the final's notes). Keep this slice append-only and strictly ascending by
// semver key; the release CI (release-notes-guard) BLOCKS a final release whose version has
// no well-formed entry here, so add the entry in the release PR.
//
// Lines: ONLY genuinely new user features, or concrete changes to how the user uses the
// tool. State the capability from the user's point of view, not the technical detail (e.g.
// "backup monitoring is new", not "monitoring decoupled from Telegram"). One change per line,
// 1 to 8 terse bullets. NOT: this what's-new screen itself or other meta, internal refactors
// or security hardening, UX polish, or anything that already existed in an earlier release.
//
// Actions: only a REAL step the user must take. "On by default" does NOT mean nothing to do:
// if a default-on feature still needs configuring, that configuration goes here. Always point
// to the DASHBOARD path, never a CLI command when a dashboard path exists, and VERIFY every
// menu label and navigation step against the current code before writing it (labels drift; a
// wrong or nonexistent label is a bug). Omit Actions entirely when there is genuinely nothing
// the user must do.
//
// Style (enforced by TestRegistryWellFormed, blocking): pure ASCII (no em-dash, en-dash, or
// emoji), no "placeholder", each line at most 120 chars, non-blank, English, terse.
var notes = []Note{
	{
		Version: "0.30.0",
		Lines: []string{
			"New interactive dashboard to run backups, restores, checks, and setup",
			"Scheduled backups now run from a resident daemon (replaces cron, reversible), on by default",
			"Backup monitoring (healthchecks) is on by default and alerts you if a backup stops running",
			"Host backup mode: back up a Proxmox host from an LXC or HA-LXC appliance",
		},
		Actions: []string{
			"In the dashboard, open Healthchecks and follow the portal link to set a password and alert channels",
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

// RenderBody builds the plain, \n-separated Pager body. It is NOT hand-styled or colored
// (the Pager sanitizes and wraps it): the version header "ProxSave <current>", a blank line,
// the fixed changes header, then either the UI-SPEC empty-state line (so continue is always
// reachable and the flag can clear) or, per note, each highlight as a plain ASCII "- " bullet
// followed, when the note has actions, by a blank line, the fixed actions header, and its
// "- " bullets. Section headers are the fixed consts, never per-release. No theme import, no
// glyph constant; the pure state tier stays stdlib-plus-semver only.
func RenderBody(current string, notes []Note) string {
	var b strings.Builder
	b.WriteString("ProxSave " + current + "\n\n")
	b.WriteString(headerChanges + "\n")
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
		if len(n.Actions) > 0 {
			b.WriteString("\n" + headerActions + "\n")
			for _, line := range n.Actions {
				b.WriteString("- " + line + "\n")
			}
		}
	}
	return b.String()
}
