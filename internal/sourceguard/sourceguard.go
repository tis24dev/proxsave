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
	Why  string // "bidi/invisible format rune" or "confusable homoglyph letter (Cyrillic/Greek/Armenian/Cherokee/Coptic/fullwidth)"
}

// The reason strings, kept as constants so callers and tests agree.
const (
	whyFormat    = "bidi/invisible format rune"
	whyHomoglyph = "confusable homoglyph letter (Cyrillic/Greek/Armenian/Cherokee/Coptic/fullwidth)"
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
	0x180E: {}, // MONGOLIAN VOWEL SEPARATOR
	0x2028: {}, // LINE SEPARATOR
	0x2029: {}, // PARAGRAPH SEPARATOR
	0xFFF9: {}, // INTERLINEAR ANNOTATION ANCHOR
	0xFFFA: {}, // INTERLINEAR ANNOTATION SEPARATOR
	0xFFFB: {}, // INTERLINEAR ANNOTATION TERMINATOR
}

// isFormatReject reports whether r is an always-deceptive format/invisible rune:
// a member of rejectSet, or a deprecated Unicode tag character (U+E0000-E007F),
// which are invisible and have been used to hide payloads in plain sight.
func isFormatReject(r rune) bool {
	if _, ok := rejectSet[r]; ok {
		return true
	}
	return r >= 0xE0000 && r <= 0xE007F
}

// isConfusableLetter reports whether r is a letter from a script block whose
// glyphs commonly impersonate Latin letters in identifiers and string literals
// (a true homoglyph, visually identical to ASCII). The curated blocks are
// Cyrillic, Greek, Armenian, Cherokee, Coptic, and the fullwidth ASCII variants.
// Legitimate, visibly-distinct non-ASCII (accented Latin, CJK, letterlike
// symbols) is deliberately NOT included, so real content is never flagged.
func isConfusableLetter(r rune) bool {
	switch {
	case r >= 0x0400 && r <= 0x04FF, // Cyrillic
		r >= 0x0370 && r <= 0x03FF, // Greek
		r >= 0x0530 && r <= 0x058F, // Armenian
		r >= 0x13A0 && r <= 0x13FF, // Cherokee
		r >= 0x2C80 && r <= 0x2CFF, // Coptic
		r >= 0xFF00 && r <= 0xFF5E: // Fullwidth ASCII variants
		return unicode.IsLetter(r)
	default:
		return false
	}
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
		if isFormatReject(r) {
			findings = append(findings, Finding{Line: line, Rune: r, Why: whyFormat})
			continue
		}
		if checkHomoglyphs && isConfusableLetter(r) {
			findings = append(findings, Finding{Line: line, Rune: r, Why: whyHomoglyph})
		}
	}
	return findings
}
