package types

import "testing"

// TestNormalizeServerID pins the single seam that decides whether two archives, or an
// archive and a host, may be compared at all. Everything it turns into "" falls back
// to the hostname rule, which is the behaviour that shipped before identities existed
// and is always safe; everything it accepts becomes a deletion decision.
//
// The refusals are the point of the table. A lenient reading is what would create a
// FALSE match: a padded, truncated or placeholder value like "0" or "unknown" is a
// value several unrelated machines could carry, and accepting it would make them one
// machine as far as retention is concerned.
func TestNormalizeServerID(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "the shape internal/identity mints", value: "1234567890123456", want: "1234567890123456"},
		{name: "surrounding space is not part of the identity", value: "  1234567890123456  ", want: "1234567890123456"},
		{name: "a trailing newline is not part of the identity", value: "1234567890123456\n", want: "1234567890123456"},
		{name: "leading zeros are digits like any other", value: "0000000000000001", want: "0000000000000001"},
		{name: "nothing at all", value: "", want: ""},
		{name: "space only", value: "   ", want: ""},
		{name: "a single digit several hosts could share", value: "0", want: ""},
		{name: "one digit short", value: "123456789012345", want: ""},
		{name: "one digit long", value: "12345678901234567", want: ""},
		{name: "a hex-looking value of the right width", value: "abcdefabcdefabcd", want: ""},
		{name: "a value with one non-digit in the middle", value: "12345678a0123456", want: ""},
		{name: "the unknown sentinel", value: "unknown", want: ""},
		{name: "internal space is not trimmed away", value: "12345678 0123456", want: ""},
		{
			// Sixteen Arabic-Indic digits. They are digits to unicode.IsDigit and are
			// not what internal/identity can ever mint, so a later refactor to that
			// predicate would widen what compares equal to something no writer wrote.
			name:  "sixteen non-ASCII digit runes",
			value: "١٢٣٤٥٦٧٨٩٠١٢٣٤٥٦",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeServerID(tt.value); got != tt.want {
				t.Fatalf("NormalizeServerID(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestNormalizeServerIDNeverMakesTwoAbsencesEqual states the absence rule as a
// property rather than as a row, because it is the assumption every caller rests on:
// "" means CANNOT COMPARE. Two archives that both fail to name an identity have not
// been shown to share one, and an implementation that compared the raw fields would
// make every archive written before this field existed match every host that also
// carries nothing, which is the whole installed base at once.
func TestNormalizeServerIDNeverMakesTwoAbsencesEqual(t *testing.T) {
	absent := []string{"", " ", "unknown", "0", "123456789012345"}
	for _, a := range absent {
		for _, b := range absent {
			if got := NormalizeServerID(a); got != "" {
				t.Fatalf("NormalizeServerID(%q) = %q, want the empty absence marker", a, got)
			}
			if NormalizeServerID(a) == NormalizeServerID(b) && NormalizeServerID(a) != "" {
				t.Fatalf("%q and %q compared equal as identities", a, b)
			}
		}
	}
}
