package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPlainArchiveHonorsSkipFn(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "backup.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"etc/fstab": "/dev/sda1 / ext4 defaults 0 1\n",
		"etc/hosts": "127.0.0.1 localhost\n",
	}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	destRoot := filepath.Join(tmpDir, "restore")
	if err := extractPlainArchive(context.Background(), archivePath, destRoot, newTestLogger(), fullRestoreSkipFn(true)); err != nil {
		t.Fatalf("extractPlainArchive error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destRoot, "etc", "fstab")); !os.IsNotExist(err) {
		t.Fatalf("expected skipped fstab to be absent, stat err=%v", err)
	}

	hosts, err := os.ReadFile(filepath.Join(destRoot, "etc", "hosts"))
	if err != nil {
		t.Fatalf("expected hosts to be extracted: %v", err)
	}
	if string(hosts) != "127.0.0.1 localhost\n" {
		t.Fatalf("hosts content=%q", string(hosts))
	}
}
