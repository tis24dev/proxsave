package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestEnsureSystemPathAddsDefaults(t *testing.T) {
	t.Setenv("PATH", "")

	ensureSystemPath()

	got := os.Getenv("PATH")
	if got == "" {
		t.Fatal("PATH should not remain empty")
	}
	for _, required := range []string{"/usr/local/sbin", "/usr/sbin", "/sbin"} {
		if !strings.Contains(got, required) {
			t.Fatalf("PATH %q should contain %s", got, required)
		}
	}
}

func TestEnsureSystemPathDeduplicates(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/usr/bin:/usr/sbin:/usr/sbin")

	ensureSystemPath()

	got := os.Getenv("PATH")
	segments := strings.Split(got, string(os.PathListSeparator))
	counts := make(map[string]int)
	for _, seg := range segments {
		counts[seg]++
		if counts[seg] > 1 {
			t.Fatalf("segment %s appears %d times in PATH %q", seg, counts[seg], got)
		}
	}
}

func TestEnsureSystemPathPreservesCustomPrefix(t *testing.T) {
	custom := "/my/custom/bin"
	t.Setenv("PATH", custom+string(os.PathListSeparator)+"/usr/bin")

	ensureSystemPath()

	got := os.Getenv("PATH")
	if !strings.HasPrefix(got, custom) {
		t.Fatalf("expected PATH %q to start with %s", got, custom)
	}
}

func TestCollectCustomPathsIgnoresEmptyEntries(t *testing.T) {
	collector := newTestCollector(t)
	collector.config.CustomBackupPaths = []string{"", "   ", ""}

	if err := collector.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths returned error for empty paths: %v", err)
	}
}

func TestCollectCustomPathsCopiesContent(t *testing.T) {
	collector := newTestCollector(t)
	tempDir := t.TempDir()

	customDir := filepath.Join(tempDir, "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("failed to create custom dir: %v", err)
	}
	wantPath := filepath.Join(customDir, "data.txt")
	if err := os.WriteFile(wantPath, []byte("custom data"), 0o644); err != nil {
		t.Fatalf("failed to write custom file: %v", err)
	}
	collector.config.CustomBackupPaths = []string{customDir}

	if err := collector.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths failed: %v", err)
	}

	dest := filepath.Join(collector.tempDir, strings.TrimPrefix(customDir, "/"), "data.txt")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("expected copied file at %s: %v", dest, err)
	}
	if string(data) != "custom data" {
		t.Fatalf("copied file contents mismatch: %q", data)
	}
}

func TestCollectCustomPathsHonorsContext(t *testing.T) {
	collector := newTestCollector(t)
	collector.config.CustomBackupPaths = []string{"/tmp"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := collector.collectCustomPaths(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestWriteReportFileCreatesDirectories(t *testing.T) {
	collector := newTestCollector(t)
	report := filepath.Join(collector.tempDir, "reports", "test", "report.txt")

	content := []byte("hello report\nsecond line\n")
	if err := collector.writeReportFile(report, content); err != nil {
		t.Fatalf("writeReportFile failed: %v", err)
	}

	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("failed to read report file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("report content mismatch: got %q want %q", got, content)
	}
}

func TestWriteReportFileDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	collector := NewCollector(logger, config, tempDir, types.ProxmoxUnknown, true)

	report := filepath.Join(tempDir, "report.txt")
	if err := collector.writeReportFile(report, []byte("dry run")); err != nil {
		t.Fatalf("writeReportFile dry-run failed: %v", err)
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Fatalf("expected no file created in dry-run, got err=%v", err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	testCases := []struct {
		name     string
		expected string
	}{
		{"normal_file.txt", "normal_file.txt"},
		{"file with spaces.txt", "file with spaces.txt"},
		{"user@domain.com", "user_domain.com"},
		{"path/to/file", "path_to_file"},
		{"special:chars*here?", "special_chars*here?"},
		{"", "entry"},
	}

	for _, tc := range testCases {
		if got := sanitizeFilename(tc.name); got != tc.expected {
			t.Fatalf("sanitizeFilename(%q) = %q, want %q", tc.name, got, tc.expected)
		}
	}
}

func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	return newTestCollectorWithDeps(t, CollectorDeps{})
}

func newTestCollectorWithDeps(t *testing.T, override CollectorDeps) *Collector {
	t.Helper()
	deps := defaultCollectorDeps()
	if override.LookPath != nil {
		deps.LookPath = override.LookPath
	}
	if override.RunCommand != nil {
		deps.RunCommand = override.RunCommand
	}
	if override.RunCommandWithEnv != nil {
		deps.RunCommandWithEnv = override.RunCommandWithEnv
	}
	if override.Stat != nil {
		deps.Stat = override.Stat
	}
	logger := logging.New(types.LogLevelDebug, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	return NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, deps)
}
