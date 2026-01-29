package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type testFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	sys     any
	isDir   bool
}

func (fi testFileInfo) Name() string       { return fi.name }
func (fi testFileInfo) Size() int64        { return fi.size }
func (fi testFileInfo) Mode() os.FileMode  { return fi.mode }
func (fi testFileInfo) ModTime() time.Time { return fi.modTime }
func (fi testFileInfo) IsDir() bool        { return fi.isDir }
func (fi testFileInfo) Sys() any           { return fi.sys }

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
	commandsDir := filepath.Join(tempDir, "var/lib/proxsave-info", "commands", "system")
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

func TestSafeCopyFile_SymlinkRelative(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)
	ctx := context.Background()

	// Create a target file and a symlink to it
	targetFile := filepath.Join(tempDir, "target.txt")
	if err := os.WriteFile(targetFile, []byte("target content"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	symlinkPath := filepath.Join(tempDir, "symlink.txt")
	if err := os.Symlink("target.txt", symlinkPath); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	// Copy the symlink
	destPath := filepath.Join(tempDir, "dest", "symlink.txt")
	if err := collector.safeCopyFile(ctx, symlinkPath, destPath, "test symlink"); err != nil {
		t.Fatalf("safeCopyFile failed for symlink: %v", err)
	}

	// Verify destination is a symlink
	info, err := os.Lstat(destPath)
	if err != nil {
		t.Fatalf("Failed to stat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("Destination is not a symlink")
	}

	// Verify symlink target
	target, err := os.Readlink(destPath)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}
	if target != "target.txt" {
		t.Errorf("Symlink target mismatch: expected 'target.txt', got '%s'", target)
	}
}

func TestSafeCopyFile_SymlinkAbsolute(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)
	ctx := context.Background()

	// Create a target file and a symlink with absolute path
	targetFile := filepath.Join(tempDir, "target.txt")
	if err := os.WriteFile(targetFile, []byte("target content"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	symlinkPath := filepath.Join(tempDir, "symlink_abs.txt")
	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	// Copy the symlink
	destPath := filepath.Join(tempDir, "dest", "symlink_abs.txt")
	if err := collector.safeCopyFile(ctx, symlinkPath, destPath, "test symlink absolute"); err != nil {
		t.Fatalf("safeCopyFile failed for absolute symlink: %v", err)
	}

	// Verify destination is a symlink
	info, err := os.Lstat(destPath)
	if err != nil {
		t.Fatalf("Failed to stat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("Destination is not a symlink")
	}

	// Verify symlink target is absolute
	target, err := os.Readlink(destPath)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}
	if !filepath.IsAbs(target) {
		t.Errorf("Expected absolute symlink target, got '%s'", target)
	}
}

func TestSafeCopyFile_SymlinkCreationFailure_ErrorFormat(t *testing.T) {
	// This test verifies that symlink creation failures return an error
	// with a properly formatted message for notification parsing

	logger := logging.New(types.LogLevelDebug, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)
	ctx := context.Background()

	origSymlink := osSymlink
	t.Cleanup(func() { osSymlink = origSymlink })
	osSymlink = func(oldname, newname string) error { return syscall.EPERM }

	// Create a symlink pointing to a non-existent target (this is valid)
	symlinkPath := filepath.Join(tempDir, "broken_symlink.txt")
	if err := os.Symlink("/nonexistent/target", symlinkPath); err != nil {
		t.Fatalf("Failed to create broken symlink: %v", err)
	}

	destPath := filepath.Join(tempDir, "dest", "broken_symlink.txt")

	// The safeCopyFile should return an error for symlink creation failures
	err := collector.safeCopyFile(ctx, symlinkPath, destPath, "test broken symlink")
	if err == nil {
		t.Fatal("Expected error for symlink creation failure, got nil")
	}

	// Verify error message uses structured format with " - " separator
	errMsg := err.Error()
	if !strings.Contains(errMsg, "symlink creation failed - ") {
		t.Errorf("Error message should use structured format with ' - ' separator, got: %s", errMsg)
	}

	// Verify that FilesFailed was incremented
	if collector.stats.FilesFailed == 0 {
		t.Error("FilesFailed counter should be incremented for symlink failure")
	}
}

func TestSafeCopyFile_SymlinkReadlinkFailureIncrementsFailure(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	origReadlink := osReadlink
	t.Cleanup(func() { osReadlink = origReadlink })
	osReadlink = func(string) (string, error) { return "", syscall.EPERM }

	symlinkPath := filepath.Join(tempDir, "symlink.txt")
	if err := os.Symlink("target.txt", symlinkPath); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	destPath := filepath.Join(tempDir, "dest", "symlink.txt")
	err := collector.safeCopyFile(context.Background(), symlinkPath, destPath, "symlink readlink failure")
	if err == nil || !strings.Contains(err.Error(), "symlink read failed") {
		t.Fatalf("expected symlink read failure, got %v", err)
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCopyFile_ReturnsErrorWhenSourceOpenFails(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	src := filepath.Join(tempDir, "source.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dest := filepath.Join(tempDir, "dest", "out.txt")

	origOpen := osOpen
	t.Cleanup(func() { osOpen = origOpen })
	osOpen = func(name string) (*os.File, error) {
		if name == src {
			return nil, syscall.EPERM
		}
		return origOpen(name)
	}

	err := collector.safeCopyFile(context.Background(), src, dest, "open fail")
	if err == nil || !strings.Contains(err.Error(), "failed to open") {
		t.Fatalf("expected open error, got %v", err)
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

type errCloseFile struct {
	*os.File
}

func (f errCloseFile) Close() error {
	_ = f.File.Close()
	return syscall.EIO
}

func TestSafeCopyFile_ReturnsErrorWhenDestCloseFails(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	src := filepath.Join(tempDir, "source.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dest := filepath.Join(tempDir, "dest", "out.txt")

	origOpenFile := osOpenFile
	t.Cleanup(func() { osOpenFile = origOpenFile })
	osOpenFile = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return errCloseFile{File: f}, nil
	}

	err := collector.safeCopyFile(context.Background(), src, dest, "close fail")
	if err == nil || !strings.Contains(err.Error(), "failed to close") {
		t.Fatalf("expected close error, got %v", err)
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestCaptureCommandOutput_SystemctlStatusUnitNotFound_Skips(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/systemctl", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.Command("sh", "-c", "echo 'Unit foo.service could not be found.'; exit 4")
			out, err := cmd.CombinedOutput()
			return out, err
		},
	}

	collector := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)
	outPath := filepath.Join(tmp, "systemctl_status.txt")

	data, err := collector.captureCommandOutput(context.Background(), "systemctl status foo", outPath, "systemctl status", false)
	if err != nil {
		t.Fatalf("captureCommandOutput returned error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data when unit not found, got %q", string(data))
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 0 {
		t.Fatalf("expected FilesFailed=0, got %d", stats.FilesFailed)
	}
}

func TestCaptureCommandOutput_SystemctlStatusSystemdUnavailable_Skips(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/systemctl", nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.Command("sh", "-c", "echo 'System has not been booted with systemd as init system (PID 1).'; exit 1")
			out, err := cmd.CombinedOutput()
			return out, err
		},
	}

	collector := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)
	outPath := filepath.Join(tmp, "systemctl_status.txt")

	data, err := collector.captureCommandOutput(context.Background(), "systemctl status ssh", outPath, "systemctl status", false)
	if err != nil {
		t.Fatalf("captureCommandOutput returned error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data when systemd is unavailable, got %q", string(data))
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file to be created, stat err=%v", err)
	}
}

func TestSafeCopyFile_SkipsNonRegularFIFO(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	collector := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)
	src := filepath.Join(tmp, "src.fifo")
	if err := syscall.Mkfifo(src, 0o600); err != nil {
		t.Skipf("mkfifo not available: %v", err)
	}

	dest := filepath.Join(tmp, "dest", "dst.fifo")
	if err := collector.safeCopyFile(context.Background(), src, dest, "fifo"); err != nil {
		t.Fatalf("safeCopyFile returned error: %v", err)
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("expected fifo not to be copied, stat err=%v", err)
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 0 {
		t.Fatalf("expected FilesFailed=0, got %d", stats.FilesFailed)
	}
}

func TestCollectorDepsFallbacksToGlobalFunctions(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommand
	origRunWithEnv := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommand = origRun
		runCommandWithEnv = origRunWithEnv
	})

	execLookPath = func(name string) (string, error) {
		if name != "foo" {
			return "", errors.New("unexpected name")
		}
		return "/bin/foo", nil
	}

	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "cmd" || len(args) != 2 || args[0] != "a" || args[1] != "b" {
			return nil, errors.New("unexpected args")
		}
		return []byte("ok"), nil
	}

	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		if name != "cmd-env" || len(args) != 1 || args[0] != "x" {
			return nil, errors.New("unexpected args")
		}
		if len(extraEnv) != 2 || extraEnv[0] != "A=1" || extraEnv[1] != "B=2" {
			return nil, errors.New("unexpected env")
		}
		return []byte("env-ok"), nil
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, CollectorDeps{})

	lp, err := c.depLookPath("foo")
	if err != nil || lp != "/bin/foo" {
		t.Fatalf("depLookPath fallback failed: path=%q err=%v", lp, err)
	}

	out, err := c.depRunCommand(context.Background(), "cmd", "a", "b")
	if err != nil || string(out) != "ok" {
		t.Fatalf("depRunCommand fallback failed: out=%q err=%v", string(out), err)
	}

	out, err = c.depRunCommandWithEnv(context.Background(), []string{"A=1", "B=2"}, "cmd-env", "x")
	if err != nil || string(out) != "env-ok" {
		t.Fatalf("depRunCommandWithEnv fallback failed: out=%q err=%v", string(out), err)
	}
}

func TestSafeCopyFileHonorsContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	collector := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := collector.safeCopyFile(ctx, filepath.Join(tmp, "missing"), filepath.Join(tmp, "dest"), "canceled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSafeCopyFileSkipsWhenExcluded(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"*.txt"}
	tmp := t.TempDir()
	collector := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	dest := filepath.Join(tmp, "dest", "copied.txt")
	if err := collector.safeCopyFile(context.Background(), src, dest, "excluded"); err != nil {
		t.Fatalf("safeCopyFile returned error: %v", err)
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("expected excluded file not to be copied, stat err=%v", err)
	}

	stats := collector.GetStats()
	if stats.FilesProcessed != 0 || stats.FilesFailed != 0 {
		t.Fatalf("expected stats unchanged, got %+v", stats)
	}
}

func TestSafeCopyFileReturnsErrorWhenDestParentIsFile(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	collector := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source.bin")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Block directory creation for filepath.Dir(dest) by placing a regular file.
	parent := filepath.Join(tmp, "dest-parent")
	if err := os.WriteFile(parent, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write parent blocker: %v", err)
	}

	dest := filepath.Join(parent, "out.bin")
	if err := collector.safeCopyFile(context.Background(), src, dest, "blocked"); err == nil {
		t.Fatalf("expected error when destination parent is a file")
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCopyFileReturnsErrorWhenSymlinkDestCannotBeReplaced(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	collector := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	src := filepath.Join(tmp, "link.txt")
	if err := os.Symlink("target.txt", src); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dest := filepath.Join(tmp, "dest", "link.txt")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}

	err := collector.safeCopyFile(context.Background(), src, dest, "symlink")
	if err == nil || !strings.Contains(err.Error(), "file replacement failed - ") {
		t.Fatalf("expected replacement error, got %v", err)
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCopyFileReturnsErrorWhenDestIsDirectory(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	collector := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	dest := filepath.Join(tmp, "dest", "as-dir")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}

	if err := collector.safeCopyFile(context.Background(), src, dest, "dir-dest"); err == nil {
		t.Fatalf("expected error when destination is a directory")
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestApplyMetadataHandlesNilInfo(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	c.applyMetadata(filepath.Join(tmp, "missing"), nil)
}

func TestApplyMetadataHandlesFailuresGracefully(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	// Use a destination path that does not exist so Chown/Chmod/Chtimes will fail deterministically.
	dest := filepath.Join(tmp, "nope", "file.txt")
	info := testFileInfo{
		name:    "file.txt",
		mode:    0o600,
		modTime: time.Now(),
		sys: &syscall.Stat_t{
			Uid:  0,
			Gid:  0,
			Atim: syscall.Timespec{Sec: 1, Nsec: 2},
			Mtim: syscall.Timespec{Sec: 3, Nsec: 4},
		},
	}

	c.applyMetadata(dest, info)
}

func TestLstatOrNilReturnsNilOnMissingPath(t *testing.T) {
	if got := lstatOrNil("/this/path/should/not/exist"); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestApplyDirectoryMetadataFromSourceSkipsOutsideTempDir(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	srcDir := t.TempDir()
	destDir := t.TempDir() // not under c.tempDir
	c.applyDirectoryMetadataFromSource(srcDir, destDir)
}

func TestApplySymlinkOwnershipSkipsWhenSysIsNotStatT(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	c.applySymlinkOwnership(filepath.Join(tmp, "missing"), testFileInfo{sys: "not-stat"})
}

func TestApplySymlinkOwnershipHandlesLchownFailureGracefully(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	c.applySymlinkOwnership(filepath.Join(tmp, "missing"), testFileInfo{sys: &syscall.Stat_t{Uid: 0, Gid: 0}})
}

func TestSafeCopyFileReturnsErrorOnLstatNonNotExist(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	// ENAMETOOLONG is deterministic and triggers the non-IsNotExist error path.
	src := "/" + strings.Repeat("a", 10000)
	err := c.safeCopyFile(context.Background(), src, filepath.Join(tmp, "dest"), "too long")
	if err == nil || !strings.Contains(err.Error(), "failed to stat") {
		t.Fatalf("expected stat error, got %v", err)
	}

	stats := c.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCopyFileSymlinkEnsureDirFailureIncrementsFilesFailed(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	src := filepath.Join(tmp, "link.txt")
	if err := os.Symlink("target.txt", src); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	parent := filepath.Join(tmp, "dest-parent")
	if err := os.WriteFile(parent, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write parent blocker: %v", err)
	}
	dest := filepath.Join(parent, "link.txt")

	err := c.safeCopyFile(context.Background(), src, dest, "symlink")
	if err == nil {
		t.Fatalf("expected error when ensureDir fails")
	}

	stats := c.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCopyFileCopyToDevFullFailsDeterministically(t *testing.T) {
	// This covers the io.Copy error path in a deterministic way on Linux.
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skipf("/dev/full not available: %v", err)
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	err := c.safeCopyFile(context.Background(), src, "/dev/full", "devfull")
	if err == nil || !strings.Contains(err.Error(), "failed to copy") {
		t.Fatalf("expected copy error, got %v", err)
	}
	if got := c.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCopyDirHonorsContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.safeCopyDir(ctx, tmp, filepath.Join(tmp, "dest"), "canceled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

type errOnSecondCallContext struct {
	calls int
}

func (c *errOnSecondCallContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *errOnSecondCallContext) Done() <-chan struct{}       { return nil }
func (c *errOnSecondCallContext) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return context.Canceled
}
func (c *errOnSecondCallContext) Value(any) any { return nil }

func TestSafeCopyDirSkipsWhenExcluded(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"source"}
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	dest := filepath.Join(tmp, "dest")

	if err := c.safeCopyDir(context.Background(), src, dest, "excluded"); err != nil {
		t.Fatalf("safeCopyDir returned error: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("expected dest not to be created, stat err=%v", err)
	}
}

func TestSafeCopyDirStopsWhenContextErrorOccursInsideWalk(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	dest := filepath.Join(tmp, "dest")
	err := c.safeCopyDir(&errOnSecondCallContext{}, src, dest, "ctx flip")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from walk callback, got %v", err)
	}
}

func TestSafeCopyDirReturnsErrorWhenDestIsFile(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	dest := filepath.Join(tmp, "dest")
	if err := os.WriteFile(dest, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write dest blocker: %v", err)
	}

	if err := c.safeCopyDir(context.Background(), src, dest, "blocked"); err == nil {
		t.Fatalf("expected error when dest path is a file")
	}
}

func TestSafeCopyDirSkipsExcludedFiles(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"skip.txt"}
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "skip.txt"), []byte("no"), 0o644); err != nil {
		t.Fatalf("write skip: %v", err)
	}

	dest := filepath.Join(tmp, "dest")
	if err := c.safeCopyDir(context.Background(), src, dest, "skip files"); err != nil {
		t.Fatalf("safeCopyDir returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "keep.txt")); err != nil {
		t.Fatalf("expected keep file to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skip.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected excluded file to be skipped, stat err=%v", err)
	}
}

func TestSafeCopyDirSkipsExcludedSubdirectories(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"skip"}
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source")
	if err := os.MkdirAll(filepath.Join(src, "keep"), 0o755); err != nil {
		t.Fatalf("mkdir keep: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "skip", "inner"), 0o755); err != nil {
		t.Fatalf("mkdir skip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep", "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "skip", "inner", "no.txt"), []byte("no"), 0o644); err != nil {
		t.Fatalf("write skip file: %v", err)
	}

	dest := filepath.Join(tmp, "dest")
	if err := c.safeCopyDir(context.Background(), src, dest, "test"); err != nil {
		t.Fatalf("safeCopyDir returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "keep", "ok.txt")); err != nil {
		t.Fatalf("expected keep file to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skip", "inner", "no.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected excluded subtree not to be copied, stat err=%v", err)
	}
}

func TestSafeCopyDirReturnsErrorWhenDestSubdirCannotBeCreated(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	src := filepath.Join(tmp, "source")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	dest := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	// Block creation of dest/sub by placing a file there.
	if err := os.WriteFile(filepath.Join(dest, "sub"), []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	if err := c.safeCopyDir(context.Background(), src, dest, "blocked-subdir"); err == nil {
		t.Fatalf("expected error when dest subdir cannot be created")
	}
}

func TestSafeCmdOutputHonorsContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.safeCmdOutput(ctx, "echo hi", filepath.Join(tmp, "out.txt"), "canceled", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSafeCmdOutputReturnsErrorOnEmptyCommand(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	if err := c.safeCmdOutput(context.Background(), "   ", filepath.Join(tmp, "out.txt"), "empty", false); err == nil {
		t.Fatalf("expected error for empty command")
	}
}

func TestSafeCmdOutputCriticalCommandNotAvailableIncrementsFilesFailed(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	err := c.safeCmdOutput(context.Background(), "does-not-exist", filepath.Join(tmp, "out.txt"), "critical", true)
	if err == nil {
		t.Fatalf("expected error for critical missing command")
	}

	stats := c.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCmdOutputWriteFailureIncrementsFilesFailed(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	outDir := filepath.Join(tmp, "output-dir")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("ok"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	err := c.safeCmdOutput(context.Background(), "echo hi", outDir, "write-fail", false)
	if err == nil {
		t.Fatalf("expected write error when output path is a directory")
	}

	stats := c.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestSafeCmdOutputEnsureDirFailureReturnsError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	output := filepath.Join(blocker, "out.txt")

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("ok"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	if err := c.safeCmdOutput(context.Background(), "echo hi", output, "ensureDir-fail", false); err == nil {
		t.Fatalf("expected ensureDir error")
	}
}

func TestCaptureCommandOutputReturnsErrorOnEmptyCommand(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	if _, err := c.captureCommandOutput(context.Background(), "   ", filepath.Join(tmp, "out.txt"), "empty", false); err == nil {
		t.Fatalf("expected error for empty command")
	}
}

func TestCaptureCommandOutputHonorsContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.captureCommandOutput(ctx, "echo hi", filepath.Join(tmp, "out.txt"), "canceled", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCaptureCommandOutputPropagatesWriteReportFileError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	outDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("ok"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	// writeReportFile should fail because output path is a directory.
	if _, err := c.captureCommandOutput(context.Background(), "echo hi", outDir, "desc", false); err == nil {
		t.Fatalf("expected writeReportFile error")
	}
}

func TestCaptureCommandOutputCriticalCommandNotAvailableIncrementsFilesFailed(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	_, err := c.captureCommandOutput(context.Background(), "missing-cmd arg", filepath.Join(tmp, "out.txt"), "critical", true)
	if err == nil {
		t.Fatalf("expected error for critical missing command")
	}
	stats := c.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestCaptureCommandOutputNonCriticalFailureReturnsNil(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			cmd := exec.Command("sh", "-c", "echo 'boom'; exit 1")
			out, err := cmd.CombinedOutput()
			return out, err
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	outPath := filepath.Join(tmp, "out.txt")
	data, err := c.captureCommandOutput(context.Background(), "cmd arg", outPath, "noncritical", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data on non-critical failure, got %q", string(data))
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file, stat err=%v", err)
	}
}

func TestCollectCommandMultiRequiresPrimaryOutput(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()
	c := NewCollector(logger, cfg, tmp, types.ProxmoxUnknown, false)

	if err := c.collectCommandMulti(context.Background(), "echo hi", "", "desc", false); err == nil {
		t.Fatalf("expected error when primary output is empty")
	}
}

func TestCollectCommandMultiFailsWhenMirrorWriteFails(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("data"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	primary := filepath.Join(tmp, "primary.txt")
	mirrorDir := filepath.Join(tmp, "mirror")
	if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
		t.Fatalf("mkdir mirrorDir: %v", err)
	}

	if err := c.collectCommandMulti(context.Background(), "echo hi", primary, "desc", false, mirrorDir, ""); err == nil {
		t.Fatalf("expected error when mirror path is a directory")
	}
}

func TestCollectCommandMultiSkipsEmptyMirrors(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("data"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	primary := filepath.Join(tmp, "primary.txt")
	if err := c.collectCommandMulti(context.Background(), "echo hi", primary, "desc", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(primary); err != nil {
		t.Fatalf("expected primary file to exist: %v", err)
	}
}

func TestCollectCommandOptionalSkipsWhenNoOutputPath(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatalf("RunCommand should not be called")
			return nil, nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)
	c.collectCommandOptional(context.Background(), "echo hi", "", "desc", filepath.Join(tmp, "mirror"))
}

func TestCollectCommandOptionalDoesNotMirrorEmptyOutput(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte{}, nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	primary := filepath.Join(tmp, "primary.txt")
	mirror := filepath.Join(tmp, "mirror.txt")
	c.collectCommandOptional(context.Background(), "echo hi", primary, "desc", mirror)

	if _, err := os.Stat(primary); err != nil {
		t.Fatalf("expected primary file to exist: %v", err)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatalf("expected mirror file not to exist for empty output, stat err=%v", err)
	}
}

func TestCollectCommandOptionalSkipsOnCaptureError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("data"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	// Force writeReportFile error by setting output path to a directory.
	outDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}

	mirror := filepath.Join(tmp, "mirror.txt")
	c.collectCommandOptional(context.Background(), "echo hi", outDir, "desc", "", mirror)
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatalf("expected mirror to be skipped on capture error, stat err=%v", err)
	}
}

func TestCollectCommandOptionalSkipsEmptyMirrorEntries(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("data"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	primary := filepath.Join(tmp, "primary.txt")
	mirror := filepath.Join(tmp, "mirror.txt")
	c.collectCommandOptional(context.Background(), "echo hi", primary, "desc", "", mirror)
	if _, err := os.Stat(mirror); err != nil {
		t.Fatalf("expected mirror file to exist: %v", err)
	}
}

func TestCollectCommandOptionalIgnoresMirrorWriteFailures(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := GetDefaultCollectorConfig()
	tmp := t.TempDir()

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("data"), nil
		},
	}
	c := NewCollectorWithDeps(logger, cfg, tmp, types.ProxmoxUnknown, false, deps)

	primary := filepath.Join(tmp, "primary.txt")
	mirrorDir := filepath.Join(tmp, "mirror")
	if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
		t.Fatalf("mkdir mirrorDir: %v", err)
	}

	c.collectCommandOptional(context.Background(), "echo hi", primary, "desc", mirrorDir)
}
