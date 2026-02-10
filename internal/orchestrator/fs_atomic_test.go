package orchestrator

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
