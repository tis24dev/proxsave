package storage

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newTestLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

func TestNormalizeGFSRetentionConfigEnforcesDailyMinimum(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	cfg := RetentionConfig{
		Policy: "gfs",
		Daily:  0,
		Weekly: 4,
	}

	effective := NormalizeGFSRetentionConfig(logger, "Test Storage", cfg)

	if effective.Daily != 1 {
		t.Fatalf("NormalizeGFSRetentionConfig() Daily = %d; want 1", effective.Daily)
	}
	if !strings.Contains(buf.String(), "RETENTION_DAILY") {
		t.Fatalf("expected log message mentioning RETENTION_DAILY adjustment, got: %s", buf.String())
	}
}

func TestLocalStorageListSkipsAssociatedFilesAndSortsByTimestamp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{
		BackupPath:            dir,
		BundleAssociatedFiles: true,
	}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	now := time.Now()
	files := []struct {
		name string
		when time.Time
	}{
		{name: "alpha-backup-2024-11-01.tar.zst", when: now.Add(-3 * time.Hour)},
		{name: "beta-backup-2024-11-02.tar.zst", when: now.Add(-1 * time.Hour)},
		{name: "proxmox-backup-legacy.tar.gz", when: now.Add(-2 * time.Hour)},
	}

	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if err := os.WriteFile(path, []byte(file.name), 0o600); err != nil {
			t.Fatalf("write %s: %v", file.name, err)
		}
		if err := os.Chtimes(path, file.when, file.when); err != nil {
			t.Fatalf("chtimes %s: %v", file.name, err)
		}
	}

	// Associated files that should be ignored
	for _, suffix := range []string{".metadata", ".sha256"} {
		name := files[1].name + suffix
		if err := os.WriteFile(filepath.Join(dir, name), []byte("aux"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	backups, err := local.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if got, want := len(backups), len(files); got != want {
		t.Fatalf("List() = %d backups, want %d", got, want)
	}

	for _, backup := range backups {
		if strings.HasSuffix(backup.BackupFile, ".metadata") || strings.HasSuffix(backup.BackupFile, ".sha256") {
			t.Fatalf("List() returned associated file %s", backup.BackupFile)
		}
	}

	expected := make([]string, len(files))
	order := append([]struct {
		name string
		when time.Time
	}(nil), files...)
	sort.Slice(order, func(i, j int) bool {
		return order[i].when.After(order[j].when)
	})
	for i, file := range order {
		expected[i] = filepath.Join(dir, file.name)
	}

	for i, backup := range backups {
		if backup.BackupFile != expected[i] {
			t.Fatalf("List()[%d] = %s, want %s", i, backup.BackupFile, expected[i])
		}
	}
}

func TestLocalStorageApplyRetentionDeletesOldBackups(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{
		BackupPath:            dir,
		BundleAssociatedFiles: false,
	}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	now := time.Now()
	type backupMeta struct {
		path string
		mod  time.Time
	}
	var metas []backupMeta
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, "node-backup-"+time.Now().Add(time.Duration(i)*time.Second).Format("150405")+".tar.zst")
		if err := os.WriteFile(name, []byte{byte(i)}, 0o600); err != nil {
			t.Fatalf("write backup: %v", err)
		}
		mod := now.Add(-time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, mod, mod); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		for _, suffix := range []string{".metadata", ".metadata.sha256", ".sha256"} {
			if err := os.WriteFile(name+suffix, []byte("aux"), 0o600); err != nil {
				t.Fatalf("write assoc: %v", err)
			}
		}
		metas = append(metas, backupMeta{path: name, mod: mod})
	}

	retentionCfg := RetentionConfig{Policy: "simple", MaxBackups: 2}
	deleted, err := local.ApplyRetention(context.Background(), retentionCfg)
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("ApplyRetention() deleted = %d, want 2", deleted)
	}

	// Determine newest two files (should remain)
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].mod.After(metas[j].mod)
	})
	kept := metas[:2]
	removed := metas[2:]

	for _, meta := range kept {
		if _, err := os.Stat(meta.path); err != nil {
			t.Fatalf("expected backup %s to remain, but stat failed: %v", meta.path, err)
		}
	}

	for _, meta := range removed {
		if _, err := os.Stat(meta.path); !os.IsNotExist(err) {
			t.Fatalf("expected backup %s to be deleted, got err=%v", meta.path, err)
		}
		for _, suffix := range []string{".metadata", ".metadata.sha256", ".sha256"} {
			if _, err := os.Stat(meta.path + suffix); err == nil {
				t.Fatalf("expected associated file %s to be deleted", meta.path+suffix)
			}
		}
	}
}

func TestLocalStorageLoadMetadataPrefersBundle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: true}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	backupPath := filepath.Join(dir, "node-bundle-backup-20240101-010101.tar.zst")
	if err := os.WriteFile(backupPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	ts := time.Date(2024, 1, 1, 1, 1, 1, 0, time.UTC)
	bundlePath := backupPath + ".bundle.tar"

	manifest := backup.Manifest{
		ArchiveSize:     1234,
		SHA256:          "deadbeef",
		CreatedAt:       ts,
		CompressionType: "zstd",
		ProxmoxType:     "qemu",
		ScriptVersion:   "1.2.3",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	tw := tar.NewWriter(f)
	header := &tar.Header{
		Name: filepath.Base(backupPath) + ".metadata",
		Mode: 0o600,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	meta, err := local.loadMetadata(backupPath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if meta.BackupFile != bundlePath {
		t.Fatalf("BackupFile = %s, want %s", meta.BackupFile, bundlePath)
	}
	if !meta.Timestamp.Equal(ts) {
		t.Fatalf("Timestamp = %v, want %v", meta.Timestamp, ts)
	}
	if meta.Size != manifest.ArchiveSize {
		t.Fatalf("Size = %d, want %d", meta.Size, manifest.ArchiveSize)
	}
	if meta.Checksum != manifest.SHA256 {
		t.Fatalf("Checksum = %s, want %s", meta.Checksum, manifest.SHA256)
	}
}

func TestLocalStorageLoadMetadataFallsBackToSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{BackupPath: dir}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	backupPath := filepath.Join(dir, "node-sidecar-backup-20240101-020202.tar.zst")
	payload := []byte("payload")
	if err := os.WriteFile(backupPath, payload, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	ts := time.Date(2024, 1, 2, 2, 2, 2, 0, time.UTC)
	if err := os.Chtimes(backupPath, ts, ts); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	manifest := backup.Manifest{
		ArchiveSize:     0,
		SHA256:          "cafebabe",
		CreatedAt:       time.Time{},
		CompressionType: "zstd",
		ProxmoxType:     "ct",
		ScriptVersion:   "9.9.9",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(backupPath+".metadata", data, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	meta, err := local.loadMetadata(backupPath)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if !meta.Timestamp.Equal(ts) {
		t.Fatalf("Timestamp = %v, want %v", meta.Timestamp, ts)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("Size = %d, want %d", meta.Size, len(payload))
	}
	if meta.Checksum != manifest.SHA256 {
		t.Fatalf("Checksum = %s, want %s", meta.Checksum, manifest.SHA256)
	}
	if meta.BackupFile != backupPath {
		t.Fatalf("BackupFile = %s, want %s", meta.BackupFile, backupPath)
	}
}

func TestLocalStorageLoadMetadataMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{BackupPath: dir}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	backupPath := filepath.Join(dir, "node-nometadata-backup-20240101-030303.tar.zst")
	if err := os.WriteFile(backupPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if _, err := local.loadMetadata(backupPath); err == nil {
		t.Fatal("loadMetadata() should fail when metadata file is missing")
	}
}

func TestLocalStorageLoadMetadataFromBundleMissingEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{BackupPath: dir}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	bundlePath := filepath.Join(dir, "node-missing-backup-20240101-040404.tar.zst.bundle.tar")
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	tw := tar.NewWriter(f)
	header := &tar.Header{
		Name: "unrelated.txt",
		Mode: 0o600,
		Size: 4,
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte("test")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	if _, err := local.loadMetadataFromBundle(bundlePath); err == nil {
		t.Fatal("expected loadMetadataFromBundle() to fail when metadata entry missing")
	}
}

func TestLocalStorageDeleteAssociatedLogRemovesFile(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	logDir := t.TempDir()
	cfg := &config.Config{
		BackupPath: baseDir,
		LogPath:    logDir,
	}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	backupPath := filepath.Join(baseDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	logPath := filepath.Join(logDir, "backup-node-20240102-030405.log")
	if err := os.WriteFile(logPath, []byte("log"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if !local.deleteAssociatedLog(backupPath) {
		t.Fatalf("deleteAssociatedLog() = false, want true")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("expected log %s to be removed, err=%v", logPath, err)
	}
	if local.deleteAssociatedLog(backupPath) {
		t.Fatalf("deleteAssociatedLog() should return false when log already removed")
	}
}

func TestLocalStorageCountLogFiles(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		var local *LocalStorage
		if local.countLogFiles() != -1 {
			t.Fatalf("nil receiver should return -1")
		}
	})

	t.Run("nil config", func(t *testing.T) {
		local := &LocalStorage{logger: newTestLogger()}
		if local.countLogFiles() != -1 {
			t.Fatalf("nil config should return -1")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		local := &LocalStorage{
			config: &config.Config{},
			logger: newTestLogger(),
		}
		if local.countLogFiles() != 0 {
			t.Fatalf("empty log path should return 0")
		}
	})

	t.Run("counts files", func(t *testing.T) {
		logDir := t.TempDir()
		for i := 0; i < 2; i++ {
			name := fmt.Sprintf("backup-node-%d.log", i)
			if err := os.WriteFile(filepath.Join(logDir, name), []byte("log"), 0o600); err != nil {
				t.Fatalf("write log: %v", err)
			}
		}
		local := &LocalStorage{
			config: &config.Config{LogPath: logDir},
			logger: newTestLogger(),
		}
		if local.countLogFiles() != 2 {
			t.Fatalf("countLogFiles() = %d, want 2", local.countLogFiles())
		}
	})

	t.Run("glob error", func(t *testing.T) {
		base := t.TempDir()
		badDir := filepath.Join(base, "[invalid")
		if err := os.MkdirAll(badDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		local := &LocalStorage{
			config: &config.Config{LogPath: badDir},
			logger: newTestLogger(),
		}
		if local.countLogFiles() != -1 {
			t.Fatalf("expected -1 for glob error")
		}
	})
}

func TestSecondaryStorageStoreCopiesBackupAndAssociatedFiles(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	destDir := t.TempDir()

	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         destDir,
		BundleAssociatedFiles: false,
	}

	secondary, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage() error = %v", err)
	}

	backupFile := filepath.Join(srcDir, "pbs-backup-2024.tar.zst")
	if err := os.WriteFile(backupFile, []byte("primary-data"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	for _, suffix := range []string{".metadata", ".metadata.sha256", ".sha256"} {
		if err := os.WriteFile(backupFile+suffix, []byte("data-"+suffix), 0o600); err != nil {
			t.Fatalf("write assoc %s: %v", suffix, err)
		}
	}

	if err := secondary.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Secondary store failed: %v", err)
	}

	destFiles := append([]string{backupFile}, backupFile+".metadata", backupFile+".metadata.sha256", backupFile+".sha256")
	for _, src := range destFiles {
		dest := filepath.Join(destDir, filepath.Base(src))
		if _, err := os.Stat(dest); err != nil {
			t.Fatalf("expected %s to exist: %v", dest, err)
		}
		srcData, _ := os.ReadFile(src)
		destData, _ := os.ReadFile(dest)
		if string(srcData) != string(destData) {
			t.Fatalf("copied file %s mismatch", dest)
		}
	}
}

func TestClassifyBackupsGFSLimitsDailyCount(t *testing.T) {
	t.Parallel()

	now := time.Now()

	var backups []*types.BackupMetadata
	for i := 0; i < 5; i++ {
		backups = append(backups, &types.BackupMetadata{
			BackupFile: fmt.Sprintf("backup-%d", i),
			Timestamp:  now.Add(-time.Duration(i) * time.Hour),
		})
	}

	cfg := RetentionConfig{
		Policy: "gfs",
		Daily:  3,
	}

	classification := ClassifyBackupsGFS(backups, cfg)

	countDaily := 0
	for _, cat := range classification {
		if cat == CategoryDaily {
			countDaily++
		}
	}

	if countDaily != 3 {
		t.Fatalf("expected 3 daily backups, got %d", countDaily)
	}
}

func TestStorageErrorFormatting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *StorageError
		expected string
	}{
		{
			name: "non-critical non-recoverable",
			err: &StorageError{
				Location:   LocationPrimary,
				Operation:  "store",
				Path:       "/backups/full.tar",
				Err:        errors.New("disk full"),
				IsCritical: false,
			},
			expected: "WARNING: primary storage store operation failed for /backups/full.tar: disk full",
		},
		{
			name: "critical and recoverable",
			err: &StorageError{
				Location:    LocationSecondary,
				Operation:   "delete",
				Path:        "/mnt/secondary/old.tar",
				Err:         errors.New("permission denied"),
				IsCritical:  true,
				Recoverable: true,
			},
			expected: "CRITICAL: secondary storage delete operation failed for /mnt/secondary/old.tar (recoverable): permission denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Fatalf("StorageError.Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFilesystemTypeHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fs                FilesystemType
		supportsOwnership bool
		isNetwork         bool
		autoExclude       bool
	}{
		{FilesystemExt4, true, false, false},
		{FilesystemZFS, true, false, false},
		{FilesystemNTFS, false, false, true},
		{FilesystemExFAT, false, false, true},
		{FilesystemNFS4, false, true, false},
		{FilesystemCIFS, false, true, true},
		{FilesystemSMB, false, true, false},
		{FilesystemFUSE, false, false, false},
		{FilesystemUnknown, false, false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.fs), func(t *testing.T) {
			if got := tt.fs.SupportsUnixOwnership(); got != tt.supportsOwnership {
				t.Fatalf("%s SupportsUnixOwnership() = %v, want %v", tt.fs, got, tt.supportsOwnership)
			}
			if got := tt.fs.IsNetworkFilesystem(); got != tt.isNetwork {
				t.Fatalf("%s IsNetworkFilesystem() = %v, want %v", tt.fs, got, tt.isNetwork)
			}
			if got := tt.fs.ShouldAutoExclude(); got != tt.autoExclude {
				t.Fatalf("%s ShouldAutoExclude() = %v, want %v", tt.fs, got, tt.autoExclude)
			}
			if got := tt.fs.String(); got != string(tt.fs) {
				t.Fatalf("%s String() = %q, want %q", tt.fs, got, tt.fs)
			}
		})
	}
}

func TestSecondaryStorageDeleteAssociatedLog(t *testing.T) {
	t.Parallel()

	logDir := t.TempDir()
	cfg := &config.Config{SecondaryLogPath: logDir}
	storage := &SecondaryStorage{
		config: cfg,
		logger: newTestLogger(),
	}

	host := "node1"
	timestamp := "20240102-030405"
	backupPath := filepath.Join(logDir, fmt.Sprintf("%s-backup-%s.tar.zst", host, timestamp))
	logPath := filepath.Join(logDir, fmt.Sprintf("backup-%s-%s.log", host, timestamp))
	if err := os.WriteFile(logPath, []byte("log"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if deleted := storage.deleteAssociatedLog(backupPath); !deleted {
		t.Fatalf("deleteAssociatedLog() = false, want true")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log file should be deleted, stat err=%v", err)
	}

	// Running again should return false since log is gone.
	if storage.deleteAssociatedLog(backupPath) {
		t.Fatalf("deleteAssociatedLog() should fail when log is missing")
	}

	// Invalid backup name should not delete anything.
	if storage.deleteAssociatedLog(filepath.Join(logDir, "invalid.tar")) {
		t.Fatalf("deleteAssociatedLog() should return false for invalid name")
	}

	// Nil receiver should be handled gracefully.
	var nilStorage *SecondaryStorage
	if nilStorage.deleteAssociatedLog(backupPath) {
		t.Fatalf("nil storage should not delete logs")
	}
}

func TestSecondaryStorageCountLogFiles(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		var storage *SecondaryStorage
		if storage.countLogFiles() != -1 {
			t.Fatalf("nil storage should return -1")
		}
	})

	t.Run("nil config", func(t *testing.T) {
		storage := &SecondaryStorage{logger: newTestLogger()}
		if storage.countLogFiles() != -1 {
			t.Fatalf("nil config should return -1")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		storage := &SecondaryStorage{
			config: &config.Config{},
			logger: newTestLogger(),
		}
		if storage.countLogFiles() != 0 {
			t.Fatalf("empty path should return 0")
		}
	})

	t.Run("counts files", func(t *testing.T) {
		logDir := t.TempDir()
		for i := 0; i < 3; i++ {
			name := fmt.Sprintf("backup-node-%d.log", i)
			if err := os.WriteFile(filepath.Join(logDir, name), []byte("log"), 0o600); err != nil {
				t.Fatalf("write log: %v", err)
			}
		}
		storage := &SecondaryStorage{
			config: &config.Config{SecondaryLogPath: logDir},
			logger: newTestLogger(),
		}
		if got := storage.countLogFiles(); got != 3 {
			t.Fatalf("countLogFiles() = %d, want 3", got)
		}
	})

	t.Run("glob error", func(t *testing.T) {
		base := t.TempDir()
		badDir := filepath.Join(base, "[invalid")
		if err := os.MkdirAll(badDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		storage := &SecondaryStorage{
			config: &config.Config{SecondaryLogPath: badDir},
			logger: newTestLogger(),
		}
		if got := storage.countLogFiles(); got != -1 {
			t.Fatalf("expected -1 for glob error, got %d", got)
		}
	})
}

func TestSecondaryStorageApplyRetentionSimple(t *testing.T) {
	t.Parallel()

	backupDir := t.TempDir()
	logDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		SecondaryLogPath:      logDir,
		BundleAssociatedFiles: false,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	baseTime := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC)
	type backupInfo struct {
		path string
		ts   time.Time
	}
	var infos []backupInfo
	for i := 0; i < 4; i++ {
		ts := baseTime.Add(-time.Duration(i) * time.Hour)
		path := createSecondaryBackup(t, backupDir, logDir, "node-simple", ts)
		infos = append(infos, backupInfo{path: path, ts: ts})
	}

	deleted, err := storage.ApplyRetention(context.Background(), RetentionConfig{
		Policy:     "simple",
		MaxBackups: 2,
	})
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("ApplyRetention() deleted = %d, want 2", deleted)
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ts.After(infos[j].ts)
	})
	kept := infos[:2]
	removed := infos[2:]

	for _, info := range kept {
		if _, err := os.Stat(info.path); err != nil {
			t.Fatalf("expected backup %s to remain: %v", info.path, err)
		}
	}
	for _, info := range removed {
		if _, err := os.Stat(info.path); !os.IsNotExist(err) {
			t.Fatalf("expected backup %s to be deleted, err=%v", info.path, err)
		}
		logPath := filepath.Join(logDir, fmt.Sprintf("backup-node-simple-%s.log", info.ts.Format("20060102-150405")))
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("expected log %s to be deleted, err=%v", logPath, err)
		}
	}

	summary := storage.LastRetentionSummary()
	if !summary.HasLogInfo {
		t.Fatalf("LastRetentionSummary() should report log info")
	}
	if summary.BackupsDeleted != 2 || summary.BackupsRemaining != 2 {
		t.Fatalf("unexpected backup summary: %+v", summary)
	}
	if summary.LogsDeleted != 2 || summary.LogsRemaining != 2 {
		t.Fatalf("unexpected log summary: %+v", summary)
	}
}

func TestSecondaryStorageApplyRetentionGFS(t *testing.T) {
	t.Parallel()

	backupDir := t.TempDir()
	logDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		SecondaryLogPath:      logDir,
		BundleAssociatedFiles: false,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	now := time.Now()
	timestamps := []time.Time{
		now.Add(-6 * time.Hour),       // daily
		now.Add(-8 * 24 * time.Hour),  // weekly
		now.Add(-15 * 24 * time.Hour), // delete
		now.Add(-32 * 24 * time.Hour), // delete
	}

	for _, ts := range timestamps {
		path := createSecondaryBackup(t, backupDir, logDir, "node-gfs", ts)
		_ = path
	}

	deleted, err := storage.ApplyRetention(context.Background(), RetentionConfig{
		Policy:  "gfs",
		Daily:   1,
		Weekly:  1,
		Monthly: 0,
		Yearly:  -1,
	})
	if err != nil {
		t.Fatalf("ApplyRetention(gfs) error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("ApplyRetention(gfs) deleted = %d, want 2", deleted)
	}

	// The newest (daily) and the first eligible weekly backup should remain.
	kept := []time.Time{timestamps[0], timestamps[1]}
	removed := []time.Time{timestamps[2], timestamps[3]}

	for _, ts := range kept {
		path := filepath.Join(backupDir, fmt.Sprintf("node-gfs-backup-%s.tar.zst", ts.Format("20060102-150405")))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected backup %s to remain: %v", path, err)
		}
	}
	for _, ts := range removed {
		path := filepath.Join(backupDir, fmt.Sprintf("node-gfs-backup-%s.tar.zst", ts.Format("20060102-150405")))
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected backup %s to be deleted, err=%v", path, err)
		}
		logPath := filepath.Join(logDir, fmt.Sprintf("backup-node-gfs-%s.log", ts.Format("20060102-150405")))
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("expected log %s to be deleted, err=%v", logPath, err)
		}
	}

	summary := storage.LastRetentionSummary()
	if !summary.HasLogInfo {
		t.Fatalf("LastRetentionSummary() should report log info")
	}
	if summary.BackupsDeleted != 2 || summary.BackupsRemaining != 2 {
		t.Fatalf("unexpected backup summary: %+v", summary)
	}
	if summary.LogsDeleted != 2 || summary.LogsRemaining != 2 {
		t.Fatalf("unexpected log summary: %+v", summary)
	}
}

func TestSecondaryStorageListSkipsAssociatedAndBundles(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         baseDir,
		BundleAssociatedFiles: true,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	ctx := context.Background()

	bundleBase := filepath.Join(baseDir, "alpha-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(bundleBase, []byte("orig"), 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}
	if err := os.WriteFile(bundleBase+".bundle.tar", []byte("bundle"), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := os.WriteFile(bundleBase+".metadata", []byte("meta"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.Chtimes(bundleBase+".bundle.tar", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes bundle: %v", err)
	}

	legacy := filepath.Join(baseDir, "proxmox-backup-legacy.tar.gz")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if err := os.Chtimes(legacy, time.Date(2023, 12, 31, 23, 0, 0, 0, time.UTC), time.Date(2023, 12, 31, 23, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes legacy: %v", err)
	}
	if err := os.WriteFile(legacy+".sha256", []byte("hash"), 0o600); err != nil {
		t.Fatalf("write sha: %v", err)
	}

	backups, err := storage.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("List() returned %d backups, want 2", len(backups))
	}

	expectedOrder := []string{
		bundleBase + ".bundle.tar",
		legacy,
	}
	for i, backup := range backups {
		if backup.BackupFile != expectedOrder[i] {
			t.Fatalf("List()[%d] = %s, want %s", i, backup.BackupFile, expectedOrder[i])
		}
		if strings.HasSuffix(backup.BackupFile, ".metadata") || strings.HasSuffix(backup.BackupFile, ".sha256") {
			t.Fatalf("List() should not include associated file %s", backup.BackupFile)
		}
	}
}

func TestSecondaryStorageStoreHandlesBundles(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         destDir,
		BundleAssociatedFiles: true,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	backupFile := filepath.Join(srcDir, "node-bundle-backup-20240202-020202.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if err := os.WriteFile(backupFile+".metadata", []byte("meta"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(backupFile+".bundle.tar", []byte("bundle"), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	destBackup := filepath.Join(destDir, filepath.Base(backupFile))
	if _, err := os.Stat(destBackup); err != nil {
		t.Fatalf("expected backup to be copied: %v", err)
	}

	destBundle := filepath.Join(destDir, filepath.Base(backupFile)+".bundle.tar")
	if _, err := os.Stat(destBundle); err != nil {
		t.Fatalf("expected bundle to be copied: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, filepath.Base(backupFile)+".metadata")); !os.IsNotExist(err) {
		t.Fatalf("metadata should not be copied when bundling is enabled, err=%v", err)
	}
}

func TestSecondaryStorageStoreHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled: true,
		SecondaryPath:    destDir,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	backupFile := filepath.Join(srcDir, "node-cancel-backup-20240303-030303.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := storage.Store(ctx, backupFile, &types.BackupMetadata{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Store() error = %v, want context.Canceled", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, filepath.Base(backupFile))); !os.IsNotExist(err) {
		t.Fatalf("backup should not be copied on cancellation, err=%v", err)
	}
}

func newSecondaryStorageForTest(t *testing.T, cfg *config.Config) *SecondaryStorage {
	t.Helper()
	storage, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage() error = %v", err)
	}
	return storage
}

func createSecondaryBackup(t *testing.T, backupDir, logDir, host string, ts time.Time) string {
	t.Helper()

	name := fmt.Sprintf("%s-backup-%s.tar.zst", host, ts.Format("20060102-150405"))
	path := filepath.Join(backupDir, name)
	if err := os.WriteFile(path, []byte(ts.String()), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("chtimes backup: %v", err)
	}

	if logDir != "" {
		logName := fmt.Sprintf("backup-%s-%s.log", host, ts.Format("20060102-150405"))
		if err := os.WriteFile(filepath.Join(logDir, logName), []byte("log"), 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}

	return path
}

func TestSecondaryStorageGetStatsIncludesFilesystemInfo(t *testing.T) {
	t.Parallel()

	backupDir := t.TempDir()
	logDir := t.TempDir()

	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		SecondaryLogPath:      logDir,
		BundleAssociatedFiles: false,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	// Simulate DetectFilesystem having already populated fsInfo.
	storage.fsInfo = &FilesystemInfo{Type: FilesystemExt4}

	ts1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)
	createSecondaryBackup(t, backupDir, logDir, "node-stats", ts1)
	createSecondaryBackup(t, backupDir, logDir, "node-stats", ts2)

	stats, err := storage.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}

	if stats.TotalBackups != 2 {
		t.Fatalf("TotalBackups = %d, want 2", stats.TotalBackups)
	}
	if stats.TotalSize == 0 {
		t.Fatalf("TotalSize should be > 0")
	}
	if stats.OldestBackup == nil || !stats.OldestBackup.Equal(ts1) {
		t.Fatalf("OldestBackup = %v, want %v", stats.OldestBackup, ts1)
	}
	if stats.NewestBackup == nil || !stats.NewestBackup.Equal(ts2) {
		t.Fatalf("NewestBackup = %v, want %v", stats.NewestBackup, ts2)
	}
	if stats.FilesystemType != FilesystemExt4 {
		t.Fatalf("FilesystemType = %s, want %s", stats.FilesystemType, FilesystemExt4)
	}
	if stats.TotalSpace == 0 || stats.AvailableSpace == 0 {
		t.Fatalf("expected filesystem stats to be populated (TotalSpace=%d, AvailableSpace=%d)", stats.TotalSpace, stats.AvailableSpace)
	}
	if stats.UsedSpace != stats.TotalSpace-stats.AvailableSpace {
		t.Fatalf("UsedSpace mismatch: got %d want %d", stats.UsedSpace, stats.TotalSpace-stats.AvailableSpace)
	}
}

func TestSecondaryStorageGetStatsListError(t *testing.T) {
	t.Parallel()

	storage := newSecondaryStorageForTest(t, &config.Config{
		SecondaryEnabled: true,
		SecondaryPath:    t.TempDir(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := storage.GetStats(ctx)
	if err == nil {
		t.Fatalf("GetStats() should fail when List() fails")
	}
}

func TestSecondaryStorageVerifyUpload(t *testing.T) {
	t.Parallel()

	storage := &SecondaryStorage{}
	ok, err := storage.VerifyUpload(context.Background(), "local", "remote")
	if err != nil {
		t.Fatalf("VerifyUpload() error = %v", err)
	}
	if !ok {
		t.Fatalf("VerifyUpload() = false, want true")
	}
}

func TestSecondaryStorageBasicAccessors(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}
	storage, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage() error = %v", err)
	}

	if storage.Name() != "Secondary Storage" {
		t.Fatalf("Name() = %s, want Secondary Storage", storage.Name())
	}
	if storage.Location() != LocationSecondary {
		t.Fatalf("Location() = %v, want %v", storage.Location(), LocationSecondary)
	}
	if !storage.IsEnabled() {
		t.Fatalf("IsEnabled() should be true when path configured")
	}
	if storage.IsCritical() {
		t.Fatalf("IsCritical() should be false for secondary storage")
	}

	storage.config.SecondaryEnabled = false
	if storage.IsEnabled() {
		t.Fatalf("IsEnabled() should be false when disabled")
	}
}

func TestSecondaryStorageDetectFilesystemCreatesDirectory(t *testing.T) {
	t.Parallel()

	tmpDir := filepath.Join(t.TempDir(), "secondary")
	cfg := &config.Config{
		SecondaryEnabled: true,
		SecondaryPath:    tmpDir,
	}
	storage, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage() error = %v", err)
	}

	info, err := storage.DetectFilesystem(context.Background())
	if err != nil {
		t.Fatalf("DetectFilesystem() error = %v", err)
	}
	if info == nil {
		t.Fatalf("DetectFilesystem() returned nil info")
	}
	if _, err := os.Stat(tmpDir); err != nil {
		t.Fatalf("expected directory to be created, stat err=%v", err)
	}
}

func TestSecondaryStorageDeleteRemovesFiles(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         baseDir,
		BundleAssociatedFiles: false,
	}
	storage := newSecondaryStorageForTest(t, cfg)

	ts := time.Date(2024, 2, 2, 2, 2, 2, 0, time.UTC)
	path := createSecondaryBackup(t, baseDir, "", "delete-node", ts)
	for _, suffix := range []string{".sha256", ".metadata", ".metadata.sha256"} {
		if err := os.WriteFile(path+suffix, []byte("aux"), 0o600); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	if err := storage.Delete(context.Background(), path); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	for _, candidate := range []string{path, path + ".sha256", path + ".metadata", path + ".metadata.sha256"} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, err=%v", candidate, err)
		}
	}
}
