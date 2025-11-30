package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestProcessPxarDatastoreSkipsNonDir(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0o640); err != nil {
		t.Fatalf("write file: %v", err)
	}
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	ds := pbsDatastore{Name: "ds", Path: filePath}
	if err := c.processPxarDatastore(context.Background(), ds, filepath.Join(tmp, "meta"), filepath.Join(tmp, "sel"), filepath.Join(tmp, "small")); err != nil {
		t.Fatalf("processPxarDatastore should skip non-dir: %v", err)
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
	if err := c.writePxarListReport(target, ds, "ct"); err != nil {
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
