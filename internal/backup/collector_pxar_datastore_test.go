package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestPreparePBSPXARStateSkipsNonDir(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0o640); err != nil {
		t.Fatalf("write file: %v", err)
	}
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	ds := pbsDatastore{Name: "ds", Path: filePath}
	state, err := c.preparePBSPXARState(context.Background(), []pbsDatastore{ds})
	if err != nil {
		t.Fatalf("preparePBSPXARState should skip non-dir: %v", err)
	}
	if len(state.eligible) != 0 {
		t.Fatalf("expected non-directory datastore to be skipped, got %+v", state.eligible)
	}
}

func TestWritePxarListReportWithFiles(t *testing.T) {
	tmp := t.TempDir()
	dsPath := filepath.Join(tmp, "ds1", "ct")
	if err := os.MkdirAll(dsPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pxarFile := filepath.Join(dsPath, "backup1.pxar")
	if err := os.WriteFile(pxarFile, []byte("data"), 0o640); err != nil {
		t.Fatalf("write pxar: %v", err)
	}

	ds := pbsDatastore{Name: "ds1", Path: filepath.Join(tmp, "ds1")}
	target := filepath.Join(tmp, "list.txt")
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	if err := c.writePxarListReport(context.Background(), target, ds, "ct", 0); err != nil {
		t.Fatalf("writePxarListReport: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read list: %v", err)
	}
	if !strings.Contains(string(content), "backup1.pxar") {
		t.Fatalf("expected pxar file listed, got %s", string(content))
	}
}

func TestRunPBSPXARStepCancelsPendingWorkersOnFirstError(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 1
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	state := &pbsPxarState{
		eligible: []pbsDatastore{
			{Name: "ds1", Path: "/tmp/ds1"},
			{Name: "ds2", Path: "/tmp/ds2"},
			{Name: "ds3", Path: "/tmp/ds3"},
		},
	}

	errBoom := errors.New("pxar failed")
	var calls int32
	err := c.runPBSPXARStep(context.Background(), state, func(ctx context.Context, _ pbsDatastore, _ *pbsPxarState) error {
		atomic.AddInt32(&calls, 1)
		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("runPBSPXARStep error = %v, want %v", err, errBoom)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fn called %d times, want 1", got)
	}
}
