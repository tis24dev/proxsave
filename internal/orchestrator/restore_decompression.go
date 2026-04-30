// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

type restoreDecompressionFormat struct {
	matches func(string) bool
	open    func(context.Context, *os.File) (io.Reader, error)
}

// createDecompressionReader creates appropriate decompression reader based on file extension
func createDecompressionReader(ctx context.Context, file *os.File, archivePath string) (io.Reader, error) {
	for _, format := range restoreDecompressionFormats() {
		if format.matches(archivePath) {
			return format.open(ctx, file)
		}
	}
	return nil, fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
}

func restoreDecompressionFormats() []restoreDecompressionFormat {
	return []restoreDecompressionFormat{
		{
			matches: func(path string) bool { return strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".tgz") },
			open:    func(_ context.Context, file *os.File) (io.Reader, error) { return gzip.NewReader(file) },
		},
		{matches: func(path string) bool { return strings.HasSuffix(path, ".tar.xz") }, open: createXZReader},
		{
			matches: func(path string) bool {
				return strings.HasSuffix(path, ".tar.zst") || strings.HasSuffix(path, ".tar.zstd")
			},
			open: createZstdReader,
		},
		{matches: func(path string) bool { return strings.HasSuffix(path, ".tar.bz2") }, open: createBzip2Reader},
		{matches: func(path string) bool { return strings.HasSuffix(path, ".tar.lzma") }, open: createLzmaReader},
		{matches: func(path string) bool { return strings.HasSuffix(path, ".tar") }, open: func(_ context.Context, file *os.File) (io.Reader, error) { return file, nil }},
	}
}

// createXZReader creates an XZ decompression reader using injectable command runner
func createXZReader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "xz", file, "-d", "-c")
}

// createZstdReader creates a Zstd decompression reader using injectable command runner
func createZstdReader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "zstd", file, "-d", "-c")
}

// createBzip2Reader creates a Bzip2 decompression reader using injectable command runner
func createBzip2Reader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "bzip2", file, "-d", "-c")
}

// createLzmaReader creates an LZMA decompression reader using injectable command runner
func createLzmaReader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "lzma", file, "-d", "-c")
}

// runRestoreCommandStream starts a command that reads from stdin and exposes stdout as a ReadCloser.
// It prefers an injectable streaming runner when available; otherwise falls back to safeexec.
func runRestoreCommandStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.Reader, error) {
	type streamingRunner interface {
		RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error)
	}
	if sr, ok := restoreCmd.(streamingRunner); ok && sr != nil {
		return sr.RunStream(ctx, name, stdin, args...)
	}

	cmd, err := safeexec.CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create %s pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		stdout.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	return &waitReadCloser{ReadCloser: stdout, wait: cmd.Wait}, nil
}
