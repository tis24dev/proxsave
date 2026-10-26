package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A compressor that stops reading its input (a full disk, a crash, a cancel that
// killed it) must not leave the tar writer blocked: CreateArchive returns, with the
// compressor's exit as the error, or the cancellation when that is what stopped it.
func TestArchiveReturnsWhenTheCompressorStopsReading(t *testing.T) {
	src := t.TempDir()
	// Far more than a pipe buffer, so the writer is still writing when the compressor goes.
	if err := os.WriteFile(filepath.Join(src, "data"), make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		script string
		cancel time.Duration
		check  func(t *testing.T, err error)
	}{
		{name: "compressor-exits", script: "head -c 1 >/dev/null; exit 3", check: func(t *testing.T, err error) {
			var ce *CompressionError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want the compressor's exit as a CompressionError", err)
			}
		}},
		{name: "cancelled", script: "exec sleep 30", cancel: 200 * time.Millisecond, check: func(t *testing.T, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		}},
	} {
		for _, algo := range []struct {
			name   string
			create func(a *Archiver, ctx context.Context, out string) error
		}{
			{"xz", func(a *Archiver, ctx context.Context, out string) error { return a.createXZArchive(ctx, src, out) }},
			{"zstd", func(a *Archiver, ctx context.Context, out string) error { return a.createZstdArchive(ctx, src, out) }},
			{"pigz", func(a *Archiver, ctx context.Context, out string) error { return a.createPigzArchive(ctx, src, out) }},
		} {
			t.Run(tc.name+"/"+algo.name, func(t *testing.T) {
				a := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionXZ, CompressionLevel: 1})
				a.deps.CommandContext = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
					return exec.CommandContext(ctx, "sh", "-c", tc.script), nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tc.cancel > 0 {
					time.AfterFunc(tc.cancel, cancel)
				}
				done := make(chan error, 1)
				go func() { done <- algo.create(a, ctx, filepath.Join(t.TempDir(), "out")) }()
				select {
				case err := <-done:
					tc.check(t, err)
				case <-time.After(10 * time.Second):
					t.Fatal("the archive is still being written 10s after the compressor stopped reading")
				}
			})
		}
	}
}
