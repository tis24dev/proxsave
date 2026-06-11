package main

import "testing"

// Regression for version-comparator-divergence-prerelease (2026-06-09 audit):
// the upgrade gate (compareVersions) stripped any "-"/"+" suffix and so treated
// "1.2.3-rc1" as EQUAL to "1.2.3", while the update nag (isNewerVersion) correctly
// treats a stable release as newer than the same-numeric pre-release. The two gave
// opposite answers on the rc -> stable transition, so an rc user was told an update
// existed yet runUpgrade refused it ("already running the latest version").
// Written after aligning compareVersions with isNewerVersion.

func TestCompareVersions_PrereleaseTieBreak(t *testing.T) {
	cases := []struct {
		current, latest string
		want            int
	}{
		{"1.2.3-rc1", "1.2.3", -1},    // rc -> stable is an upgrade
		{"1.2.3", "1.2.3-rc1", 1},     // stable -> rc is not a downgrade target
		{"1.2.3-rc1", "1.2.3-rc2", 0}, // both pre-release, same numeric core
		{"1.2.3", "1.2.3", 0},         // both stable
		{"1.2.3+build", "1.2.3", 0},   // build metadata is not a pre-release
		{"1.2.3", "1.2.3+build", 0},   // build metadata is not a pre-release
		{"1.2.3-rc1", "1.2.4", -1},    // numeric core still dominates
	}
	for _, c := range cases {
		if got := compareVersions(c.current, c.latest); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.current, c.latest, got, c.want)
		}
	}
}

// The upgrade gate and the update nag must agree: whenever isNewerVersion says an
// update exists, compareVersions must report current < latest (-1), and vice versa.
func TestCompareVersions_AgreesWithIsNewerVersion(t *testing.T) {
	pairs := [][2]string{
		{"1.2.3-rc1", "1.2.3"},
		{"1.2.2", "1.2.3"},
		{"1.2.3", "1.3.0"},
		{"1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3-rc1"},
		{"2.0.0", "1.9.9"},
	}
	for _, p := range pairs {
		cur, lat := p[0], p[1]
		newer := isNewerVersion(cur, lat)
		cmp := compareVersions(cur, lat)
		if newer && cmp >= 0 {
			t.Errorf("isNewerVersion(%q,%q)=true but compareVersions=%d (want <0)", cur, lat, cmp)
		}
		if !newer && cmp < 0 {
			t.Errorf("isNewerVersion(%q,%q)=false but compareVersions=%d (want >=0)", cur, lat, cmp)
		}
	}
}
