package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newTestLogger() *logging.Logger {
	return logging.New(types.LogLevelError, false)
}

func TestTempDirRegistryRegisterAndDeregister(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "temp-dirs.json")
	registry, err := NewTempDirRegistry(newTestLogger(), regPath)
	if err != nil {
		t.Fatalf("NewTempDirRegistry failed: %v", err)
	}

	tmpDir := filepath.Join(t.TempDir(), "collector")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}

	if err := registry.Register(tmpDir); err != nil {
		t.Fatalf("register temp dir: %v", err)
	}

	entries, err := registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	if err := registry.Deregister(tmpDir); err != nil {
		t.Fatalf("deregister temp dir: %v", err)
	}

	entries, err = registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected registry to be empty, got %d", len(entries))
	}
}

func TestTempDirRegistryCleanupOrphaned(t *testing.T) {
	origRoot := workspaceRoot
	workspaceRoot = t.TempDir()
	t.Cleanup(func() { workspaceRoot = origRoot })

	regPath := filepath.Join(t.TempDir(), "temp-dirs.json")
	registry, err := NewTempDirRegistry(newTestLogger(), regPath)
	if err != nil {
		t.Fatalf("NewTempDirRegistry failed: %v", err)
	}

	// A legitimate workspace: under the trusted root and carrying the marker.
	staleDir := filepath.Join(workspaceRoot, "proxsave-stale")
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, workspaceMarker), []byte("m"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// A poisoned entry pointing outside the trusted root: must NOT be deleted (#55).
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}

	for _, dir := range []string{staleDir, outsideDir} {
		if err := registry.Register(dir); err != nil {
			t.Fatalf("register %s: %v", dir, err)
		}
	}

	entries, err := registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	for i := range entries {
		entries[i].CreatedAt = time.Now().Add(-48 * time.Hour)
		entries[i].PID = -1
	}
	if err := registry.saveEntries(entries); err != nil {
		t.Fatalf("save entries: %v", err)
	}

	cleaned, err := registry.CleanupOrphaned(time.Hour)
	if err != nil {
		t.Fatalf("cleanup orphaned: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected exactly 1 directory cleaned (the contained workspace), got %d", cleaned)
	}

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("expected contained workspace to be removed, err=%v", err)
	}
	// The out-of-root entry must be left on disk (only dropped from the registry).
	if _, err := os.Stat(outsideDir); err != nil {
		t.Fatalf("out-of-root dir must NOT be removed by CleanupOrphaned: %v", err)
	}

	entries, err = registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected registry empty after cleanup (workspace removed, untrusted entry dropped), got %d: %+v", len(entries), entries)
	}
}

func TestEnsureSecureTempRoot(t *testing.T) {
	t.Run("creates missing root 0700", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "proxsave")
		if err := ensureSecureTempRoot(osFS{}, root); err != nil {
			t.Fatalf("ensureSecureTempRoot: %v", err)
		}
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatalf("lstat: %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("expected created root mode 0700, got %#o", info.Mode().Perm())
		}
	})

	t.Run("accepts existing root-owned 0755 dir", func(t *testing.T) {
		root := t.TempDir() // created 0700 by default; relax to 0755
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ensureSecureTempRoot(osFS{}, root); err != nil {
			t.Fatalf("expected existing 0755 dir accepted, got %v", err)
		}
	})

	t.Run("rejects symlink", func(t *testing.T) {
		realDir := t.TempDir()
		link := filepath.Join(t.TempDir(), "proxsave-link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		if err := ensureSecureTempRoot(osFS{}, link); err == nil {
			t.Fatal("expected ensureSecureTempRoot to reject a symlinked temp root (issue #54)")
		}
	})

	t.Run("rejects world-writable dir", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := ensureSecureTempRoot(osFS{}, root); err == nil {
			t.Fatal("expected ensureSecureTempRoot to reject a world-writable temp root (issue #54)")
		}
	})
}
