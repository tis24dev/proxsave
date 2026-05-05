package orchestrator

import (
	"context"
	"errors"
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
