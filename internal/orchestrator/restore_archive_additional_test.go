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

// TestExtractArchiveNativeFailOnPartialExtraction locks in the BH-002 fix: a
// staged extraction (failOnPartialExtraction=true) with any failed entry must
// return an error so the caller skips applying a partial result, while the
// best-effort default tolerates the failure and still extracts the good entries.
func TestExtractArchiveNativeFailOnPartialExtraction(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "backup.tar")
	// One extractable entry plus one entry whose name escapes the destination root,
	// which extractTarEntry rejects (counted as a failed entry, not a skipped one).
	if err := writeTarFile(archivePath, map[string]string{
		"etc/hosts":     "127.0.0.1 localhost\n",
		"../escape.txt": "evil\n",
	}); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	// Best-effort (default): the failed entry is tolerated, extraction returns nil,
	// and the good entry is still written.
	bestEffort := filepath.Join(tmpDir, "best-effort")
	if err := extractArchiveNative(context.Background(), restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    bestEffort,
		logger:      newTestLogger(),
		mode:        RestoreModeFull,
	}); err != nil {
		t.Fatalf("best-effort extraction should tolerate a failed entry, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(bestEffort, "etc", "hosts")); err != nil {
		t.Fatalf("good entry should still be extracted best-effort: %v", err)
	}

	// Strict (staged path): the failed entry makes extraction return an error so the
	// caller will refuse to apply a partial result.
	strict := filepath.Join(tmpDir, "strict")
	if err := extractArchiveNative(context.Background(), restoreArchiveOptions{
		archivePath:             archivePath,
		destRoot:                strict,
		logger:                  newTestLogger(),
		mode:                    RestoreModeFull,
		failOnPartialExtraction: true,
	}); err == nil {
		t.Fatalf("strict extraction should fail when an entry fails to extract")
	}
}
