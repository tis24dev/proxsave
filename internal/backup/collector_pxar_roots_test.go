package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestComputePxarWorkerRootsCachesResults(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a/one", "b", "c/d"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PxarScanFanoutLevel = 2
	cfg.PxarScanMaxRoots = 2
	c := NewCollector(newTestLogger(), cfg, root, types.ProxmoxBS, false)

	ctx := context.Background()
	first, err := c.computePxarWorkerRoots(ctx, root, "test")
	if err != nil {
		t.Fatalf("computePxarWorkerRoots error: %v", err)
	}
	if len(first) == 0 || len(first) > 2 {
		t.Fatalf("unexpected roots count: %d", len(first))
	}

	// Remove the directory to ensure cached results are used.
	os.RemoveAll(root)
	second, err := c.computePxarWorkerRoots(ctx, root, "test")
	if err != nil {
		t.Fatalf("computePxarWorkerRoots (cached) error: %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("cached results length mismatch: %d vs %d", len(second), len(first))
	}
}
