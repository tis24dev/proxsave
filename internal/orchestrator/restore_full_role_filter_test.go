package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// dualArchiveEntries is a PVE+PBS archive made only of direct-write categories, so the
// FakeFS staging collapse (collapseStagingWhenUnavailable) plays no part: each entry
// reaches the live system in production exactly as it does here.
var dualArchiveEntries = map[string]string{
	"var/lib/pve-cluster/config.db":      "db\n",                     // pve_cluster
	"etc/corosync/corosync.conf":         "totem {}\n",               // corosync
	"etc/ceph/ceph.conf":                 "[global]\n",               // ceph
	"etc/proxmox-backup/maintenance.cfg": "maintenance\n",            // maintenance_pbs
	"etc/ssh/sshd_config":                "PermitRootLogin backup\n", // ssh (common)
}

var (
	pveOnlyArchivePaths = []string{"var/lib/pve-cluster/config.db", "etc/corosync/corosync.conf", "etc/ceph/ceph.conf"}
	pbsOnlyArchivePaths = []string{"etc/proxmox-backup/maintenance.cfg"}
	commonArchivePaths  = []string{"etc/ssh/sshd_config"}
)

// dpkgStatusForHost is the package database the production detection reads under
// RootPrefix: it decides what the host is detected as after the restore.
var dpkgStatusForHost = map[SystemType]string{
	SystemTypePVE:  "Package: pve-manager\nStatus: install ok installed\nVersion: 9.0.10\n\n",
	SystemTypePBS:  "Package: proxmox-backup-server\nStatus: install ok installed\nVersion: 4.2.5\n\n",
	SystemTypeDual: "Package: pve-manager\nStatus: install ok installed\nVersion: 9.0.10\n\nPackage: proxmox-backup-server\nStatus: install ok installed\nVersion: 4.2.5\n\n",
}

type roleFilterOutcome struct {
	written       map[string]bool // archive path -> present on the live system
	exported      map[string]bool // archive path -> present under the export directory
	detectedAfter types.ProxmoxType
}

// fullRestoreOfDualArchive restores dualArchiveEntries in FULL mode on a host of the
// given type, then runs the production host detection against the same root. Nothing
// from the archive is on the live system beforehand, so present means written.
func fullRestoreOfDualArchive(t *testing.T, host SystemType) roleFilterOutcome {
	t.Helper()
	origRestoreFS, origRestoreCmd, origRestoreSystem, origRestoreTime := restoreFS, restoreCmd, restoreSystem, restoreTime
	origCompatFS, origPrepare, origSafetyFS, origSafetyNow := compatFS, prepareRestoreBundleFunc, safetyFS, safetyNow
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreSystem, restoreTime = origRestoreFS, origRestoreCmd, origRestoreSystem, origRestoreTime
		compatFS, prepareRestoreBundleFunc, safetyFS, safetyNow = origCompatFS, origPrepare, origSafetyFS, origSafetyNow
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS, compatFS, safetyFS = fakeFS, fakeFS, fakeFS
	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime, safetyNow = fakeNow, fakeNow.Now

	if err := fakeFS.AddFile("/var/lib/dpkg/status", []byte(dpkgStatusForHost[host])); err != nil {
		t.Fatal(err)
	}
	restoreSystem = fakeSystemDetector{systemType: host}

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{"umount /etc/pve": []byte("not mounted\n")},
		Errors:  map[string]error{"umount /etc/pve": errors.New("not mounted")},
	}
	for _, svc := range []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd", "proxmox-backup-proxy", "proxmox-backup"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	restoreCmd = cmd

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, dualArchiveEntries); err != nil {
		t.Fatal(err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	prepareRestoreBundleFunc = stubPreparedRestoreBundle("/bundle.tar", &backup.Manifest{
		CreatedAt: fakeNow.Now(), ClusterMode: "standalone", ProxmoxType: "dual", ScriptVersion: "vtest",
	})

	// RECOVERY: wherever pve_cluster is written, it is written through the guarded path.
	ui := &fakeRestoreWorkflowUI{mode: RestoreModeFull, confirmRestore: true, confirmCompatible: true, clusterMode: ClusterRestoreRecovery}
	logger := logging.New(types.LogLevelError, false)
	if err := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logger, "vtest", ui, ""); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI(host=%s): %v", host, err)
	}

	o := roleFilterOutcome{written: map[string]bool{}, exported: map[string]bool{}}
	exportRoot := exportDestRoot("/base")
	for p := range dualArchiveEntries {
		if _, err := fakeFS.ReadFile("/" + p); err == nil {
			o.written[p] = true
		}
		if _, err := fakeFS.ReadFile(filepath.Join(exportRoot, p)); err == nil {
			o.exported[p] = true
		}
	}
	info, _ := environment.DetectWith(environment.DetectOptions{RootPrefix: fakeFS.Root})
	if info != nil {
		o.detectedAfter = info.Type
	}
	return o
}

// A FULL restore of a PVE+PBS archive on a PBS-only host used to write pve_cluster,
// corosync and ceph over the live system, after a compatibility notice promising only
// the categories compatible with the host. config.db alone then made the production
// detection call the host dual. Those categories now go to the export directory.
func TestFullRestoreOnPBSHostExportsThePVECategories(t *testing.T) {
	o := fullRestoreOfDualArchive(t, SystemTypePBS)
	for _, p := range pveOnlyArchivePaths {
		if o.written[p] {
			t.Errorf("FULL restore on a PBS host wrote the PVE-only path /%s", p)
		}
		if !o.exported[p] {
			t.Errorf("FULL restore on a PBS host did not export the PVE-only path /%s", p)
		}
	}
	for _, p := range append(append([]string{}, pbsOnlyArchivePaths...), commonArchivePaths...) {
		if !o.written[p] {
			t.Errorf("FULL restore on a PBS host did not write /%s, which the host runs", p)
		}
	}
	if o.detectedAfter != types.ProxmoxBS {
		t.Errorf("after the restore the PBS host is detected as %q, want %q", o.detectedAfter, types.ProxmoxBS)
	}
}

// The symmetric case: on a PVE-only host the PBS direct-write category
// (maintenance_pbs) used to be written to /etc/proxmox-backup/.
func TestFullRestoreOnPVEHostExportsThePBSCategories(t *testing.T) {
	o := fullRestoreOfDualArchive(t, SystemTypePVE)
	for _, p := range pbsOnlyArchivePaths {
		if o.written[p] {
			t.Errorf("FULL restore on a PVE host wrote the PBS-only path /%s", p)
		}
		if !o.exported[p] {
			t.Errorf("FULL restore on a PVE host did not export the PBS-only path /%s", p)
		}
	}
	for _, p := range append(append([]string{}, pveOnlyArchivePaths...), commonArchivePaths...) {
		if !o.written[p] {
			t.Errorf("FULL restore on a PVE host did not write /%s, which the host runs", p)
		}
	}
	if o.detectedAfter != types.ProxmoxVE {
		t.Errorf("after the restore the PVE host is detected as %q, want %q", o.detectedAfter, types.ProxmoxVE)
	}
}

// Control: the same archive on a dual host, which runs both products, is written in
// full, as before. Only the host changes between this test and the two above.
// maintenance.cfg is exported on every host, and was before: pbs_config is export-only
// over the whole /etc/proxmox-backup/, so only the PVE paths say anything here.
func TestFullRestoreOnDualHostStillWritesBothProducts(t *testing.T) {
	o := fullRestoreOfDualArchive(t, SystemTypeDual)
	for p := range dualArchiveEntries {
		if !o.written[p] {
			t.Errorf("FULL restore on a dual host did not write /%s", p)
		}
	}
	for _, p := range pveOnlyArchivePaths {
		if o.exported[p] {
			t.Errorf("FULL restore on a dual host exported /%s", p)
		}
	}
	if o.detectedAfter != types.ProxmoxDual {
		t.Errorf("after the restore the dual host is detected as %q, want %q", o.detectedAfter, types.ProxmoxDual)
	}
}

// The filter lives in the plan, so it holds for every mode and survives the SAFE
// toggle, which rebuilds the lists from scratch. It applies only when the host is
// known to run one product: a dual or unknown host keeps every category where it was.
func TestPlanRestoreExportsTheOtherProductsCategories(t *testing.T) {
	selected := []Category{
		mustCategoryByID(t, "pve_cluster"),       // PVE, direct write
		mustCategoryByID(t, "storage_pve"),       // PVE, staged
		mustCategoryByID(t, "pve_config_export"), // PVE, export-only
		mustCategoryByID(t, "maintenance_pbs"),   // PBS, direct write
		mustCategoryByID(t, "datastore_pbs"),     // PBS, staged
		mustCategoryByID(t, "ssh"),               // common, direct write
		mustCategoryByID(t, "network"),           // common, staged
	}
	// Compared as sets: the SAFE toggle reorders the lists, which is not what this pins.
	ids := func(cats []Category) string {
		out := make([]string, 0, len(cats))
		for _, c := range cats {
			out = append(out, c.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	set := func(list string) string {
		out := strings.Split(list, ",")
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		host                   SystemType
		normal, staged, export string
	}{
		{SystemTypePBS, "maintenance_pbs,ssh", "datastore_pbs,network", "pve_config_export,pve_cluster,storage_pve"},
		{SystemTypePVE, "pve_cluster,ssh", "storage_pve,network", "pve_config_export,maintenance_pbs,datastore_pbs"},
		{SystemTypeDual, "pve_cluster,maintenance_pbs,ssh", "storage_pve,datastore_pbs,network", "pve_config_export"},
		{SystemTypeUnknown, "pve_cluster,maintenance_pbs,ssh", "storage_pve,datastore_pbs,network", "pve_config_export"},
	} {
		for _, mode := range []RestoreMode{RestoreModeFull, RestoreModeStorage, RestoreModeBase, RestoreModeCustom} {
			plan := PlanRestore(true, selected, tc.host, mode)
			for _, safe := range []bool{false, true, false} {
				plan.ApplyClusterSafeMode(safe)
				wantNormal, wantExport := tc.normal, tc.export
				// SAFE moves pve_cluster to export from wherever it is being written.
				if safe && strings.Contains(wantNormal, "pve_cluster") {
					wantNormal = strings.Replace(wantNormal, "pve_cluster,", "", 1)
					wantExport += ",pve_cluster"
				}
				if got := ids(plan.NormalCategories); got != set(wantNormal) {
					t.Errorf("host=%s mode=%s safe=%v: normal=%s, want %s", tc.host, mode, safe, got, wantNormal)
				}
				if got := ids(plan.StagedCategories); got != set(tc.staged) {
					t.Errorf("host=%s mode=%s safe=%v: staged=%s, want %s", tc.host, mode, safe, got, tc.staged)
				}
				if got := ids(plan.ExportCategories); got != set(wantExport) {
					t.Errorf("host=%s mode=%s safe=%v: export=%s, want %s", tc.host, mode, safe, got, wantExport)
				}
			}
			if tc.host == SystemTypePBS && plan.NeedsClusterRestore {
				t.Errorf("host=%s mode=%s: NeedsClusterRestore with pve_cluster exported", tc.host, mode)
			}
		}
	}
}

// fallbackRestoreOnHost runs the analysis-failure fallback on a host of the given type
// with a live /etc/ceph/ceph.conf and no /etc/corosync, and reports what each of the
// two holds afterwards ("<missing>" when absent).
func fallbackRestoreOnHost(t *testing.T, host SystemType) (hosts, corosync, ceph string) {
	t.Helper()
	origRestoreFS, origRestoreCmd, origRestoreSystem, origCompatFS := restoreFS, restoreCmd, restoreSystem, compatFS
	origPrepare, origAnalyze, origSafetyFS := prepareRestoreBundleFunc, analyzeRestoreArchiveFunc, safetyFS
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreSystem, compatFS = origRestoreFS, origRestoreCmd, origRestoreSystem, origCompatFS
		prepareRestoreBundleFunc, analyzeRestoreArchiveFunc, safetyFS = origPrepare, origAnalyze, origSafetyFS
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS, compatFS, safetyFS = fakeFS, fakeFS, fakeFS
	restoreCmd = runOnlyRunner{}
	restoreSystem = fakeSystemDetector{systemType: host}

	if err := fakeFS.AddFile("/etc/ceph/ceph.conf", []byte("live\n")); err != nil {
		t.Fatal(err)
	}
	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                  "127.0.0.1 localhost\n",
		"etc/corosync/corosync.conf": "totem {}\n",
		"etc/ceph/ceph.conf":         "from-archive\n",
	}); err != nil {
		t.Fatal(err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	prepareRestoreBundleFunc = stubPreparedRestoreBundle("/bundle.tar", &backup.Manifest{
		CreatedAt: time.Unix(1700000000, 0), ClusterMode: "standalone", ProxmoxType: "dual", ScriptVersion: "vtest",
	})
	analyzeRestoreArchiveFunc = func(archivePath string, logger *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
		return nil, nil, errors.New("boom")
	}

	ui := &fakeRestoreWorkflowUI{confirmRestore: true, confirmCompatible: true, continuePBSServices: true}
	logger := logging.New(types.LogLevelError, false)
	if err := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logger, "vtest", ui, ""); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI(host=%s): %v", host, err)
	}
	read := func(p string) string {
		b, err := fakeFS.ReadFile(p)
		if err != nil {
			return "<missing>"
		}
		return strings.TrimSpace(string(b))
	}
	return read("/etc/hosts"), read("/etc/corosync/corosync.conf"), read("/etc/ceph/ceph.conf")
}

// The analysis-failure fallback extracts the whole archive minus a skip list. On a
// PBS-only host it used to write the PVE categories too: those present on the live
// system (ceph here) and those absent from it (corosync here) alike.
func TestFullRestoreFallbackOnPBSHostSkipsThePVECategories(t *testing.T) {
	hosts, corosync, ceph := fallbackRestoreOnHost(t, SystemTypePBS)
	if hosts == "<missing>" {
		t.Errorf("the fallback did not extract /etc/hosts: it must stay a FULL restore")
	}
	if corosync != "<missing>" {
		t.Errorf("the fallback on a PBS host wrote /etc/corosync/corosync.conf, absent from the live system: %q", corosync)
	}
	if ceph != "live" {
		t.Errorf("the fallback on a PBS host overwrote the live /etc/ceph/ceph.conf: %q", ceph)
	}
}

// Control: the same fallback on a dual host writes both, as before.
func TestFullRestoreFallbackOnDualHostStillWritesThePVECategories(t *testing.T) {
	hosts, corosync, ceph := fallbackRestoreOnHost(t, SystemTypeDual)
	if hosts == "<missing>" || corosync != "totem {}" || ceph != "from-archive" {
		t.Errorf("the fallback on a dual host: hosts=%q corosync=%q ceph=%q, want all three from the archive", hosts, corosync, ceph)
	}
}
