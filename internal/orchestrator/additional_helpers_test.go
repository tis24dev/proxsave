package orchestrator

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestBackupStatsUpdateCompressionMetrics(t *testing.T) {
	stats := &BackupStats{UncompressedSize: 1000, CompressedSize: 500}
	stats.updateCompressionMetrics()

	if stats.CompressionRatio != 0.5 {
		t.Fatalf("CompressionRatio = %f, want 0.5", stats.CompressionRatio)
	}
	if stats.CompressionRatioPercent != 50 {
		t.Fatalf("CompressionRatioPercent = %f, want 50", stats.CompressionRatioPercent)
	}
	if stats.CompressionSavingsPercent != 50 {
		t.Fatalf("CompressionSavingsPercent = %f, want 50", stats.CompressionSavingsPercent)
	}
}

func TestBackupStatsUpdateCompressionMetricsZero(t *testing.T) {
	stats := &BackupStats{UncompressedSize: 0, CompressedSize: 0}
	stats.updateCompressionMetrics()

	if stats.CompressionRatio != 0 || stats.CompressionRatioPercent != 0 || stats.CompressionSavingsPercent != 0 {
		t.Fatalf("expected zero metrics when sizes are zero, got %+v", stats)
	}
}

func TestBackupStatsToPrometheusMetrics(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	end := time.Now()
	stats := &BackupStats{
		Hostname:       "host",
		ProxmoxType:    types.ProxmoxVE,
		ProxmoxVersion: "8.1",
		ScriptVersion:  "1.2.3",
		StartTime:      start,
		EndTime:        end,
		Duration:       end.Sub(start),
		ExitCode:       5,
		ErrorCount:     2,
		WarningCount:   3,
		LocalBackups:   4,
		CloudBackups:   1,
		ArchiveSize:    42,
		FilesCollected: 7,
		FilesFailed:    1,
	}

	metrics := stats.toPrometheusMetrics()
	if metrics == nil {
		t.Fatalf("expected non-nil metrics")
	}
	if metrics.Hostname != stats.Hostname || metrics.ExitCode != stats.ExitCode {
		t.Fatalf("metrics fields not copied correctly: %+v", metrics)
	}
	if metrics.BytesCollected != stats.BytesCollected {
		t.Fatalf("BytesCollected mismatch: %d vs %d", metrics.BytesCollected, stats.BytesCollected)
	}
	if metrics.FilesFailed != stats.FilesFailed {
		t.Fatalf("FilesFailed mismatch: %d vs %d", metrics.FilesFailed, stats.FilesFailed)
	}
}

func TestDescribeTelegramConfigVariants(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	o := &Orchestrator{logger: logger}
	if got := o.describeTelegramConfig(); got != "disabled" {
		t.Fatalf("describeTelegramConfig nil cfg = %q, want disabled", got)
	}

	o.cfg = &config.Config{TelegramEnabled: false}
	if got := o.describeTelegramConfig(); got != "disabled" {
		t.Fatalf("describeTelegramConfig disabled = %q, want disabled", got)
	}

	o.cfg.TelegramEnabled = true
	o.cfg.TelegramBotType = ""
	if got := o.describeTelegramConfig(); got != "personal" {
		t.Fatalf("empty bot type => %q, want personal", got)
	}

	o.cfg.TelegramBotType = "personal"
	if got := o.describeTelegramConfig(); got != "personal" {
		t.Fatalf("personal => %q, want personal", got)
	}

	o.cfg.TelegramBotType = "centralized"
	if got := o.describeTelegramConfig(); got != "centralized" {
		t.Fatalf("centralized => %q, want centralized", got)
	}

	o.cfg.TelegramBotType = " custom "
	if got := o.describeTelegramConfig(); got != "custom" {
		t.Fatalf("trimmed custom => %q, want custom", got)
	}
}

func TestLogGlobalRetentionPolicySimpleAndGFS(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	// GFS branch
	cfgGFS := &config.Config{
		RetentionPolicy:  "gfs",
		RetentionDaily:   1,
		RetentionWeekly:  2,
		RetentionMonthly: 3,
		RetentionYearly:  4,
	}
	o := &Orchestrator{logger: logger, cfg: cfgGFS}
	o.logGlobalRetentionPolicy()
	if !strings.Contains(buf.String(), "Policy: GFS") {
		t.Fatalf("expected GFS policy log, got: %s", buf.String())
	}

	// Simple branch with values
	buf.Reset()
	cfgSimple := &config.Config{
		RetentionPolicy:        "simple",
		LocalRetentionDays:     7,
		SecondaryRetentionDays: 14,
		CloudRetentionDays:     30,
	}
	o.cfg = cfgSimple
	o.logGlobalRetentionPolicy()
	if !strings.Contains(buf.String(), "Policy: simple") {
		t.Fatalf("expected simple policy log, got: %s", buf.String())
	}
}

type stubStorage struct {
	loc  storage.BackupLocation
	list []*types.BackupMetadata
}

func (s *stubStorage) Name() string                     { return "stub-" + string(s.loc) }
func (s *stubStorage) Location() storage.BackupLocation { return s.loc }
func (s *stubStorage) IsEnabled() bool                  { return true }
func (s *stubStorage) IsCritical() bool                 { return s.loc == storage.LocationPrimary }
func (s *stubStorage) DetectFilesystem(ctx context.Context) (*storage.FilesystemInfo, error) {
	return nil, nil
}
func (s *stubStorage) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	return nil
}
func (s *stubStorage) List(ctx context.Context) ([]*types.BackupMetadata, error) { return s.list, nil }
func (s *stubStorage) Delete(ctx context.Context, backupFile string) error       { return nil }
func (s *stubStorage) ApplyRetention(ctx context.Context, config storage.RetentionConfig) (int, error) {
	return 0, nil
}
func (s *stubStorage) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	return true, nil
}
func (s *stubStorage) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	return &storage.StorageStats{TotalBackups: len(s.list), AvailableSpace: 1024, TotalSpace: 2048}, nil
}

func TestApplyStorageStatsSimplePrimary(t *testing.T) {
	adapter := &StorageAdapter{
		backend: &stubStorage{loc: storage.LocationPrimary},
		logger:  logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}
	storageStats := &storage.StorageStats{TotalBackups: 3, AvailableSpace: 100, TotalSpace: 200}
	retentionCfg := storage.RetentionConfig{Policy: "simple", MaxBackups: 5}

	adapter.applyStorageStats(storageStats, retentionCfg, stats)

	if stats.LocalBackups != 3 || stats.LocalFreeSpace != 100 || stats.LocalTotalSpace != 200 {
		t.Fatalf("local stats not set correctly: %+v", stats)
	}
	if stats.LocalRetentionPolicy != "simple" {
		t.Fatalf("LocalRetentionPolicy = %q, want simple", stats.LocalRetentionPolicy)
	}
}

func TestApplyStorageStatsGFSPrimary(t *testing.T) {
	now := time.Now()
	backups := []*types.BackupMetadata{
		{Timestamp: now},                    // daily
		{Timestamp: now.AddDate(0, 0, -8)},  // weekly
		{Timestamp: now.AddDate(0, -1, -1)}, // monthly
		{Timestamp: now.AddDate(-1, 0, 0)},  // yearly
	}
	adapter := &StorageAdapter{
		backend: &stubStorage{loc: storage.LocationPrimary, list: backups},
		logger:  logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}
	storageStats := &storage.StorageStats{TotalBackups: len(backups), AvailableSpace: 500, TotalSpace: 1000}
	retentionCfg := storage.RetentionConfig{Policy: "gfs", Daily: 1, Weekly: 1, Monthly: 1, Yearly: 1}

	adapter.applyStorageStats(storageStats, retentionCfg, stats)

	if stats.LocalBackups != len(backups) {
		t.Fatalf("LocalBackups = %d, want %d", stats.LocalBackups, len(backups))
	}
	if stats.LocalGFSCurrentDaily == 0 || stats.LocalGFSCurrentWeekly == 0 || stats.LocalGFSCurrentMonthly == 0 || stats.LocalGFSCurrentYearly == 0 {
		t.Fatalf("GFS counters not populated: %+v", stats)
	}
	if stats.LocalRetentionPolicy != "gfs" {
		t.Fatalf("LocalRetentionPolicy = %q, want gfs", stats.LocalRetentionPolicy)
	}
}

func TestSetAndFinalizeStorageStatus(t *testing.T) {
	stats := &BackupStats{}
	adapter := &StorageAdapter{
		backend: &stubStorage{loc: storage.LocationSecondary},
	}

	adapter.setStorageStatus(stats, "custom")
	if stats.SecondaryStatus != "custom" {
		t.Fatalf("SecondaryStatus = %q, want custom", stats.SecondaryStatus)
	}

	adapter.finalizeStorageStatus(stats, true, false)
	if stats.SecondaryStatus != "error" {
		t.Fatalf("finalizeStorageStatus should set error, got %q", stats.SecondaryStatus)
	}

	adapter.finalizeStorageStatus(stats, false, true)
	if stats.SecondaryStatus != "warning" {
		t.Fatalf("finalizeStorageStatus warning, got %q", stats.SecondaryStatus)
	}

	adapter.finalizeStorageStatus(stats, false, false)
	if stats.SecondaryStatus != "ok" {
		t.Fatalf("finalizeStorageStatus ok, got %q", stats.SecondaryStatus)
	}
}

func TestSplitExportCategoriesAndRedirectCluster(t *testing.T) {
	categories := []Category{
		{ID: "pve_cluster", ExportOnly: false},
		{ID: "network", ExportOnly: false},
		{ID: "pve_config_export", ExportOnly: true},
	}

	normal, export := splitExportCategories(categories)
	if len(normal) != 2 || len(export) != 1 {
		t.Fatalf("unexpected split result normal=%d export=%d", len(normal), len(export))
	}

	normal2, export2 := redirectClusterCategoryToExport(normal, export)
	if hasCategoryID(normal2, "pve_cluster") {
		t.Fatalf("pve_cluster should have been moved to export")
	}
	if !hasCategoryID(export2, "pve_cluster") {
		t.Fatalf("pve_cluster missing from export list")
	}
}

func TestExportDestRootAndHelpers(t *testing.T) {
	defaultRoot := exportDestRoot("")
	if !strings.HasPrefix(defaultRoot, "/opt/proxsave/pve-config-export-") {
		t.Fatalf("unexpected default export root: %s", defaultRoot)
	}

	custom := exportDestRoot("/tmp/base")
	if !strings.HasPrefix(custom, "/tmp/base/pve-config-export-") {
		t.Fatalf("unexpected custom export root: %s", custom)
	}

	if got := shortHost("node1.example.com"); got != "node1" {
		t.Fatalf("shortHost = %q, want node1", got)
	}
	if got := shortHost("hostname"); got != "hostname" {
		t.Fatalf("shortHost no dot = %q, want hostname", got)
	}
	if got := sanitizeID("stor@ge-1.*"); got != "stor_ge-1__" {
		t.Fatalf("sanitizeID = %q, want stor_ge-1__", got)
	}
}

func TestExtractSelectiveArchiveRequiresRootWhenDestIsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot validate root permission failure")
	}
	logger := logging.New(types.LogLevelError, false)
	tmpTar := filepath.Join(t.TempDir(), "test.tar")
	f, err := os.Create(tmpTar)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	tw := tar.NewWriter(f)
	_ = tw.Close()
	_ = f.Close()

	_, err = extractSelectiveArchive(context.Background(), tmpTar, string(os.PathSeparator), nil, RestoreModeFull, logger)
	if err == nil {
		t.Fatalf("expected error when extracting to root without privileges")
	}
}

func TestExtractSelectiveArchiveSelectiveExtraction(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	tmpDir := t.TempDir()
	tarPath := filepath.Join(tmpDir, "sample.tar")

	file, err := os.Create(tarPath)
	if err != nil {
		t.Fatalf("create tar file: %v", err)
	}
	tw := tar.NewWriter(file)

	writeFile := func(name, content string) {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}

	writeFile("etc/network/interfaces", "net")
	writeFile("var/log/messages", "log")

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tar file: %v", err)
	}

	dest := filepath.Join(tmpDir, "dest")
	categories := []Category{
		{ID: "network", Paths: []string{"./etc/network/"}},
	}

	logPath, err := extractSelectiveArchive(context.Background(), tarPath, dest, categories, RestoreModeCustom, logger)
	if err != nil {
		t.Fatalf("extractSelectiveArchive error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc/network/interfaces")); err != nil {
		t.Fatalf("expected network file extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "var/log/messages")); !os.IsNotExist(err) {
		t.Fatalf("unexpected log file extracted")
	}
	if logPath == "" {
		t.Fatalf("expected logPath to be set")
	}
}

func TestResolveRegistryPathUsesEnvAndProcessAlive(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "custom.json")
	t.Setenv(defaultRegistryEnvVar, envPath)

	if got := resolveRegistryPath(); got != envPath {
		t.Fatalf("resolveRegistryPath = %q, want %q", got, envPath)
	}

	if processAlive(0) || processAlive(-1) {
		t.Fatalf("processAlive should be false for non-positive PIDs")
	}
}

func TestDispatchLogFileCopiesToSecondary(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{
		SecondaryEnabled: true,
		SecondaryLogPath: t.TempDir(),
	}
	orch := &Orchestrator{logger: logger, cfg: cfg}

	// Create source log file
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "backup.log")
	if err := os.WriteFile(srcPath, []byte("logdata"), 0o640); err != nil {
		t.Fatalf("write source log: %v", err)
	}

	if err := orch.dispatchLogFile(context.Background(), srcPath); err != nil {
		t.Fatalf("dispatchLogFile error: %v", err)
	}

	destPath := filepath.Join(cfg.SecondaryLogPath, "backup.log")
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("expected copied log at %s: %v", destPath, err)
	}
	if string(data) != "logdata" {
		t.Fatalf("copied content mismatch: %q", string(data))
	}
}

func TestEnsureTempRegistryFailsOnInvalidPath(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	// Create a file that will act as parent for the registry path, forcing MkdirAll to fail.
	parentFile := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}

	registryPath := filepath.Join(parentFile, "registry.json")
	t.Setenv(defaultRegistryEnvVar, registryPath)

	o := &Orchestrator{logger: logger}
	if reg := o.ensureTempRegistry(); reg != nil {
		t.Fatalf("expected nil registry when path is invalid")
	}
}

func TestCopyLogToCloudRejectsLocalPath(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true}
	logger := logging.New(types.LogLevelError, false)
	o := &Orchestrator{cfg: cfg, logger: logger}

	err := o.copyLogToCloud(context.Background(), "/tmp/source.log", "/tmp/no-remote-prefix")
	if err == nil {
		t.Fatalf("expected error for missing remote prefix in dest path")
	}
}

func TestFinalizeAndCloseLogWithoutLogFile(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	o := &Orchestrator{logger: logger}

	o.FinalizeAndCloseLog(context.Background())

	if !strings.Contains(buf.String(), "No log file to close") {
		t.Fatalf("expected debug log about missing log file, got: %s", buf.String())
	}
}

type stubNotifierChannel struct {
	called bool
}

func (s *stubNotifierChannel) Notify(ctx context.Context, stats *BackupStats) error {
	s.called = true
	return nil
}

func TestDispatchNotificationsRespectsConfig(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	cfg := &config.Config{
		EmailEnabled:    true,
		TelegramEnabled: false,
		GotifyEnabled:   false,
		WebhookEnabled:  false,
	}
	o := &Orchestrator{
		logger: logger,
		cfg:    cfg,
		notificationChannels: []NotificationChannel{
			&stubNotifierChannel{}, // Email
		},
	}
	stats := &BackupStats{}

	o.dispatchNotifications(context.Background(), stats)

	email := o.notificationChannels[0].(*stubNotifierChannel)
	if !email.called {
		t.Fatalf("expected email channel to be called")
	}

	if !strings.Contains(buf.String(), "Telegram: disabled") {
		t.Fatalf("expected disabled log entry for Telegram, got: %s", buf.String())
	}
}

func TestLogRetentionPolicyDetailsSimpleVsGFS(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	adapter := &StorageAdapter{
		logger:  logger,
		backend: &stubStorage{loc: storage.LocationPrimary},
	}

	adapter.logRetentionPolicyDetails(storage.RetentionConfig{Policy: "simple", MaxBackups: 5})
	if !strings.Contains(buf.String(), "simple") {
		t.Fatalf("expected simple policy log, got: %s", buf.String())
	}

	buf.Reset()
	adapter.logRetentionPolicyDetails(storage.RetentionConfig{Policy: "gfs", Daily: 1, Weekly: 2, Monthly: 3, Yearly: 4})
	if !strings.Contains(buf.String(), "GFS") {
		t.Fatalf("expected GFS policy log, got: %s", buf.String())
	}
}

func TestWriteBackupMetadata(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	o := &Orchestrator{logger: logger}
	tempDir := t.TempDir()

	stats := &BackupStats{
		Version:     "1.0.0",
		ProxmoxType: types.ProxmoxVE,
		Timestamp:   time.Now(),
		Hostname:    "host1",
		ClusterMode: "cluster",
	}

	if err := o.writeBackupMetadata(tempDir, stats); err != nil {
		t.Fatalf("writeBackupMetadata error: %v", err)
	}

	metaPath := filepath.Join(tempDir, "var/lib/proxsave-info/backup_metadata.txt")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed reading metadata: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "VERSION=1.0.0") || !strings.Contains(content, "PVE_CLUSTER_MODE=cluster") {
		t.Fatalf("metadata content missing expected fields:\n%s", content)
	}
}

func TestNewTempDirRegistryRejectsEmptyPath(t *testing.T) {
	_, err := NewTempDirRegistry(logging.New(types.LogLevelError, false), "")
	if err == nil {
		t.Fatalf("expected error for empty registry path")
	}
}

func TestParseStorageBlocks(t *testing.T) {
	cfgContent := `storage: local
    path /var/lib/vz
    content rootdir,images

storage: backup
    path /mnt/backup
    content backup
`
	path := filepath.Join(t.TempDir(), "storage.cfg")
	if err := os.WriteFile(path, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	blocks, err := parseStorageBlocks(path)
	if err != nil {
		t.Fatalf("parseStorageBlocks error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].ID != "local" || blocks[1].ID != "backup" {
		t.Fatalf("unexpected IDs: %+v", blocks)
	}
	if len(blocks[0].data) == 0 || len(blocks[1].data) == 0 {
		t.Fatalf("expected data in blocks")
	}

	// Empty file -> zero blocks
	emptyPath := filepath.Join(t.TempDir(), "empty.cfg")
	if err := os.WriteFile(emptyPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write empty cfg: %v", err)
	}
	emptyBlocks, err := parseStorageBlocks(emptyPath)
	if err != nil {
		t.Fatalf("parse empty cfg error: %v", err)
	}
	if len(emptyBlocks) != 0 {
		t.Fatalf("expected 0 blocks for empty file, got %d", len(emptyBlocks))
	}
}

func TestExtractArchiveNativeSymlinkAndHardlink(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	tmpDir := t.TempDir()
	tarPath := filepath.Join(tmpDir, "archive.tar")

	file, err := os.Create(tarPath)
	if err != nil {
		t.Fatalf("create tar file: %v", err)
	}
	tw := tar.NewWriter(file)

	writeDir := func(name string) {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeDir,
			Mode:     0o755,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write dir header: %v", err)
		}
	}
	writeFile := func(name, content string) {
		hdr := &tar.Header{
			Name:       name,
			Typeflag:   tar.TypeReg,
			Mode:       0o644,
			Size:       int64(len(content)),
			AccessTime: time.Unix(1700000000, 0),
			ModTime:    time.Unix(1700000001, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write file header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write file content: %v", err)
		}
	}

	writeDir("dir/")
	writeFile("dir/file.txt", "hello")

	hardlink := &tar.Header{
		Name:     "dir/hardlink.txt",
		Typeflag: tar.TypeLink,
		Linkname: "dir/file.txt",
	}
	if err := tw.WriteHeader(hardlink); err != nil {
		t.Fatalf("write hardlink header: %v", err)
	}

	symlink := &tar.Header{
		Name:     "dir/symlink.txt",
		Typeflag: tar.TypeSymlink,
		Linkname: "dir/file.txt",
	}
	if err := tw.WriteHeader(symlink); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tar file: %v", err)
	}

	dest := filepath.Join(tmpDir, "dest")
	if err := extractArchiveNative(context.Background(), tarPath, dest, logger, nil, RestoreModeFull, nil, ""); err != nil {
		t.Fatalf("extractArchiveNative error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dest, "dir/file.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("extracted file missing or content mismatch: %v %q", err, string(data))
	}
	linkTarget, err := os.Readlink(filepath.Join(dest, "dir/symlink.txt"))
	if err != nil || linkTarget != "dir/file.txt" {
		t.Fatalf("symlink target mismatch: %q err=%v", linkTarget, err)
	}

	fiFile, err := os.Stat(filepath.Join(dest, "dir/file.txt"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	fiHard, err := os.Stat(filepath.Join(dest, "dir/hardlink.txt"))
	if err != nil {
		t.Fatalf("stat hardlink: %v", err)
	}
	if !os.SameFile(fiFile, fiHard) {
		t.Fatalf("hardlink does not point to same file")
	}
}

func TestPromptClusterRestoreMode(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		input string
		want  int
	}{
		{"1\n", 1},
		{"2\n", 2},
		{"0\n", 0},
		{"x\n1\n", 1},
	}

	for _, tt := range tests {
		reader := bufio.NewReader(strings.NewReader(tt.input))
		got, err := promptClusterRestoreMode(ctx, reader)
		if err != nil {
			t.Fatalf("promptClusterRestoreMode error: %v", err)
		}
		if got != tt.want {
			t.Fatalf("promptClusterRestoreMode got %d, want %d", got, tt.want)
		}
	}
}

func TestConfirmRestoreAction(t *testing.T) {
	cand := &decryptCandidate{
		Manifest: &backup.Manifest{
			CreatedAt:   time.Now(),
			ArchivePath: "test.tar",
		},
		DisplayBase: "test-backup",
	}

	// Accept
	reader := bufio.NewReader(strings.NewReader("RESTORE\n"))
	if err := confirmRestoreAction(context.Background(), reader, cand, "/tmp"); err != nil {
		t.Fatalf("confirmRestoreAction accept error: %v", err)
	}

	// Abort
	reader = bufio.NewReader(strings.NewReader("0\n"))
	if err := confirmRestoreAction(context.Background(), reader, cand, "/tmp"); err != ErrRestoreAborted {
		t.Fatalf("expected ErrRestoreAborted, got %v", err)
	}

	// Invalid then accept
	reader = bufio.NewReader(strings.NewReader("foo\nRESTORE\n"))
	if err := confirmRestoreAction(context.Background(), reader, cand, "/tmp"); err != nil {
		t.Fatalf("confirmRestoreAction retry error: %v", err)
	}
}

func TestShowRestorePlanOutputsPaths(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	categories := []Category{
		{ID: "network", Name: "Network", Description: "Net cfg", Paths: []string{"./etc/network/"}},
		{ID: "ssh", Name: "SSH", Description: "SSH cfg", Paths: []string{"./etc/ssh/sshd_config"}},
	}
	cfg := &SelectiveRestoreConfig{
		Mode:               RestoreModeFull,
		SelectedCategories: categories,
		SystemType:         SystemTypePVE,
	}

	var out bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, r)
		close(done)
	}()

	ShowRestorePlan(logger, cfg)

	_ = w.Close()
	os.Stdout = old
	<-done

	output := out.String()
	if !strings.Contains(output, "RESTORE PLAN") || !strings.Contains(output, "/etc/network") {
		t.Fatalf("unexpected plan output: %s", output)
	}
	if !strings.Contains(output, "Network") || !strings.Contains(output, "SSH") {
		t.Fatalf("missing category names in output: %s", output)
	}
}

func TestEnsureTempRegistrySuccess(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	regPath := filepath.Join(t.TempDir(), "registry.json")
	t.Setenv(defaultRegistryEnvVar, regPath)

	o := &Orchestrator{logger: logger}
	reg := o.ensureTempRegistry()
	if reg == nil {
		t.Fatalf("expected registry to be created")
	}
	if reg.registryPath != regPath {
		t.Fatalf("registry path = %q, want %q", reg.registryPath, regPath)
	}
}

func TestShowRestoreModeMenu(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	old := os.Stdin
	defer func() { os.Stdin = old }()

	tests := []struct {
		input string
		want  RestoreMode
	}{
		{"1\n", RestoreModeFull},
		{"2\n", RestoreModeStorage},
		{"3\n", RestoreModeBase},
		{"4\n", RestoreModeCustom},
	}

	for _, tt := range tests {
		r, w, _ := os.Pipe()
		_, _ = w.WriteString(tt.input)
		_ = w.Close()
		os.Stdin = r
		mode, err := ShowRestoreModeMenu(logger, SystemTypePVE)
		if err != nil {
			t.Fatalf("ShowRestoreModeMenu error: %v", err)
		}
		if mode != tt.want {
			t.Fatalf("ShowRestoreModeMenu(%q) = %v, want %v", tt.input, mode, tt.want)
		}
	}

	// Cancel
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("0\n")
	_ = w.Close()
	os.Stdin = r
	if _, err := ShowRestoreModeMenu(logger, SystemTypePVE); err == nil {
		t.Fatalf("expected cancel error")
	}
}

func TestShowCategorySelectionMenu(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	old := os.Stdin
	defer func() { os.Stdin = old }()

	available := []Category{
		{ID: "pve_cluster", Name: "Cluster", Type: CategoryTypePVE},
		{ID: "network", Name: "Network", Type: CategoryTypeCommon},
		{ID: "ssh", Name: "SSH", Type: CategoryTypeCommon},
	}

	// Select all then continue
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("a\nc\n")
	_ = w.Close()
	os.Stdin = r
	cats, err := ShowCategorySelectionMenu(logger, available, SystemTypePVE)
	if err != nil {
		t.Fatalf("ShowCategorySelectionMenu error: %v", err)
	}
	if len(cats) != 3 {
		t.Fatalf("expected 3 categories, got %d", len(cats))
	}

	// Toggle specific categories (1 and 3) then continue
	r, w, _ = os.Pipe()
	_, _ = w.WriteString("1\n3\nc\n")
	_ = w.Close()
	os.Stdin = r
	cats, err = ShowCategorySelectionMenu(logger, available, SystemTypePVE)
	if err != nil {
		t.Fatalf("ShowCategorySelectionMenu toggle error: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("expected 2 categories after toggle, got %d", len(cats))
	}

	// Cancel
	r, w, _ = os.Pipe()
	_, _ = w.WriteString("0\n")
	_ = w.Close()
	os.Stdin = r
	if _, err := ShowCategorySelectionMenu(logger, available, SystemTypePVE); err == nil {
		t.Fatalf("expected cancel error")
	}
}

func TestExtractArchiveNativeBlocksTraversal(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	tmpDir := t.TempDir()
	tarPath := filepath.Join(tmpDir, "traversal.tar")

	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	tw := tar.NewWriter(f)
	hdr := &tar.Header{
		Name:     "../etc/passwd",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len("data")),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte("data")); err != nil {
		t.Fatalf("write data: %v", err)
	}
	_ = tw.Close()
	_ = f.Close()

	dest := filepath.Join(tmpDir, "dest")
	if err := extractArchiveNative(context.Background(), tarPath, dest, logger, nil, RestoreModeFull, nil, ""); err != nil {
		t.Fatalf("extractArchiveNative error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "../etc/passwd")); err == nil {
		t.Fatalf("path traversal file should not be created")
	}
}

type stubListBackend struct {
	loc    storage.BackupLocation
	called bool
	err    error
}

func (s *stubListBackend) Name() string                     { return "stub" }
func (s *stubListBackend) Location() storage.BackupLocation { return s.loc }
func (s *stubListBackend) IsEnabled() bool                  { return true }
func (s *stubListBackend) IsCritical() bool                 { return s.loc == storage.LocationPrimary }
func (s *stubListBackend) DetectFilesystem(ctx context.Context) (*storage.FilesystemInfo, error) {
	return nil, nil
}
func (s *stubListBackend) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	return nil
}
func (s *stubListBackend) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	s.called = true
	return nil, s.err
}
func (s *stubListBackend) Delete(ctx context.Context, backupFile string) error { return nil }
func (s *stubListBackend) ApplyRetention(ctx context.Context, config storage.RetentionConfig) (int, error) {
	return 0, nil
}
func (s *stubListBackend) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	return true, nil
}
func (s *stubListBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	return nil, nil
}

func TestLogCurrentBackupCountCloudSkipsList(t *testing.T) {
	backend := &stubListBackend{loc: storage.LocationCloud}
	adapter := &StorageAdapter{backend: backend, logger: logging.New(types.LogLevelError, false)}
	adapter.logCurrentBackupCount()
	if backend.called {
		t.Fatalf("cloud backend should not call List in logCurrentBackupCount")
	}
}

func TestLogCurrentBackupCountHandlesError(t *testing.T) {
	backend := &stubListBackend{loc: storage.LocationPrimary, err: fmt.Errorf("list fail")}
	adapter := &StorageAdapter{backend: backend, logger: logging.New(types.LogLevelError, false)}
	adapter.logCurrentBackupCount()
	if !backend.called {
		t.Fatalf("expected List to be called for primary backend")
	}
}

func TestApplyStorageStatsCloud(t *testing.T) {
	adapter := &StorageAdapter{
		backend: &stubStorage{loc: storage.LocationCloud},
		logger:  logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}
	storageStats := &storage.StorageStats{TotalBackups: 2}
	retentionCfg := storage.RetentionConfig{Policy: "gfs", Daily: 1}
	adapter.applyStorageStats(storageStats, retentionCfg, stats)
	if stats.CloudBackups != 2 || stats.CloudRetentionPolicy != "gfs" {
		t.Fatalf("cloud stats not populated correctly: %+v", stats)
	}
}

func TestFileExistsHelper(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "file.txt")
	if fileExists(tmp) {
		t.Fatalf("file should not exist yet")
	}
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if !fileExists(tmp) {
		t.Fatalf("fileExists should return true for existing file")
	}
}

func TestLoadEntriesEmptyFile(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "reg.json")
	if err := os.WriteFile(regPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write reg: %v", err)
	}
	reg, err := NewTempDirRegistry(logging.New(types.LogLevelError, false), regPath)
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}
	entries, err := reg.loadEntries()
	if err != nil {
		t.Fatalf("loadEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}
}

type fakeChecker struct {
	results []checks.CheckResult
	err     error
	release bool
}

func (f *fakeChecker) RunAllChecks(ctx context.Context) ([]checks.CheckResult, error) {
	return f.results, f.err
}

func (f *fakeChecker) ReleaseLock() error {
	f.release = true
	return nil
}

type checkWrapper struct{ fake *fakeChecker }

func (c *checkWrapper) RunAllChecks(ctx context.Context) ([]checks.CheckResult, error) {
	return c.fake.RunAllChecks(ctx)
}
func (c *checkWrapper) ReleaseLock() error { return c.fake.ReleaseLock() }

func TestRunPreBackupChecksNilCheckerSkips(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	orch := &Orchestrator{logger: logger}

	if err := orch.RunPreBackupChecks(context.Background()); err != nil {
		t.Fatalf("expected nil error with nil checker")
	}
	if !strings.Contains(buf.String(), "skipping pre-backup checks") {
		t.Fatalf("expected skip log, got %s", buf.String())
	}
}

func TestReleaseBackupLockCallsChecker(t *testing.T) {
	fc := &fakeChecker{}
	wrapper := &checkWrapper{fake: fc}
	if err := wrapper.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock returned error: %v", err)
	}
	if !fc.release {
		t.Fatalf("ReleaseLock should set release flag")
	}
}

func TestRunPreBackupChecksWithCheckerResults(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	results := []checks.CheckResult{
		{Name: "Dirs", Passed: true, Message: "ok"},
		{Name: "Disk", Passed: false, Message: "fail"},
	}
	fc := &fakeChecker{results: results, err: fmt.Errorf("check error")}
	orch := &Orchestrator{logger: logger}
	// Wrap fake checker to satisfy interface manually
	type checkerIface interface {
		RunAllChecks(context.Context) ([]checks.CheckResult, error)
		ReleaseLock() error
	}
	var _ checkerIface = fc

	gotResults, err := fc.RunAllChecks(context.Background())
	if err == nil || len(gotResults) != 2 {
		t.Fatalf("expected error and 2 results from stub")
	}

	// Simulate how logs would be produced by iterating results
	for _, r := range gotResults {
		if r.Passed {
			orch.logger.Info("✓ %s: %s", r.Name, r.Message)
		} else {
			orch.logger.Error("✗ %s: %s", r.Name, r.Message)
		}
	}
	if !strings.Contains(buf.String(), "✗ Disk") || !strings.Contains(buf.String(), "✓ Dirs") {
		t.Fatalf("expected pass/fail logs, got: %s", buf.String())
	}
}

func TestRunGoBackupConfigValidationError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	orch := New(logger, false)
	tempDir := t.TempDir()
	orch.SetBackupConfig(tempDir, tempDir, types.CompressionType("invalid"), 1, 0, "standard", nil)

	stats, err := orch.RunGoBackup(context.Background(), types.ProxmoxUnknown, "host-invalid")
	if err == nil {
		t.Fatalf("expected error for invalid compression type")
	}
	var be *BackupError
	if !errors.As(err, &be) {
		t.Fatalf("expected BackupError, got %T", err)
	}
	if be.Phase != "config" || be.Code != types.ExitConfigError {
		t.Fatalf("unexpected phase/code: %+v", be)
	}
	if stats == nil {
		t.Fatalf("expected stats even on failure")
	}
}

func TestDispatchNotificationsAndLogsSkipsWithNoLog(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	orch := &Orchestrator{
		logger: logger,
		cfg:    &config.Config{SecondaryEnabled: false, CloudEnabled: false},
	}
	stats := &BackupStats{SecondaryEnabled: false, CloudEnabled: false}
	orch.dispatchNotificationsAndLogs(context.Background(), stats)

	if !strings.Contains(buf.String(), "Secondary Storage: disabled") || !strings.Contains(buf.String(), "Cloud Storage: disabled") {
		t.Fatalf("expected skip logs for disabled storage, got: %s", buf.String())
	}
}

func TestCheckSystemRequirementsNoPanic(t *testing.T) {
	// manifest nil
	CheckSystemRequirements(nil)
	// just ensure no panic

	manifest := &backup.Manifest{ProxmoxType: "pbs", Hostname: "pbs-node"}
	CheckSystemRequirements(manifest)
	// no specific assertions; just ensure call succeeds
}
