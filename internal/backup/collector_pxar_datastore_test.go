package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestRunPBSPXARStepContinuesAllDatastoresOnError(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 2
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
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("fn called %d times, want 3", got)
	}
}

func TestRunPBSPXARStepPartialFailure(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 2
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	okPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(okPath, "vm"), 0o755); err != nil {
		t.Fatalf("mkdir vm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(okPath, "vm", "backup.pxar"), []byte("data"), 0o640); err != nil {
		t.Fatalf("write pxar: %v", err)
	}

	metaRoot := filepath.Join(c.tempDir, "var/lib/proxsave-info", "pbs", "pxar", "metadata")
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		t.Fatalf("mkdir metaRoot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaRoot, "badds"), []byte("file"), 0o640); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	state := &pbsPxarState{
		eligible: []pbsDatastore{
			{Name: "badds", Path: t.TempDir()},
			{Name: "okds", Path: okPath},
		},
		metaRoot: metaRoot,
	}

	var calls int32
	err := c.runPBSPXARStep(context.Background(), state, func(ctx context.Context, ds pbsDatastore, st *pbsPxarState) error {
		atomic.AddInt32(&calls, 1)
		return c.collectPBSPXARMetadataForDatastore(ctx, ds, st)
	})

	if err == nil || !strings.Contains(err.Error(), "failed to create PXAR metadata directory for badds") {
		t.Fatalf("runPBSPXARStep error = %v, want broken datastore metadata directory error", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("fn called %d times, want 2", got)
	}

	okMeta := filepath.Join(metaRoot, "okds", "metadata.json")
	if _, err := os.Stat(okMeta); err != nil {
		t.Fatalf("expected okds metadata.json: %v", err)
	}
	badMeta := filepath.Join(metaRoot, "badds", "metadata.json")
	if _, err := os.Stat(badMeta); err == nil {
		t.Fatalf("expected no metadata for broken datastore badds")
	}
}

func TestRunPBSPXARStepRespectsParentContextCancellation(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 2
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	state := &pbsPxarState{
		eligible: []pbsDatastore{
			{Name: "ds1", Path: "/tmp/ds1"},
			{Name: "ds2", Path: "/tmp/ds2"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.runPBSPXARStep(ctx, state, func(context.Context, pbsDatastore, *pbsPxarState) error {
		t.Fatal("fn should not run when parent context is already canceled")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runPBSPXARStep error = %v, want context.Canceled", err)
	}
}

func TestRunPBSPXARStepPropagatesParentDeadlineExceeded(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 1
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	state := &pbsPxarState{
		eligible: []pbsDatastore{
			{Name: "ds1", Path: "/tmp/ds1"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := c.runPBSPXARStep(ctx, state, func(ctx context.Context, _ pbsDatastore, _ *pbsPxarState) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runPBSPXARStep error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRunPBSPXARStepKeepsLocalCancellationError(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 1
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	state := &pbsPxarState{
		eligible: []pbsDatastore{
			{Name: "ds1", Path: "/tmp/ds1"},
		},
	}

	err := c.runPBSPXARStep(context.Background(), state, func(context.Context, pbsDatastore, *pbsPxarState) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runPBSPXARStep error = %v, want local context.Canceled", err)
	}
}

func TestRunPBSPXARStepJoinsParentCancellationWithDatastoreErrors(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 1
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	state := &pbsPxarState{
		eligible: []pbsDatastore{
			{Name: "ds1", Path: "/tmp/ds1"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errBoom := errors.New("pxar failed")
	err := c.runPBSPXARStep(ctx, state, func(context.Context, pbsDatastore, *pbsPxarState) error {
		cancel()
		return errBoom
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runPBSPXARStep error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("runPBSPXARStep error = %v, want datastore error %v", err, errBoom)
	}
}

func TestPBSPXARBrickPropagatesParentCancellation(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	c := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	state := newCollectionState(c)
	state.pbs.datastores = []pbsDatastore{
		{Name: "ds1", Path: t.TempDir()},
	}
	brick := requireBrick(t, newPBSPXARRecipe(), brickPBSPXARMetadata)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := brick.Run(ctx, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("brick.Run error = %v, want context.Canceled", err)
	}
}

func TestHandlePBSPXARStepErrorOnlyPropagatesParentContext(t *testing.T) {
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)

	if err := c.handlePBSPXARStepError(context.Background(), "local cancel", context.Canceled); err != nil {
		t.Fatalf("local context.Canceled should remain a best-effort warning, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.handlePBSPXARStepError(ctx, "parent cancel", context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent context.Canceled should propagate, got %v", err)
	}
}
