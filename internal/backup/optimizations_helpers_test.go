package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSplitFileAndChunks(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "data.bin")
	content := bytes.Repeat([]byte("x"), 40)
	if err := os.WriteFile(source, content, 0o640); err != nil {
		t.Fatalf("write source: %v", err)
	}

	destBase := filepath.Join(tmp, "chunks", "data.bin")
	if err := splitFile(source, destBase, 16); err != nil {
		t.Fatalf("splitFile: %v", err)
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
	if err := normalizeTextFile(textPath); err != nil {
		t.Fatalf("normalizeTextFile: %v", err)
	}
	data, _ := os.ReadFile(textPath)
	if bytes.Contains(data, []byte("\r")) {
		t.Fatalf("expected CR removed, got %q", data)
	}

	cfgPath := filepath.Join(tmp, "app.conf")
	cfgContent := "#comment\nz=1\n\n;ignored\na=2\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o640); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	if err := normalizeConfigFile(cfgPath); err != nil {
		t.Fatalf("normalizeConfigFile: %v", err)
	}
	cfgData, _ := os.ReadFile(cfgPath)
	if string(cfgData) != "a=2\nz=1" {
		t.Fatalf("config not normalized/sorted, got %q", cfgData)
	}

	jsonPath := filepath.Join(tmp, "data.json")
	if err := os.WriteFile(jsonPath, []byte("{\n \"a\": 1,\n \"b\": 2\n}\n"), 0o640); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if err := minifyJSON(jsonPath); err != nil {
		t.Fatalf("minifyJSON: %v", err)
	}
	jdata, _ := os.ReadFile(jsonPath)
	if bytes.Contains(jdata, []byte(" ")) || bytes.Contains(jdata, []byte("\n")) {
		t.Fatalf("expected minified JSON, got %q", jdata)
	}

	if err := os.WriteFile(jsonPath, []byte("{invalid"), 0o640); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}
	if err := minifyJSON(jsonPath); err == nil {
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
	if err := minifyJSON(path); err != nil {
		t.Fatalf("minifyJSON: %v", err)
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
