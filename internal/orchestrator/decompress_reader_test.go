package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateDecompressionReaderUnsupported(t *testing.T) {
	f, err := os.CreateTemp("", "archive-*.foo")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if _, err := createDecompressionReader(context.Background(), f, f.Name()); err == nil {
		t.Fatalf("expected error for unsupported extension")
	}
}

func TestCreateDecompressionReaderTar(t *testing.T) {
	f, err := os.CreateTemp("", "archive-*.tar")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	reader, err := createDecompressionReader(context.Background(), f, f.Name())
	if err != nil {
		t.Fatalf("expected tar reader, got %v", err)
	}
	if reader == nil {
		t.Fatalf("reader should not be nil for tar")
	}
	_ = reader.Close()
}

type fakeStreamCommandRunner struct {
	outputs map[string]string
	calls   []string
}

func (f *fakeStreamCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if out, ok := f.outputs[name]; ok {
		return []byte(out), nil
	}
	return nil, nil
}

func (f *fakeStreamCommandRunner) RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if out, ok := f.outputs[name]; ok {
		return io.NopCloser(strings.NewReader(out)), nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

type extractionCloseErrorReadCloser struct {
	*bytes.Reader
	err error
}

func (r *extractionCloseErrorReadCloser) Close() error {
	return r.err
}

type closeErrorStreamCommandRunner struct {
	data     []byte
	closeErr error
}

func (f *closeErrorStreamCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func (f *closeErrorStreamCommandRunner) RunStream(context.Context, string, io.Reader, ...string) (io.ReadCloser, error) {
	return &extractionCloseErrorReadCloser{Reader: bytes.NewReader(f.data), err: f.closeErr}, nil
}

func TestCreateDecompressionReaderUsesStreamingRunnerForCompressedFormats(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &fakeStreamCommandRunner{
		outputs: map[string]string{
			"zstd":  "zstd-out",
			"bzip2": "bzip2-out",
			"lzma":  "lzma-out",
		},
	}
	restoreCmd = fake

	tests := []struct {
		ext      string
		wantCmd  string
		wantText string
	}{
		{ext: ".tar.zst", wantCmd: "zstd -d -c", wantText: "zstd-out"},
		{ext: ".tar.bz2", wantCmd: "bzip2 -d -c", wantText: "bzip2-out"},
		{ext: ".tar.lzma", wantCmd: "lzma -d -c", wantText: "lzma-out"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.ext, func(t *testing.T) {
			fake.calls = nil

			f, err := os.CreateTemp("", "archive-*"+tt.ext)
			if err != nil {
				t.Fatalf("CreateTemp: %v", err)
			}
			defer os.Remove(f.Name())
			defer f.Close()

			reader, err := createDecompressionReader(context.Background(), f, f.Name())
			if err != nil {
				t.Fatalf("createDecompressionReader(%s) error: %v", tt.ext, err)
			}
			defer reader.Close()

			out, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(out) != tt.wantText {
				t.Fatalf("output=%q; want %q", string(out), tt.wantText)
			}
			if len(fake.calls) != 1 || fake.calls[0] != tt.wantCmd {
				t.Fatalf("calls=%v; want [%q]", fake.calls, tt.wantCmd)
			}
		})
	}
}

func TestExtractArchiveNativeReturnsDecompressionCloseError(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	dir := t.TempDir()
	tarPath := filepath.Join(dir, "source.tar")
	if err := writeTarFile(tarPath, map[string]string{"etc/example.conf": "ok\n"}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarData, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	closeErr := errors.New("decompressor exited 2")
	restoreCmd = &closeErrorStreamCommandRunner{data: tarData, closeErr: closeErr}
	restoreFS = osFS{}

	archivePath := filepath.Join(dir, "archive.tar.zst")
	if err := os.WriteFile(archivePath, []byte("compressed"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = extractArchiveNative(context.Background(), restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    filepath.Join(dir, "dest"),
		logger:      newTestLogger(),
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("extractArchiveNative error = %v, want close error %v", err, closeErr)
	}
}
