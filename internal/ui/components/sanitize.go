package components

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// sanitize strips ANSI escape sequences and control characters (except
// newline and tab) from untrusted data before it is styled. The legacy tview
// stack was structurally immune (tview.Escape + tcell cell model); with
// lipgloss a filename containing raw ESC could restyle its own row, so every
// component sanitizes data strings at the constructor boundary.
func sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		// C0, DEL, and C1 (0x80-0x9F): C1 runes re-encode to real control
		// bytes on non-UTF-8 consoles (0x9B is CSI on latin-1 serial).
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

// SanitizeText is the exported form of sanitize, for callers OUTSIDE this
// package (orchestrator, cmd) that build a PRE-STYLED prompt and must scrub
// free-form / untrusted text (external tool output, error strings, remote
// filenames) BEFORE it reaches the verbatim WithSelectorPromptStyled path. It
// strips ANSI and C0/DEL/C1 control bytes while keeping newlines and tabs, so a
// multi-line explanation survives. Use this denylist for human-readable text,
// not an allowlist (which would mangle it).
func SanitizeText(s string) string { return sanitize(s) }

// SanitizeLine is the exported form of sanitizeLine, for callers OUTSIDE this
// package that print an untrusted value into a SINGLE-line context (a table
// cell, a filename, a menu row) via the plain CLI path, which bypasses the
// NewSelector/NewConfirm constructor scrub the graphical screens get. It strips
// ANSI + C0/DEL/C1 and collapses newlines/tabs to spaces so the row stays one
// line.
func SanitizeLine(s string) string { return sanitizeLine(s) }

// sanitizeLine is sanitize for single-line contexts (labels): newlines and
// tabs collapse to spaces.
func sanitizeLine(s string) string {
	s = sanitize(s)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}

// sanitizeStreamLine is the COLOR-PRESERVING sibling of sanitizeLine for the
// streaming viewport: it KEEPS ANSI SGR (color/style) escapes so a colored
// "[ts] LEVEL msg" line survives into the scrollable panel, but neutralizes
// everything a rogue log line could weaponize. It splits the line into ANSI
// tokens (charmbracelet/x/ansi): SGR sequences (ESC[...m) pass through
// verbatim, all other escapes are dropped, and printable text has its raw
// control characters (C0/DEL/C1) and embedded newlines/tabs flattened to
// spaces so one physical row stays one logical line in the ring.
func sanitizeStreamLine(s string) string {
	var b strings.Builder
	var state byte
	for len(s) > 0 {
		seq, width, n, newState := ansi.DecodeSequence(s, state, nil)
		if width > 0 {
			// A printable grapheme cluster: strip control runes only.
			b.WriteString(stripStreamText(seq))
		} else if isSGR(seq) {
			// A color/style escape (ESC[...m): keep it verbatim.
			b.WriteString(seq)
		}
		// Any other escape (cursor moves, mode toggles, OSC, ...) is dropped.
		s = s[n:]
		state = newState
	}
	return b.String()
}

// isSGR reports whether an ANSI escape is a Select Graphic Rendition sequence
// (ESC[...m), i.e. a color/style change that is safe to keep.
func isSGR(seq string) bool {
	return strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m")
}

// stripStreamText flattens control runes in a printable run: newline/tab become
// a space (keep one logical line), and C0/DEL/C1 are dropped, matching
// sanitize()'s control policy but without touching ANSI (already tokenized out).
func stripStreamText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
