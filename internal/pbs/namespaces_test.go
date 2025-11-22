package pbs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

func TestListNamespacesResponseParse(t *testing.T) {
	jsonData := `{
		"data": [
			{"ns": "", "path": "/mnt/datastore", "comment": "root namespace"},
			{"ns": "prod", "path": "/mnt/datastore/prod", "parent": "", "ctime": 1700000000}
		]
	}`

	var resp listNamespacesResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(resp.Data))
	}
	if resp.Data[0].Ns != "" || resp.Data[0].Comment != "root namespace" {
		t.Fatalf("unexpected root namespace: %#v", resp.Data[0])
	}
	if resp.Data[1].Ns != "prod" || resp.Data[1].Path != "/mnt/datastore/prod" {
		t.Fatalf("unexpected prod namespace: %#v", resp.Data[1])
	}
}

func TestDiscoverNamespacesFromFilesystem_DetectsSupportedDirs(t *testing.T) {
	tmpDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(tmpDir, "vm-ns", "vm"))
	mustMkdirAll(t, filepath.Join(tmpDir, "ct-ns", "ct"))
	mustMkdirAll(t, filepath.Join(tmpDir, "host-ns", "host"))
	mustMkdirAll(t, filepath.Join(tmpDir, "nested-ns", "namespace"))

	namespaces, err := discoverNamespacesFromFilesystem(tmpDir)
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}

	got := namespacesToMap(namespaces)
	if root, ok := got[""]; !ok {
		t.Fatalf("root namespace missing in %+v", namespaces)
	} else {
		if root.Path != tmpDir {
			t.Fatalf("root path mismatch: got %q want %q", root.Path, tmpDir)
		}
		if root.Comment != "root namespace" {
			t.Fatalf("root comment mismatch: %q", root.Comment)
		}
	}

	want := map[string]string{
		"vm-ns":     filepath.Join(tmpDir, "vm-ns"),
		"ct-ns":     filepath.Join(tmpDir, "ct-ns"),
		"host-ns":   filepath.Join(tmpDir, "host-ns"),
		"nested-ns": filepath.Join(tmpDir, "nested-ns"),
	}
	for ns, path := range want {
		entry, ok := got[ns]
		if !ok {
			t.Fatalf("missing namespace %q in %+v", ns, namespaces)
		}
		if entry.Path != path {
			t.Fatalf("path mismatch for %q: got %q want %q", ns, entry.Path, path)
		}
	}
}

func TestDiscoverNamespacesFromFilesystem_IgnoresNonDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	mustWriteFile(t, filepath.Join(tmpDir, "some-file.txt"), []byte("ignore me"))
	mustMkdirAll(t, filepath.Join(tmpDir, "valid-ns", "vm"))

	namespaces, err := discoverNamespacesFromFilesystem(tmpDir)
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
	if _, err := discoverNamespacesFromFilesystem(""); err == nil || !strings.Contains(err.Error(), "datastore path is empty") {
		t.Fatalf("expected error for empty path, got %v", err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := discoverNamespacesFromFilesystem(missing); err == nil || !strings.Contains(err.Error(), "cannot read datastore path") {
		t.Fatalf("expected error for missing path, got %v", err)
	}
}

func TestListNamespaces_CLISuccess(t *testing.T) {
	setExecCommandStub(t, "cli-success")

	namespaces, usedFallback, err := ListNamespaces("dummy", t.TempDir())
	if err != nil {
		t.Fatalf("ListNamespaces failed: %v", err)
	}
	if usedFallback {
		t.Fatal("expected CLI result, got fallback")
	}

	got := namespacesToMap(namespaces)
	if len(got) != 2 {
		t.Fatalf("expected 2 namespaces, got %+v", namespaces)
	}
	if _, ok := got["prod"]; !ok {
		t.Fatalf("CLI namespace missing in %+v", namespaces)
	}
}

func TestListNamespaces_CLIFallback(t *testing.T) {
	setExecCommandStub(t, "cli-error")

	tmpDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(tmpDir, "local", "vm"))

	namespaces, usedFallback, err := ListNamespaces("dummy", tmpDir)
	if err != nil {
		t.Fatalf("ListNamespaces failed: %v", err)
	}
	if !usedFallback {
		t.Fatal("expected fallback to filesystem")
	}

	got := namespacesToMap(namespaces)
	if _, ok := got["local"]; !ok {
		t.Fatalf("expected filesystem namespace, got %+v", namespaces)
	}
}

func TestListNamespacesViaCLI_ErrorIncludesStderr(t *testing.T) {
	setExecCommandStub(t, "cli-error")
	if _, err := listNamespacesViaCLI("dummy"); err == nil || !strings.Contains(err.Error(), "stderr: CLI exploded") {
		t.Fatalf("expected stderr text in error, got %v", err)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	switch os.Getenv("PBS_HELPER_SCENARIO") {
	case "cli-success":
		fmt.Fprint(os.Stdout, `{"data":[{"ns":"","path":"/mnt/datastore","comment":"root namespace"},{"ns":"prod","path":"/mnt/datastore/prod","parent":"","ctime":1700000000}]}`)
		os.Exit(0)
	case "cli-error":
		fmt.Fprint(os.Stderr, "CLI exploded")
		os.Exit(1)
	default:
		fmt.Fprint(os.Stderr, "unknown scenario")
		os.Exit(2)
	}
}

func setExecCommandStub(t *testing.T, scenario string) {
	t.Helper()
	original := execCommand
	execCommand = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
		cmd.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"PBS_HELPER_SCENARIO="+scenario,
		)
		return cmd
	}
	t.Cleanup(func() {
		execCommand = original
	})
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
