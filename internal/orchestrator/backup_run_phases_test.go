package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

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
