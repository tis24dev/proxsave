package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func writeRawBackup(t *testing.T, dir, name string) *backup.Manifest {
	t.Helper()
	archive := filepath.Join(dir, name)
	if err := os.WriteFile(archive, []byte("data"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	manifest := &backup.Manifest{
		ArchivePath: archive,
		CreatedAt:   time.Now(),
		Hostname:    "host",
	}
	manifestPath := archive + ".metadata"
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, data, 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(archive+".sha256", []byte("checksum  file"), 0o640); err != nil {
		t.Fatalf("write checksum: %v", err)
	}
	return manifest
}

func TestRunDecryptWorkflow_SuccessSelection(t *testing.T) {
	dir := t.TempDir()
	writeRawBackup(t, dir, "one.bundle.tar")
	writeRawBackup(t, dir, "two.bundle.tar")

	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = osFS{} })

	logger := logging.New(types.LogLevelInfo, false)
	cands, err := discoverBackupCandidates(logger, dir)
	if err != nil {
		t.Fatalf("discoverBackupCandidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
}

func TestRunDecryptWorkflow_BundleNotFound(t *testing.T) {
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = osFS{} })

	logger := logging.New(types.LogLevelInfo, false)
	if _, err := discoverBackupCandidates(logger, "/nonexistent"); err == nil {
		t.Fatalf("expected error for missing path")
	}
}

func TestPreparePlainBundle_InvalidChecksum(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.bundle.tar")
	if err := os.WriteFile(archive, []byte("data"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	manifest := &backup.Manifest{
		ArchivePath: archive,
		CreatedAt:   time.Now(),
		Hostname:    "host",
	}
	metaPath := archive + ".metadata"
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(metaPath, data, 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	// No checksum file to trigger error

	cand := &decryptCandidate{
		Manifest:        manifest,
		Source:          sourceRaw,
		RawArchivePath:  archive,
		RawMetadataPath: metaPath,
		RawChecksumPath: archive + ".sha256",
		DisplayBase:     filepath.Base(archive),
	}

	reader := bufio.NewReader(strings.NewReader(""))
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = osFS{} })

	if _, err := preparePlainBundle(context.Background(), reader, cand, "", logging.New(types.LogLevelInfo, false)); err == nil {
		t.Fatalf("expected error due to missing checksum file")
	}
}

func TestDiscoverBackupCandidates_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = osFS{} })

	cands, err := discoverBackupCandidates(logger, dir)
	if err != nil {
		t.Fatalf("discoverBackupCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(cands))
	}
}
