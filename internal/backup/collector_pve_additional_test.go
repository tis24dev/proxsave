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

// TestCollectDetailedPVEBackups tests the detailed backup file scanning
func TestCollectDetailedPVEBackups(t *testing.T) {
	t.Run("scans backup files matching patterns", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		if err := os.MkdirAll(storagePath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Create test backup files
		testFiles := []string{
			"vzdump-qemu-100-2024_01_01-00_00_00.vma",
			"vzdump-qemu-100-2024_01_01-00_00_00.vma.gz",
			"vzdump-lxc-101-2024_01_01-00_00_00.tar.gz",
			"vzdump-qemu-100-2024_01_01-00_00_00.log",
			"vzdump-qemu-100-2024_01_01-00_00_00.notes",
			"random-file.txt", // should not be matched
		}
		for _, name := range testFiles {
			if err := os.WriteFile(filepath.Join(storagePath, name), []byte("content"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{
			Name: "local",
			Path: storagePath,
			Type: "dir",
		}
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err := collector.collectDetailedPVEBackups(context.Background(), storage, metaDir)
		if err != nil {
			t.Fatalf("collectDetailedPVEBackups error: %v", err)
		}

		// Verify analysis directory was created
		analysisDir := filepath.Join(metaDir, "backup_analysis")
		if _, err := os.Stat(analysisDir); os.IsNotExist(err) {
			t.Error("backup_analysis directory should exist")
		}

		// Verify summary file was created
		summaryFile := filepath.Join(analysisDir, "local_backup_summary.txt")
		if _, err := os.Stat(summaryFile); os.IsNotExist(err) {
			t.Error("backup summary file should exist")
		}
	})

	t.Run("context cancelled stops scan", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		if err := os.MkdirAll(storagePath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: storagePath}
		metaDir := filepath.Join(tmpDir, "meta")

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := collector.collectDetailedPVEBackups(ctx, storage, metaDir)
		if err == nil || err != context.Canceled {
			t.Errorf("expected context.Canceled error, got: %v", err)
		}
	})

	t.Run("dry-run mode skips file creation", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		if err := os.MkdirAll(storagePath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Create a test backup file
		if err := os.WriteFile(filepath.Join(storagePath, "backup.vma"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", true) // dry-run = true

		storage := pveStorageEntry{Name: "local", Path: storagePath}
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err := collector.collectDetailedPVEBackups(context.Background(), storage, metaDir)
		if err != nil {
			t.Fatalf("collectDetailedPVEBackups error: %v", err)
		}

		// In dry-run mode, summary file should not be created
		summaryFile := filepath.Join(metaDir, "backup_analysis", "local_backup_summary.txt")
		if _, err := os.Stat(summaryFile); err == nil {
			t.Error("summary file should not exist in dry-run mode")
		}
	})

	t.Run("copies small backups when enabled", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		if err := os.MkdirAll(storagePath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Create a small backup file
		smallContent := "small backup content"
		if err := os.WriteFile(filepath.Join(storagePath, "small.vma"), []byte(smallContent), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.BackupSmallPVEBackups = true
		cfg.MaxPVEBackupSizeBytes = 1024 * 1024 // 1MB
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: storagePath}
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err := collector.collectDetailedPVEBackups(context.Background(), storage, metaDir)
		if err != nil {
			t.Fatalf("collectDetailedPVEBackups error: %v", err)
		}

		// Verify small backup was copied
		smallBackupDir := filepath.Join(tmpDir, "var/lib/pve-cluster/small_backups/local")
		copiedFile := filepath.Join(smallBackupDir, "small.vma")
		if _, err := os.Stat(copiedFile); os.IsNotExist(err) {
			t.Error("small backup should have been copied")
		}
	})

	t.Run("copies backups matching include pattern", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		if err := os.MkdirAll(storagePath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Create backup files with specific pattern
		if err := os.WriteFile(filepath.Join(storagePath, "vm-100-backup.vma"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.WriteFile(filepath.Join(storagePath, "vm-200-backup.vma"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEBackupIncludePattern = "vm-100"
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: storagePath}
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err := collector.collectDetailedPVEBackups(context.Background(), storage, metaDir)
		if err != nil {
			t.Fatalf("collectDetailedPVEBackups error: %v", err)
		}

		// Verify only vm-100 backup was copied to selected_backups
		selectedDir := filepath.Join(tmpDir, "var/lib/pve-cluster/selected_backups/local")
		if _, err := os.Stat(filepath.Join(selectedDir, "vm-100-backup.vma")); os.IsNotExist(err) {
			t.Error("vm-100 backup should have been copied")
		}
		// vm-200 should NOT be copied
		if _, err := os.Stat(filepath.Join(selectedDir, "vm-200-backup.vma")); err == nil {
			t.Error("vm-200 backup should NOT have been copied")
		}
	})

	t.Run("handles empty pattern list", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		if err := os.MkdirAll(storagePath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PxarFileIncludePatterns = []string{} // Empty patterns - should use defaults
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: storagePath}
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err := collector.collectDetailedPVEBackups(context.Background(), storage, metaDir)
		if err != nil {
			t.Fatalf("collectDetailedPVEBackups error: %v", err)
		}
	})

	t.Run("handles nested directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		storagePath := filepath.Join(tmpDir, "storage")
		nestedDir := filepath.Join(storagePath, "dump", "subdir")
		if err := os.MkdirAll(nestedDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Create backup files in nested directory
		if err := os.WriteFile(filepath.Join(nestedDir, "nested.vma"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: storagePath}
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err := collector.collectDetailedPVEBackups(context.Background(), storage, metaDir)
		if err != nil {
			t.Fatalf("collectDetailedPVEBackups error: %v", err)
		}

		// Verify list file contains the nested file
		listFile := filepath.Join(metaDir, "backup_analysis", "local__vma_list.txt")
		if content, err := os.ReadFile(listFile); err == nil {
			if !strings.Contains(string(content), "nested.vma") {
				t.Error("nested backup should be listed")
			}
		}
	})
}

// TestWritePatternSummary tests the pattern summary writing
func TestWritePatternSummary(t *testing.T) {
	t.Run("writes summary with valid writers", func(t *testing.T) {
		tmpDir := t.TempDir()
		analysisDir := filepath.Join(tmpDir, "analysis")
		if err := os.MkdirAll(analysisDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: "/var/lib/vz"}
		writers := []*patternWriter{
			{pattern: "*.vma", count: 10, totalSize: 1024 * 1024},
			{pattern: "*.tar", count: 5, totalSize: 512 * 1024},
			{pattern: "*.log", count: 0, totalSize: 0},
		}

		err := collector.writePatternSummary(storage, analysisDir, writers, 15, 1536*1024)
		if err != nil {
			t.Fatalf("writePatternSummary error: %v", err)
		}

		summaryFile := filepath.Join(analysisDir, "local_backup_summary.txt")
		content, err := os.ReadFile(summaryFile)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "*.vma") {
			t.Error("summary should mention *.vma pattern")
		}
		if !strings.Contains(contentStr, "Files: 10") {
			t.Error("summary should show file count for *.vma")
		}
		if !strings.Contains(contentStr, "No files found") {
			t.Error("summary should show 'No files found' for *.log")
		}
		if !strings.Contains(contentStr, "Total backup files: 15") {
			t.Error("summary should show total files count")
		}
	})

	t.Run("writes summary with error counts", func(t *testing.T) {
		tmpDir := t.TempDir()
		analysisDir := filepath.Join(tmpDir, "analysis")
		if err := os.MkdirAll(analysisDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: "/var/lib/vz"}
		writers := []*patternWriter{
			{pattern: "*.vma", count: 10, totalSize: 1024, errorCount: 2},
		}

		err := collector.writePatternSummary(storage, analysisDir, writers, 10, 1024)
		if err != nil {
			t.Fatalf("writePatternSummary error: %v", err)
		}

		summaryFile := filepath.Join(analysisDir, "local_backup_summary.txt")
		content, err := os.ReadFile(summaryFile)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "Files with errors: 2") {
			t.Error("summary should show error count")
		}
	})

	t.Run("dry-run mode skips file creation", func(t *testing.T) {
		tmpDir := t.TempDir()
		analysisDir := filepath.Join(tmpDir, "analysis")
		if err := os.MkdirAll(analysisDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", true) // dry-run

		storage := pveStorageEntry{Name: "local", Path: "/var/lib/vz"}
		writers := []*patternWriter{{pattern: "*.vma", count: 1}}

		err := collector.writePatternSummary(storage, analysisDir, writers, 1, 100)
		if err != nil {
			t.Fatalf("writePatternSummary error: %v", err)
		}

		summaryFile := filepath.Join(analysisDir, "local_backup_summary.txt")
		if _, err := os.Stat(summaryFile); err == nil {
			t.Error("summary file should not exist in dry-run mode")
		}
	})
}

// TestSampleMetadataFileStats tests metadata file sampling
func TestSampleMetadataFileStats(t *testing.T) {
	t.Run("samples files up to limit", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create multiple files
		for i := 0; i < 20; i++ {
			if err := os.WriteFile(filepath.Join(tmpDir, "file"+string(rune('a'+i))+".txt"), []byte("content"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		lines, err := collector.sampleMetadataFileStats(context.Background(), tmpDir, 3, 10)
		if err != nil {
			t.Fatalf("sampleMetadataFileStats error: %v", err)
		}

		if len(lines) > 10 {
			t.Errorf("got %d lines, want at most 10", len(lines))
		}
	})

	t.Run("respects maxDepth", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create nested structure
		deepDir := filepath.Join(tmpDir, "a", "b", "c", "d")
		if err := os.MkdirAll(deepDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		// File at depth 1
		if err := os.WriteFile(filepath.Join(tmpDir, "a", "file1.txt"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		// File at depth 4 (should be skipped with maxDepth=2)
		if err := os.WriteFile(filepath.Join(deepDir, "deep.txt"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		lines, err := collector.sampleMetadataFileStats(context.Background(), tmpDir, 2, 100)
		if err != nil {
			t.Fatalf("sampleMetadataFileStats error: %v", err)
		}

		for _, line := range lines {
			if strings.Contains(line, "deep.txt") {
				t.Error("should not include file beyond maxDepth")
			}
		}
	})

	t.Run("returns empty for empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		lines, err := collector.sampleMetadataFileStats(context.Background(), tmpDir, 3, 10)
		if err != nil {
			t.Fatalf("sampleMetadataFileStats error: %v", err)
		}

		if len(lines) != 0 {
			t.Errorf("got %d lines, want 0 for empty dir", len(lines))
		}
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := collector.sampleMetadataFileStats(ctx, tmpDir, 3, 10)
		if err == nil {
			t.Error("expected context cancelled error")
		}
	})

	t.Run("handles limit of zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		lines, err := collector.sampleMetadataFileStats(context.Background(), tmpDir, 3, 0)
		if err != nil {
			t.Fatalf("sampleMetadataFileStats error: %v", err)
		}

		if len(lines) != 0 {
			t.Errorf("got %d lines, want 0 with limit=0", len(lines))
		}
	})
}

// TestWriteDatastoreMetadataText tests metadata text generation
func TestWriteDatastoreMetadataText(t *testing.T) {
	t.Run("writes complete metadata", func(t *testing.T) {
		tmpDir := t.TempDir()
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{
			Name:    "local",
			Path:    "/var/lib/vz",
			Type:    "dir",
			Content: "images,iso",
		}
		dirSamples := []string{"dump", "images", "template"}
		diskUsage := "Used: 10GB / Total: 100GB (Free: 90GB)"
		fileSamples := []string{"2024-01-01T00:00:00Z 1024 /var/lib/vz/dump/backup.vma"}

		err := collector.writeDatastoreMetadataText(metaDir, storage, dirSamples, nil, diskUsage, nil, fileSamples, nil)
		if err != nil {
			t.Fatalf("writeDatastoreMetadataText error: %v", err)
		}

		metaFile := filepath.Join(metaDir, "metadata.txt")
		content, err := os.ReadFile(metaFile)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "# Datastore: local") {
			t.Error("should contain datastore name")
		}
		if !strings.Contains(contentStr, "# Path: /var/lib/vz") {
			t.Error("should contain path")
		}
		if !strings.Contains(contentStr, "# Type: dir") {
			t.Error("should contain type")
		}
		if !strings.Contains(contentStr, "dump") {
			t.Error("should contain directory samples")
		}
		if !strings.Contains(contentStr, "Used: 10GB") {
			t.Error("should contain disk usage")
		}
	})

	t.Run("handles errors in metadata", func(t *testing.T) {
		tmpDir := t.TempDir()
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: "/var/lib/vz"}

		// Simulate errors
		dirErr := os.ErrPermission
		diskErr := os.ErrNotExist
		fileErr := os.ErrPermission

		err := collector.writeDatastoreMetadataText(metaDir, storage, nil, dirErr, "", diskErr, nil, fileErr)
		if err != nil {
			t.Fatalf("writeDatastoreMetadataText error: %v", err)
		}

		metaFile := filepath.Join(metaDir, "metadata.txt")
		content, err := os.ReadFile(metaFile)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "Error:") {
			t.Error("should contain error indicators")
		}
		if !strings.Contains(contentStr, "Data Quality Notes") {
			t.Error("should contain quality notes section")
		}
	})

	t.Run("truncates long directory samples", func(t *testing.T) {
		tmpDir := t.TempDir()
		metaDir := filepath.Join(tmpDir, "meta")
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		storage := pveStorageEntry{Name: "local", Path: "/var/lib/vz"}

		// Create more than 20 directory samples
		dirSamples := make([]string, 30)
		for i := range dirSamples {
			dirSamples[i] = "dir" + string(rune('a'+i%26))
		}

		err := collector.writeDatastoreMetadataText(metaDir, storage, dirSamples, nil, "Usage", nil, nil, nil)
		if err != nil {
			t.Fatalf("writeDatastoreMetadataText error: %v", err)
		}

		metaFile := filepath.Join(metaDir, "metadata.txt")
		content, err := os.ReadFile(metaFile)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}

		if !strings.Contains(string(content), "truncated") {
			t.Error("should indicate truncation when > 20 directories")
		}
	})
}
