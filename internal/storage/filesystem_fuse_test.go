package storage

import (
	"context"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// Issue 355: every fuse.* type was "no ownership" with no probe, so a mergerfs pool
// (chown/chmod passed to XFS/ext4 branches) never got BACKUP_USER/BACKUP_GROUP. FUSE
// is now probed like NFS/CIFS/Ceph, without being labelled a network filesystem, and
// no longer auto-excluded, so the probe alone decides.
func TestFUSEIsProbedAndNotANetworkFilesystem(t *testing.T) {
	if got := parseFilesystemType("fuse.mergerfs"); got != FilesystemFUSE {
		t.Fatalf("parseFilesystemType(fuse.mergerfs) = %q, want fuse", got)
	}
	if !FilesystemFUSE.NeedsOwnershipProbe() {
		t.Fatal("FUSE must need the ownership probe")
	}
	if FilesystemFUSE.IsNetworkFilesystem() {
		t.Fatal("FUSE must not be labelled a network filesystem")
	}
	if FilesystemFUSE.ShouldAutoExclude() {
		t.Fatal("FUSE must not be auto-excluded: the probe decides")
	}
	for _, probe := range []bool{true, false} {
		called := false
		d := NewFilesystemDetector(newTestLogger())
		d.mountPointLookup = func(string) (string, error) { return "/mnt/pool", nil }
		d.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
			return FilesystemFUSE, "pool", nil
		}
		d.ownershipSupportTest = func(context.Context, string) bool { called = true; return probe }
		info, err := d.DetectFilesystem(context.Background(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatalf("probe %v: the ownership probe did not run on FUSE", probe)
		}
		if info.SupportsOwnership != probe {
			t.Fatalf("probe %v: SupportsOwnership = %v", probe, info.SupportsOwnership)
		}
		if info.IsNetworkFS {
			t.Fatalf("probe %v: FUSE reported as a network filesystem", probe)
		}
	}
}

// The probe set is the network filesystems plus FUSE; a local POSIX type is never
// probed, and CIFS/SMB stay auto-excluded whatever the probe says.
func TestNeedsOwnershipProbeSet(t *testing.T) {
	for _, f := range []FilesystemType{FilesystemNFS, FilesystemNFS4, FilesystemCIFS, FilesystemSMB, FilesystemCeph, FilesystemFUSE} {
		if !f.NeedsOwnershipProbe() {
			t.Fatalf("%s: NeedsOwnershipProbe = false, want true", f)
		}
	}
	for _, f := range []FilesystemType{FilesystemExt4, FilesystemXFS, FilesystemZFS, FilesystemNTFS, FilesystemFAT32, FilesystemUnknown} {
		if f.NeedsOwnershipProbe() {
			t.Fatalf("%s: NeedsOwnershipProbe = true, want false", f)
		}
	}
	for _, f := range []FilesystemType{FilesystemCIFS, FilesystemSMB} {
		if !f.ShouldAutoExclude() {
			t.Fatalf("%s: ShouldAutoExclude = false, want true", f)
		}
	}
}

// A dry run writes no probe file on FUSE either: the type default (no ownership) stays.
func TestFUSEProbeSkippedInDryRun(t *testing.T) {
	d := NewFilesystemDetector(newTestLogger(), WithDryRun(true))
	d.mountPointLookup = func(string) (string, error) { return "/mnt/pool", nil }
	d.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
		return FilesystemFUSE, "pool", nil
	}
	d.ownershipSupportTest = func(context.Context, string) bool {
		t.Fatal("probe ran in dry run")
		return true
	}
	info, err := d.DetectFilesystem(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.SupportsOwnership {
		t.Fatal("dry run: SupportsOwnership = true, want the FUSE default false")
	}
}

// The probe chowns to the owner the run will apply. An owner the process cannot
// give a file makes the probe fail; the current user passes.
func TestOwnershipProbeChownsToTheConfiguredOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown to any uid; the refusal case needs an unprivileged user")
	}
	dir := t.TempDir()
	ok := NewFilesystemDetector(newTestLogger(), WithProbeOwner(os.Getuid(), os.Getgid())).testOwnershipSupport(context.Background(), dir)
	if !ok {
		t.Fatal("probe to the current owner failed on a local directory")
	}
	refused := NewFilesystemDetector(newTestLogger(), WithProbeOwner(os.Getuid()+1, os.Getgid())).testOwnershipSupport(context.Background(), dir)
	if refused {
		t.Fatal("probe to an owner the process cannot set passed")
	}
}

type probeFileInfo struct {
	mode fs.FileMode
	sys  any
}

func (p probeFileInfo) Name() string       { return "probe" }
func (p probeFileInfo) Size() int64        { return 0 }
func (p probeFileInfo) Mode() fs.FileMode  { return p.mode }
func (p probeFileInfo) ModTime() time.Time { return time.Time{} }
func (p probeFileInfo) IsDir() bool        { return false }
func (p probeFileInfo) Sys() any           { return p.sys }

// A filesystem that accepts chown and ignores it (the owner reads back unchanged)
// fails the probe; so does one that ignores chmod.
func TestProbeResultChecksOwnerAndMode(t *testing.T) {
	cases := []struct {
		name string
		info probeFileInfo
		want bool
	}{
		{"owner and mode applied", probeFileInfo{0o600, &syscall.Stat_t{Uid: 34, Gid: 34}}, true},
		{"uid ignored", probeFileInfo{0o600, &syscall.Stat_t{Uid: 0, Gid: 34}}, false},
		{"gid ignored", probeFileInfo{0o600, &syscall.Stat_t{Uid: 34, Gid: 0}}, false},
		{"mode ignored", probeFileInfo{0o644, &syscall.Stat_t{Uid: 34, Gid: 34}}, false},
		{"no stat data", probeFileInfo{0o600, nil}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := probeTookEffect(tc.info, 34, 34); got != tc.want {
				t.Fatalf("probeTookEffect = %v, want %v", got, tc.want)
			}
		})
	}
}

// BackupOwnerProbe gives the probe BACKUP_USER/BACKUP_GROUP only when
// SET_BACKUP_PERMISSIONS resolves them; otherwise the probe keeps the current user.
func TestBackupOwnerProbe(t *testing.T) {
	cur, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	grp, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skip(err)
	}
	probeOwner := func(cfg *config.Config) (bool, int, int) {
		d := NewFilesystemDetector(newTestLogger(), BackupOwnerProbe(cfg))
		return d.probeOwnerSet, d.probeUID, d.probeGID
	}
	for name, cfg := range map[string]*config.Config{
		"nil config":    nil,
		"flag off":      {SetBackupPermissions: false, BackupUser: cur.Username, BackupGroup: grp.Name},
		"empty user":    {SetBackupPermissions: true, BackupGroup: grp.Name},
		"unknown user":  {SetBackupPermissions: true, BackupUser: "proxsave-no-such-user-355", BackupGroup: grp.Name},
		"unknown group": {SetBackupPermissions: true, BackupUser: cur.Username, BackupGroup: "proxsave-no-such-group-355"},
	} {
		if set, _, _ := probeOwner(cfg); set {
			t.Fatalf("%s: probe owner set, want the current user", name)
		}
	}
	set, uid, gid := probeOwner(&config.Config{SetBackupPermissions: true, BackupUser: cur.Username, BackupGroup: grp.Name})
	if !set || uid != os.Getuid() || gid != os.Getgid() {
		t.Fatalf("resolved owner: set=%v uid=%d gid=%d, want true %d %d", set, uid, gid, os.Getuid(), os.Getgid())
	}
}
