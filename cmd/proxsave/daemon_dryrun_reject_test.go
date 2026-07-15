package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
)

// F02-02 + F02-01: --dry-run must be rejected for the daemon modes that mutate
// (supervisor spawns real backups+prune; setup/remove mutate systemd/cron).
// --daemon-status is read-only, so --dry-run there is a harmless no-op.
func TestValidateDaemonCompatibility_RejectsDryRunOnMutatingModes(t *testing.T) {
	cases := []struct {
		name    string
		args    cli.Args
		wantErr bool
	}{
		{"daemon+dryrun", cli.Args{Daemon: true, DryRun: true}, true},
		{"setup+dryrun", cli.Args{DaemonSetup: true, DryRun: true}, true},
		{"remove+dryrun", cli.Args{DaemonRemove: true, DryRun: true}, true},
		{"status+dryrun allowed", cli.Args{DaemonStatus: true, DryRun: true}, false},
		{"daemon no dryrun", cli.Args{Daemon: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			got := validateDaemonCompatibility(&args)
			if tc.wantErr && len(got) == 0 {
				t.Fatalf("expected a validation error, got none")
			}
			if !tc.wantErr && len(got) != 0 {
				t.Fatalf("expected no validation error, got %v", got)
			}
		})
	}
}
