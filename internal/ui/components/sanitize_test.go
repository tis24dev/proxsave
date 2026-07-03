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

func TestSanitizeLineCollapsesWhitespace(t *testing.T) {
	got := sanitizeLine("multi\nline\tvalue")
	if got != "multi line value" {
		t.Fatalf("sanitizeLine = %q", got)
	}
	if strings.ContainsAny(got, "\n\t") {
		t.Fatal("sanitizeLine must not leave newlines or tabs")
	}
}
