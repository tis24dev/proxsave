package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
)

func TestRunDecryptWorkflowTUI_SuccessLocalEncrypted(t *testing.T) {
	lockDecryptTUIE2E(t)

	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	fixture := createDecryptTUIEncryptedFixture(t)
	withTimedSimAppSequence(t, successDecryptTUISequence(fixture.Secret))

	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()

	if err := runDecryptWorkflowTUIForTest(t, ctx, fixture.Config, fixture.ConfigPath); err != nil {
		t.Fatalf("RunDecryptWorkflowTUI error: %v", err)
	}

	if _, err := os.Stat(fixture.ExpectedBundlePath); err != nil {
		t.Fatalf("expected decrypted bundle at %s: %v", fixture.ExpectedBundlePath, err)
	}

	entries := readTarEntries(t, fixture.ExpectedBundlePath)

	archiveData, ok := entries[fixture.ExpectedArchiveName]
	if !ok {
		t.Fatalf("bundle missing archive entry %s", fixture.ExpectedArchiveName)
	}
	if string(archiveData) != string(fixture.ArchivePlaintext) {
		t.Fatalf("archive entry content mismatch: got %q want %q", string(archiveData), string(fixture.ArchivePlaintext))
	}

	metadataName := fixture.ExpectedArchiveName + ".metadata"
	metadataData, ok := entries[metadataName]
	if !ok {
		t.Fatalf("bundle missing metadata entry %s", metadataName)
	}

	var manifest backup.Manifest
	if err := json.Unmarshal(metadataData, &manifest); err != nil {
		t.Fatalf("unmarshal metadata entry %s: %v", metadataName, err)
	}
	if manifest.EncryptionMode != "none" {
		t.Fatalf("metadata EncryptionMode=%q; want %q", manifest.EncryptionMode, "none")
	}
	expectedArchivePath := filepath.Join(fixture.DestinationDir, fixture.ExpectedArchiveName)
	if manifest.ArchivePath != expectedArchivePath {
		t.Fatalf("metadata ArchivePath=%q; want %q", manifest.ArchivePath, expectedArchivePath)
	}
	if manifest.SHA256 != fixture.ExpectedChecksum {
		t.Fatalf("metadata SHA256=%q; want %q", manifest.SHA256, fixture.ExpectedChecksum)
	}

	checksumName := fixture.ExpectedArchiveName + ".sha256"
	checksumData, ok := entries[checksumName]
	if !ok {
		t.Fatalf("bundle missing checksum entry %s", checksumName)
	}
	expectedChecksumLine := checksumLineForArchiveHex(fixture.ExpectedArchiveName, fixture.ExpectedChecksum)
	if string(checksumData) != expectedChecksumLine {
		t.Fatalf("checksum entry=%q; want %q", string(checksumData), expectedChecksumLine)
	}
}

func TestRunDecryptWorkflowTUI_AbortAtSecretPrompt(t *testing.T) {
	lockDecryptTUIE2E(t)

	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	fixture := createDecryptTUIEncryptedFixture(t)
	withTimedSimAppSequence(t, abortDecryptTUISequence())

	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()

	err := runDecryptWorkflowTUIForTest(t, ctx, fixture.Config, fixture.ConfigPath)
	if !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("RunDecryptWorkflowTUI error=%v; want %v", err, ErrDecryptAborted)
	}

	if _, err := os.Stat(fixture.ExpectedBundlePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no decrypted bundle at %s, stat err=%v", fixture.ExpectedBundlePath, err)
	}
}
