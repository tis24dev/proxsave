package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func BenchmarkChunkLargeFiles(b *testing.B) {
	const (
		fileSize   = 256 * 1024 // 256 KiB per file
		fileCount  = 4
		chunkBytes = 128 * 1024
	)

	ctx := context.Background()
	logger := logging.New(types.LogLevelError, false)

	template := b.TempDir()
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(template, fmt.Sprintf("seed-%02d.dat", i))
		if err := writeFileOfSize(path, fileSize); err != nil {
			b.Fatalf("write seed file: %v", err)
		}
	}

	workspace := b.TempDir()
	b.ReportAllocs()
	b.SetBytes(fileSize * fileCount)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		iterRoot := filepath.Join(workspace, fmt.Sprintf("iter-%d", i))
		if err := copyDir(template, iterRoot); err != nil {
			b.Fatalf("copy template: %v", err)
		}
		if err := chunkLargeFiles(ctx, logger, iterRoot, chunkBytes, chunkBytes); err != nil {
			b.Fatalf("chunkLargeFiles: %v", err)
		}
	}
}

func BenchmarkPrefilterFiles(b *testing.B) {
	const maxSize = 256 * 1024

	ctx := context.Background()
	logger := logging.New(types.LogLevelError, false)

	template := b.TempDir()
	mustWrite := func(rel, content string) {
		path := filepath.Join(template, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			b.Fatalf("write %s: %v", path, err)
		}
	}

	mustWrite("logs/app.log", "line one\r\nline two\r\n")
	mustWrite("conf/settings.conf", "# comment\nkey=value\n\n;ignored\nalpha=beta\n")
	mustWrite("meta/data.json", "{\n  \"a\": 1,\n  \"b\": 2\n}\n")

	workspace := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		iterRoot := filepath.Join(workspace, fmt.Sprintf("iter-%d", i))
		if err := copyDir(template, iterRoot); err != nil {
			b.Fatalf("copy template: %v", err)
		}
		if err := prefilterFiles(ctx, logger, iterRoot, maxSize); err != nil {
			b.Fatalf("prefilterFiles: %v", err)
		}
	}
}

func writeFileOfSize(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	chunk := bytes.Repeat([]byte("x"), 32*1024)
	var written int64
	for written < size {
		left := size - written
		toWrite := chunk
		if left < int64(len(chunk)) {
			toWrite = chunk[:left]
		}
		if _, err := f.Write(toWrite); err != nil {
			return err
		}
		written += int64(len(toWrite))
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			srcFile.Close()
			return err
		}
		_, err = io.Copy(dstFile, srcFile)
		srcFile.Close()
		if err != nil {
			_ = dstFile.Close()
			return err
		}
		return dstFile.Close()
	})
}
