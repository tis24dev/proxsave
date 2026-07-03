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

// sanitizeLine is sanitize for single-line contexts (labels): newlines and
// tabs collapse to spaces.
func sanitizeLine(s string) string {
	s = sanitize(s)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}
