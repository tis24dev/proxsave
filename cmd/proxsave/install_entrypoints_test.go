package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// withTempEntrypointDirs points globalEntrypointDirs and PATH at the given temp
// directory so entrypoint cleanup tests never touch the real /usr/local/bin etc.
func withTempEntrypointDirs(t *testing.T, dir string) {
	t.Helper()
	prev := globalEntrypointDirs
	globalEntrypointDirs = []string{dir}
	t.Cleanup(func() { globalEntrypointDirs = prev })
	t.Setenv("PATH", dir)
}

// TestCleanupGlobalEntrypointsSkipsWhenExecPathEmpty is the H5 guard: without a
// known current binary, cleanup must remove nothing (it cannot tell its own
// entrypoint from the others, nor recreate a replacement).
func TestCleanupGlobalEntrypointsSkipsWhenExecPathEmpty(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "proxsave")
	if err := os.WriteFile(keep, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	withTempEntrypointDirs(t, dir)

	cleanupGlobalProxmoxBackupEntrypoints("", logging.NewBootstrapLogger())

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("empty execPath must not remove entrypoints, but %s is gone: %v", keep, err)
	}
}

// TestCleanupGlobalEntrypointsKeepsCurrentRemovesLegacy verifies the normal
// behaviour: an entrypoint pointing at the current binary is kept, a stale one
// is removed.
func TestCleanupGlobalEntrypointsKeepsCurrentRemovesLegacy(t *testing.T) {
	binDir := t.TempDir()
	execPath := filepath.Join(binDir, "proxsave-bin")
	if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}

	dir := t.TempDir()
	// Symlink to the current binary => must be kept.
	keep := filepath.Join(dir, "proxsave")
	if err := os.Symlink(execPath, keep); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Stale SYMLINK not pointing to the current binary => must be removed.
	legacy := filepath.Join(dir, "proxmox-backup")
	if err := os.Symlink(execPath+"-old", legacy); err != nil {
		t.Fatalf("symlink legacy: %v", err)
	}
	withTempEntrypointDirs(t, dir)

	cleanupGlobalProxmoxBackupEntrypoints(execPath, logging.NewBootstrapLogger())

	if _, err := os.Lstat(keep); err != nil {
		t.Fatalf("symlink to current binary must be kept, got: %v", err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("stale symlink must be removed, stat err=%v", err)
	}
}

// TestCleanupGlobalEntrypointsKeepsRealNonSymlinkFile pins the safety fix: a real
// (non-symlink) entrypoint that does not resolve to the current binary — e.g. a
// distro/package-managed /usr/bin/proxsave — must be left in place, because the
// recreation step only writes /usr/local/bin/proxsave and could never restore it.
func TestCleanupGlobalEntrypointsKeepsRealNonSymlinkFile(t *testing.T) {
	binDir := t.TempDir()
	execPath := filepath.Join(binDir, "proxsave-bin")
	if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}

	dir := t.TempDir()
	// A real, package-managed-looking binary that does not resolve to execPath.
	packaged := filepath.Join(dir, "proxsave")
	if err := os.WriteFile(packaged, []byte("#!/bin/sh\n# distro package\n"), 0o755); err != nil {
		t.Fatalf("write packaged: %v", err)
	}
	withTempEntrypointDirs(t, dir)

	cleanupGlobalProxmoxBackupEntrypoints(execPath, logging.NewBootstrapLogger())

	if _, err := os.Stat(packaged); err != nil {
		t.Fatalf("a real (non-symlink) entrypoint must be left untouched, got: %v", err)
	}
}

// TestRemoveLegacyEntrypoint verifies the legacy "proxmox-backup" command is
// dropped only when it is a symlink we created — a real file (e.g. an operator's
// own script) is never deleted, keeping PBS and unrelated files safe.
func TestRemoveLegacyEntrypoint(t *testing.T) {
	t.Run("removes a symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "proxsave-bin")
		if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
			t.Fatalf("write target: %v", err)
		}
		link := filepath.Join(dir, "proxmox-backup")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		removeLegacyEntrypoint(link, logging.NewBootstrapLogger())

		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Fatalf("legacy symlink must be removed, stat err=%v", err)
		}
	})

	t.Run("leaves a real file in place", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "proxmox-backup")
		if err := os.WriteFile(real, []byte("not ours"), 0o755); err != nil {
			t.Fatalf("write file: %v", err)
		}

		removeLegacyEntrypoint(real, logging.NewBootstrapLogger())

		if _, err := os.Stat(real); err != nil {
			t.Fatalf("a real (non-symlink) file must be left untouched, got: %v", err)
		}
	})

	t.Run("absent path is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		removeLegacyEntrypoint(filepath.Join(dir, "proxmox-backup"), logging.NewBootstrapLogger())
	})
}
