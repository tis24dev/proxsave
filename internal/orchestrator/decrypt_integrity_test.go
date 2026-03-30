package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestResolveStagedIntegrityExpectation_RejectsConflictingSources(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	checksumPath := dir + "/backup.tar.sha256"
	if err := os.WriteFile(checksumPath, checksumLineForBytes("backup.tar", []byte("archive")), 0o640); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	_, err := resolveStagedIntegrityExpectation(stagedFiles{ChecksumPath: checksumPath}, &backup.Manifest{
		SHA256: checksumHexForBytes([]byte("different")),
	})
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
}

func TestPreparePlainBundle_RejectsMissingChecksumVerification(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	archivePath := dir + "/backup.tar"
	if err := os.WriteFile(archivePath, []byte("archive"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	metaPath := archivePath + ".metadata"
	manifest := &backup.Manifest{
		ArchivePath:    archivePath,
		CreatedAt:      time.Now(),
		EncryptionMode: "none",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(metaPath, data, 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cand := &backupCandidate{
		Manifest:        manifest,
		Source:          sourceRaw,
		RawArchivePath:  archivePath,
		RawMetadataPath: metaPath,
		DisplayBase:     "backup.tar",
	}

	reader := bufio.NewReader(strings.NewReader(""))
	logger := logging.New(types.LogLevelError, false)

	_, err = preparePlainBundle(context.Background(), reader, cand, "", logger)
	if err == nil {
		t.Fatalf("expected missing checksum verification error")
	}
	if !strings.Contains(err.Error(), "no checksum verification available") {
		t.Fatalf("expected checksum availability error, got %v", err)
	}
}

func TestPreparePlainBundle_RejectsChecksumMismatch(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	archiveData := []byte("archive")
	archivePath := dir + "/backup.tar"
	if err := os.WriteFile(archivePath, archiveData, 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	metaPath := archivePath + ".metadata"
	manifest := &backup.Manifest{
		ArchivePath:    archivePath,
		CreatedAt:      time.Now(),
		EncryptionMode: "none",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(metaPath, data, 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	checksumPath := archivePath + ".sha256"
	if err := os.WriteFile(checksumPath, checksumLineForBytes("backup.tar", []byte("tampered")), 0o640); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	cand := &backupCandidate{
		Manifest:        manifest,
		Source:          sourceRaw,
		RawArchivePath:  archivePath,
		RawMetadataPath: metaPath,
		RawChecksumPath: checksumPath,
		DisplayBase:     "backup.tar",
	}

	reader := bufio.NewReader(strings.NewReader(""))
	logger := logging.New(types.LogLevelError, false)

	_, err = preparePlainBundle(context.Background(), reader, cand, "", logger)
	if err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
}

func TestVerifyStagedArchiveIntegrity_UsesCandidateIntegrityExpectation(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	archiveData := []byte("archive")
	archivePath := dir + "/backup.tar"
	if err := os.WriteFile(archivePath, archiveData, 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	got, err := verifyStagedArchiveIntegrity(context.Background(), logging.New(types.LogLevelError, false), stagedFiles{
		ArchivePath: archivePath,
	}, &backupCandidate{
		Integrity: &stagedIntegrityExpectation{
			Checksum: strings.ToUpper(checksumHexForBytes(archiveData)),
			Source:   "checksum file",
		},
	})
	if err != nil {
		t.Fatalf("verifyStagedArchiveIntegrity() error = %v", err)
	}
	want := checksumHexForBytes(archiveData)
	if got != want {
		t.Fatalf("verifyStagedArchiveIntegrity() = %q; want %q", got, want)
	}
}
