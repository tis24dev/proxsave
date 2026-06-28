package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/storage"
)

// expiredContext returns a context whose deadline is already in the past, so any
// safefs operation returns ErrTimeout at entry without a real blocking mount.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	t.Cleanup(cancel)
	return ctx
}

// TestVerifyDirectoriesSkipsOnTimeout simulates a dead/stale mount: every stat
// times out, so verifyDirectories must warn and skip each path without erroring
// and without creating anything.
func TestVerifyDirectoriesSkipsOnTimeout(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.Config{
		BaseDir:    baseDir,
		BackupPath: filepath.Join(baseDir, "backup"),
		LogPath:    filepath.Join(baseDir, "log"),
	}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second

	checker.verifyDirectories(expiredContext(t))

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("expected no errors on timeout, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a timeout warning, got %+v", checker.result.Issues)
	}
	if _, err := os.Stat(cfg.BackupPath); !os.IsNotExist(err) {
		t.Fatalf("backup dir must not be created on timeout; stat err = %v", err)
	}
}

// TestVerifyDirectoriesDryRunSkipsCreate verifies a dry-run never materializes a
// missing directory.
func TestVerifyDirectoriesDryRunSkipsCreate(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.Config{
		BaseDir:    baseDir,
		BackupPath: filepath.Join(baseDir, "backup"),
		LogPath:    filepath.Join(baseDir, "log"),
		DryRun:     true,
	}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second

	checker.verifyDirectories(context.Background())

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("dry-run should not error, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	for _, p := range []string{cfg.BackupPath, cfg.LogPath, filepath.Join(baseDir, "identity")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("dry-run must not create %s; stat err = %v", p, err)
		}
	}
}

// TestShouldSkipPOSIXDirectoryChecksOnDetectionTimeout verifies a filesystem
// detection timeout is treated as "skip the path" (warning), not "proceed".
func TestShouldSkipPOSIXDirectoryChecksOnDetectionTimeout(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second
	checker.filesystemInfoLookup = func(context.Context, string) (*storage.FilesystemInfo, error) {
		return nil, &safefs.TimeoutError{Op: "statfs", Path: "/mnt/dead", Timeout: 30 * time.Second}
	}

	if !checker.shouldSkipPOSIXDirectoryChecks(context.Background(), "/mnt/dead") {
		t.Fatal("expected shouldSkipPOSIXDirectoryChecks to return true on detection timeout")
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a timeout warning, got %+v", checker.result.Issues)
	}
}
