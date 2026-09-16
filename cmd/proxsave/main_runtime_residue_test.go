package main

import (
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// replayedWarnings returns what ReplayConsoleSince prints for entries recorded after
// mark. That method replays warning and worse only, so an empty result is proof the
// entries in between were below warning.
func replayedWarnings(t *testing.T, bootstrap *logging.BootstrapLogger, mark int) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	bootstrap.ReplayConsoleSince(mark)
	_ = w.Close()
	os.Stderr = old
	return <-done
}

// TestResidueIsReportedBelowWarning pins the level, not just the presence of the line.
// A residue is recorded only when a product was NOT proved installed, and that leaves
// two cases: a correct verdict, where the product is genuinely absent and the backup is
// complete, or an unknown verdict, which already carries its own warnings. Reporting
// this at warning pinned an otherwise healthy host at exit 1 on every single run, for
// leftovers its operator often cannot delete.
func TestResidueIsReportedBelowWarning(t *testing.T) {
	bootstrap := logging.NewBootstrapLogger()
	mark := bootstrap.EntryCount()

	reportDetectionResidue(bootstrap, &environment.EnvironmentInfo{
		Type:        types.ProxmoxVE,
		PBSResidual: "directory (/etc/proxmox-backup)",
	})

	if got := bootstrap.EntryCount() - mark; got != 1 {
		t.Fatalf("recorded %d entries, want 1", got)
	}
	if replayed := replayedWarnings(t, bootstrap, mark); replayed != "" {
		t.Fatalf("the residue line replayed as warning-or-worse, so it still promotes a clean run off exit 0:\n%s", replayed)
	}
}

// TestResidueStaysQuietWhenAProductWasFound: a product that was found records no
// residue, so a healthy host of either kind must produce no line at all. A message that
// appeared on every run would be ignored by the time it mattered.
//
// It is the RESIDUE FIELD being empty that makes it quiet, not the ladder stopping
// early: since the versionless-command rung keeps walking, a host whose command
// answered without a version does reach the residue rungs, and detectPVE/detectPBS
// still return an empty residue because the product was proved installed. That is
// covered on the detection side by TestAVersionlessCommandStillProvesTheInstall.
func TestResidueStaysQuietWhenAProductWasFound(t *testing.T) {
	for _, info := range []*environment.EnvironmentInfo{
		{Type: types.ProxmoxVE},
		{Type: types.ProxmoxBS},
		{Type: types.ProxmoxDual},
		{Type: types.ProxmoxVE, PVEResidual: "   "},
	} {
		bootstrap := logging.NewBootstrapLogger()
		before := bootstrap.EntryCount()
		reportDetectionResidue(bootstrap, info)
		if got := bootstrap.EntryCount() - before; got != 0 {
			t.Fatalf("type %s with residual %q logged %d entries, want silence", info.Type, info.PVEResidual, got)
		}
	}
}

// TestResidueIsReportedOncePerProduct: the host in issue #315 has PBS residue and no
// PBS, and the operator looking at a type they did not expect never sees the trace,
// which is debug only.
func TestResidueIsReportedOncePerProduct(t *testing.T) {
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
			reportDetectionResidue(bootstrap, tc.info)
			if got := bootstrap.EntryCount() - before; got != tc.want {
				t.Fatalf("logged %d entries, want %d", got, tc.want)
			}
		})
	}
}

// TestResidueReportSurvivesNilArguments: it runs on the bootstrap path, before the
// main logger exists, and must not be the thing that takes a run down.
func TestResidueReportSurvivesNilArguments(t *testing.T) {
	reportDetectionResidue(nil, &environment.EnvironmentInfo{PBSResidual: "directory (/etc/proxmox-backup)"})
	reportDetectionResidue(logging.NewBootstrapLogger(), nil)
}
