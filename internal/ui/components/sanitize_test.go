package components

import (
	"strings"
	"testing"
)

// Direct unit coverage of the sanitize boundary: deleting any clause of the
// control-character map must fail here (the component-level tests only prove
// the ANSI-strip half).
func TestSanitizeStripsControlCharacters(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ansi sgr", "evil\x1b[31mred", "evilred"},
		// ESC+b parses as a two-byte escape sequence, stripped whole.
		{"bare esc", "a\x1bb", "a"},
		{"osc bel", "a\x1b]0;title\x07b", "ab"},
		{"bell", "a\x07b", "ab"},
		{"del", "a\x7fb", "ab"},
		{"carriage return", "a\rb", "ab"},
		{"c1 nel", "a\u0085b", "ab"},
		{"c1 csi", "a\u009bb", "ab"},
		{"keeps newline", "a\nb", "a\nb"},
		{"keeps tab", "a\tb", "a\tb"},
		{"keeps accents", "caffé", "caffé"},
		{"keeps box drawing", "┌─┐│└┘", "┌─┐│└┘"},
		{"keeps u2028", "a\u2028b", "a\u2028b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitize(tc.in); got != tc.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// SanitizeText is the exported scrub for free-form untrusted text on the
// pre-styled prompt path: malicious escape/control bytes must be gone while
// legitimate text (newlines, tabs, unicode) survives. Each case asserts BOTH
// the dangerous byte is absent AND the good text is present, so it is not
// vacuous.
func TestSanitizeText(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []rune // bytes/runes that must NOT survive
		contain []string
	}{
		{
			name:    "csi sgr stripped",
			in:      "\x1b[31mred\x1b[0m",
			absent:  []rune{0x1b},
			contain: []string{"red"},
		},
		{
			name:    "osc set title stripped",
			in:      "before\x1b]0;pwned\x07after",
			absent:  []rune{0x1b, 0x07},
			contain: []string{"before", "after"},
		},
		{
			name:   "bel backspace del removed",
			in:     "a\x07b\x08c\x7fd",
			absent: []rune{0x07, 0x08, 0x7f},
			// The letters between the control bytes survive.
			contain: []string{"a", "b", "c", "d"},
		},
		{
			name:    "c1 csi byte removed",
			in:      "x\u009b31my",
			absent:  []rune{0x9b},
			contain: []string{"x", "31my"},
		},
		{
			name:    "newlines and tabs preserved",
			in:      "line1\nline2\tcol",
			contain: []string{"line1", "line2", "col", "\n", "\t"},
		},
		{
			name:    "printable unicode preserved",
			in:      "ok /mnt/nas-backup (café)",
			contain: []string{"ok /mnt/nas-backup (café)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := SanitizeText(tc.in)
			for _, r := range tc.absent {
				if strings.ContainsRune(out, r) {
					t.Fatalf("SanitizeText(%q) = %q, must not contain %#U", tc.in, out, r)
				}
			}
			for _, want := range tc.contain {
				if !strings.Contains(out, want) {
					t.Fatalf("SanitizeText(%q) = %q, must contain %q", tc.in, out, want)
				}
			}
		})
	}
}

func TestSanitizeLineCollapsesWhitespace(t *testing.T) {
	got := sanitizeLine("multi\nline\tvalue")
	if got != "multi line value" {
		t.Fatalf("sanitizeLine = %q", got)
	}
	if strings.ContainsAny(got, "\n\t") {
		t.Fatal("sanitizeLine must not leave newlines or tabs")
	}
}

// SanitizeLine is the exported single-line scrub for untrusted values printed
// into a table cell, filename, or menu row on the plain CLI path. Like
// SanitizeText it removes escape/control bytes, but UNLIKE SanitizeText it
// collapses newlines and tabs to spaces so the row stays one line. Each case
// asserts BOTH the dangerous byte is absent AND the good text is present, so it
// is not vacuous.
func TestSanitizeLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []rune // bytes/runes that must NOT survive
		contain []string
	}{
		{
			name:    "csi sgr stripped",
			in:      "\x1b[31mhost\x1b[0m",
			absent:  []rune{0x1b},
			contain: []string{"host"},
		},
		{
			name:    "osc set title stripped",
			in:      "a\x1b]0;pwned\x07b",
			absent:  []rune{0x1b, 0x07},
			contain: []string{"a", "b"},
		},
		{
			name:    "c1 csi byte and del removed",
			in:      "x\u009by\x7fz",
			absent:  []rune{0x9b, 0x7f},
			contain: []string{"x", "y", "z"},
		},
		{
			// The key difference from SanitizeText: newlines and tabs
			// collapse to spaces so the row stays one line.
			name:    "newlines and tabs collapse to spaces",
			in:      "line1\nline2\tcol",
			absent:  []rune{'\n', '\t'},
			contain: []string{"line1 line2 col"},
		},
		{
			name:    "printable unicode preserved",
			in:      "pve-node-01 (café)",
			contain: []string{"pve-node-01 (café)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := SanitizeLine(tc.in)
			for _, r := range tc.absent {
				if strings.ContainsRune(out, r) {
					t.Fatalf("SanitizeLine(%q) = %q, must not contain %#U", tc.in, out, r)
				}
			}
			for _, want := range tc.contain {
				if !strings.Contains(out, want) {
					t.Fatalf("SanitizeLine(%q) = %q, must contain %q", tc.in, out, want)
				}
			}
		})
	}
}

// sanitize must drop Unicode Cf format runes (zero-width and bidi controls) that
// are >= 0x20 and would otherwise pass the C0/C1 filter, defeating Trojan-source
// display spoofing of filenames/values shown to root.
func TestSanitizeDropsFormatAndBidiRunes(t *testing.T) {
	// U+202E RIGHT-TO-LEFT OVERRIDE, U+200B ZERO WIDTH SPACE.
	in := "a‮b​c"
	got := sanitize(in)
	if got != "abc" {
		t.Fatalf("sanitize(%q) = %q, want %q", in, got, "abc")
	}
}

// TestSanitizeStreamLine covers the color-preserving stream scrub directly: SGR
// (color) escapes survive verbatim, every other escape (OSC, cursor/mode CSI) is
// dropped WITH its payload, and C0/DEL/C1 plus newlines/tabs are flattened. The
// security-critical arm is "drop a non-SGR escape" - a bug in isSGR that matched
// any CSI would slip cursor/OSC escapes through, which only this test catches.
func TestSanitizeStreamLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		contain []string
		absent  []string
	}{
		{"sgr kept verbatim", "\x1b[31mred\x1b[0m", []string{"\x1b[31m", "red", "\x1b[0m"}, nil},
		{"non-sgr csi erase dropped", "before\x1b[2Jafter", []string{"before", "after"}, []string{"\x1b[2J"}},
		{"cursor-move csi dropped", "x\x1b[Hy", []string{"x", "y"}, []string{"\x1b[H"}},
		{"osc title dropped with payload", "a\x1b]0;pwned\x07b", []string{"a", "b"}, []string{"\x1b]0;", "pwned", "\x07"}},
		{"bel flattened", "a\x07b", []string{"ab"}, []string{"\x07"}},
		{"newline and tab become spaces", "l1\nl2\tc", []string{"l1 l2 c"}, []string{"\n", "\t"}},
		{"printable unicode kept", "café /mnt/x", []string{"café /mnt/x"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sanitizeStreamLine(tc.in)
			for _, want := range tc.contain {
				if !strings.Contains(out, want) {
					t.Fatalf("sanitizeStreamLine(%q) = %q, must contain %q", tc.in, out, want)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(out, bad) {
					t.Fatalf("sanitizeStreamLine(%q) = %q, must NOT contain %q", tc.in, out, bad)
				}
			}
		})
	}
}
