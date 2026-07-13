package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
)

// upgradeAcquireBinary in --localfile mode must NOT touch the network: it returns
// the binary already on disk so upgradeFinalizePhase acts on it, and never signals
// terminal (the finalize has to run). This is the seam upgrade-beta.sh relies on --
// a fetch here would resolve the latest STABLE release and could pull a beta tester
// off their build. The running version is reported as the installed version,
// stripped of a leading "v" to match the download path's format.
func TestUpgradeAcquireBinary_LocalFile(t *testing.T) {
	cases := []struct {
		name           string
		currentVersion string
		wantVersion    string
	}{
		{"plain semver", "1.2.3", "1.2.3"},
		{"leading v trimmed", "v9.9.9", "9.9.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := &cli.Args{LocalFile: true}
			var execPath, versionInstalled string
			var upgradeErr error
			var exitCode int
			var terminal bool
			out := captureStdout(t, func() {
				execPath, versionInstalled, exitCode, terminal, upgradeErr = upgradeAcquireBinary(
					context.Background(), args, logging.NewBootstrapLogger(), t.TempDir(), c.currentVersion)
			})

			if terminal {
				t.Errorf("localfile must not be terminal (finalize has to run); exitCode=%d", exitCode)
			}
			if upgradeErr != nil {
				t.Errorf("localfile must not error: %v", upgradeErr)
			}
			if exitCode != 0 {
				t.Errorf("localfile exitCode = %d, want 0", exitCode)
			}
			if execPath == "" {
				t.Error("localfile must return the on-disk exec path, got empty")
			}
			if versionInstalled != c.wantVersion {
				t.Errorf("versionInstalled = %q, want %q", versionInstalled, c.wantVersion)
			}
			if !strings.Contains(out, "no download") {
				t.Errorf("localfile must announce it skips the download; stdout:\n%s", out)
			}
		})
	}
}
