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
	ok := NewFilesystemDetector(newTestLogger(), WithProbeOwner(os.Getuid(), os.Getgid(), 0o600)).testOwnershipSupport(context.Background(), dir)
	if !ok {
		t.Fatal("probe to the current owner failed on a local directory")
	}
	refused := NewFilesystemDetector(newTestLogger(), WithProbeOwner(os.Getuid()+1, os.Getgid(), 0o600)).testOwnershipSupport(context.Background(), dir)
	if refused {
		t.Fatal("probe to an owner the process cannot set passed")
	}
}

// The probe chmods its file to the mode the run applies and reads that mode back:
// with SET_BACKUP_PERMISSIONS the files get 0640, so 0640 is what must hold.
func TestOwnershipProbeAppliesTheConfiguredMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0o600, 0o640} {
		d := NewFilesystemDetector(newTestLogger(), WithProbeOwner(os.Getuid(), os.Getgid(), mode))
		if !d.testOwnershipSupport(context.Background(), dir) {
			t.Fatalf("probe with mode %04o failed on a local directory", mode)
		}
	}
	if got := NewFilesystemDetector(newTestLogger()).probeMode; got != 0o600 {
		t.Fatalf("default probe mode = %04o, want 0600 (the root-only backup files)", got)
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
// fails the probe; so does one that ignores chmod, or keeps a mode other than the
// one the run applies (a mount forcing 0600 when SET_BACKUP_PERMISSIONS asks 0640).
func TestProbeResultChecksOwnerAndMode(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
		info probeFileInfo
		want bool
	}{
		{"owner and mode applied", 0o600, probeFileInfo{0o600, &syscall.Stat_t{Uid: 34, Gid: 34}}, true},
		{"owner and shared mode applied", 0o640, probeFileInfo{0o640, &syscall.Stat_t{Uid: 34, Gid: 34}}, true},
		{"uid ignored", 0o600, probeFileInfo{0o600, &syscall.Stat_t{Uid: 0, Gid: 34}}, false},
		{"gid ignored", 0o600, probeFileInfo{0o600, &syscall.Stat_t{Uid: 34, Gid: 0}}, false},
		{"mode ignored", 0o600, probeFileInfo{0o644, &syscall.Stat_t{Uid: 34, Gid: 34}}, false},
		{"mount forces 0600, run applies 0640", 0o640, probeFileInfo{0o600, &syscall.Stat_t{Uid: 34, Gid: 34}}, false},
		{"mount forces 0640, run applies 0600", 0o600, probeFileInfo{0o640, &syscall.Stat_t{Uid: 34, Gid: 34}}, false},
		{"no stat data", 0o600, probeFileInfo{0o600, nil}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := probeTookEffect(tc.info, 34, 34, tc.mode); got != tc.want {
				t.Fatalf("probeTookEffect = %v, want %v", got, tc.want)
			}
		})
	}
}

// BackupOwnerProbe gives the probe BACKUP_USER/BACKUP_GROUP and mode 0640 only when
// SET_BACKUP_PERMISSIONS resolves them; otherwise the probe keeps the current user
// and mode 0600, the owner and mode the backup files get.
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
		wantMode := os.FileMode(0o600)
		if d.probeOwnerSet {
			wantMode = 0o640
		}
		if d.probeMode != wantMode {
			t.Fatalf("probe mode = %04o with owner set=%v, want %04o", d.probeMode, d.probeOwnerSet, wantMode)
		}
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
	// The configured owner must differ from the running identity, or a probe that
	// took the running user instead of resolving the names would pass too.
	other, otherGroup := otherUserAndGroup(t)
	set, uid, gid := probeOwner(&config.Config{SetBackupPermissions: true, BackupUser: other.Username, BackupGroup: otherGroup.Name})
	if !set || strconv.Itoa(uid) != other.Uid || strconv.Itoa(gid) != otherGroup.Gid {
		t.Fatalf("resolved owner: set=%v uid=%d gid=%d, want true %s %s", set, uid, gid, other.Uid, otherGroup.Gid)
	}
}

// otherUserAndGroup returns a user and a group that exist on the host and are not the
// running uid and gid, looked up by id (root, nobody/nogroup, daemon), so the test does
// not depend on how a distribution names them.
func otherUserAndGroup(t *testing.T) (*user.User, *user.Group) {
	t.Helper()
	var u *user.User
	var g *user.Group
	for _, id := range []string{"0", "65534", "1"} {
		if u == nil && id != strconv.Itoa(os.Getuid()) {
			if found, err := user.LookupId(id); err == nil {
				u = found
			}
		}
		if g == nil && id != strconv.Itoa(os.Getgid()) {
			if found, err := user.LookupGroupId(id); err == nil {
				g = found
			}
		}
	}
	if u == nil || g == nil {
		t.Skip("no user and group other than the running ones can be resolved")
	}
	return u, g
}
