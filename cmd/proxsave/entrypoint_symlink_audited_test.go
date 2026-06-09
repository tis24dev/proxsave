package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// Regression for symlink-delete-then-recreate-no-rollback (2026-06-09 audit):
// ensureGoSymlink removed an existing /usr/local/bin/proxsave and only then created
// the new symlink, so a failed os.Symlink left the host with NO proxsave entrypoint.
// installEntrypointSymlink now replaces atomically (symlink to a temp path + rename
// over dest), so dest is never left missing. Written after that change.

func writeFakeExec(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "proxsave-bin")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake exec: %v", err)
	}
	return p
}

func assertSymlinkTo(t *testing.T, dest, target string) {
	t.Helper()
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("dest %s missing: %v", dest, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dest %s is not a symlink (mode %v)", dest, info.Mode())
	}
	got, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("readlink %s: %v", dest, err)
	}
	if got != target {
		t.Fatalf("symlink %s -> %s, want -> %s", dest, got, target)
	}
}

func TestInstallEntrypointSymlink_CreatesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	exec := writeFakeExec(t, dir)
	dest := filepath.Join(dir, "proxsave")

	installEntrypointSymlink(exec, dest, logging.NewBootstrapLogger())
	assertSymlinkTo(t, dest, exec)
}

func TestInstallEntrypointSymlink_ReplacesWrongSymlinkAndFile(t *testing.T) {
	dir := t.TempDir()
	exec := writeFakeExec(t, dir)

	// Wrong symlink.
	destA := filepath.Join(dir, "a")
	if err := os.Symlink("/some/other/target", destA); err != nil {
		t.Fatal(err)
	}
	installEntrypointSymlink(exec, destA, logging.NewBootstrapLogger())
	assertSymlinkTo(t, destA, exec)

	// Real file occupying the entrypoint path.
	destB := filepath.Join(dir, "b")
	if err := os.WriteFile(destB, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	installEntrypointSymlink(exec, destB, logging.NewBootstrapLogger())
	assertSymlinkTo(t, destB, exec)
}

func TestInstallEntrypointSymlink_KeepsCorrectSymlink(t *testing.T) {
	dir := t.TempDir()
	exec := writeFakeExec(t, dir)
	dest := filepath.Join(dir, "proxsave")
	if err := os.Symlink(exec, dest); err != nil {
		t.Fatal(err)
	}
	installEntrypointSymlink(exec, dest, logging.NewBootstrapLogger())
	assertSymlinkTo(t, dest, exec)
}

// The regression guard: when the install cannot complete, the existing entrypoint
// must be preserved (never deleted-then-not-recreated). A directory at dest makes
// the final rename fail; dest must survive and the temp symlink must be cleaned up.
func TestInstallEntrypointSymlink_FailurePreservesExistingDest(t *testing.T) {
	dir := t.TempDir()
	exec := writeFakeExec(t, dir)
	dest := filepath.Join(dir, "proxsave")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	installEntrypointSymlink(exec, dest, logging.NewBootstrapLogger())

	// dest still exists (rename over a directory fails) and its contents survive.
	if info, err := os.Lstat(dest); err != nil || !info.IsDir() {
		t.Fatalf("dest should still be the original directory, got info=%v err=%v", info, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("existing dest contents were lost: %v", err)
	}
	// The temp symlink must not be left behind.
	if _, err := os.Lstat(dest + ".proxsave-new"); !os.IsNotExist(err) {
		t.Fatalf("temp symlink should have been cleaned up, err=%v", err)
	}
}
