package types

import "strings"

// NormalizeHostname puts a hostname into the form host comparisons use: lower
// case, no surrounding space and no trailing root dot ("pve.home.arpa." and
// "pve.home.arpa" are one name). It normalises spelling only: it never strips a
// domain, because "pve.siteA.example" and "pve.siteB.example" are two machines.
//
// Lower casing is not the same rule as the strings.EqualFold the ownership
// comparison used before, and neither rule contains the other: U+017F and "s" fold
// together but do not lower case together, while U+0130 lower cases to "i" but does
// not fold to it. A hostname is letters, digits and hyphens, where the two rules
// agree on every pair, so no real name reaches either difference.
func NormalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
