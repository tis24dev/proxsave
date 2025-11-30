package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestOptimizationConfigEnabled(t *testing.T) {
	cfg := OptimizationConfig{}
	if cfg.Enabled() {
		t.Fatal("expected disabled config when all stages are false")
	}
	cfg.EnableChunking = true
	if !cfg.Enabled() {
		t.Fatal("expected Enabled() to return true when a stage is active")
	}
}

func TestApplyOptimizationsRunsAllStages(t *testing.T) {
	root := t.TempDir()

	mustWriteFile := func(rel, content string) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return path
	}

	mustWriteFile(filepath.Join("dup", "one.txt"), "identical data")
	dupB := mustWriteFile(filepath.Join("dup", "two.txt"), "identical data")

	logFile := mustWriteFile(filepath.Join("logs", "app.log"), "line one\r\nline two\r\n")
	confFile := mustWriteFile(filepath.Join("conf", "settings.conf"), "# comment\nkey=value\n\n;ignored\nalpha=beta\n")
	jsonFile := mustWriteFile(filepath.Join("meta", "data.json"), "{\n  \"a\": 1,\n  \"b\": 2\n}\n")

	chunkTarget := mustWriteFile("chunk.bin", string(bytes.Repeat([]byte("x"), 96)))

	logger := logging.New(types.LogLevelError, false)
	cfg := OptimizationConfig{
		EnableChunking:            true,
		EnableDeduplication:       true,
		EnablePrefilter:           true,
		ChunkSizeBytes:            16,
		ChunkThresholdBytes:       64,
		PrefilterMaxFileSizeBytes: 1024,
	}

	if err := ApplyOptimizations(context.Background(), logger, root, cfg); err != nil {
		t.Fatalf("ApplyOptimizations: %v", err)
	}

	// Deduplication should replace the duplicate with a symlink that still resolves.
	info, err := os.Lstat(dupB)
	if err != nil {
		t.Fatalf("stat duplicate: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink after deduplication", dupB)
	}
	data, err := os.ReadFile(dupB)
	if err != nil {
		t.Fatalf("read dedup symlink: %v", err)
	}
	if string(data) != "identical data" {
		t.Fatalf("symlink data mismatch, got %q", data)
	}

	// Prefilter should strip CR characters and comments/sort config files.
	logContents, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if bytes.Contains(logContents, []byte("\r")) {
		t.Fatalf("expected CR characters removed from %s", logFile)
	}
	confContents, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if string(confContents) != "alpha=beta\nkey=value" {
		t.Fatalf("unexpected config contents: %q", confContents)
	}
	jsonContents, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("read json file: %v", err)
	}
	if bytes.Contains(jsonContents, []byte(" ")) || bytes.Contains(jsonContents, []byte("\n")) {
		t.Fatalf("expected minified JSON, got %q", jsonContents)
	}

	// Chunking should remove the original file, create a marker, and emit chunks.
	if _, err := os.Stat(chunkTarget); !os.IsNotExist(err) {
		t.Fatalf("expected original chunk target removed, stat err=%v", err)
	}
	if _, err := os.Stat(chunkTarget + ".chunked"); err != nil {
		t.Fatalf("chunk marker missing: %v", err)
	}
	chunkPath := filepath.Join(root, "chunked_files", "chunk.bin.001.chunk")
	if _, err := os.Stat(chunkPath); err != nil {
		t.Fatalf("expected first chunk at %s: %v", chunkPath, err)
	}
}
