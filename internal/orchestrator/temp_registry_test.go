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
	t.Parallel()

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
	t.Parallel()

	regPath := filepath.Join(t.TempDir(), "temp-dirs.json")
	registry, err := NewTempDirRegistry(newTestLogger(), regPath)
	if err != nil {
		t.Fatalf("NewTempDirRegistry failed: %v", err)
	}

	staleDir := filepath.Join(t.TempDir(), "stale")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}

	if err := registry.Register(staleDir); err != nil {
		t.Fatalf("register stale dir: %v", err)
	}

	entries, err := registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entries[0].CreatedAt = time.Now().Add(-48 * time.Hour)
	entries[0].PID = -1
	if err := registry.saveEntries(entries); err != nil {
		t.Fatalf("save entries: %v", err)
	}

	cleaned, err := registry.CleanupOrphaned(time.Hour)
	if err != nil {
		t.Fatalf("cleanup orphaned: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 directory cleaned, got %d", cleaned)
	}

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("expected stale dir to be removed, err=%v", err)
	}

	entries, err = registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected registry to be empty after cleanup, got %d", len(entries))
	}
}
