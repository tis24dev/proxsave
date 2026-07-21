package whatsnew

import (
	"strings"
	"testing"
)

// TestIsUnseen covers the semver gate matrix: upgrade fires, equal/downgrade do not,
// prerelease ordering is correct, a missing v is tolerated, an absent flag is unseen,
// and every unparseable input fails toward silence (false + non-nil error).
func TestIsUnseen(t *testing.T) {
	cases := []struct {
		name     string
		current  string
		lastSeen string
		present  bool
		want     bool
		wantErr  bool
	}{
		{"newer fires", "0.30.0", "0.29.0", true, true, false},
		{"equal does not fire", "0.30.0", "0.30.0", true, false, false},
		{"downgrade does not fire", "0.29.0", "0.30.0", true, false, false},
		{"prerelease before release is unseen", "0.30.0", "0.30.0-beta5", true, true, false},
		{"release after prerelease already seen", "0.30.0-beta5", "0.30.0", true, false, false},
		{"missing v tolerated", "0.30.0", "v0.29.0", true, true, false},
		{"absent flag is unseen", "0.30.0", "", false, true, false},
		{"unparseable current fails toward silence", "not-a-version", "0.29.0", true, false, true},
		{"unparseable lastSeen fails toward silence", "0.30.0", "garbage", true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsUnseen(tc.current, tc.lastSeen, tc.present)
			if tc.wantErr && err == nil {
				t.Fatalf("IsUnseen(%q,%q,%v) err = nil, want non-nil", tc.current, tc.lastSeen, tc.present)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("IsUnseen(%q,%q,%v) unexpected err %v", tc.current, tc.lastSeen, tc.present, err)
			}
			if got != tc.want {
				t.Fatalf("IsUnseen(%q,%q,%v) = %v, want %v", tc.current, tc.lastSeen, tc.present, got, tc.want)
			}
		})
	}
}

// TestIsDevBuild smoke-tests the dev/pseudo-version guard SEAM (full STATE-05 matrix is
// Phase 3).
func TestIsDevBuild(t *testing.T) {
	cases := []struct {
		current string
		want    bool
	}{
		{"", true},
		{"0.0.0-dev", true},
		{"v0.0.0-dev", true},
		{"v0.0.0-20260101120000-abcdef123456", true}, // Go pseudo-version
		{"0.0.0-20260101120000-abcdef123456", true},
		{"0.30.0", false},
		{"v0.30.0", false},
		{"0.30.0-beta5", false},
	}
	for _, tc := range cases {
		if got := IsDevBuild(tc.current); got != tc.want {
			t.Fatalf("IsDevBuild(%q) = %v, want %v", tc.current, got, tc.want)
		}
	}
}

// TestDecideAbsentFlagShows: a temp base with an absent flag and current 0.30.0 shows the
// screen with a non-empty body containing the placeholder note.
func TestDecideAbsentFlagShows(t *testing.T) {
	base := t.TempDir()
	show, body, err := Decide(base, "0.30.0")
	if err != nil {
		t.Fatalf("Decide: unexpected err %v", err)
	}
	if !show {
		t.Fatalf("Decide on absent flag show = false, want true")
	}
	if !strings.Contains(body, "ProxSave 0.30.0") {
		t.Fatalf("body missing version header\n%s", body)
	}
	if !strings.Contains(body, "Placeholder release note.") {
		t.Fatalf("body missing placeholder note\n%s", body)
	}
}

// TestDecideDevBuildSilent: a dev-build current short-circuits to (false, "", nil) via the
// IsDevBuild guard, before any state read.
func TestDecideDevBuildSilent(t *testing.T) {
	base := t.TempDir()
	show, body, err := Decide(base, "0.0.0-dev")
	if err != nil {
		t.Fatalf("Decide dev build: unexpected err %v", err)
	}
	if show || body != "" {
		t.Fatalf("Decide dev build = (%v, %q), want (false, \"\")", show, body)
	}
}

// TestDecideAfterMarkSeenSilent: after MarkSeen(base, 0.30.0), Decide with current 0.30.0
// returns (false, "", nil) because the version is already seen.
func TestDecideAfterMarkSeenSilent(t *testing.T) {
	base := t.TempDir()
	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	show, body, err := Decide(base, "0.30.0")
	if err != nil {
		t.Fatalf("Decide after MarkSeen: unexpected err %v", err)
	}
	if show || body != "" {
		t.Fatalf("Decide after MarkSeen = (%v, %q), want (false, \"\")", show, body)
	}
}
