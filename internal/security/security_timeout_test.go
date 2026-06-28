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

// TestEnsureOwnershipAndPermDryRunIsReadOnly verifies that, in dry-run, the
// preflight does not chmod an existing file with wrong permissions (it only
// reports what it would do).
func TestEnsureOwnershipAndPermDryRunIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg := &config.Config{
		BaseDir:            dir,
		AutoFixPermissions: true,
		DryRun:             true,
	}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second

	checker.ensureOwnershipAndPerm(context.Background(), file, nil, 0o600, "test file")

	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("dry-run must not chmod existing file; perm = %o, want 644", info.Mode().Perm())
	}
}

// TestVerifyBinaryIntegrityDryRunDoesNotRegenerateHash verifies that a dry-run
// does not rewrite the .md5 hash file on mismatch (the "Regenerated hash file"
// write from issue #242).
func TestVerifyBinaryIntegrityDryRunDoesNotRegenerateHash(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("real content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	hashPath := execPath + ".md5"
	if err := os.WriteFile(hashPath, []byte("stale-hash"), 0o600); err != nil {
		t.Fatalf("write stale hash: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true, DryRun: true}, execPath)
	checker.verifyBinaryIntegrity()

	data, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if string(data) != "stale-hash" {
		t.Fatalf("dry-run must not regenerate hash file; got %q, want %q", string(data), "stale-hash")
	}
}

// TestVerifyBinaryIntegrityDryRunDoesNotCreateHash verifies that a dry-run does
// not create a missing .md5 hash file.
func TestVerifyBinaryIntegrityDryRunDoesNotCreateHash(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true, DryRun: true}, execPath)
	checker.verifyBinaryIntegrity()

	if _, err := os.Stat(execPath + ".md5"); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create hash file; stat err = %v", err)
	}
}

// TestVerifyBinaryIntegrityFromFDDryRunDoesNotChmod verifies that a dry-run does
// not fchmod the executable (ensureOwnershipAndPermFromFD) when its mode differs
// from the expected 0o700.
func TestVerifyBinaryIntegrityFromFDDryRunDoesNotChmod(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	if err := os.Chmod(execPath, 0o755); err != nil { // wrong perm vs expected 0o700
		t.Fatalf("chmod: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoFixPermissions: true, AutoUpdateHashes: false, DryRun: true}, execPath)
	checker.verifyBinaryIntegrity()

	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("dry-run must not fchmod the executable; perm = %o, want 755", info.Mode().Perm())
	}
}
