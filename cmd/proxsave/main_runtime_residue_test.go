package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestResidueWarningStaysQuietOnAHealthyHost is the one that keeps this line useful.
// Detection returns at the first marker that proves an install and never reaches the
// residue rungs, so a host that has the product records no residue and must produce no
// warning. A line that fired on every run would be ignored by the time it mattered.
func TestResidueWarningStaysQuietOnAHealthyHost(t *testing.T) {
	for _, info := range []*environment.EnvironmentInfo{
		{Type: types.ProxmoxVE},
		{Type: types.ProxmoxBS},
		{Type: types.ProxmoxDual},
		{Type: types.ProxmoxVE, PVEResidual: "   "},
	} {
		bootstrap := logging.NewBootstrapLogger()
		before := bootstrap.EntryCount()
		warnDetectionResidue(bootstrap, info)
		if got := bootstrap.EntryCount() - before; got != 0 {
			t.Fatalf("type %s with residual %q logged %d entries, want silence", info.Type, info.PVEResidual, got)
		}
	}
}

// TestResidueWarningFiresOncePerProduct: the host in issue #315 has PBS residue and no
// PBS, and the operator looking at a type they did not expect never sees the trace,
// which is debug only.
func TestResidueWarningFiresOncePerProduct(t *testing.T) {
	for _, tc := range []struct {
		name string
		info *environment.EnvironmentInfo
		want int
	}{
		{
			name: "pbs residue on a pve host",
			info: &environment.EnvironmentInfo{Type: types.ProxmoxVE, PBSResidual: "directory (/etc/proxmox-backup)"},
			want: 1,
		},
		{
			name: "both products left something behind",
			info: &environment.EnvironmentInfo{
				Type:        types.ProxmoxUnknown,
				PVEResidual: "directory (/etc/pve)",
				PBSResidual: "directory (/etc/proxmox-backup)",
			},
			want: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootstrap := logging.NewBootstrapLogger()
			before := bootstrap.EntryCount()
			warnDetectionResidue(bootstrap, tc.info)
			if got := bootstrap.EntryCount() - before; got != tc.want {
				t.Fatalf("logged %d entries, want %d", got, tc.want)
			}
		})
	}
}

// TestResidueWarningSurvivesNilArguments: it runs on the bootstrap path, before the
// main logger exists, and must not be the thing that takes a run down.
func TestResidueWarningSurvivesNilArguments(t *testing.T) {
	warnDetectionResidue(nil, &environment.EnvironmentInfo{PBSResidual: "directory (/etc/proxmox-backup)"})
	warnDetectionResidue(logging.NewBootstrapLogger(), nil)
}
