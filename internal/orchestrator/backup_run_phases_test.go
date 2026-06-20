package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCleanupBackupWorkspaceRemovesAndDeregisters(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	orch := New(logger, false)

	reg, err := NewTempDirRegistry(logger, filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}

	tempDir := t.TempDir()
	// Represent plaintext staged secrets that must not survive a finished run.
	if err := os.WriteFile(filepath.Join(tempDir, "shadow"), []byte("hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tempDir); err != nil {
		t.Fatalf("register: %v", err)
	}

	orch.cleanupBackupWorkspace(&backupWorkspace{registry: reg, fs: osFS{}, tempDir: tempDir})

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("workspace must be removed when the run finishes (issue #53), stat err=%v", err)
	}
	entries, err := reg.loadEntries()
	if err != nil {
		t.Fatalf("loadEntries: %v", err)
	}
	for _, e := range entries {
		if e.Path == tempDir {
			t.Fatalf("workspace must be deregistered after removal; still present in %+v", entries)
		}
	}
}

func TestCreateBackupArchiveClassifiesAgeRecipientFailureAsEncryption(t *testing.T) {
	orch := New(newTestLogger(), false)
	orch.SetConfig(&config.Config{
		EncryptArchive: true,
		BaseDir:        t.TempDir(),
	})
	orch.SetBackupConfig(t.TempDir(), t.TempDir(), types.CompressionNone, 0, 0, "standard", nil)

	run := orch.newBackupRunContext(context.Background(), nil, "test-host")
	_, err := orch.createBackupArchive(run, &backupWorkspace{tempDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected createBackupArchive error")
	}

	var backupErr *BackupError
	if !errors.As(err, &backupErr) {
		t.Fatalf("expected BackupError, got %T: %v", err, err)
	}
	if backupErr.Phase != "encryption" {
		t.Fatalf("Phase=%q; want encryption", backupErr.Phase)
	}
	if backupErr.Code != types.ExitEncryptionError {
		t.Fatalf("Code=%v; want %v", backupErr.Code, types.ExitEncryptionError)
	}
}

func TestWriteArchiveChecksumPropagatesWriteError(t *testing.T) {
	orch := New(newTestLogger(), false)
	checksumPath := "/backups/test.tar.sha256"
	writeErr := errors.New("disk full")
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = fakeFS.Cleanup() })

	err := orch.writeArchiveChecksum(
		&backupWorkspace{fs: writeFileFailFS{FS: fakeFS, failPath: checksumPath, err: writeErr}},
		&backupArtifacts{archivePath: "/backups/test.tar", checksumPath: checksumPath},
		"abc123",
	)
	if err == nil {
		t.Fatal("expected writeArchiveChecksum error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
	if !strings.Contains(err.Error(), checksumPath) {
		t.Fatalf("expected checksum path in error, got %q", err.Error())
	}
}
