package backup

import (
	"os"
	"path/filepath"
	"testing"

	"strings"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestWritePxarSubdirReportHandlesMissingPath(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "report.txt")
	ds := pbsDatastore{Name: "ds1", Path: filepath.Join(tmp, "missing")}

	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	if err := c.writePxarSubdirReport(target, ds); err != nil {
		t.Fatalf("writePxarSubdirReport error: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(content), "Unable to read datastore path") {
		t.Fatalf("expected fallback message for missing path, got %s", string(content))
	}
}

func TestWritePxarListReportNoFiles(t *testing.T) {
	tmp := t.TempDir()
	base := filepath.Join(tmp, "ds1")
	vmPath := filepath.Join(base, "vm")
	if err := os.MkdirAll(vmPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ds := pbsDatastore{Name: "ds1", Path: base}
	target := filepath.Join(tmp, "list.txt")

	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	if err := c.writePxarListReport(target, ds, "vm"); err != nil {
		t.Fatalf("writePxarListReport error: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read list: %v", err)
	}
	if !strings.Contains(string(content), "No .pxar files found") {
		t.Fatalf("expected no pxar files message, got %s", string(content))
	}
}
