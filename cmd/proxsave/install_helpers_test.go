package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBaseDirFromExecutablePath(t *testing.T) {
	t.Run("standard proxsave build layout", func(t *testing.T) {
		got, found := resolveBaseDirFromExecutablePath("/opt/proxsave/build/proxsave")
		if !found || got != "/opt/proxsave" {
			t.Fatalf("base dir = %q, found=%v; want /opt/proxsave true", got, found)
		}
	})

	t.Run("standard legacy build layout", func(t *testing.T) {
		got, found := resolveBaseDirFromExecutablePath("/opt/proxmox-backup/build/proxsave")
		if !found || got != "/opt/proxmox-backup" {
			t.Fatalf("base dir = %q, found=%v; want /opt/proxmox-backup true", got, found)
		}
	})

	t.Run("symlink to installed binary", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "proxsave")
		build := filepath.Join(base, "build")
		if err := os.MkdirAll(build, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		target := filepath.Join(build, "proxsave")
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		linkDir := filepath.Join(root, "bin")
		if err := os.MkdirAll(linkDir, 0o755); err != nil {
			t.Fatalf("MkdirAll link dir: %v", err)
		}
		link := filepath.Join(linkDir, "proxsave")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		got, found := resolveBaseDirFromExecutablePath(link)
		if !found || got != base {
			t.Fatalf("base dir = %q, found=%v; want %q true", got, found, base)
		}
	})

	t.Run("install marker", func(t *testing.T) {
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Join(base, "configs"), 0o755); err != nil {
			t.Fatalf("MkdirAll marker: %v", err)
		}
		got, found := resolveBaseDirFromExecutablePath(filepath.Join(base, "bin", "proxsave"))
		if !found || got != base {
			t.Fatalf("base dir = %q, found=%v; want %q true", got, found, base)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		got, found := resolveBaseDirFromExecutablePath(filepath.Join(t.TempDir(), "proxsave"))
		if found || got != "/opt/proxsave" {
			t.Fatalf("base dir = %q, found=%v; want /opt/proxsave false", got, found)
		}
	})
}

func TestCleanupTempConfig(t *testing.T) {
	t.Run("empty is noop", func(t *testing.T) {
		cleanupTempConfig("")
	})

	t.Run("removes file when present", func(t *testing.T) {
		dir := t.TempDir()
		tmp := filepath.Join(dir, "config.tmp")
		if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cleanupTempConfig(tmp)

		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", tmp, err)
		}
	})

	t.Run("missing file is noop", func(t *testing.T) {
		dir := t.TempDir()
		tmp := filepath.Join(dir, "missing.tmp")
		cleanupTempConfig(tmp)
	})
}
