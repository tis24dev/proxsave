package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPatternWriterWrite_DryRunCountsOnly(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "sample-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()

	if _, err := f.WriteString("payload"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	info, err := os.Stat(f.Name())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	pw := &patternWriter{}
	if err := pw.Write(f.Name(), info); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if pw.count != 1 {
		t.Fatalf("count=%d; want 1", pw.count)
	}
	if pw.totalSize != info.Size() {
		t.Fatalf("totalSize=%d; want %d", pw.totalSize, info.Size())
	}
}

func TestPatternWriterWrite_WritesRelativePathLine(t *testing.T) {
	storagePath := t.TempDir()
	analysisDir := t.TempDir()

	srcDir := filepath.Join(storagePath, "sub")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	srcPath := filepath.Join(srcDir, "file.tar")
	if err := os.WriteFile(srcPath, []byte("payload"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	pw, err := newPatternWriter("local", storagePath, analysisDir, "*.tar", false)
	if err != nil {
		t.Fatalf("newPatternWriter: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close() })

	if err := pw.Write(srcPath, info); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	content, err := os.ReadFile(pw.filePath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", pw.filePath, err)
	}
	out := string(content)
	if !strings.Contains(out, "matching pattern: *.tar") {
		t.Fatalf("expected header to mention pattern, got: %q", out)
	}
	if !strings.Contains(out, filepath.ToSlash("sub/file.tar")) && !strings.Contains(out, filepath.FromSlash("sub/file.tar")) {
		t.Fatalf("expected output to contain relative path, got: %q", out)
	}
	if pw.count != 1 {
		t.Fatalf("count=%d; want 1", pw.count)
	}
	if pw.totalSize != info.Size() {
		t.Fatalf("totalSize=%d; want %d", pw.totalSize, info.Size())
	}
}

func TestCollectorCopyBackupSample_CopiesFile(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	c := &Collector{
		logger: logger,
		config: &CollectorConfig{},
		stats:  &CollectionStats{},
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "backup.tar")
	if err := os.WriteFile(src, []byte("payload"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	destDir := filepath.Join(tmp, "samples")
	if err := c.copyBackupSample(context.Background(), src, destDir, "sample"); err != nil {
		t.Fatalf("copyBackupSample error: %v", err)
	}

	dest := filepath.Join(destDir, filepath.Base(src))
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", dest, err)
	}
	if string(got) != "payload" {
		t.Fatalf("copied content=%q; want %q", string(got), "payload")
	}
	if c.stats.DirsCreated != 1 {
		t.Fatalf("DirsCreated=%d; want 1", c.stats.DirsCreated)
	}
	if c.stats.FilesProcessed != 1 {
		t.Fatalf("FilesProcessed=%d; want 1", c.stats.FilesProcessed)
	}
	if c.stats.BytesCollected != int64(len("payload")) {
		t.Fatalf("BytesCollected=%d; want %d", c.stats.BytesCollected, len("payload"))
	}
}
