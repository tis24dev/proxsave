package orchestrator

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

type closeErrorReadCloser struct {
	*bytes.Reader
	closeErr error
}

func (r *closeErrorReadCloser) Close() error {
	return r.closeErr
}

type streamCommandRunner struct {
	stream io.ReadCloser
	calls  []string
}

func (r *streamCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func (r *streamCommandRunner) RunStream(_ context.Context, name string, _ io.Reader, args ...string) (io.ReadCloser, error) {
	r.calls = append(r.calls, commandKey(name, args))
	return r.stream, nil
}

func tarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		data := []byte(content)
		hdr := &tar.Header{
			Name: name,
			Mode: 0o640,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close tar writer: %v", err)
	}
	return buf.Bytes()
}

func TestAnalyzeRestoreArchive_UsesInternalMetadataWhenCategoriesAreCommonOnly(t *testing.T) {
	origRestoreFS := restoreFS
	t.Cleanup(func() { restoreFS = origRestoreFS })
	restoreFS = osFS{}

	archivePath := filepath.Join(t.TempDir(), "backup.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"etc/hosts": "127.0.0.1 localhost\n",
		"var/lib/proxsave-info/backup_metadata.txt": "# ProxSave Metadata\nBACKUP_TYPE=pbs\nHOSTNAME=pbs-node\nPVE_CLUSTER_MODE=cluster\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	categories, decision, err := AnalyzeRestoreArchive(archivePath, logger)
	if err != nil {
		t.Fatalf("AnalyzeRestoreArchive() error: %v", err)
	}
	if backupType, ambiguous := detectBackupTypeFromCategories(categories); backupType != SystemTypeUnknown || ambiguous {
		t.Fatalf("detectBackupTypeFromCategories() = (%s, %v); want (%s, false)", backupType, ambiguous, SystemTypeUnknown)
	}
	if decision == nil {
		t.Fatalf("decision info is nil")
	}
	if decision.BackupType != SystemTypePBS {
		t.Fatalf("BackupType=%s; want %s", decision.BackupType, SystemTypePBS)
	}
	if decision.Source != RestoreDecisionSourceInternalMetadata {
		t.Fatalf("Source=%s; want %s", decision.Source, RestoreDecisionSourceInternalMetadata)
	}
	if decision.BackupHostname != "pbs-node" {
		t.Fatalf("BackupHostname=%q; want %q", decision.BackupHostname, "pbs-node")
	}
	if decision.ClusterPayload {
		t.Fatalf("ClusterPayload should stay false without pve_cluster payload")
	}
}

func TestAnalyzeRestoreArchive_ClusterPayloadUsesArchiveContents(t *testing.T) {
	origRestoreFS := restoreFS
	t.Cleanup(func() { restoreFS = origRestoreFS })
	restoreFS = osFS{}

	archivePath := filepath.Join(t.TempDir(), "backup.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"var/lib/pve-cluster/config.db":             "db\n",
		"var/lib/proxsave-info/backup_metadata.txt": "BACKUP_TYPE=pve\nPVE_CLUSTER_MODE=standalone\nHOSTNAME=node1\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	_, decision, err := AnalyzeRestoreArchive(archivePath, logger)
	if err != nil {
		t.Fatalf("AnalyzeRestoreArchive() error: %v", err)
	}
	if decision == nil {
		t.Fatalf("decision info is nil")
	}
	if !decision.ClusterPayload {
		t.Fatalf("ClusterPayload should be true when pve_cluster payload exists")
	}
	if decision.BackupType != SystemTypePVE {
		t.Fatalf("BackupType=%s; want %s", decision.BackupType, SystemTypePVE)
	}
}

func TestCollectRestoreArchiveFacts_RejectsOversizedMetadata(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "backup.tar")
	oversized := "BACKUP_TYPE=pbs\nHOSTNAME=pbs-node\n" + strings.Repeat("A", restoreDecisionMetadataMaxBytes)
	if err := writeTarFile(archivePath, map[string]string{
		"var/lib/proxsave-info/backup_metadata.txt": oversized,
		"var/lib/pve-cluster/config.db":             "db\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("os.Open: %v", err)
	}
	defer file.Close()

	archivePaths, metadata, metadataErr, err := collectRestoreArchiveFacts(tar.NewReader(file))
	if err != nil {
		t.Fatalf("collectRestoreArchiveFacts() error: %v", err)
	}
	if metadata != nil {
		t.Fatalf("metadata = %#v; want nil for oversized entry", metadata)
	}
	if metadataErr == nil {
		t.Fatalf("metadataErr = nil; want oversize error")
	}
	if !strings.Contains(metadataErr.Error(), "too large") {
		t.Fatalf("metadataErr = %v; want oversize error", metadataErr)
	}

	foundMeta := false
	foundCluster := false
	for _, archivePath := range archivePaths {
		if archivePath == restoreDecisionMetadataPath {
			foundMeta = true
		}
		if archivePath == "var/lib/pve-cluster/config.db" {
			foundCluster = true
		}
	}
	if !foundMeta || !foundCluster {
		t.Fatalf("archivePaths = %#v; want metadata and cluster entries present", archivePaths)
	}
}

func TestAnalyzeRestoreArchive_IgnoresOversizedInternalMetadata(t *testing.T) {
	origRestoreFS := restoreFS
	t.Cleanup(func() { restoreFS = origRestoreFS })
	restoreFS = osFS{}

	archivePath := filepath.Join(t.TempDir(), "backup.tar")
	oversized := "BACKUP_TYPE=pbs\nHOSTNAME=pbs-node\n" + strings.Repeat("A", restoreDecisionMetadataMaxBytes)
	if err := writeTarFile(archivePath, map[string]string{
		"etc/hosts": "127.0.0.1 localhost\n",
		"var/lib/proxsave-info/backup_metadata.txt": oversized,
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	_, decision, err := AnalyzeRestoreArchive(archivePath, logger)
	if err != nil {
		t.Fatalf("AnalyzeRestoreArchive() error: %v", err)
	}
	if decision == nil {
		t.Fatalf("decision info is nil")
	}
	if decision.BackupType != SystemTypeUnknown {
		t.Fatalf("BackupType=%s; want %s when metadata is oversized", decision.BackupType, SystemTypeUnknown)
	}
	if decision.Source != RestoreDecisionSourceUnknown {
		t.Fatalf("Source=%s; want %s when metadata is oversized", decision.Source, RestoreDecisionSourceUnknown)
	}
	if decision.BackupHostname != "" {
		t.Fatalf("BackupHostname=%q; want empty string when metadata is oversized", decision.BackupHostname)
	}
}

func TestAnalyzeRestoreArchive_PropagatesDecompressionCloseError(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
	})
	restoreFS = osFS{}

	closeErr := errors.New("decompressor wait failed")
	restoreCmd = &streamCommandRunner{
		stream: &closeErrorReadCloser{
			Reader:   bytes.NewReader(tarBytes(t, map[string]string{"etc/hosts": "127.0.0.1 localhost\n"})),
			closeErr: closeErr,
		},
	}

	archivePath := filepath.Join(t.TempDir(), "backup.tar.zst")
	if err := os.WriteFile(archivePath, []byte("compressed payload"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	categories, decision, err := AnalyzeRestoreArchive(archivePath, logger)
	if categories != nil {
		t.Fatalf("categories = %#v; want nil on close error", categories)
	}
	if decision != nil {
		t.Fatalf("decision = %#v; want nil on close error", decision)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("AnalyzeRestoreArchive() err = %v; want close error", err)
	}
}

func TestInspectRestoreArchiveContents_PrefersInspectErrorOverCloseError(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
	})
	restoreFS = osFS{}

	closeErr := errors.New("decompressor wait failed")
	restoreCmd = &streamCommandRunner{
		stream: &closeErrorReadCloser{
			Reader:   bytes.NewReader([]byte("not a tar archive")),
			closeErr: closeErr,
		},
	}

	archivePath := filepath.Join(t.TempDir(), "backup.tar.zst")
	if err := os.WriteFile(archivePath, []byte("compressed payload"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	inspection, err := inspectRestoreArchiveContents(archivePath, logger)
	if inspection != nil {
		t.Fatalf("inspection = %#v; want nil on inspect error", inspection)
	}
	if err == nil {
		t.Fatal("inspectRestoreArchiveContents() err = nil; want inspect error")
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("inspectRestoreArchiveContents() err = %v; want inspect error to take precedence over close error", err)
	}
	if !strings.Contains(err.Error(), "inspect archive") {
		t.Fatalf("inspectRestoreArchiveContents() err = %v; want inspect archive context", err)
	}
}
