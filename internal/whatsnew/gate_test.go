package whatsnew

import (
	"os"
	"path/filepath"
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

// TestShouldWarn covers the non-interactive gate: it shares the IsDevBuild+LoadState+
// IsUnseen core with Decide and returns the v-stripped current version only when unseen
// (absent or older last-seen), while every seen/downgrade/dev-build/parse-error path
// fails toward silence (show=false, empty version).
func TestShouldWarn(t *testing.T) {
	t.Run("absent flag shows normalized version", func(t *testing.T) {
		base := t.TempDir()
		show, ver, err := ShouldWarn(base, "0.30.0")
		if err != nil {
			t.Fatalf("ShouldWarn absent flag: unexpected err %v", err)
		}
		if !show || ver != "0.30.0" {
			t.Fatalf("ShouldWarn absent flag = (%v, %q), want (true, \"0.30.0\")", show, ver)
		}
	})

	t.Run("older last-seen shows", func(t *testing.T) {
		base := t.TempDir()
		if err := MarkSeen(base, "0.29.0"); err != nil {
			t.Fatalf("MarkSeen: %v", err)
		}
		show, ver, err := ShouldWarn(base, "0.30.0")
		if err != nil {
			t.Fatalf("ShouldWarn older last-seen: unexpected err %v", err)
		}
		if !show || ver != "0.30.0" {
			t.Fatalf("ShouldWarn older last-seen = (%v, %q), want (true, \"0.30.0\")", show, ver)
		}
	})

	t.Run("seen is silent", func(t *testing.T) {
		base := t.TempDir()
		if err := MarkSeen(base, "0.30.0"); err != nil {
			t.Fatalf("MarkSeen: %v", err)
		}
		show, ver, err := ShouldWarn(base, "0.30.0")
		if err != nil {
			t.Fatalf("ShouldWarn seen: unexpected err %v", err)
		}
		if show || ver != "" {
			t.Fatalf("ShouldWarn seen = (%v, %q), want (false, \"\")", show, ver)
		}
	})

	t.Run("downgrade is silent", func(t *testing.T) {
		base := t.TempDir()
		if err := MarkSeen(base, "0.30.0"); err != nil {
			t.Fatalf("MarkSeen: %v", err)
		}
		show, ver, err := ShouldWarn(base, "0.29.0")
		if err != nil {
			t.Fatalf("ShouldWarn downgrade: unexpected err %v", err)
		}
		if show || ver != "" {
			t.Fatalf("ShouldWarn downgrade = (%v, %q), want (false, \"\")", show, ver)
		}
	})

	t.Run("dev build short-circuits before state read", func(t *testing.T) {
		base := t.TempDir()
		show, ver, err := ShouldWarn(base, "0.0.0-dev")
		if err != nil {
			t.Fatalf("ShouldWarn dev build: unexpected err %v", err)
		}
		if show || ver != "" {
			t.Fatalf("ShouldWarn dev build = (%v, %q), want (false, \"\")", show, ver)
		}
	})

	t.Run("version is v-stripped", func(t *testing.T) {
		base := t.TempDir()
		show, ver, err := ShouldWarn(base, "v0.30.0")
		if err != nil {
			t.Fatalf("ShouldWarn v-prefixed: unexpected err %v", err)
		}
		if !show || ver != "0.30.0" {
			t.Fatalf("ShouldWarn v-prefixed = (%v, %q), want (true, \"0.30.0\")", show, ver)
		}
	})

	t.Run("corrupt flag fails toward silence", func(t *testing.T) {
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(StatePath(base)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(StatePath(base), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write garbage: %v", err)
		}
		show, ver, err := ShouldWarn(base, "0.30.0")
		if err == nil {
			t.Fatalf("ShouldWarn corrupt flag err = nil, want non-nil")
		}
		if show || ver != "" {
			t.Fatalf("ShouldWarn corrupt flag = (%v, %q), want (false, \"\")", show, ver)
		}
	})

	t.Run("unparseable current fails toward silence", func(t *testing.T) {
		base := t.TempDir()
		show, ver, err := ShouldWarn(base, "not-a-version")
		if err == nil {
			t.Fatalf("ShouldWarn unparseable current err = nil, want non-nil")
		}
		if show || ver != "" {
			t.Fatalf("ShouldWarn unparseable current = (%v, %q), want (false, \"\")", show, ver)
		}
	})
}

// TestIsDevBuild is the full STATE-05 matrix: the broadened classifier catches the
// make-build (X.Y.Z-dev.N+gSHA), dirty-metadata (X.Y.Z+gSHA), and prerelease-dev
// (X.Y.Z-dev[.N]) forms plus the retained literal/pseudo cases, while clean releases,
// legit beta/rc prereleases, and any non-semver string stay non-dev.
func TestIsDevBuild(t *testing.T) {
	cases := []struct {
		current string
		want    bool
	}{
		// Broadened (STATE-05): make-build, dirty-metadata, prerelease-dev.
		{"0.30.0-dev.5+gabc1234", true},   // prerelease dev.5 + metadata
		{"0.30.0-dev.5", true},            // prerelease first id dev
		{"0.30.0-dev", true},              // prerelease exactly dev
		{"0.30.0+gabc1234", true},         // metadata present (dirty)
		{"v0.30.0-dev.5+gabc1234", true},  // v-prefix tolerated
		// Existing (must stay true).
		{"", true},
		{"0.0.0-dev", true},
		{"v0.0.0-dev", true},
		{"v0.0.0-20260101120000-abcdef123456", true}, // Go pseudo-version
		{"0.0.0-20260101120000-abcdef123456", true},
		// Clean releases and legit prereleases (must stay false).
		{"0.30.0", false},
		{"v0.30.0", false},
		{"1.0.0", false},
		{"0.30.0-beta5", false},
		{"0.30.0-rc1", false},
		// Prerelease first dot-identifier must equal "dev" EXACTLY (not a prefix/substring).
		{"0.30.0-dev2", false},
		{"0.30.0-development", false},
		// Non-semver is NOT dev here (IsUnseen fails toward silence on it).
		{"not-a-version", false},
	}
	for _, tc := range cases {
		if got := IsDevBuild(tc.current); got != tc.want {
			t.Fatalf("IsDevBuild(%q) = %v, want %v", tc.current, got, tc.want)
		}
	}
}

// TestDevBuildGateSilent: on every make-build dev format, the broadened IsDevBuild
// short-circuits both gate entry points before any state read, so Decide and ShouldWarn
// return (false, "", nil) even over a TempDir base with an absent flag (STATE-05).
func TestDevBuildGateSilent(t *testing.T) {
	devVersions := []string{
		"0.30.0-dev.5+gabc1234",
		"0.30.0-dev.5",
		"0.30.0-dev",
		"0.30.0+gabc1234",
		"v0.30.0-dev.5+gabc1234",
	}
	for _, v := range devVersions {
		t.Run(v, func(t *testing.T) {
			base := t.TempDir()
			if show, body, err := Decide(base, v); show || body != "" || err != nil {
				t.Fatalf("Decide(%q) = (%v, %q, %v), want (false, \"\", nil)", v, show, body, err)
			}
			if show, ver, err := ShouldWarn(base, v); show || ver != "" || err != nil {
				t.Fatalf("ShouldWarn(%q) = (%v, %q, %v), want (false, \"\", nil)", v, show, ver, err)
			}
		})
	}
}

// TestDecideAbsentFlagShows: a temp base with an absent flag and current 0.30.0 shows the
// screen with a non-empty body carrying the version header, the change-list header, and at
// least one bulleted highlight (structural, so it does not couple to the exact 0.30 copy).
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
	if !strings.Contains(body, "What changed in this version:") {
		t.Fatalf("body missing change-list header\n%s", body)
	}
	if !strings.Contains(body, "\n- ") {
		t.Fatalf("body missing a bulleted highlight\n%s", body)
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

// TestDecideDowngradeSilent (STATE-08): a reinstall/downgrade older than last-seen does
// not fire Screen 0. IsUnseen's cur.GreaterThan(last) is false when current < last-seen;
// this pins Decide's downgrade silence, which TestDecideAfterMarkSeenSilent (equal case)
// did not cover.
func TestDecideDowngradeSilent(t *testing.T) {
	base := t.TempDir()
	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	show, body, err := Decide(base, "0.29.0")
	if err != nil {
		t.Fatalf("Decide downgrade: unexpected err %v", err)
	}
	if show || body != "" {
		t.Fatalf("Decide downgrade = (%v, %q), want (false, \"\")", show, body)
	}
}

// TestShouldWarnDowngradeSilent (STATE-08): the non-interactive entry point is silent on a
// downgrade too, complementing the existing TestShouldWarn "downgrade is silent" subtest.
func TestShouldWarnDowngradeSilent(t *testing.T) {
	base := t.TempDir()
	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	show, ver, err := ShouldWarn(base, "0.29.0")
	if err != nil {
		t.Fatalf("ShouldWarn downgrade: unexpected err %v", err)
	}
	if show || ver != "" {
		t.Fatalf("ShouldWarn downgrade = (%v, %q), want (false, \"\")", show, ver)
	}
}
