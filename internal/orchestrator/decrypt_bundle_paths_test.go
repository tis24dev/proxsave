package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The decrypt workflow writes one file in the destination, the bundle. It asked about
// an existing "decrypted archive" at a path it never writes, and Overwrite deleted that
// file; a different path for the bundle in a directory that did not exist failed the
// move with ENOENT. Only the bundle is asked about now, and its directory is created.
func TestDecryptBundleAsksOnlyAboutTheBundleAndCreatesItsDirectory(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}
	logger := logging.New(types.LogLevelError, false)
	tmp := t.TempDir()
	raw := filepath.Join(tmp, "src", "backup.tar")
	if err := os.MkdirAll(filepath.Dir(raw), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{raw: []byte("payload-data"), raw + ".metadata": []byte(`{"manifest":true}`), raw + ".sha256": checksumLineForBytes("backup.tar", []byte("payload-data"))} {
		if err := os.WriteFile(path, data, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	cand := &backupCandidate{
		Manifest:        &backup.Manifest{ArchivePath: raw, EncryptionMode: "none", CreatedAt: time.Now(), Hostname: "node1"},
		Source:          sourceRaw,
		RawArchivePath:  raw,
		RawMetadataPath: raw + ".metadata",
		RawChecksumPath: raw + ".sha256",
		DisplayBase:     "test-backup",
	}
	prepared, err := preparePlainBundleWithUI(context.Background(), cand, "1.0.0", logger, &countingSecretPrompter{}, 0)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Cleanup()

	destDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file named like the inner archive (an earlier tar -xf) and an earlier bundle.
	sameName := filepath.Join(destDir, filepath.Base(prepared.ArchivePath))
	earlierBundle := sameName + ".decrypted.bundle.tar"
	for _, p := range []string{sameName, earlierBundle} {
		if err := os.WriteFile(p, []byte("keep"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := filepath.Join(tmp, "missing", "dir", "new.decrypted.bundle.tar")
	var asked []string
	ui := &fakeDecryptWorkflowUI{resolveExistingPathFn: func(_ context.Context, path, description, _ string) (ExistingPathDecision, string, error) {
		asked = append(asked, description+" "+path)
		return PathDecisionNewPath, elsewhere, nil
	}}

	got, err := writeDecryptedBundle(context.Background(), ui, logger, prepared, destDir)
	if err != nil {
		t.Fatalf("writeDecryptedBundle: %v", err)
	}
	if len(asked) != 1 || asked[0] != "decrypted bundle "+earlierBundle {
		t.Fatalf("asked %q, want only the existing bundle", asked)
	}
	if got != elsewhere {
		t.Fatalf("bundle at %q, want %q", got, elsewhere)
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Fatalf("bundle not written in the new directory: %v", err)
	}
	if data, err := os.ReadFile(sameName); err != nil || string(data) != "keep" {
		t.Fatalf("the file named like the archive was touched: %q %v", data, err)
	}
}
