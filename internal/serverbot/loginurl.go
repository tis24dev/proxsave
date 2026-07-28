package serverbot

import (
	"net/url"
	"strings"
)

// SanitizeLoginURL returns the portal magic-link only if it is a clean http(s) URL
// made of printable ASCII (0x21-0x7e); otherwise "". A URL is printable ASCII by RFC
// 3986 (non-ASCII is percent-encoded), so this drops C0/C1 controls, DEL, spaces, and
// every Unicode format/bidi/line-separator trick a hostile server might inject. The
// link is display-only (proxsave never fetches it), but it must not be able to spoof
// the console. All-or-nothing: never truncated (that would break the link); fail
// closed to "". This is the ONE canonical sanitizer; callers carry the link RAW and
// pass it through here ONLY at the display boundary.
func SanitizeLoginURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
		return ""
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return raw
}

// TrustedLoginURL returns the portal magic-link only if it is BOTH a clean http(s) URL
// (SanitizeLoginURL rules: printable ASCII, no controls/bidi/space) AND lives on the same
// registrable domain as serverAPIHost, the bot-server the run already authenticates
// against. A login_url on any other domain is a phishing host injected by a
// compromised/MITM response and MUST NOT be surfaced to root. All-or-nothing, fail-closed
// to "". Callers gate the RAW link through this at the capture boundary.
func TrustedLoginURL(raw, serverAPIHost string) string {
	safe := SanitizeLoginURL(raw)
	if safe == "" {
		return ""
	}
	if !sameRegistrableDomain(safe, serverAPIHost) {
		return ""
	}
	return safe
}

// portalLoginMaxLen caps the sign-in identity at the RFC 5321 maximum forward-path
// length. The identity is a display string, so an over-long value is a hostile or
// broken server, not a legitimate address.
const portalLoginMaxLen = 254

// SanitizePortalLogin returns the portal sign-in identity only if it is non-empty
// printable ASCII (0x21-0x7e) within the address-length cap; otherwise "". It is the
// sibling of SanitizeLoginURL for the value that is NOT a URL: the email an operator
// types into the portal once they have a password. It deliberately has NO scheme check
// and NO domain gate, because an identity is not navigable, and it deliberately does NOT
// validate address syntax, because the server owns that vocabulary and a stricter client
// would silently hide a legitimate identity. Same all-or-nothing, fail-closed contract as
// SanitizeLoginURL: never truncated, "" on the first bad rune.
func SanitizePortalLogin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > portalLoginMaxLen {
		return ""
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return raw
}

// sameRegistrableDomain reports whether both URLs resolve to the same registrable domain.
// Heuristic: the last two dot-labels of the host (port stripped, lowercased). Sufficient
// for the sole real deployment (*.proxsave.dev): bot.proxsave.dev and hc.proxsave.dev both
// reduce to proxsave.dev. Multi-part public suffixes (e.g. co.uk) are NOT handled; the
// trusted host set is a single two-label domain, so this is acceptable. Fail-closed:
// unparseable input or an empty host yields "" and never matches.
func sameRegistrableDomain(rawA, rawB string) bool {
	a := registrableDomain(rawA)
	b := registrableDomain(rawB)
	return a != "" && a == b
}

func registrableDomain(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
