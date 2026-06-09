package main

import (
	"strings"
	"testing"
)

// Regression for arch-advertised-not-built (2026-06-09 audit): detectOSArch accepted
// runtime.GOARCH == "arm64", so on an arm64 host downloadAndInstallLatest built a URL
// for proxsave_<v>_linux_arm64.tar.gz, which the release pipeline never publishes
// (goreleaser builds amd64 only) - failing later with a confusing 404. The platform
// mapping now lives in the pure resolveReleaseTarget so the rejection is testable
// without depending on the host's GOARCH.
func TestResolveReleaseTarget(t *testing.T) {
	cases := []struct {
		goos, goarch     string
		wantOS, wantArch string
		wantErr          string // substring; "" means no error
	}{
		{"linux", "amd64", "linux", "amd64", ""},
		{"Linux", "amd64", "linux", "amd64", ""}, // OS is lower-cased
		{"linux", "arm64", "", "", "no prebuilt release for architecture arm64"},
		{"linux", "386", "", "", "no prebuilt release for architecture 386"},
		{"darwin", "amd64", "", "", "unsupported OS: darwin"},
		{"windows", "amd64", "", "", "unsupported OS: windows"},
	}
	for _, c := range cases {
		os, arch, err := resolveReleaseTarget(c.goos, c.goarch)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("resolveReleaseTarget(%q,%q) error = %v, want nil", c.goos, c.goarch, err)
				continue
			}
			if os != c.wantOS || arch != c.wantArch {
				t.Errorf("resolveReleaseTarget(%q,%q) = (%q,%q), want (%q,%q)", c.goos, c.goarch, os, arch, c.wantOS, c.wantArch)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("resolveReleaseTarget(%q,%q) error = %v, want substring %q", c.goos, c.goarch, err, c.wantErr)
		}
	}
}
