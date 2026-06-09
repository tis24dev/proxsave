package orchestrator

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression for backup-file-parse-fails-feature-broken (2026-06-09 audit):
// mapping_<type>.json is the raw output of `pvesh get /cluster/mapping/<type>
// --output-format=json`, whose `map` field is an ARRAY OF PROPERTY STRINGS
// ("node=pve01,path=...,id=..."), not an array of objects. The old struct
// (Map []map[string]interface{}) failed to unmarshal real output, so the whole
// resource-mapping restore silently never ran on any real backup. Written after
// the fix to accept the real string-array shape, hence the _audited suffix.

// Direct parser test with the real on-disk format, plus a non-canonical key order
// to also exercise renderMappingEntry's normalization (node, path, id first).
func TestReadPVEClusterResourceMappings_RealStringArrayFormat(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	exportRoot := t.TempDir()
	mappingPath := filepath.Join(exportRoot, "var", "lib", "proxsave-info", "commands", "pve", "mapping_pci.json")
	if err := os.MkdirAll(filepath.Dir(mappingPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Real pvesh output: map is an array of property strings (one per node).
	if err := os.WriteFile(mappingPath, []byte(strings.TrimSpace(`[
  {"id":"gpu","map":["path=0000:01:00.0,id=8086:1234,node=pve01","node=pve02,path=0000:02:00.0,id=8086:1234"]}
]`)), 0o640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	specs, err := readPVEClusterResourceMappingsFromExport(exportRoot, "pci")
	if err != nil {
		t.Fatalf("readPVEClusterResourceMappingsFromExport returned error on real pvesh format: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1: %+v", len(specs), specs)
	}
	if specs[0].ID != "gpu" {
		t.Errorf("ID = %q, want gpu", specs[0].ID)
	}
	want := []string{
		"node=pve01,path=0000:01:00.0,id=8086:1234", // keys normalized to node,path,id
		"node=pve02,path=0000:02:00.0,id=8086:1234",
	}
	if strings.Join(specs[0].MapEntries, "|") != strings.Join(want, "|") {
		t.Errorf("MapEntries = %#v, want %#v", specs[0].MapEntries, want)
	}
}

// End-to-end: a real-format backup must actually produce the pvesh create call
// (before the fix it silently produced none).
func TestRunSafeClusterApply_AppliesRealStringArrayMappings(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "pvesh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pvesh stub: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	mappingPath := filepath.Join(exportRoot, "var", "lib", "proxsave-info", "commands", "pve", "mapping_pci.json")
	if err := os.MkdirAll(filepath.Dir(mappingPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(mappingPath, []byte(strings.TrimSpace(`[
  {"id":"device1","map":["node=pve01,path=0000:01:00.0"]}
]`)), 0o640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("yes\n"))
	if err := runSafeClusterApply(context.Background(), reader, exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply error: %v", err)
	}

	wantPrefix := "pvesh create /cluster/mapping/pci --id device1 --map node=pve01,path=0000:01:00.0"
	for _, call := range runner.calls {
		if strings.HasPrefix(call, wantPrefix) {
			return
		}
	}
	t.Fatalf("expected a pvesh create call with prefix %q; calls=%#v", wantPrefix, runner.calls)
}
