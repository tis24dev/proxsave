package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestDiscoverChunksSortsNumerically(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "chunked_files", "big.bin")
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create chunk files in mixed order (including >999) to ensure numeric sort.
	chunkPaths := []string{
		filepath.Join(filepath.Dir(base), "big.bin.010.chunk"),
		filepath.Join(filepath.Dir(base), "big.bin.001.chunk"),
		filepath.Join(filepath.Dir(base), "big.bin.1000.chunk"),
		filepath.Join(filepath.Dir(base), "big.bin.002.chunk"),
		filepath.Join(filepath.Dir(base), "big.bin.999.chunk"),
		filepath.Join(filepath.Dir(base), "big.bin.003.chunk"),
	}
	for _, p := range chunkPaths {
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	chunks, err := discoverChunks(base)
	if err != nil {
		t.Fatalf("discoverChunks: %v", err)
	}

	got := make([]int, 0, len(chunks))
	for _, c := range chunks {
		got = append(got, c.Index)
	}
	want := []int{1, 2, 3, 10, 999, 1000}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("numeric sort mismatch: got %v want %v", got, want)
		}
	}
}

func TestReassembleChunkedFiles_SkipsWhenLastChunkMissing(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)

	originalPath := filepath.Join(root, "file.bin")
	markerPath := originalPath + ".chunked"
	chunkDir := filepath.Join(root, "chunked_files")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	meta := chunkedFileMetadata{
		Version:         1,
		SizeBytes:       9,
		ChunkSizeBytes:  4,
		ChunkCount:      3, // expects 3 chunks
		Mode:            0o640,
		UID:             -1,
		GID:             -1,
		ModTimeUnixNano: time.Now().UnixNano(),
	}
	payload, _ := json.Marshal(meta)
	if err := os.WriteFile(markerPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}

	// Only two chunks present -> should not reassemble.
	if err := os.WriteFile(filepath.Join(chunkDir, "file.bin.001.chunk"), []byte("abcd"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chunkDir, "file.bin.002.chunk"), []byte("efgh"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := ReassembleChunkedFiles(logger, root); err != nil {
		t.Fatalf("ReassembleChunkedFiles: %v", err)
	}

	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("expected original not to be created, stat err=%v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected marker to remain, stat err=%v", err)
	}
}

func TestReassembleChunkedFiles_SkipsWhenSHA256Mismatch(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)

	originalPath := filepath.Join(root, "file.bin")
	markerPath := originalPath + ".chunked"
	chunkDir := filepath.Join(root, "chunked_files")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	data := []byte("hello world")
	meta := chunkedFileMetadata{
		Version:         1,
		SizeBytes:       int64(len(data)),
		ChunkSizeBytes:  8,
		ChunkCount:      2,
		SHA256:          "0000000000000000000000000000000000000000000000000000000000000000", // wrong
		Mode:            0o640,
		UID:             -1,
		GID:             -1,
		ModTimeUnixNano: time.Now().UnixNano(),
	}
	payload, _ := json.Marshal(meta)
	if err := os.WriteFile(markerPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chunkDir, "file.bin.001.chunk"), data[:8], 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chunkDir, "file.bin.002.chunk"), data[8:], 0o640); err != nil {
		t.Fatal(err)
	}

	if err := ReassembleChunkedFiles(logger, root); err != nil {
		t.Fatalf("ReassembleChunkedFiles: %v", err)
	}

	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("expected original not to be created, stat err=%v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected marker to remain, stat err=%v", err)
	}
}

func TestChunkAndReassemble_PreservesModeAndMtime(t *testing.T) {
	root := t.TempDir()
	logger := logging.New(types.LogLevelError, false)

	original := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), 6) // 96 bytes
	target := filepath.Join(root, "subdir", "large.bin")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1700000000, 123456789)
	if err := os.Chtimes(target, mt, mt); err != nil {
		t.Fatal(err)
	}

	if err := chunkLargeFiles(context.Background(), logger, root, 16, 64); err != nil {
		t.Fatalf("chunkLargeFiles: %v", err)
	}
	if err := ReassembleChunkedFiles(logger, root); err != nil {
		t.Fatalf("ReassembleChunkedFiles: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read reassembled: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("content mismatch: got %d bytes want %d bytes", len(got), len(original))
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat reassembled: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode mismatch: got %o want %o", info.Mode().Perm(), 0o600)
	}
	if info.ModTime().UnixNano() != mt.UnixNano() {
		t.Fatalf("mtime mismatch: got %d want %d", info.ModTime().UnixNano(), mt.UnixNano())
	}
}
