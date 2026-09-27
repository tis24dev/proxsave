package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// /etc/hostid is 4 bytes, the host id in little-endian order: `hostid` prints
// 0badc0de for de c0 ad 0b.
var (
	oldHostHostid = []byte{0xde, 0xc0, 0xad, 0x0b}
	thisHostid    = []byte{0x63, 0xed, 0x50, 0xdc}
)

func TestFormatHostid(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{oldHostHostid, "0badc0de"},
		{thisHostid, "dc50ed63"},
		{nil, "none"},
		{[]byte{1, 2}, "0102"},
	} {
		if got := formatHostid(tc.in); got != tc.want {
			t.Errorf("formatHostid(%x) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHostidRestoreDecision(t *testing.T) {
	cases := []struct {
		name    string
		backup  []byte
		live    []byte
		pools   []string
		listErr error
		write   bool
	}{
		{name: "same hostid", backup: thisHostid, live: thisHostid, pools: []string{"rpool"}, write: true},
		{name: "different hostid, pools imported", backup: oldHostHostid, live: thisHostid, pools: []string{"rpool", "vm"}},
		{name: "different hostid, no pool imported", backup: oldHostHostid, live: thisHostid, write: true},
		{name: "no live hostid, pools imported", backup: oldHostHostid, pools: []string{"rpool"}},
		{name: "pools cannot be listed", backup: oldHostHostid, live: thisHostid, listErr: errors.New("exit status 1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write, reason := hostidRestoreDecision(tc.backup, tc.live, tc.pools, tc.listErr)
			if write != tc.write {
				t.Fatalf("write = %v, want %v (reason %q)", write, tc.write, reason)
			}
			if !write && strings.TrimSpace(reason) == "" {
				t.Fatal("a skipped hostid must carry the reason")
			}
		})
	}
}

// runZFSRestore runs the real workflow on a PVE host with the zfs category selected.
func runZFSRestore(t *testing.T, liveHostid []byte, outputs map[string]string, errs map[string]error) bootRestoreRun {
	t.Helper()
	live := map[string]string{"/etc/hostid": string(liveHostid)}
	archive := map[string]string{
		"etc/hostid":           string(oldHostHostid),
		"etc/zfs/zfs-list.txt": "rpool\n",
	}
	return runBootRestore(t, live, archive, outputs, errs, RestoreModeCustom, "zfs")
}

// Reproduced on a PVE 9.2 VM with root on ZFS: with the old host's /etc/hostid in
// place, the next update-initramfs (any kernel or ZFS upgrade) left the host at
// "cannot import 'rpool': pool was previously in use from another system". A host
// whose pools were imported under its own hostid keeps it.
func TestZFSRestoreKeepsHostidWhenPoolsAreImported(t *testing.T) {
	r := runZFSRestore(t, thisHostid, map[string]string{
		"which zpool":           "/usr/sbin/zpool\n",
		"zpool list -H -o name": "rpool\nvm\n",
	}, nil)
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if got := r.read(t, "/etc/hostid"); got != string(thisHostid) {
		t.Fatalf("/etc/hostid overwritten with %x", got)
	}
	if got := r.read(t, "/etc/zfs/zfs-list.txt"); got != "rpool\n" {
		t.Fatalf("control: the rest of the zfs category must be restored: %q", got)
	}
	want := "ZFS hostid - backup value 0badc0de not written: pools rpool, vm use this host's hostid dc50ed63"
	if !strings.Contains(r.log, want) {
		t.Fatalf("log misses %q:\n%s", want, r.log)
	}
	if !strings.Contains(r.log, "Restore completed with warnings.") {
		t.Fatalf("a skipped hostid is a warning:\n%s", r.log)
	}
}

// With no pool imported (moved disks not yet imported, or no ZFS in use) the old
// host's hostid is what those pools were last used under: it is written as before.
func TestZFSRestoreWritesHostidWithoutImportedPools(t *testing.T) {
	for name, outputs := range map[string]map[string]string{
		"zpool lists nothing": {"which zpool": "/usr/sbin/zpool\n", "zpool list -H -o name": ""},
		"zpool not installed": {},
	} {
		t.Run(name, func(t *testing.T) {
			var errs map[string]error
			if name == "zpool not installed" {
				errs = map[string]error{"which zpool": errors.New("exit status 1")}
			}
			r := runZFSRestore(t, thisHostid, outputs, errs)
			if r.err != nil {
				t.Fatalf("restore: %v", r.err)
			}
			if got := r.read(t, "/etc/hostid"); got != string(oldHostHostid) {
				t.Fatalf("/etc/hostid = %x, want the backup's", got)
			}
		})
	}
}

// When the pools cannot be listed the hostid is not written: keeping the host's own
// never leaves it unbootable.
func TestZFSRestoreKeepsHostidWhenPoolsCannotBeListed(t *testing.T) {
	r := runZFSRestore(t, thisHostid, map[string]string{"which zpool": "/usr/sbin/zpool\n"},
		map[string]error{"zpool list -H -o name": errors.New("exit status 1")})
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if got := r.read(t, "/etc/hostid"); got != string(thisHostid) {
		t.Fatalf("/etc/hostid overwritten with %x", got)
	}
	if !strings.Contains(r.log, "ZFS hostid - backup value 0badc0de not written: this host's pools cannot be listed") {
		t.Fatalf("log:\n%s", r.log)
	}
}

// The analysis-failure fallback applies the same rules to /etc/hostid and the pool
// cache.
func TestFullRestoreFallbackKeepsZFSHostFilesWhenPoolsAreImported(t *testing.T) {
	origRestoreFS, origRestoreCmd, origRestoreSystem := restoreFS, restoreCmd, restoreSystem
	origCompatFS, origPrepare, origAnalyze, origSafetyFS := compatFS, prepareRestoreBundleFunc, analyzeRestoreArchiveFunc, safetyFS
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreSystem = origRestoreFS, origRestoreCmd, origRestoreSystem
		compatFS, prepareRestoreBundleFunc, analyzeRestoreArchiveFunc, safetyFS = origCompatFS, origPrepare, origAnalyze, origSafetyFS
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS, compatFS, safetyFS = fakeFS, fakeFS, fakeFS
	restoreCmd = &FakeCommandRunner{Outputs: map[string][]byte{
		"which zpool":           []byte("/usr/sbin/zpool\n"),
		"zpool list -H -o name": []byte("rpool\n"),
	}}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	if err := fakeFS.AddFile("/etc/hostid", thisHostid); err != nil {
		t.Fatal(err)
	}
	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := fakeFS.AddFile("/etc/zfs/zpool.cache", []byte("this host's cache\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":           "127.0.0.1 localhost\n",
		"etc/hostid":          string(oldHostHostid),
		"etc/zfs/zpool.cache": "old host's cache\n",
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
		CreatedAt: time.Unix(1700000000, 0), ClusterMode: "standalone", ProxmoxType: "pve", ScriptVersion: "vtest",
	})
	analyzeRestoreArchiveFunc = func(string, *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
		return nil, nil, errors.New("boom")
	}
	ui := &fakeRestoreWorkflowUI{confirmRestore: true, confirmCompatible: true}
	if err := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logging.New(types.LogLevelError, false), "vtest", ui, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := fakeFS.ReadFile("/etc/hosts"); err != nil {
		t.Fatalf("control: the fallback must still extract /etc/hosts: %v", err)
	}
	if got, _ := fakeFS.ReadFile("/etc/hostid"); string(got) != string(thisHostid) {
		t.Fatalf("/etc/hostid overwritten with %x", got)
	}
	if got, _ := fakeFS.ReadFile("/etc/zfs/zpool.cache"); string(got) != "this host's cache\n" {
		t.Fatalf("/etc/zfs/zpool.cache overwritten: %q", got)
	}
}

func TestZFSCacheRestoreDecision(t *testing.T) {
	if write, _ := zfsCacheRestoreDecision(nil, nil); !write {
		t.Fatal("no pool imported: the backup's cache must be written")
	}
	if write, reason := zfsCacheRestoreDecision([]string{"vm", "rpool"}, nil); write || !strings.Contains(reason, "pools rpool, vm are imported on this host") {
		t.Fatalf("pools imported: write=%v reason=%q", write, reason)
	}
	if write, reason := zfsCacheRestoreDecision(nil, errors.New("exit status 1")); write || !strings.Contains(reason, "cannot be listed") {
		t.Fatalf("listing failed: write=%v reason=%q", write, reason)
	}
}

// runZFSCacheRestore restores the zfs category over a host that already has its own
// pool cache, from an archive holding another host's cache files.
func runZFSCacheRestore(t *testing.T, outputs map[string]string, errs map[string]error) bootRestoreRun {
	t.Helper()
	live := map[string]string{
		"/etc/hostid":                  string(thisHostid),
		"/etc/zfs/zpool.cache":         "this host's cache\n",
		"/etc/zfs/zfs-list.cache/tank": "this host's mount list\n",
	}
	archive := map[string]string{
		"etc/hostid":                   string(thisHostid),
		"etc/zfs/zpool.cache":          "old host's cache\n",
		"etc/zfs/zfs-list.cache/rpool": "old host's mount list\n",
		"etc/zfs/zed.d/zed.rc":         "ZED_EMAIL_ADDR=root\n",
	}
	return runBootRestore(t, live, archive, outputs, errs, RestoreModeCustom, "zfs")
}

// Reproduced on a PVE 9.2 VM with root on ZFS and a data pool: another host's
// zpool.cache made zfs-import-cache.service fail ("cannot import 'rpool': pool
// already exists") and the data pool was not imported at boot; a pool that is not a
// PVE storage (a PBS datastore, a manual mount) stayed out. A host with pools
// imported keeps its own cache files.
func TestZFSRestoreKeepsPoolCacheWhenPoolsAreImported(t *testing.T) {
	r := runZFSCacheRestore(t, map[string]string{
		"which zpool":           "/usr/sbin/zpool\n",
		"zpool list -H -o name": "rpool\nvm\n",
	}, nil)
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if got := r.read(t, "/etc/zfs/zpool.cache"); got != "this host's cache\n" {
		t.Fatalf("/etc/zfs/zpool.cache overwritten: %q", got)
	}
	r.assertAbsent(t, "/etc/zfs/zfs-list.cache/rpool")
	if got := r.read(t, "/etc/zfs/zfs-list.cache/tank"); got != "this host's mount list\n" {
		t.Fatalf("this host's zfs-list.cache touched: %q", got)
	}
	if got := r.read(t, "/etc/zfs/zed.d/zed.rc"); got != "ZED_EMAIL_ADDR=root\n" {
		t.Fatalf("control: the rest of /etc/zfs must be restored: %q", got)
	}
	want := "ZFS pool cache - backup zpool.cache and zfs-list.cache not written: pools rpool, vm are imported on this host"
	if !strings.Contains(r.log, want) {
		t.Fatalf("log misses %q:\n%s", want, r.log)
	}
}

func TestZFSRestoreWritesPoolCacheWithoutImportedPools(t *testing.T) {
	r := runZFSCacheRestore(t, map[string]string{"which zpool": "/usr/sbin/zpool\n", "zpool list -H -o name": ""}, nil)
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if got := r.read(t, "/etc/zfs/zpool.cache"); got != "old host's cache\n" {
		t.Fatalf("/etc/zfs/zpool.cache = %q, want the backup's", got)
	}
	if got := r.read(t, "/etc/zfs/zfs-list.cache/rpool"); got != "old host's mount list\n" {
		t.Fatalf("zfs-list.cache/rpool = %q, want the backup's", got)
	}
}
