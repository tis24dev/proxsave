package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestSplitFileAndChunks(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "data.bin")
	content := bytes.Repeat([]byte("x"), 40)
	if err := os.WriteFile(source, content, 0o640); err != nil {
		t.Fatalf("write source: %v", err)
	}

	destBase := filepath.Join(tmp, "chunks", "data.bin")
	res, err := splitFile(source, destBase, 16)
	if err != nil {
		t.Fatalf("splitFile: %v", err)
	}
	if res.ChunkCount != 3 {
		t.Fatalf("chunk count %d, want 3", res.ChunkCount)
	}
	if res.SizeBytes != int64(len(content)) {
		t.Fatalf("split size %d, want %d", res.SizeBytes, len(content))
	}

	chunks := []string{
		destBase + ".001.chunk",
		destBase + ".002.chunk",
		destBase + ".003.chunk",
	}
	var total int
	for _, c := range chunks {
		b, err := os.ReadFile(c)
		if err != nil {
			t.Fatalf("read chunk %s: %v", c, err)
		}
		total += len(b)
	}
	if total != len(content) {
		t.Fatalf("combined chunk size %d, want %d", total, len(content))
	}
}

func TestNormalizeTextFileAndConfigAndJSON(t *testing.T) {
	tmp := t.TempDir()

	textPath := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(textPath, []byte("line1\r\nline2\r\n"), 0o640); err != nil {
		t.Fatalf("write text: %v", err)
	}
	if changed, err := normalizeTextFile(textPath); err != nil {
		t.Fatalf("normalizeTextFile: %v", err)
	} else if !changed {
		t.Fatalf("expected text to be normalized")
	}
	data, _ := os.ReadFile(textPath)
	if bytes.Contains(data, []byte("\r")) {
		t.Fatalf("expected CR removed, got %q", data)
	}

	cfgPath := filepath.Join(tmp, "app.conf")
	cfgContent := "#comment\r\nz=1\r\n\r\n;ignored\r\na=2\r\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o640); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	if changed, err := normalizeConfigFile(cfgPath); err != nil {
		t.Fatalf("normalizeConfigFile: %v", err)
	} else if !changed {
		t.Fatalf("expected config to be normalized")
	}
	cfgData, _ := os.ReadFile(cfgPath)
	if bytes.Contains(cfgData, []byte("\r")) {
		t.Fatalf("expected CR removed from config, got %q", cfgData)
	}
	if string(cfgData) != strings.ReplaceAll(cfgContent, "\r", "") {
		t.Fatalf("config contents changed unexpectedly, got %q", cfgData)
	}

	jsonPath := filepath.Join(tmp, "data.json")
	if err := os.WriteFile(jsonPath, []byte("{\n \"a\": 1,\n \"b\": 2\n}\n"), 0o640); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if changed, err := minifyJSON(jsonPath); err != nil {
		t.Fatalf("minifyJSON: %v", err)
	} else if !changed {
		t.Fatalf("expected JSON to be minified")
	}
	jdata, _ := os.ReadFile(jsonPath)
	if bytes.Contains(jdata, []byte(" ")) || bytes.Contains(jdata, []byte("\n")) {
		t.Fatalf("expected minified JSON, got %q", jdata)
	}

	if err := os.WriteFile(jsonPath, []byte("{invalid"), 0o640); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}
	if _, err := minifyJSON(jsonPath); err == nil {
		t.Fatalf("expected error for invalid json")
	}
}

func TestMinifyJSONKeepsData(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "data.json")
	original := map[string]int{"a": 1, "b": 2}
	payload, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(path, payload, 0o640); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if changed, err := minifyJSON(path); err != nil {
		t.Fatalf("minifyJSON: %v", err)
	} else if !changed {
		t.Fatalf("expected JSON to be minified")
	}
	roundTrip, _ := os.ReadFile(path)
	var decoded map[string]int
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatalf("unmarshal minified: %v", err)
	}
	if decoded["a"] != 1 || decoded["b"] != 2 {
		t.Fatalf("unexpected decoded content: %+v", decoded)
	}
}

// TestMinifyJSONPreservesLargeIntegers verifies that json.Compact preserves
// numeric values that exceed float64 precision (integers > 2^53).
func TestMinifyJSONPreservesLargeIntegers(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "data.json")
	// 9007199254740993 is 2^53 + 1, which loses precision under float64.
	input := `{"id": 9007199254740993, "name": "test"}`
	if err := os.WriteFile(path, []byte(input), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := minifyJSON(path); err != nil {
		t.Fatalf("minifyJSON: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Contains(got, []byte("9007199254740993")) {
		t.Fatalf("large integer lost precision: got %q", got)
	}
}

// TestMinifyJSONPreservesKeyOrder verifies that json.Compact does not
// reorder object keys (unlike json.Marshal on map[string]any).
func TestMinifyJSONPreservesKeyOrder(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "data.json")
	// Keys deliberately in reverse alphabetical order.
	input := "{\n  \"z\": 1,\n  \"a\": 2\n}\n"
	if err := os.WriteFile(path, []byte(input), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := minifyJSON(path); err != nil {
		t.Fatalf("minifyJSON: %v", err)
	}
	got, _ := os.ReadFile(path)
	expected := `{"z":1,"a":2}`
	if string(got) != expected {
		t.Fatalf("key order changed: expected %q, got %q", expected, string(got))
	}
}

// TestMinifyJSONNoopOnAlreadyCompact verifies no disk write when file is
// already compact.
func TestMinifyJSONNoopOnAlreadyCompact(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "data.json")
	compact := `{"a":1,"b":2}`
	if err := os.WriteFile(path, []byte(compact), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	info1, _ := os.Stat(path)
	if _, err := minifyJSON(path); err != nil {
		t.Fatalf("minifyJSON: %v", err)
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("file was rewritten even though already compact")
	}
}

// TestReassembleChunkedFilesRoundTrip verifies that chunk + reassemble is a
// lossless round-trip: the reassembled file is byte-identical to the original.
func TestReassembleChunkedFilesRoundTrip(t *testing.T) {
	root := t.TempDir()

	// Create a file that will be chunked (96 bytes, threshold 64, chunk size 16).
	original := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), 6) // 96 bytes
	bigFile := filepath.Join(root, "subdir", "large.bin")
	if err := os.MkdirAll(filepath.Dir(bigFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bigFile, original, 0o640); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := OptimizationConfig{
		EnableChunking:      true,
		ChunkSizeBytes:      16,
		ChunkThresholdBytes: 64,
	}

	// Apply optimizations (only chunking enabled).
	if err := ApplyOptimizations(context.Background(), logger, root, cfg); err != nil {
		t.Fatalf("ApplyOptimizations: %v", err)
	}

	// Verify the original is gone and the marker exists.
	if _, err := os.Stat(bigFile); !os.IsNotExist(err) {
		t.Fatalf("expected original removed, stat err=%v", err)
	}
	if _, err := os.Stat(bigFile + ".chunked"); err != nil {
		t.Fatalf("chunk marker missing: %v", err)
	}
	// Regression: if file size is an exact multiple of chunk size, we must not
	// create an extra empty chunk.
	if _, err := os.Stat(filepath.Join(root, "chunked_files", "subdir", "large.bin.006.chunk")); err != nil {
		t.Fatalf("expected last chunk to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "chunked_files", "subdir", "large.bin.007.chunk")); !os.IsNotExist(err) {
		t.Fatalf("expected no extra empty chunk, stat err=%v", err)
	}

	// Reassemble.
	if err := ReassembleChunkedFiles(logger, root); err != nil {
		t.Fatalf("ReassembleChunkedFiles: %v", err)
	}

	// Verify byte-identical round-trip.
	reassembled, err := os.ReadFile(bigFile)
	if err != nil {
		t.Fatalf("read reassembled: %v", err)
	}
	if !bytes.Equal(reassembled, original) {
		t.Fatalf("reassembled content differs: got %d bytes, want %d bytes", len(reassembled), len(original))
	}

	// Verify cleanup: marker removed, chunked_files dir removed.
	if _, err := os.Stat(bigFile + ".chunked"); !os.IsNotExist(err) {
		t.Fatalf("chunk marker should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "chunked_files")); !os.IsNotExist(err) {
		t.Fatalf("chunked_files dir should be removed, stat err=%v", err)
	}
}

// TestReassembleNoopWithoutChunks verifies ReassembleChunkedFiles is a no-op
// when the directory contains no chunked files.
func TestReassembleNoopWithoutChunks(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "normal.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelError, false)
	if err := ReassembleChunkedFiles(logger, root); err != nil {
		t.Fatalf("ReassembleChunkedFiles on clean dir: %v", err)
	}
	got, _ := os.ReadFile(filePath)
	if string(got) != "hello" {
		t.Fatalf("file modified unexpectedly: %q", got)
	}
}

// TestNormalizeConfigFileSafeOperations verifies each of the four safe
// operations performed by normalizeConfigFile individually and combined.
func TestNormalizeConfigFileSafeOperations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "BOM removal",
			input:    "\xef\xbb\xbf[section]\n\tkey = value\n",
			expected: "[section]\n\tkey = value\n",
		},
		{
			name:     "trailing whitespace per line",
			input:    "datastore: Data1\n\tpath /mnt/data  \n\tgc-schedule 05:00\t\n",
			expected: "datastore: Data1\n\tpath /mnt/data\n\tgc-schedule 05:00\n",
		},
		{
			name:     "trailing newlines consolidated",
			input:    "[section]\n\tkey = value\n\n\n\n",
			expected: "[section]\n\tkey = value\n",
		},
		{
			name:     "CRLF normalized to LF",
			input:    "datastore: X\r\n\tpath /tmp\r\n",
			expected: "datastore: X\n\tpath /tmp\n",
		},
		{
			name:     "stray CR normalized to LF",
			input:    "line1\rline2\n",
			expected: "line1\nline2\n",
		},
		{
			name:     "all operations combined",
			input:    "\xef\xbb\xbfdatastore: D1\r\n\tpath /mnt/d1  \r\n\tgc 05:00\t\r\n\n\n",
			expected: "datastore: D1\n\tpath /mnt/d1\n\tgc 05:00\n",
		},
		{
			name:     "clean file unchanged",
			input:    "datastore: Data1\n\tpath /mnt/data\n\tgc-schedule 05:00\n",
			expected: "datastore: Data1\n\tpath /mnt/data\n\tgc-schedule 05:00\n",
		},
		{
			name:     "preserves leading indentation",
			input:    "\t\tdeep indent\n\t\t\tdeeper\n",
			expected: "\t\tdeep indent\n\t\t\tdeeper\n",
		},
		{
			name:     "preserves blank lines between sections",
			input:    "datastore: A\n\tpath /a\n\ndatastore: B\n\tpath /b\n",
			expected: "datastore: A\n\tpath /a\n\ndatastore: B\n\tpath /b\n",
		},
		{
			name:     "preserves comments",
			input:    "# main config\n; alt comment\nkey = value\n",
			expected: "# main config\n; alt comment\nkey = value\n",
		},
		{
			name:     "empty file stays empty",
			input:    "",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, "test.cfg")
			if err := os.WriteFile(path, []byte(tc.input), 0o640); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := normalizeConfigFile(path); err != nil {
				t.Fatalf("normalizeConfigFile: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != tc.expected {
				t.Fatalf("mismatch\ninput:    %q\nexpected: %q\ngot:      %q", tc.input, tc.expected, string(got))
			}
		})
	}
}
