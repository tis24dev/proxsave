package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/notify"
)

// TestConsoleStatusGlyphIsTextNotEmoji locks the fix for the framed-panel border
// misalignment: the console "Exit status" line must use single-rune TEXT glyphs,
// never an emoji-presentation glyph carrying the U+FE0F variation selector (whose
// terminal width disagrees with lipgloss and shifts the panel border one column).
func TestConsoleStatusGlyphIsTextNotEmoji(t *testing.T) {
	const emojiVariationSelector rune = 0xFE0F
	for _, st := range []notify.NotificationStatus{
		notify.StatusSuccess, notify.StatusWarning, notify.StatusFailure,
	} {
		g := consoleStatusGlyph(st)
		if len([]rune(g)) != 1 {
			t.Fatalf("status %v glyph %q must be a single text rune", st, g)
		}
		for _, r := range g {
			if r == emojiVariationSelector {
				t.Fatalf("status %v glyph %q carries U+FE0F (emoji presentation) - misaligns the panel", st, g)
			}
		}
	}
}
