package serverbot

import "strings"

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
