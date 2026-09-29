package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A storage.cfg path written with a trailing slash ("path /var/lib/vz/", kept by a hand
// edit or by an nfs/cifs --path given that way) used to put "//" in the host path the
// include pattern is matched against: under SYSTEM_ROOT_PREFIX "vz/dump" then selected
// nothing, silently, and metadata.txt named /var/lib/vz//dump/... Without a prefix the
// same path must keep matching as it always did.
func TestPVEBackupIncludePatternWithTrailingSlashStoragePath(t *testing.T) {
	const backupName = "vzdump-qemu-100-2026_09_01-00_00_00.vma.zst"
	selected := func(t *testing.T, prefix, storagePath, pattern string) bool {
		t.Helper()
		cfg := GetDefaultCollectorConfig()
		cfg.SystemRootPrefix = prefix
		cfg.BackupPVEBackupFiles = true
		cfg.PVEBackupIncludePattern = pattern
		logger := logging.New(types.LogLevelError, false)
		logger.SetOutput(&bytes.Buffer{})
		tempDir := t.TempDir()
		c := NewCollector(logger, cfg, tempDir, types.ProxmoxVE, false)
		storage := pveStorageEntry{Name: "local", Path: storagePath, Type: "dir"}
		if err := c.collectDetailedPVEBackups(context.Background(), storage, filepath.Join(tempDir, "meta"), 5*time.Second); err != nil {
			t.Fatalf("collectDetailedPVEBackups: %v", err)
		}
		_, err := os.Stat(filepath.Join(tempDir, "var/lib/pve-cluster/selected_backups", "local", backupName))
		return err == nil
	}

	root := t.TempDir()
	writeArchivePathFixture(t, root, map[string]string{"var/lib/vz/dump/" + backupName: "x"})
	host := t.TempDir()
	writeArchivePathFixture(t, host, map[string]string{"vz/dump/" + backupName: "x"})

	for _, tc := range []struct{ name, prefix, path, pattern string }{
		{"prefix, trailing slash, pattern across the storage root", root, "/var/lib/vz/", "vz/dump"},
		{"prefix, trailing slash, full host path", root, "/var/lib/vz/", "/var/lib/vz/dump/vzdump-qemu-100"},
		{"prefix, trailing slash, file name", root, "/var/lib/vz/", "vzdump-qemu-100"},
		{"prefix, clean path", root, "/var/lib/vz", "vz/dump"},
		{"no prefix, trailing slash", "", filepath.Join(host, "vz") + "/", "vz/dump"},
		{"no prefix, double trailing slash, full host path", "", filepath.Join(host, "vz") + "//", filepath.Join(host, "vz", "dump", "vzdump-qemu-100")},
	} {
		if !selected(t, tc.prefix, tc.path, tc.pattern) {
			t.Errorf("%s: path %q pattern %q selected nothing", tc.name, tc.path, tc.pattern)
		}
	}

	// The metadata sample lines name the file at its host path, without "//".
	writeArchivePathFixture(t, root, map[string]string{
		"etc/pve/storage.cfg": "dir: local\n\tpath /var/lib/vz/\n\tcontent backup,iso\n",
	})
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	cfg.BackupPVEBackupFiles = true
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(&bytes.Buffer{})
	tempDir := t.TempDir()
	c := NewCollector(logger, cfg, tempDir, types.ProxmoxVE, false)
	runSelectedBricksForTest(t, context.Background(), c, newPVERecipe(), nil,
		brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON,
		brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary,
	)
	sawHostPath := false
	for _, file := range archiveFileList(t, tempDir) {
		data, err := os.ReadFile(filepath.Join(tempDir, file))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "/var/lib/vz//") {
			t.Errorf("%s names a path with \"//\":\n%s", file, data)
		}
		if strings.Contains(string(data), "/var/lib/vz/dump/"+backupName) {
			sawHostPath = true
		}
	}
	if !sawHostPath {
		t.Fatalf("no collected file names /var/lib/vz/dump/%s: the check above saw nothing", backupName)
	}
}
