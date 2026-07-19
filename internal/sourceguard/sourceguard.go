// Package sourceguard is a pure detector for deceptive Unicode in source text.
//
// It is the source-side companion to the runtime UI scrub (F04-07): where the
// UI hardens what is DISPLAYED to root, this hardens the tracked SOURCE, so a
// future commit, fork import, or contributor cannot smuggle in text that
// renders differently from its bytes and deceives a human or an AI code
// reviewer on a later re-read (a Trojan-source attack).
package sourceguard

import "unicode"

// Finding is one deceptive rune occurrence.
type Finding struct {
	Line int
	Rune rune
	Why  string // "bidi/invisible format rune" or "Cyrillic/Greek homoglyph letter"
}

// The reason strings, kept as constants so callers and tests agree.
const (
	whyFormat    = "bidi/invisible format rune"
	whyHomoglyph = "Cyrillic/Greek homoglyph letter"
)

// rejectSet holds runes that are always deceptive in source: bidi
// embeddings/overrides/isolates, zero-width space/joiner/non-joiner, LRM/RLM,
// word-joiner, soft-hyphen, arabic-letter-mark, and BOM/ZWNBSP. Each renders as
// nothing or silently reorders neighboring text, so its bytes differ from what
// a reader sees.
var rejectSet = map[rune]struct{}{
	0x00AD: {}, // SOFT HYPHEN
	0x061C: {}, // ARABIC LETTER MARK
	0x200B: {}, // ZERO WIDTH SPACE
	0x200C: {}, // ZERO WIDTH NON-JOINER
	0x200D: {}, // ZERO WIDTH JOINER
	0x200E: {}, // LEFT-TO-RIGHT MARK
	0x200F: {}, // RIGHT-TO-LEFT MARK
	0x202A: {}, // LEFT-TO-RIGHT EMBEDDING
	0x202B: {}, // RIGHT-TO-LEFT EMBEDDING
	0x202C: {}, // POP DIRECTIONAL FORMATTING
	0x202D: {}, // LEFT-TO-RIGHT OVERRIDE
	0x202E: {}, // RIGHT-TO-LEFT OVERRIDE
	0x2060: {}, // WORD JOINER
	0x2066: {}, // LEFT-TO-RIGHT ISOLATE
	0x2067: {}, // RIGHT-TO-LEFT ISOLATE
	0x2068: {}, // FIRST STRONG ISOLATE
	0x2069: {}, // POP DIRECTIONAL ISOLATE
	0xFEFF: {}, // ZERO WIDTH NO-BREAK SPACE / BYTE ORDER MARK
}

// isHomoglyphLetter reports whether r is a Cyrillic (U+0400-U+04FF) or Greek
// (U+0370-U+03FF) letter. Those blocks hold the glyphs that most often
// impersonate Latin letters in identifiers and string literals (for example
// Cyrillic 'a' U+0430 for Latin 'a'). Non-letter runes in those blocks
// (punctuation, combining marks) are left alone.
func isHomoglyphLetter(r rune) bool {
	inCyrillic := r >= 0x0400 && r <= 0x04FF
	inGreek := r >= 0x0370 && r <= 0x03FF
	if !inCyrillic && !inGreek {
		return false
	}
	return unicode.IsLetter(r)
}

// ScanText reports deceptive runes in content. checkHomoglyphs is true only for
// Go source (identifier and string-literal spoofing matters there; prose such
// as Markdown may legitimately carry accented proper nouns). Line numbers are
// 1-based, counted by '\n'.
func ScanText(content string, checkHomoglyphs bool) []Finding {
	var findings []Finding
	line := 1
	for _, r := range content {
		if r == '\n' {
			line++
			continue
		}
		if _, bad := rejectSet[r]; bad {
			findings = append(findings, Finding{Line: line, Rune: r, Why: whyFormat})
			continue
		}
		if checkHomoglyphs && isHomoglyphLetter(r) {
			findings = append(findings, Finding{Line: line, Rune: r, Why: whyHomoglyph})
		}
	}
	return findings
}
