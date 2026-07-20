package main

import (
	"os"
	"strings"
	"testing"
)

// keyB64 extracts the base64 body of the first PEM "PUBLIC KEY" block in s,
// keeping only base64 characters between the BEGIN/END markers. This ignores any
// surrounding shell quoting or line-continuation, so the same key embedded in Go,
// install.sh and the release workflow can be compared regardless of how each file
// formats it.
func keyB64(t *testing.T, name, s string) string {
	t.Helper()
	const begin = "BEGIN PUBLIC KEY-----"
	const end = "-----END PUBLIC KEY"
	bi := strings.Index(s, begin)
	ei := strings.Index(s, end)
	if bi < 0 || ei < 0 || ei <= bi {
		t.Fatalf("%s: no PEM PUBLIC KEY block found", name)
	}
	var b strings.Builder
	for _, r := range s[bi+len(begin) : ei] {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		t.Fatalf("%s: PUBLIC KEY block has no base64 body", name)
	}
	return b.String()
}

// TestReleaseSigningKeyNoDrift guards the five pinned copies of the release
// signing public key against drifting apart: the Go constant
// (releaseSigningPubKeyPEM), the installer (install.sh), the release workflow
// (release.yml), the provenance verification doc
// (docs/PROVENANCE_VERIFICATION.md) and the beta upgrade script
// (upgrade-beta.sh) must all embed the exact same key, or signature
// verification would pass in one place and fail in another.
func TestReleaseSigningKeyNoDrift(t *testing.T) {
	want := keyB64(t, "releaseSigningPubKeyPEM", releaseSigningPubKeyPEM)

	for _, f := range []struct{ name, path string }{
		{"install.sh", "../../install.sh"},
		{"release.yml", "../../.github/workflows/release.yml"},
		{"PROVENANCE_VERIFICATION.md", "../../docs/PROVENANCE_VERIFICATION.md"},
		{"upgrade-beta.sh", "../../upgrade-beta.sh"},
	} {
		data, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatalf("read %s: %v", f.path, err)
		}
		if got := keyB64(t, f.name, string(data)); got != want {
			t.Fatalf("release signing public key in %s drifted from releaseSigningPubKeyPEM\n got: %s\nwant: %s", f.name, got, want)
		}
	}
}
