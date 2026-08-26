package types

import "strings"

// NormalizeHostname puts a hostname into the form host comparisons use: lower
// case, no surrounding space and no trailing root dot ("pve.home.arpa." and
// "pve.home.arpa" are one name). It normalises spelling only: it never strips a
// domain, because "pve.siteA.example" and "pve.siteB.example" are two machines.
func NormalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
