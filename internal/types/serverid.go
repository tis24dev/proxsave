package types

import "strings"

// serverIDLength is the width identity.generateServerID mints and the identity file
// persists. It is restated here rather than imported because internal/types is a
// leaf package that nothing else in the tree may depend on in reverse; the two
// definitions are held together by a test rather than by the compiler.
const serverIDLength = 16

// NormalizeServerID puts a server identity into the form ownership comparisons use,
// and refuses everything that is not one. The only accepted shape is exactly
// serverIDLength ASCII decimal digits after the surrounding space is trimmed, which
// is what internal/identity mints and persists.
//
// "" means ABSENT. Every caller must read it as "cannot compare" and never as a
// match, in the same spirit as NormalizeHostname's note that it never strips a
// domain: two archives that both fail to name an identity have not been shown to
// share one, and treating the empty value as equal would hand every unidentified
// archive to every host at once.
//
// Anything else degrades to "" on purpose: a truncated value, a hex-looking value, a
// future wider format or the literal "unknown" is corruption or a format this binary
// does not understand, and the hostname rule is still there to classify the archive.
// A lenient reading is what would create the false match, because a short or padded
// value like "0" is a value several machines could carry.
//
// The digit test is byte by byte and deliberately not unicode.IsDigit: the latter
// accepts the Arabic-Indic and Devanagari digit ranges among others, which the
// minting side can never produce, so admitting them would only widen what compares
// equal to something no writer wrote.
func NormalizeServerID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != serverIDLength {
		return ""
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return ""
		}
	}
	return value
}
