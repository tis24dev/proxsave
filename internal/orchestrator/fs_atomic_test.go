package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// rollbackFailFS wraps FakeFS to fail OpenFile for specific temp paths only AFTER a
// designated rename has been attempted (the forward commit phase has run). This lets a
// test break the rollback writes without also breaking the forward prepare, which share
// the same temp name under a pinned clock.
type rollbackFailFS struct {
	*FakeFS
	tripOnRename      string
	tripped           bool
	failOpenAfterTrip map[string]bool
}

func (r *rollbackFailFS) Rename(oldpath, newpath string) error {
	if filepath.Clean(oldpath) == r.tripOnRename {
		r.tripped = true
	}
	return r.FakeFS.Rename(oldpath, newpath)
}

func (r *rollbackFailFS) OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	if r.tripped && r.failOpenAfterTrip[filepath.Clean(path)] {
		return nil, errors.New("forced rollback open failure: " + filepath.Clean(path))
	}
	return r.FakeFS.OpenFile(path, flag, perm)
}

type statOverrideFS struct {
	FS
	Infos  map[string]os.FileInfo
	Errors map[string]error
}

func (s statOverrideFS) Stat(path string) (os.FileInfo, error) {
	clean := filepath.Clean(path)
	if s.Errors != nil {
		if err, ok := s.Errors[clean]; ok {
			return nil, err
		}
	}
	if s.Infos != nil {
		if info, ok := s.Infos[clean]; ok {
			return info, nil
		}
	}
	return s.FS.Stat(path)
}

type fakeFileInfo struct {
	path  string
	mode  os.FileMode
	isDir bool
	uid   int
	gid   int
}

func (f fakeFileInfo) Name() string { return filepath.Base(f.path) }
func (f fakeFileInfo) Size() int64  { return 0 }
func (f fakeFileInfo) Mode() os.FileMode {
	mode := f.mode
	if f.isDir {
		mode |= os.ModeDir
	}
	return mode
}
func (f fakeFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() interface{} {
	return &syscall.Stat_t{Uid: uint32(f.uid), Gid: uint32(f.gid)}
}

func TestWriteFileAtomic_EnforcesModeDespiteUmask(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(10, 0)}

	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	destPath := "/etc/proxmox-backup/user.cfg"
	if err := writeFileAtomic(destPath, []byte("test\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	info, err := fakeFS.Stat(destPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode=%#o, want %#o", got, 0o640)
	}
}

func TestWriteFileAtomic_PreservesOwnershipFromExistingDest(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	origGeteuid := atomicGeteuid
	origChown := atomicFileChown
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		atomicGeteuid = origGeteuid
		atomicFileChown = origChown
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreTime = &FakeTime{Current: time.Unix(11, 0)}

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}

	destPath := "/etc/proxmox-backup/user.cfg"
	restoreFS = statOverrideFS{
		FS: fakeFS,
		Infos: map[string]os.FileInfo{
			filepath.Clean(parentDir): fakeFileInfo{path: parentDir, mode: 0o755, isDir: true, uid: 777, gid: 888},
			filepath.Clean(destPath):  fakeFileInfo{path: destPath, mode: 0o640, uid: 123, gid: 456},
		},
	}

	atomicGeteuid = func() int { return 0 }
	var got []uidGid
	atomicFileChown = func(f *os.File, uid, gid int) error {
		got = append(got, uidGid{uid: uid, gid: gid, ok: true})
		return nil
	}

	if err := writeFileAtomic(destPath, []byte("new\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("chown calls=%d, want %d", len(got), 1)
	}
	if got[0].uid != 123 || got[0].gid != 456 {
		t.Fatalf("chown uid/gid=%d:%d, want %d:%d", got[0].uid, got[0].gid, 123, 456)
	}
}

func TestWriteFileAtomic_RepairsGroupFromParentWhenDestHasRootGroup(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	origGeteuid := atomicGeteuid
	origChown := atomicFileChown
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		atomicGeteuid = origGeteuid
		atomicFileChown = origChown
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreTime = &FakeTime{Current: time.Unix(12, 0)}

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}

	destPath := "/etc/proxmox-backup/user.cfg"
	restoreFS = statOverrideFS{
		FS: fakeFS,
		Infos: map[string]os.FileInfo{
			filepath.Clean(parentDir): fakeFileInfo{path: parentDir, mode: 0o700, isDir: true, uid: 777, gid: 888},
			filepath.Clean(destPath):  fakeFileInfo{path: destPath, mode: 0o640, uid: 123, gid: 0},
		},
	}

	atomicGeteuid = func() int { return 0 }
	var got []uidGid
	atomicFileChown = func(f *os.File, uid, gid int) error {
		got = append(got, uidGid{uid: uid, gid: gid, ok: true})
		return nil
	}

	if err := writeFileAtomic(destPath, []byte("new\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("chown calls=%d, want %d", len(got), 1)
	}
	if got[0].uid != 123 || got[0].gid != 888 {
		t.Fatalf("chown uid/gid=%d:%d, want %d:%d", got[0].uid, got[0].gid, 123, 888)
	}
}

func TestWriteFileAtomic_InheritsGroupFromParentWhenDestMissing(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	origGeteuid := atomicGeteuid
	origChown := atomicFileChown
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		atomicGeteuid = origGeteuid
		atomicFileChown = origChown
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreTime = &FakeTime{Current: time.Unix(12, 0)}

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}

	destPath := "/etc/proxmox-backup/remote.cfg"
	restoreFS = statOverrideFS{
		FS: fakeFS,
		Infos: map[string]os.FileInfo{
			filepath.Clean(parentDir): fakeFileInfo{path: parentDir, mode: 0o700, isDir: true, uid: 777, gid: 888},
		},
	}

	atomicGeteuid = func() int { return 0 }
	var got []uidGid
	atomicFileChown = func(f *os.File, uid, gid int) error {
		got = append(got, uidGid{uid: uid, gid: gid, ok: true})
		return nil
	}

	if err := writeFileAtomic(destPath, []byte("remote: test\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("chown calls=%d, want %d", len(got), 1)
	}
	if got[0].uid != 0 || got[0].gid != 888 {
		t.Fatalf("chown uid/gid=%d:%d, want %d:%d", got[0].uid, got[0].gid, 0, 888)
	}
}

// REFUTER: force committed==true (rename succeeds, directory fsync fails) at a middle
// index and assert (a) the live file at that index is reverted, (b) earlier committed
// files are reverted, (c) later untouched files stay original, (d) NO temp leaks, and
// (e) the rollback temp re-create does NOT collide on O_EXCL with the (now-consumed)
// original temp name despite the pinned fixed clock.
func TestWriteFilesAtomic_CommittedTrueDirFsyncFailRollsBackLiveFile(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	origSync := atomicFileSync
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		atomicFileSync = origSync
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(10, 0)}

	pA := "/etc/auth/a"
	pB := "/etc/auth/b"
	pC := "/etc/auth/c"
	if err := fakeFS.MkdirAll("/etc/auth", 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	for p, orig := range map[string]string{pA: "A-orig\n", pB: "B-orig\n", pC: "C-orig\n"} {
		if err := fakeFS.WriteFile(p, []byte(orig), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	// Fail ONLY B's dir fsync (the second directory-fsync call): A's commit (call 1)
	// succeeds, B's commit renames OK but its dir fsync fails -> committed==true at
	// index 1. All later dir fsyncs (calls 3+, performed by the rollback writes) must
	// succeed so we exercise the rollback-SUCCESS path, not the failing-device path.
	calls := 0
	atomicFileSync = func(f *os.File) error {
		if f == nil {
			return nil
		}
		info, err := f.Stat()
		if err == nil && info != nil && info.IsDir() {
			calls++
			if calls == 2 {
				return errors.New("forced dir fsync failure") // B's dir fsync fails -> committed==true at index 1
			}
			return nil
		}
		return nil
	}

	writes := []atomicFileWrite{
		{path: pA, data: []byte("A-new\n"), original: []byte("A-orig\n"), perm: 0o644},
		{path: pB, data: []byte("B-new\n"), original: []byte("B-orig\n"), perm: 0o644},
		{path: pC, data: []byte("C-new\n"), original: []byte("C-orig\n"), perm: 0o644},
	}
	err := writeFilesAtomic(writes)
	if err == nil {
		t.Fatal("expected an error when a committed file's dir fsync fails")
	}
	if strings.Contains(err.Error(), "CRITICAL") {
		t.Fatalf("rollback should have succeeded (not CRITICAL): %v", err)
	}
	// F06-08: a single-fault (rollback succeeded) is a LEGITIMATE warning, NOT the inconsistent
	// state -> it must NOT wrap ErrRestoreInconsistentState, so it stays downgradable.
	if errors.Is(err, ErrRestoreInconsistentState) {
		t.Fatalf("single-fault rollback-succeeded error must NOT wrap ErrRestoreInconsistentState: %v", err)
	}

	// A committed then was rolled back; B committed (rename ok) then dir-fsync failed,
	// so B is live and must ALSO be reverted; C never committed.
	for p, want := range map[string]string{pA: "A-orig\n", pB: "B-orig\n", pC: "C-orig\n"} {
		got, rerr := fakeFS.ReadFile(p)
		if rerr != nil {
			t.Fatalf("read %s: %v", p, rerr)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want rolled-back original %q", p, string(got), want)
		}
	}

	// No temp may leak. Scan the dir for any .proxsave.tmp. residue.
	entries, derr := fakeFS.ReadDir("/etc/auth")
	if derr != nil {
		t.Fatalf("readdir: %v", derr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".proxsave.tmp.") {
			t.Errorf("leaked temp file: %s", e.Name())
		}
	}
}

// REFUTER: rollback-of-rollback. Force a commit failure that triggers rollback, and
// make the rollback writes ALSO fail; assert a CRITICAL error, that ALL rollbacks are
// still attempted (no early return / panic / infinite loop), and the later temps are
// still cleaned.
func TestWriteFilesAtomic_RollbackAlsoFailsReturnsCritical(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		_ = os.RemoveAll(fakeFS.Root)
	})
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(10, 0)}

	pA := "/etc/auth/a"
	pB := "/etc/auth/b"
	pC := "/etc/auth/c"
	if err := fakeFS.MkdirAll("/etc/auth", 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	for _, p := range []string{pA, pB, pC} {
		if err := fakeFS.WriteFile(p, []byte("orig\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	tmp := func(p string) string {
		return filepath.Clean(p) + ".proxsave.tmp." + "10000000000" // time.Unix(10,0).UnixNano()
	}
	// C's rename fails -> rollback of A and B. Because the clock is pinned, the rollback
	// temp name for A/B is IDENTICAL to the name used in the forward prepare/commit, so a
	// static OpenFileErr keyed on that name would also break the forward prepare. To fail
	// ONLY the rollback writes we wrap the FS and start rejecting OpenFile for A/B's temp
	// after the forward commit phase has run (tripped once C's rename is requested).
	cf := &rollbackFailFS{FakeFS: fakeFS, failOpenAfterTrip: map[string]bool{tmp(pA): true, tmp(pB): true}}
	cf.RenameErr[tmp(pC)] = errors.New("forced C rename failure")
	// Trip the gate when C's (failing) rename is attempted.
	cf.tripOnRename = tmp(pC)
	restoreFS = cf

	writes := []atomicFileWrite{
		{path: pA, data: []byte("A-new\n"), original: []byte("A-orig\n"), perm: 0o644},
		{path: pB, data: []byte("B-new\n"), original: []byte("B-orig\n"), perm: 0o644},
		{path: pC, data: []byte("C-new\n"), original: []byte("C-orig\n"), perm: 0o644},
	}
	err := writeFilesAtomic(writes)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "CRITICAL") {
		t.Fatalf("expected CRITICAL error when rollbacks fail, got: %v", err)
	}
	// BOTH failed rollbacks must be named (proves the loop did not stop at the first).
	if !strings.Contains(err.Error(), pA) || !strings.Contains(err.Error(), pB) {
		t.Errorf("CRITICAL error should list both failed rollbacks %s and %s, got: %v", pA, pB, err)
	}
	// F06-08: the both-failed (inconsistent-state) branch must be a TYPED error so the restore
	// aborts instead of downgrading to "completed with warnings".
	if !errors.Is(err, ErrRestoreInconsistentState) {
		t.Errorf("both-failed error must wrap ErrRestoreInconsistentState, got: %v", err)
	}
}

func TestEnsureDirExistsWithInheritedMeta_CreatesNestedDirsWithInheritedMeta(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	t.Cleanup(func() {
		restoreFS = origFS
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}

	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	destDir := "/etc/proxmox-backup/a/b/c"
	if err := ensureDirExistsWithInheritedMeta(destDir); err != nil {
		t.Fatalf("ensureDirExistsWithInheritedMeta: %v", err)
	}

	created := []string{
		"/etc/proxmox-backup/a",
		"/etc/proxmox-backup/a/b",
		"/etc/proxmox-backup/a/b/c",
	}
	for _, path := range created {
		info, err := fakeFS.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info == nil || !info.IsDir() {
			t.Fatalf("%s not a directory", path)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s mode=%#o, want %#o", path, got, 0o700)
		}
	}
}

func TestEnsureDirExistsWithInheritedMeta_ChownsNewDirsWhenSimulatedRoot(t *testing.T) {
	fakeFS := NewFakeFS()
	origFS := restoreFS
	origGeteuid := atomicGeteuid
	origChown := atomicFileChown
	t.Cleanup(func() {
		restoreFS = origFS
		atomicGeteuid = origGeteuid
		atomicFileChown = origChown
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS
	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}

	atomicGeteuid = func() int { return 0 }
	var got []uidGid
	atomicFileChown = func(f *os.File, uid, gid int) error {
		got = append(got, uidGid{uid: uid, gid: gid, ok: true})
		return nil
	}

	destDir := "/etc/proxmox-backup/a/b"
	if err := ensureDirExistsWithInheritedMeta(destDir); err != nil {
		t.Fatalf("ensureDirExistsWithInheritedMeta: %v", err)
	}

	wantUID := os.Getuid()
	wantGID := os.Getgid()
	if len(got) != 2 {
		t.Fatalf("chown calls=%d, want %d", len(got), 2)
	}
	for idx, call := range got {
		if call.uid != wantUID || call.gid != wantGID {
			t.Fatalf("chown[%d] uid/gid=%d:%d, want %d:%d", idx, call.uid, call.gid, wantUID, wantGID)
		}
	}

	for _, path := range []string{"/etc/proxmox-backup/a", "/etc/proxmox-backup/a/b"} {
		info, err := fakeFS.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info == nil || !info.IsDir() {
			t.Fatalf("%s not a directory", path)
		}
	}
}
