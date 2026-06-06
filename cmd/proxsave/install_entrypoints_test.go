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
	// Stale entrypoint not pointing to the current binary => must be removed.
	legacy := filepath.Join(dir, "proxmox-backup")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	withTempEntrypointDirs(t, dir)

	cleanupGlobalProxmoxBackupEntrypoints(execPath, logging.NewBootstrapLogger())

	if _, err := os.Lstat(keep); err != nil {
		t.Fatalf("symlink to current binary must be kept, got: %v", err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("stale entrypoint must be removed, stat err=%v", err)
	}
}
