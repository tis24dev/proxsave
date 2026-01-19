package orchestrator

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestMaybeRepairResolvConfAfterRestoreUsesArchiveWhenSymlinkBroken(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = &FakeCommandRunner{}

	// Create broken symlink /etc/resolv.conf -> ../commands/resolv_conf.txt (target not present on disk).
	resolvOnDisk := filepath.Join(fakeFS.Root, "etc", "resolv.conf")
	if err := os.MkdirAll(filepath.Dir(resolvOnDisk), 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.Symlink("../commands/resolv_conf.txt", resolvOnDisk); err != nil {
		t.Fatalf("create broken resolv.conf symlink: %v", err)
	}

	// Create an archive containing commands/resolv_conf.txt to be used for repair.
	archiveOnDisk := filepath.Join(fakeFS.Root, "archive.tar")
	archiveFile, err := os.Create(archiveOnDisk)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	tw := tar.NewWriter(archiveFile)
	content := []byte("nameserver 192.0.2.53\nnameserver 1.1.1.1\n")
	hdr := &tar.Header{
		Name: "commands/resolv_conf.txt",
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		_ = tw.Close()
		_ = archiveFile.Close()
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		_ = tw.Close()
		_ = archiveFile.Close()
		t.Fatalf("tar write: %v", err)
	}
	_ = tw.Close()
	_ = archiveFile.Close()

	logger := logging.New(types.LogLevelDebug, false)
	if err := maybeRepairResolvConfAfterRestore(context.Background(), logger, "/archive.tar", false); err != nil {
		t.Fatalf("repair resolv.conf: %v", err)
	}

	info, err := os.Lstat(resolvOnDisk)
	if err != nil {
		t.Fatalf("stat resolv.conf: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected resolv.conf to be a regular file after repair, got symlink")
	}

	got, err := fakeFS.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatalf("read resolv.conf: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("unexpected resolv.conf content.\nGot:\n%s\nWant:\n%s", got, content)
	}
}
