package pbs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamespaceJSONRoundTrip(t *testing.T) {
	original := Namespace{
		Ns:      "backup-ns",
		Path:    "/mnt/datastore/backup-ns",
		Parent:  "root",
		Comment: "test namespace",
		Ctime:   1700000000,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Namespace
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded != original {
		t.Fatalf("round-trip mismatch: got %#v want %#v", decoded, original)
	}
}

func TestDiscoverNamespacesFromFilesystem_IgnoresNonDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	mustWriteFile(t, filepath.Join(tmpDir, "some-file.txt"), []byte("ignore me"))
	mustMkdirAll(t, filepath.Join(tmpDir, "ns"))
	mustWriteFile(t, filepath.Join(tmpDir, "ns", "not-a-namespace"), []byte("ignore me"))
	mustMkdirAll(t, filepath.Join(tmpDir, "ns", "valid-ns"))

	namespaces, err := discoverNamespacesFromFilesystem(context.Background(), tmpDir, 0)
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}

	got := namespacesToMap(namespaces)
	if len(got) != 2 {
		t.Fatalf("expected only root + valid namespace, got %+v", namespaces)
	}
	if _, ok := got["valid-ns"]; !ok {
		t.Fatalf("valid namespace missing in %+v", namespaces)
	}
}

func TestDiscoverNamespacesFromFilesystem_Errors(t *testing.T) {
	if _, err := discoverNamespacesFromFilesystem(context.Background(), "", 0); err == nil || !strings.Contains(err.Error(), "datastore path is empty") {
		t.Fatalf("expected error for empty path, got %v", err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := discoverNamespacesFromFilesystem(context.Background(), missing, 0); err == nil || !strings.Contains(err.Error(), "cannot read datastore path") {
		t.Fatalf("expected error for missing path, got %v", err)
	}
}

func TestDiscoverNamespacesFromFilesystemExportedHelper(t *testing.T) {
	tmpDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(tmpDir, "ns", "prod"))

	namespaces, err := DiscoverNamespacesFromFilesystem(context.Background(), tmpDir, 0)
	if err != nil {
		t.Fatalf("DiscoverNamespacesFromFilesystem failed: %v", err)
	}

	got := namespacesToMap(namespaces)
	if _, ok := got["prod"]; !ok {
		t.Fatalf("expected exported helper to discover namespace, got %+v", namespaces)
	}
}

func namespacesToMap(namespaces []Namespace) map[string]Namespace {
	result := make(map[string]Namespace, len(namespaces))
	for _, ns := range namespaces {
		result[ns.Ns] = ns
	}
	return result
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s failed: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s failed: %v", path, err)
	}
}
