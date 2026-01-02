package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNewCollector(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	if collector == nil {
		t.Fatal("NewCollector returned nil")
	}

	if collector.tempDir != tempDir {
		t.Errorf("Expected tempDir %s, got %s", tempDir, collector.tempDir)
	}

	if collector.proxType != types.ProxmoxVE {
		t.Errorf("Expected proxType PVE, got %s", collector.proxType)
	}
}

func TestGetDefaultCollectorConfig(t *testing.T) {
	config := GetDefaultCollectorConfig()

	if !config.BackupClusterConfig {
		t.Error("Expected BackupClusterConfig to be true")
	}

	if !config.BackupNetworkConfigs {
		t.Error("Expected BackupNetworkConfigs to be true")
	}

	if !config.BackupVMConfigs {
		t.Error("Expected BackupVMConfigs to be true")
	}
}

func TestCollectorEnsureDir(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	testDir := filepath.Join(tempDir, "test", "nested", "dir")
	if err := collector.ensureDir(testDir); err != nil {
		t.Fatalf("ensureDir failed: %v", err)
	}

	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Error("Directory was not created")
	}

	if collector.stats.DirsCreated == 0 {
		t.Error("DirsCreated counter not incremented")
	}
}

func TestCollectorSafeCopyFile(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)
	ctx := context.Background()

	// Create a test source file
	srcFile := filepath.Join(tempDir, "source.txt")
	content := []byte("test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Copy to destination
	destFile := filepath.Join(tempDir, "dest", "dest.txt")
	if err := collector.safeCopyFile(ctx, srcFile, destFile, "test file"); err != nil {
		t.Fatalf("safeCopyFile failed: %v", err)
	}

	// Verify destination exists
	if _, err := os.Stat(destFile); os.IsNotExist(err) {
		t.Error("Destination file was not created")
	}

	// Verify content matches
	destContent, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}

	if string(destContent) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", content, destContent)
	}

	srcInfo, err := os.Stat(srcFile)
	if err != nil {
		t.Fatalf("Failed to stat source file: %v", err)
	}
	destInfo, err := os.Stat(destFile)
	if err != nil {
		t.Fatalf("Failed to stat destination file: %v", err)
	}

	if srcInfo.Mode().Perm() != destInfo.Mode().Perm() {
		t.Errorf("Mode mismatch: expected %04o, got %04o", srcInfo.Mode().Perm(), destInfo.Mode().Perm())
	}

	srcStat, srcOk := srcInfo.Sys().(*syscall.Stat_t)
	destStat, destOk := destInfo.Sys().(*syscall.Stat_t)
	if srcOk && destOk {
		if srcStat.Uid != destStat.Uid || srcStat.Gid != destStat.Gid {
			t.Errorf("Ownership mismatch: expected %d:%d, got %d:%d", srcStat.Uid, srcStat.Gid, destStat.Uid, destStat.Gid)
		}
	}

	if collector.stats.FilesProcessed == 0 {
		t.Error("FilesProcessed counter not incremented")
	}
}

func TestCollectorSafeCopyFileNotFound(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	// Try to copy non-existent file
	srcFile := filepath.Join(tempDir, "nonexistent.txt")
	destFile := filepath.Join(tempDir, "dest.txt")

	ctx := context.Background()

	err := collector.safeCopyFile(ctx, srcFile, destFile, "test file")

	// Should return nil (file not found is not an error, just logged)
	if err != nil {
		t.Errorf("Expected nil for non-existent file, got error: %v", err)
	}

	// Stats should not be incremented
	if collector.stats.FilesProcessed != 0 {
		t.Error("FilesProcessed should not be incremented for non-existent file")
	}
}

func TestCollectorSafeCopyDir(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	// Create test source directory with files
	srcDir := filepath.Join(tempDir, "source")
	os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("content2"), 0644)
	if err := os.Chmod(srcDir, 0700); err != nil {
		t.Fatalf("Failed to chmod source dir: %v", err)
	}
	if err := os.Chmod(filepath.Join(srcDir, "subdir"), 0711); err != nil {
		t.Fatalf("Failed to chmod source subdir: %v", err)
	}

	// Copy directory
	destDir := filepath.Join(tempDir, "dest")
	ctx := context.Background()

	if err := collector.safeCopyDir(ctx, srcDir, destDir, "test dir"); err != nil {
		t.Fatalf("safeCopyDir failed: %v", err)
	}

	// Verify files exist
	if _, err := os.Stat(filepath.Join(destDir, "file1.txt")); os.IsNotExist(err) {
		t.Error("file1.txt was not copied")
	}

	if _, err := os.Stat(filepath.Join(destDir, "subdir", "file2.txt")); os.IsNotExist(err) {
		t.Error("subdir/file2.txt was not copied")
	}

	// Verify content
	content1, _ := os.ReadFile(filepath.Join(destDir, "file1.txt"))
	if string(content1) != "content1" {
		t.Error("file1.txt content mismatch")
	}

	content2, _ := os.ReadFile(filepath.Join(destDir, "subdir", "file2.txt"))
	if string(content2) != "content2" {
		t.Error("file2.txt content mismatch")
	}

	// Verify directory permissions are preserved
	destRootInfo, err := os.Stat(destDir)
	if err != nil {
		t.Fatalf("Failed to stat dest dir: %v", err)
	}
	if destRootInfo.Mode().Perm() != 0700 {
		t.Errorf("Dest dir mode mismatch: expected %04o, got %04o", 0700, destRootInfo.Mode().Perm())
	}

	destSubInfo, err := os.Stat(filepath.Join(destDir, "subdir"))
	if err != nil {
		t.Fatalf("Failed to stat dest subdir: %v", err)
	}
	if destSubInfo.Mode().Perm() != 0711 {
		t.Errorf("Dest subdir mode mismatch: expected %04o, got %04o", 0711, destSubInfo.Mode().Perm())
	}

	// Verify file permissions are preserved
	destFile1Info, err := os.Stat(filepath.Join(destDir, "file1.txt"))
	if err != nil {
		t.Fatalf("Failed to stat copied file1.txt: %v", err)
	}
	if destFile1Info.Mode().Perm() != 0644 {
		t.Errorf("file1.txt mode mismatch: expected %04o, got %04o", 0644, destFile1Info.Mode().Perm())
	}

	destFile2Info, err := os.Stat(filepath.Join(destDir, "subdir", "file2.txt"))
	if err != nil {
		t.Fatalf("Failed to stat copied subdir/file2.txt: %v", err)
	}
	if destFile2Info.Mode().Perm() != 0644 {
		t.Errorf("file2.txt mode mismatch: expected %04o, got %04o", 0644, destFile2Info.Mode().Perm())
	}
}

func TestCollectorSafeCmdOutput(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	// Use a simple command that should be available on all systems
	outputFile := filepath.Join(tempDir, "output.txt")
	ctx := context.Background()
	err := collector.safeCmdOutput(ctx, "echo test", outputFile, "test command", false)

	if err != nil {
		t.Fatalf("safeCmdOutput failed: %v", err)
	}

	// Verify output file exists
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Error("Output file was not created")
	}

	// Verify content
	content, _ := os.ReadFile(outputFile)
	if len(content) == 0 {
		t.Error("Output file is empty")
	}
}

func TestCollectorSafeCmdOutputNonCriticalFailure(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	ctx := context.Background()
	outputFile := filepath.Join(tempDir, "non_critical.txt")

	if err := collector.safeCmdOutput(ctx, "false", outputFile, "non critical failure", false); err != nil {
		t.Fatalf("non-critical command should not return error: %v", err)
	}

	if collector.stats.FilesFailed != 0 {
		t.Fatalf("expected FilesFailed to remain 0, got %d", collector.stats.FilesFailed)
	}

	if _, err := os.Stat(outputFile); err == nil {
		t.Fatalf("non-critical failure should not create output file")
	}
}

func TestCollectorSafeCmdOutputCriticalFailure(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	ctx := context.Background()
	outputFile := filepath.Join(tempDir, "critical.txt")

	if err := collector.safeCmdOutput(ctx, "false", outputFile, "critical failure", true); err == nil {
		t.Fatalf("expected error for critical command failure")
	}

	if collector.stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed to be 1, got %d", collector.stats.FilesFailed)
	}

	if _, err := os.Stat(outputFile); err == nil {
		t.Fatalf("critical failure should not create output file")
	}
}

func TestCollectorSafeCmdOutputCommandNotFound(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	// Use a command that definitely doesn't exist
	outputFile := filepath.Join(tempDir, "output.txt")
	ctx := context.Background()
	err := collector.safeCmdOutput(ctx, "nonexistent_command_xyz", outputFile, "test command", false)

	// Should return nil (command not found is not an error for non-critical commands)
	if err != nil {
		t.Errorf("Expected nil for non-existent command, got error: %v", err)
	}

	// Output file should not be created
	if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
		t.Error("Output file should not be created for non-existent command")
	}
}

func TestCollectorDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	// Create collector in dry-run mode
	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, true)

	// Try to create directory in dry-run mode
	testDir := filepath.Join(tempDir, "dryrun", "test")
	if err := collector.ensureDir(testDir); err != nil {
		t.Fatalf("ensureDir in dry-run failed: %v", err)
	}

	// Directory should NOT be created
	if _, err := os.Stat(testDir); !os.IsNotExist(err) {
		t.Error("Directory should not be created in dry-run mode")
	}

	// Create a test file and try to copy it
	srcFile := filepath.Join(tempDir, "source.txt")
	os.WriteFile(srcFile, []byte("test"), 0644)

	destFile := filepath.Join(tempDir, "dryrun", "dest.txt")
	ctx := context.Background()
	if err := collector.safeCopyFile(ctx, srcFile, destFile, "test file"); err != nil {
		t.Fatalf("safeCopyFile in dry-run failed: %v", err)
	}

	// Destination should NOT be created
	if _, err := os.Stat(destFile); !os.IsNotExist(err) {
		t.Error("File should not be copied in dry-run mode")
	}

	// Stats should still be updated
	if collector.stats.FilesProcessed == 0 {
		t.Error("FilesProcessed should be incremented even in dry-run mode")
	}
}

func TestCollectSystemInfo(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxUnknown, false)

	ctx := context.Background()
	if err := collector.CollectSystemInfo(ctx); err != nil {
		t.Fatalf("CollectSystemInfo failed: %v", err)
	}

	// Verify commands directory was created (new implementation)
	commandsDir := filepath.Join(tempDir, "commands")
	if _, err := os.Stat(commandsDir); os.IsNotExist(err) {
		t.Error("commands directory was not created")
	}

	// Check that OS release file was created (CRITICAL command)
	osReleaseFile := filepath.Join(commandsDir, "os_release.txt")
	if _, err := os.Stat(osReleaseFile); os.IsNotExist(err) {
		t.Error("os_release.txt was not created")
	}

	// At least some files should have been processed
	// (Note: stats may be 0 in test environment without actual commands)
	if collector.stats.FilesFailed > collector.stats.FilesProcessed {
		t.Errorf("More files failed (%d) than processed (%d)",
			collector.stats.FilesFailed, collector.stats.FilesProcessed)
	}
}

func TestCollectSystemInfoWithCustomRootPrefix(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	root := t.TempDir()
	tempDir := t.TempDir()

	hostnamePath := filepath.Join(root, "etc", "hostname")
	if err := os.MkdirAll(filepath.Dir(hostnamePath), 0o755); err != nil {
		t.Fatalf("mkdir fake etc: %v", err)
	}
	if err := os.WriteFile(hostnamePath, []byte("custom-host\n"), 0o644); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root

	deps := defaultCollectorDeps()
	deps.LookPath = func(string) (string, error) { return "/bin/true", nil }
	deps.RunCommand = func(context.Context, string, ...string) ([]byte, error) { return []byte("stub"), nil }
	deps.RunCommandWithEnv = func(context.Context, []string, string, ...string) ([]byte, error) { return []byte("stub"), nil }

	collector := NewCollectorWithDeps(logger, cfg, tempDir, types.ProxmoxUnknown, false, deps)

	ctx := context.Background()
	if err := collector.CollectSystemInfo(ctx); err != nil {
		t.Fatalf("CollectSystemInfo with custom root failed: %v", err)
	}

	gotHostname, err := os.ReadFile(filepath.Join(tempDir, "etc", "hostname"))
	if err != nil {
		t.Fatalf("hostname not copied: %v", err)
	}
	if string(gotHostname) != "custom-host\n" {
		t.Fatalf("hostname content mismatch: %q", string(gotHostname))
	}
}

func TestGetStats(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	stats := collector.GetStats()

	if stats == nil {
		t.Fatal("GetStats returned nil")
	}

	// Initially all counters should be 0
	if stats.FilesProcessed != 0 || stats.FilesFailed != 0 || stats.DirsCreated != 0 {
		t.Error("Initial stats should be all zeros")
	}

	// Perform an operation
	testDir := filepath.Join(tempDir, "test")
	collector.ensureDir(testDir)

	// Check stats updated
	stats = collector.GetStats()
	if stats.DirsCreated != 1 {
		t.Errorf("Expected DirsCreated=1, got %d", stats.DirsCreated)
	}
}

func TestCollectorShouldExcludePatterns(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	config.ExcludePatterns = []string{
		"*.log",
		"etc/pve/**",
		"**/cache/**",
	}

	tempDir := t.TempDir()
	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	absoluteEtc := string(os.PathSeparator) + filepath.Join("etc", "pve", "nodes", "node1.conf")
	tempCacheDir := filepath.Join(tempDir, "cache", "session")

	testCases := []struct {
		name     string
		path     string
		expected bool
	}{
		{"basename match", filepath.Join(tempDir, "errors.log"), true},
		{"double-star absolute", absoluteEtc, true},
		{"relative temp match", tempCacheDir, true},
		{"non matching file", filepath.Join(tempDir, "config", "system.yaml"), false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := collector.shouldExclude(tc.path)
			if result != tc.expected {
				t.Fatalf("shouldExclude(%s) = %v, want %v", tc.path, result, tc.expected)
			}
		})
	}
}

func TestSummarizeCommandOutput(t *testing.T) {
	var buf bytes.Buffer
	if got := summarizeCommandOutput(&buf); got != "(no stdout/stderr)" {
		t.Fatalf("empty buffer summary = %q", got)
	}

	buf.WriteString("line1\nline2")
	if got := summarizeCommandOutput(&buf); got != "line1 | line2" {
		t.Fatalf("summary unexpected: %q", got)
	}

	buf.Reset()
	buf.WriteString(strings.Repeat("x", 2100))
	summary := summarizeCommandOutput(&buf)
	if !strings.HasSuffix(summary, "…") {
		t.Fatalf("expected summary to end with ellipsis, got %q", summary[len(summary)-1:])
	}
	if len([]rune(summary)) != 2049 {
		t.Fatalf("expected rune length 2049, got %d", len([]rune(summary)))
	}
}

func TestCollectorClusteredPVEFlag(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	collector := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxVE, false)

	if collector.IsClusteredPVE() {
		t.Fatalf("expected default clusteredPVE to be false")
	}

	collector.clusteredPVE = true
	if !collector.IsClusteredPVE() {
		t.Fatalf("expected IsClusteredPVE to reflect flag")
	}
}
