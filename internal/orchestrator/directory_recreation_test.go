package orchestrator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newDirTestLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

func overridePath(t *testing.T, target *string, filename string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	prev := *target
	*target = path
	return path, func() {
		*target = prev
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRecreateStorageDirectoriesCreatesStructure(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "local")
	cfg := fmt.Sprintf("dir: local\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	if err := RecreateStorageDirectories(logger); err != nil {
		t.Fatalf("RecreateStorageDirectories error: %v", err)
	}

	for _, sub := range []string{"dump", "images", "template", "snippets", "private"} {
		if _, err := os.Stat(filepath.Join(baseDir, sub)); err != nil {
			t.Fatalf("expected subdir %s to exist: %v", sub, err)
		}
	}
}

func TestRecreateStorageDirectoriesReturnsSubdirError(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "local")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatalf("mkdir base dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "dump"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	cfg := fmt.Sprintf("dir: local\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	err := RecreateStorageDirectories(logger)
	if err == nil {
		t.Fatalf("expected subdirectory creation error")
	}
	if !strings.Contains(err.Error(), "dump") {
		t.Fatalf("expected error to mention failed subdir, got: %v", err)
	}
}

func TestCreatePVEStorageStructureHandlesVariousTypes(t *testing.T) {
	logger := newDirTestLogger()
	baseNFS := filepath.Join(t.TempDir(), "nfs")
	if err := createPVEStorageStructure(baseNFS, "nfs", logger); err != nil {
		t.Fatalf("createPVEStorageStructure(nfs): %v", err)
	}
	for _, sub := range []string{"dump", "images", "template"} {
		if _, err := os.Stat(filepath.Join(baseNFS, sub)); err != nil {
			t.Fatalf("expected nfs subdir %s: %v", sub, err)
		}
	}

	baseOther := filepath.Join(t.TempDir(), "rbd")
	if err := createPVEStorageStructure(baseOther, "rbd", logger); err != nil {
		t.Fatalf("createPVEStorageStructure(rbd): %v", err)
	}
	if _, err := os.Stat(baseOther); err != nil {
		t.Fatalf("expected base directory for other type: %v", err)
	}
}

func TestRecreateDatastoreDirectoriesCreatesStructure(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "datastore1")
	cfg := fmt.Sprintf("datastore: backup\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	cachePath, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()
	// Ensure cache path does not exist to simulate non-ZFS environment
	if err := os.RemoveAll(cachePath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("cleanup cache path: %v", err)
	}

	if err := RecreateDatastoreDirectories(logger); err != nil {
		t.Fatalf("RecreateDatastoreDirectories error: %v", err)
	}

	chunksInfo, err := os.Stat(filepath.Join(baseDir, ".chunks"))
	if err != nil {
		t.Fatalf("expected .chunks to exist: %v", err)
	}
	if !chunksInfo.IsDir() {
		t.Fatalf("expected .chunks to be a directory")
	}

	lockInfo, err := os.Stat(filepath.Join(baseDir, ".lock"))
	if err != nil {
		t.Fatalf("expected .lock to exist: %v", err)
	}
	if !lockInfo.Mode().IsRegular() {
		t.Fatalf("expected .lock to be a file, got mode=%s", lockInfo.Mode())
	}
}

func TestInitializePBSDatastoreReturnsSubdirError(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "datastore")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatalf("mkdir base dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, ".chunks"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	changed, err := initializePBSDatastore(baseDir, "ds", logger)
	if err == nil {
		t.Fatalf("expected subdirectory creation error")
	}
	if changed {
		t.Fatalf("changed=%t; want false on subdir error", changed)
	}
	if !strings.Contains(err.Error(), ".chunks") {
		t.Fatalf("expected error to mention failed subdir, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(baseDir, ".index")); !os.IsNotExist(statErr) {
		t.Fatalf("expected .index creation to be skipped after first error, stat err=%v", statErr)
	}
}

func TestRecreateDatastoreDirectoriesSkipsZFSMountPoints(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "backup-ds")
	cfg := fmt.Sprintf("datastore: ds\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	cachePath, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()
	writeFile(t, cachePath, "cache")

	if err := RecreateDatastoreDirectories(logger); err != nil {
		t.Fatalf("RecreateDatastoreDirectories zfs scenario: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, ".chunks")); !os.IsNotExist(err) {
		t.Fatalf("expected ZFS path to be skipped, got err=%v", err)
	}
}

func TestIsLikelyZFSMountPointRequiresCache(t *testing.T) {
	logger := newDirTestLogger()
	cachePath, restore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer restore()

	// Without cache file the function should return false even for matching paths.
	if isLikelyZFSMountPoint("/mnt/pbs", logger) {
		t.Fatalf("expected false when cache file is missing")
	}

	writeFile(t, cachePath, "cache")
	if !isLikelyZFSMountPoint("/mnt/pbs", logger) {
		t.Fatalf("expected true when cache exists and path matches patterns")
	}
}

func TestSetDatastoreOwnershipNoop(t *testing.T) {
	logger := newDirTestLogger()
	if err := setDatastoreOwnership(t.TempDir(), logger); err != nil {
		t.Fatalf("setDatastoreOwnership returned error: %v", err)
	}
}

func TestNormalizePBSDatastoreCfgContentFixesIndentation(t *testing.T) {
	input := strings.TrimSpace(`
datastore: Data1
gc-schedule 0/2:00
path /mnt/datastore/Data1
`)
	got, fixed := normalizePBSDatastoreCfgContent(input)
	if fixed != 2 {
		t.Fatalf("fixed=%d; want 2", fixed)
	}
	if strings.Contains(got, "\ngc-schedule ") {
		t.Fatalf("expected gc-schedule to be indented, got:\n%s", got)
	}
	if strings.Contains(got, "\npath ") {
		t.Fatalf("expected path to be indented, got:\n%s", got)
	}
	if !strings.Contains(got, "\n    gc-schedule ") || !strings.Contains(got, "\n    path ") {
		t.Fatalf("expected normalized config to include indented properties, got:\n%s", got)
	}
}

func TestNormalizePBSDatastoreCfgContentNoChangesWhenValid(t *testing.T) {
	input := "datastore: Data1\n    gc-schedule 0/2:00\n    path /mnt/datastore/Data1\n"
	got, fixed := normalizePBSDatastoreCfgContent(input)
	if fixed != 0 {
		t.Fatalf("fixed=%d; want 0", fixed)
	}
	if got != input {
		t.Fatalf("unexpected change.\nGot:\n%s\nWant:\n%s", got, input)
	}
}

func TestRecreateDirectoriesFromConfigRoutes(t *testing.T) {
	t.Run("PVE", testRecreateDirectoriesFromConfigPVE)
	t.Run("PBS", testRecreateDirectoriesFromConfigPBS)
	t.Run("Dual", testRecreateDirectoriesFromConfigDual)
	t.Run("Unknown", testRecreateDirectoriesFromConfigUnknown)
}

func testRecreateDirectoriesFromConfigPVE(t *testing.T) {
	logger := newTestLogger()
	baseDir := filepath.Join(t.TempDir(), "local")
	cfg := fmt.Sprintf("dir: local\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
	t.Cleanup(restore)
	writeFile(t, cfgPath, cfg)

	if err := RecreateDirectoriesFromConfig(SystemTypePVE, logger); err != nil {
		t.Fatalf("RecreateDirectoriesFromConfig PVE: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "images")); err != nil {
		t.Fatalf("expected PVE directories to be created: %v", err)
	}
}

func testRecreateDirectoriesFromConfigPBS(t *testing.T) {
	logger := newTestLogger()
	baseDir := filepath.Join(t.TempDir(), "data")
	cfg := fmt.Sprintf("datastore: main\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	t.Cleanup(restore)
	writeFile(t, cfgPath, cfg)
	removeZpoolCacheForTest(t)

	if err := RecreateDirectoriesFromConfig(SystemTypePBS, logger); err != nil {
		t.Fatalf("RecreateDirectoriesFromConfig PBS: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, ".chunks")); err != nil {
		t.Fatalf("expected PBS directories to be created: %v", err)
	}
}

func testRecreateDirectoriesFromConfigDual(t *testing.T) {
	logger := newTestLogger()
	pveBaseDir := filepath.Join(t.TempDir(), "local")
	pveCfg := fmt.Sprintf("dir: local\n    path %s\n", pveBaseDir)
	pveCfgPath, restorePVE := overridePath(t, &storageCfgPath, "storage.cfg")
	t.Cleanup(restorePVE)
	writeFile(t, pveCfgPath, pveCfg)

	pbsBaseDir := filepath.Join(t.TempDir(), "data")
	pbsCfg := fmt.Sprintf("datastore: main\n    path %s\n", pbsBaseDir)
	pbsCfgPath, restorePBS := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	t.Cleanup(restorePBS)
	writeFile(t, pbsCfgPath, pbsCfg)
	removeZpoolCacheForTest(t)

	if err := RecreateDirectoriesFromConfig(SystemTypeDual, logger); err != nil {
		t.Fatalf("RecreateDirectoriesFromConfig Dual: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pveBaseDir, "images")); err != nil {
		t.Fatalf("expected PVE directories to be created for dual system: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pbsBaseDir, ".chunks")); err != nil {
		t.Fatalf("expected PBS directories to be created for dual system: %v", err)
	}
}

func testRecreateDirectoriesFromConfigUnknown(t *testing.T) {
	logger := newTestLogger()
	if err := RecreateDirectoriesFromConfig(SystemTypeUnknown, logger); err != nil {
		t.Fatalf("RecreateDirectoriesFromConfig unknown: %v", err)
	}
}

func removeZpoolCacheForTest(t *testing.T) {
	t.Helper()
	cachePath, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	t.Cleanup(cacheRestore)
	if err := os.RemoveAll(cachePath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("cleanup cache path: %v", err)
	}
}

// Test: RecreateStorageDirectories quando il file non esiste
func TestRecreateStorageDirectoriesFileNotExist(t *testing.T) {
	logger := newDirTestLogger()
	_, restore := overridePath(t, &storageCfgPath, "nonexistent.cfg")
	defer restore()
	// Non creiamo il file, quindi non esiste

	err := RecreateStorageDirectories(logger)
	if err != nil {
		t.Fatalf("expected nil error when file doesn't exist, got: %v", err)
	}
}

// Test: RecreateStorageDirectories salta commenti e linee vuote
func TestRecreateStorageDirectoriesSkipsCommentsAndEmptyLines(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "storage1")
	cfg := fmt.Sprintf(`# This is a comment
dir: storage1
    # Another comment
    path %s

# Empty line above and comment

`, baseDir)
	cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	if err := RecreateStorageDirectories(logger); err != nil {
		t.Fatalf("RecreateStorageDirectories error: %v", err)
	}

	// Verifica che le directory siano state create nonostante commenti e linee vuote
	if _, err := os.Stat(filepath.Join(baseDir, "dump")); err != nil {
		t.Fatalf("expected dump subdir to exist: %v", err)
	}
}

// Test: RecreateStorageDirectories con multiple storage entries
func TestRecreateStorageDirectoriesMultipleEntries(t *testing.T) {
	logger := newDirTestLogger()
	tmpDir := t.TempDir()
	dir1 := filepath.Join(tmpDir, "local1")
	dir2 := filepath.Join(tmpDir, "nfs1")
	dir3 := filepath.Join(tmpDir, "cifs1")

	cfg := fmt.Sprintf(`dir: local1
    path %s

nfs: nfs1
    path %s

cifs: cifs1
    path %s
`, dir1, dir2, dir3)

	cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	if err := RecreateStorageDirectories(logger); err != nil {
		t.Fatalf("RecreateStorageDirectories error: %v", err)
	}

	// Verifica dir type (ha 5 subdirs)
	for _, sub := range []string{"dump", "images", "template", "snippets", "private"} {
		if _, err := os.Stat(filepath.Join(dir1, sub)); err != nil {
			t.Fatalf("expected dir1 subdir %s to exist: %v", sub, err)
		}
	}

	// Verifica nfs type (ha 3 subdirs)
	for _, sub := range []string{"dump", "images", "template"} {
		if _, err := os.Stat(filepath.Join(dir2, sub)); err != nil {
			t.Fatalf("expected nfs subdir %s to exist: %v", sub, err)
		}
	}

	// Verifica cifs type (ha 3 subdirs)
	for _, sub := range []string{"dump", "images", "template"} {
		if _, err := os.Stat(filepath.Join(dir3, sub)); err != nil {
			t.Fatalf("expected cifs subdir %s to exist: %v", sub, err)
		}
	}
}

// Test: createPVEStorageStructure per CIFS type
func TestCreatePVEStorageStructureCIFS(t *testing.T) {
	logger := newDirTestLogger()
	baseCIFS := filepath.Join(t.TempDir(), "cifs")
	if err := createPVEStorageStructure(baseCIFS, "cifs", logger); err != nil {
		t.Fatalf("createPVEStorageStructure(cifs): %v", err)
	}
	for _, sub := range []string{"dump", "images", "template"} {
		if _, err := os.Stat(filepath.Join(baseCIFS, sub)); err != nil {
			t.Fatalf("expected cifs subdir %s: %v", sub, err)
		}
	}
	// Verifica che non abbia creato snippets e private (specifici per dir)
	for _, sub := range []string{"snippets", "private"} {
		if _, err := os.Stat(filepath.Join(baseCIFS, sub)); !os.IsNotExist(err) {
			t.Fatalf("expected cifs to NOT have subdir %s", sub)
		}
	}
}

// Test: RecreateDatastoreDirectories quando il file non esiste
func TestRecreateDatastoreDirectoriesFileNotExist(t *testing.T) {
	logger := newDirTestLogger()
	_, restore := overridePath(t, &datastoreCfgPath, "nonexistent.cfg")
	defer restore()
	// Non creiamo il file

	err := RecreateDatastoreDirectories(logger)
	if err != nil {
		t.Fatalf("expected nil error when file doesn't exist, got: %v", err)
	}
}

// Test: RecreateDatastoreDirectories salta commenti e linee vuote
func TestRecreateDatastoreDirectoriesSkipsCommentsAndEmptyLines(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "ds1")
	cfg := fmt.Sprintf(`# Datastore configuration
datastore: ds1
    # Path comment
    path %s

# Another comment

`, baseDir)
	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	_, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()
	// Non creiamo il cache file per evitare ZFS detection

	if err := RecreateDatastoreDirectories(logger); err != nil {
		t.Fatalf("RecreateDatastoreDirectories error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(baseDir, ".chunks")); err != nil {
		t.Fatalf("expected .chunks subdir to exist: %v", err)
	}
}

// Test: RecreateDatastoreDirectories con multiple datastore entries
func TestRecreateDatastoreDirectoriesMultipleEntries(t *testing.T) {
	logger := newDirTestLogger()
	tmpDir := t.TempDir()
	dir1 := filepath.Join(tmpDir, "ds1")
	dir2 := filepath.Join(tmpDir, "ds2")

	cfg := fmt.Sprintf(`datastore: ds1
    path %s

datastore: ds2
    path %s
`, dir1, dir2)

	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	_, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()
	// Non creiamo il cache file

	if err := RecreateDatastoreDirectories(logger); err != nil {
		t.Fatalf("RecreateDatastoreDirectories error: %v", err)
	}

	for _, dir := range []string{dir1, dir2} {
		chunksInfo, err := os.Stat(filepath.Join(dir, ".chunks"))
		if err != nil {
			t.Fatalf("expected %s/.chunks to exist: %v", dir, err)
		}
		if !chunksInfo.IsDir() {
			t.Fatalf("expected %s/.chunks to be a directory", dir)
		}

		lockInfo, err := os.Stat(filepath.Join(dir, ".lock"))
		if err != nil {
			t.Fatalf("expected %s/.lock to exist: %v", dir, err)
		}
		if !lockInfo.Mode().IsRegular() {
			t.Fatalf("expected %s/.lock to be a file, got mode=%s", dir, lockInfo.Mode())
		}
	}
}

// Test: isLikelyZFSMountPoint con path senza match
func TestIsLikelyZFSMountPointNoMatch(t *testing.T) {
	logger := newDirTestLogger()
	cachePath, restore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer restore()
	writeFile(t, cachePath, "cache")

	// Path che non matcha nessun pattern ZFS
	if isLikelyZFSMountPoint("/var/lib/something", logger) {
		t.Fatalf("expected false for path without ZFS patterns")
	}
	if isLikelyZFSMountPoint("/opt/storage", logger) {
		t.Fatalf("expected false for /opt/storage")
	}
}

// Test: isLikelyZFSMountPoint with a path containing "datastore"
func TestIsLikelyZFSMountPointDatastorePath(t *testing.T) {
	logger := newDirTestLogger()
	cachePath, restore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer restore()
	writeFile(t, cachePath, "cache")

	// A path with "datastore" in the name should match
	if !isLikelyZFSMountPoint("/var/lib/datastore", logger) {
		t.Fatalf("expected true for path containing 'datastore'")
	}
	if !isLikelyZFSMountPoint("/DATASTORE/pool", logger) {
		t.Fatalf("expected true for path containing 'DATASTORE' (case insensitive)")
	}
}

// Test: createPVEStorageStructure returns an error if the base directory can't be created
func TestCreatePVEStorageStructureBaseError(t *testing.T) {
	logger := newDirTestLogger()
	// A path containing a NUL byte is invalid
	invalidPath := "/dev/null/cannot/create/here"
	err := createPVEStorageStructure(invalidPath, "dir", logger)
	if err == nil {
		t.Fatalf("expected error for invalid base path")
	}
}

// Test: createPBSDatastoreStructure returns an error if the base directory can't be created
func TestCreatePBSDatastoreStructureBaseError(t *testing.T) {
	logger := newDirTestLogger()
	// Override zpoolCachePath to avoid ZFS detection
	_, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()

	invalidPath := "/dev/null/cannot/create/here"
	_, err := createPBSDatastoreStructure(invalidPath, "ds", logger)
	if err == nil {
		t.Fatalf("expected error for invalid base path")
	}
}

// Test: RecreateDirectoriesFromConfig propagates an error from RecreateStorageDirectories
func TestRecreateDirectoriesFromConfigPVEStatError(t *testing.T) {
	logger := newDirTestLogger()
	// Create a directory and make it unreadable to trigger a stat error
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, "noperm")
	if err := os.MkdirAll(cfgDir, 0o000); err != nil {
		t.Skipf("cannot create restricted directory: %v", err)
	}
	defer func() { _ = os.Chmod(cfgDir, 0o755) }()

	cfgPath := filepath.Join(cfgDir, "storage.cfg")
	prev := storageCfgPath
	storageCfgPath = cfgPath
	defer func() { storageCfgPath = prev }()

	err := RecreateDirectoriesFromConfig(SystemTypePVE, logger)
	// If we're root, the test won't behave as expected
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user")
	}
	if err == nil {
		t.Fatalf("expected error from stat on restricted path")
	}
}

// Test: RecreateDirectoriesFromConfig propagates an error from RecreateDatastoreDirectories
func TestRecreateDirectoriesFromConfigPBSStatError(t *testing.T) {
	logger := newDirTestLogger()
	// Create a directory and make it unreadable
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, "noperm")
	if err := os.MkdirAll(cfgDir, 0o000); err != nil {
		t.Skipf("cannot create restricted directory: %v", err)
	}
	defer func() { _ = os.Chmod(cfgDir, 0o755) }()

	cfgPath := filepath.Join(cfgDir, "datastore.cfg")
	prev := datastoreCfgPath
	datastoreCfgPath = cfgPath
	defer func() { datastoreCfgPath = prev }()

	err := RecreateDirectoriesFromConfig(SystemTypePBS, logger)
	// If we're root, the test won't behave as expected
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user")
	}
	if err == nil {
		t.Fatalf("expected error from stat on restricted path")
	}
}
