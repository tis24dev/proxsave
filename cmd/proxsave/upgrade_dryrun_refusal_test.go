package main

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
)

// Not one line of cmd/proxsave/upgrade.go reads args.DryRun. `--upgrade --dry-run`
// therefore merges the configuration, refreshes the support docs and symlinks,
// repoints legacy cron entries, may install or restart the resident daemon, and
// normalizes permissions, while the operator was told nothing would change. That
// predates --upgrade-finalize; the finalize mode inherited it.
//
// The combination is refused rather than implemented. A truthful dry run would
// have to model every one of those effects, and a partial one is worse than none
// because it teaches the operator the flag is honoured here.
func TestDryRunIsRefusedWithUpgradeAndItsFinalize(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        cli.Args
		wantRefusal bool
	}{
		{"upgrade with dry-run is refused", cli.Args{Upgrade: true, DryRun: true}, true},
		{"finalize with dry-run is refused", cli.Args{UpgradeFinalize: true, DryRun: true}, true},
		{"upgrade without dry-run is fine", cli.Args{Upgrade: true}, false},
		{"finalize without dry-run is fine", cli.Args{UpgradeFinalize: true}, false},
		{"dry-run alone is fine", cli.Args{DryRun: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := validateModeCompatibility(&tc.args)
			refused := false
			for _, m := range messages {
				if strings.Contains(m, "--dry-run is not supported with --upgrade") {
					refused = true
				}
			}
			if refused != tc.wantRefusal {
				t.Fatalf("refused = %v, want %v (messages: %v)", refused, tc.wantRefusal, messages)
			}
		})
	}
}

// A restore cannot run without modifying the system: it stops services, unmounts
// /etc/pve in RECOVERY, extracts the direct-write categories over the live files and
// writes a safety backup, and only some of its later steps ever read the flag. Under
// --dry-run it did all of that while the operator was told nothing would change.
//
// The combination is refused rather than implemented, for the same reason as the
// upgrade above. DRY_RUN=true in the configuration is refused too, at the restore
// dispatch, because it is only known once the configuration is loaded.
func TestDryRunIsRefusedWithRestore(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        cli.Args
		wantRefusal bool
	}{
		{"restore with dry-run is refused", cli.Args{Restore: true, DryRun: true}, true},
		{"restore without dry-run is fine", cli.Args{Restore: true}, false},
		{"dry-run alone is fine", cli.Args{DryRun: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := validateModeCompatibility(&tc.args)
			refused := false
			for _, m := range messages {
				if strings.Contains(m, "--dry-run is not supported with --restore") {
					refused = true
				}
			}
			if refused != tc.wantRefusal {
				t.Fatalf("refused = %v, want %v (messages: %v)", refused, tc.wantRefusal, messages)
			}
		})
	}
}

// The guards must not catch anything else: --backup has its own dry-run support and
// must keep it.
func TestDryRunStaysAllowedOnTheModesThatImplementIt(t *testing.T) {
	for _, args := range []cli.Args{
		{Backup: true, DryRun: true},
	} {
		for _, m := range validateModeCompatibility(&args) {
			if strings.Contains(m, "--dry-run is not supported") {
				t.Fatalf("a dry-run guard fired on %+v: %s", args, m)
			}
		}
	}
}
