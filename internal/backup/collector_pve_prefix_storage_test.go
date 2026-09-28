package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func notVisibleLine(name, path string) string {
	return "PVE datastore " + name + " skipped: path " + path +
		" not visible under SYSTEM_ROOT_PREFIX (a separate mount is not carried into this system). Not scanning for vzdump backup files."
}

// Under SYSTEM_ROOT_PREFIX a storage path is read under the prefix and reported at its host
// path. A path that is not there, or a network storage whose mount point sits on the prefix
// root's filesystem (a separate mount the bind does not carry), reads as not visible, not as
// a fault: it used to be probed in this system and warned on every appliance run.
func TestPVEStorageScanUnderSystemRootPrefix(t *testing.T) {
	root := t.TempDir()
	writeArchivePathFixture(t, root, map[string]string{
		"etc/pve/storage.cfg": "dir: local\n\tpath /var/lib/vz\n\tcontent backup,iso\n\n" +
			"nfs: nas\n\tpath /mnt/pve/nas\n\tcontent backup\n\n" +
			"dir: gone\n\tpath /srv/gone\n\tcontent backup\n",
		"var/lib/vz/dump/vzdump-qemu-100-2026_09_01-00_00_00.vma.zst": "x",
	})
	if err := os.MkdirAll(filepath.Join(root, "mnt/pve/nas"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	cfg.BackupPVEBackupFiles = true
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	tempDir := t.TempDir()
	c := NewCollector(logger, cfg, tempDir, types.ProxmoxVE, false)
	runSelectedBricksForTest(t, context.Background(), c, newPVERecipe(), nil,
		brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON,
		brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary,
	)

	log := buf.String()
	if strings.Contains(log, "WARNING") {
		t.Fatalf("storage scan under the prefix warned:\n%s", log)
	}
	for _, want := range []string{notVisibleLine("nas", "/mnt/pve/nas"), notVisibleLine("gone", "/srv/gone")} {
		if !strings.Contains(log, want) {
			t.Errorf("log misses %q:\n%s", want, log)
		}
	}
	if !strings.Contains(log, "Found 1 backup files") {
		t.Errorf("local was not scanned under the prefix:\n%s", log)
	}

	var written []string
	for _, file := range archiveFileList(t, tempDir) {
		data, err := os.ReadFile(filepath.Join(tempDir, file))
		if err != nil {
			t.Fatal(err)
		}
		written = append(written, string(data))
	}
	all := strings.Join(written, "\n")
	if strings.Contains(all, root) {
		t.Fatalf("storage reports carry the prefix %s:\n%s", root, all)
	}
	if !strings.Contains(all, "/var/lib/vz") {
		t.Fatalf("storage reports miss the host path /var/lib/vz:\n%s", all)
	}
}

// On a real root a missing storage path stays a warning.
func TestPVEStorageMissingPathWarnsWithoutPrefix(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "gone")
	writeArchivePathFixture(t, base, map[string]string{
		"pve/storage.cfg": "dir: gone\n\tpath " + missing + "\n\tcontent backup\n",
	})
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = filepath.Join(base, "pve")
	cfg.BackupPVEBackupFiles = true
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxVE, false)
	runSelectedBricksForTest(t, context.Background(), c, newPVERecipe(), nil,
		brickPVEStorageResolve, brickPVEStorageProbe,
	)
	if !strings.Contains(buf.String(), "PVE datastore gone skipped: path "+missing+" not accessible") {
		t.Fatalf("missing path on a real root did not warn:\n%s", buf.String())
	}
}
