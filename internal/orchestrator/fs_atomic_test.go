package orchestrator

import (
	"os"
	"syscall"
	"testing"
	"time"
)

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
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(11, 0)}

	destPath := "/etc/proxmox-backup/user.cfg"
	if err := fakeFS.WriteFile(destPath, []byte("old\n"), 0o640); err != nil {
		t.Fatalf("seed dest: %v", err)
	}
	if err := os.Chown(fakeFS.onDisk(destPath), 123, 456); err != nil {
		t.Fatalf("chown seed dest: %v", err)
	}
	if err := os.Chmod(fakeFS.onDisk(destPath), 0o640); err != nil {
		t.Fatalf("chmod seed dest: %v", err)
	}

	if err := writeFileAtomic(destPath, []byte("new\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	info, err := fakeFS.Stat(destPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	owner := uidGidFromFileInfo(info)
	if !owner.ok {
		t.Fatalf("uid/gid not available from fileinfo")
	}
	if owner.uid != 123 || owner.gid != 456 {
		t.Fatalf("uid/gid=%d:%d, want %d:%d", owner.uid, owner.gid, 123, 456)
	}
}

func TestWriteFileAtomic_RepairsGroupFromParentWhenDestHasRootGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(12, 0)}

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}
	if err := os.Chown(fakeFS.onDisk(parentDir), 777, 888); err != nil {
		t.Fatalf("chown seed parent dir: %v", err)
	}
	if err := os.Chmod(fakeFS.onDisk(parentDir), 0o700); err != nil {
		t.Fatalf("chmod seed parent dir: %v", err)
	}

	destPath := "/etc/proxmox-backup/user.cfg"
	if err := fakeFS.WriteFile(destPath, []byte("old\n"), 0o640); err != nil {
		t.Fatalf("seed dest: %v", err)
	}
	if err := os.Chown(fakeFS.onDisk(destPath), 0, 0); err != nil {
		t.Fatalf("chown seed dest: %v", err)
	}
	if err := os.Chmod(fakeFS.onDisk(destPath), 0o640); err != nil {
		t.Fatalf("chmod seed dest: %v", err)
	}

	if err := writeFileAtomic(destPath, []byte("new\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	info, err := fakeFS.Stat(destPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	owner := uidGidFromFileInfo(info)
	if !owner.ok {
		t.Fatalf("uid/gid not available from fileinfo")
	}
	if owner.uid != 0 || owner.gid != 888 {
		t.Fatalf("uid/gid=%d:%d, want %d:%d", owner.uid, owner.gid, 0, 888)
	}
}

func TestWriteFileAtomic_InheritsGroupFromParentWhenDestMissing(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	fakeFS := NewFakeFS()
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(12, 0)}

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}
	if err := os.Chown(fakeFS.onDisk(parentDir), 777, 888); err != nil {
		t.Fatalf("chown seed parent dir: %v", err)
	}
	if err := os.Chmod(fakeFS.onDisk(parentDir), 0o700); err != nil {
		t.Fatalf("chmod seed parent dir: %v", err)
	}

	destPath := "/etc/proxmox-backup/remote.cfg"
	if err := writeFileAtomic(destPath, []byte("remote: test\n"), 0o640); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	info, err := fakeFS.Stat(destPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	owner := uidGidFromFileInfo(info)
	if !owner.ok {
		t.Fatalf("uid/gid not available from fileinfo")
	}
	if owner.uid != 0 || owner.gid != 888 {
		t.Fatalf("uid/gid=%d:%d, want %d:%d", owner.uid, owner.gid, 0, 888)
	}
}

func TestEnsureDirExistsWithInheritedMeta_CreatesNestedDirsWithInheritedMeta(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	fakeFS := NewFakeFS()
	origFS := restoreFS
	t.Cleanup(func() {
		restoreFS = origFS
		_ = os.RemoveAll(fakeFS.Root)
	})

	restoreFS = fakeFS

	parentDir := "/etc/proxmox-backup"
	if err := fakeFS.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}
	if err := os.Chown(fakeFS.onDisk(parentDir), 777, 888); err != nil {
		t.Fatalf("chown seed parent dir: %v", err)
	}
	if err := os.Chmod(fakeFS.onDisk(parentDir), 0o755); err != nil {
		t.Fatalf("chmod seed parent dir: %v", err)
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
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("%s mode=%#o, want %#o", path, got, 0o755)
		}
		owner := uidGidFromFileInfo(info)
		if !owner.ok {
			t.Fatalf("%s uid/gid not available from fileinfo", path)
		}
		if owner.uid != 777 || owner.gid != 888 {
			t.Fatalf("%s uid/gid=%d:%d, want %d:%d", path, owner.uid, owner.gid, 777, 888)
		}
	}
}
