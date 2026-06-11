package orchestrator

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestIsDedupManifestEntry(t *testing.T) {
	cases := map[string]bool{
		"./" + backup.DedupManifestRelPath:                  true,
		backup.DedupManifestRelPath:                         true,
		"/" + backup.DedupManifestRelPath:                   true,
		"etc/pve/user.cfg":                                  false,
		"var/lib/proxsave-info/commands/pve/pve_users.json": false,
	}
	for in, want := range cases {
		if got := isDedupManifestEntry(in); got != want {
			t.Errorf("isDedupManifestEntry(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSkipRestoreArchiveEntryAlwaysExtractsDedupManifest(t *testing.T) {
	hdr := &tar.Header{Name: "./" + backup.DedupManifestRelPath}
	// Selective restore with a category that the manifest path does not match.
	opts := restoreArchiveOptions{categories: []Category{{ID: "pve_access_control"}}}
	var stats restoreExtractionStats
	if skipRestoreArchiveEntry(hdr, opts, true, &restoreExtractionLog{}, &stats) {
		t.Fatal("dedup manifest must never be skipped, even in selective restore")
	}
}

func TestMaterializeDedupSymlinksFullRestore(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destRoot, "a", "one.cfg"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "a/two.cfg", Mode: 0o640}})

	materializeDedupSymlinks(destRoot, logging.New(types.LogLevelError, false))

	two := filepath.Join(destRoot, "a", "two.cfg")
	info, err := os.Lstat(two)
	if err != nil {
		t.Fatalf("lstat materialized file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected a/two.cfg to be a regular file after materialization, got symlink")
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected mode 0640, got %o", info.Mode().Perm())
	}
	if data, err := os.ReadFile(two); err != nil || string(data) != "payload" {
		t.Fatalf("materialized content mismatch: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))); !os.IsNotExist(err) {
		t.Fatalf("dedup manifest should be removed after materialization, stat err=%v", err)
	}
}

func TestMaterializeDedupSymlinksRemovesDanglingLink(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlink whose target was not extracted (its category was not selected).
	if err := os.Symlink("missing.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "a/two.cfg", Mode: 0o640}})

	materializeDedupSymlinks(destRoot, logging.New(types.LogLevelError, false))

	if _, err := os.Lstat(filepath.Join(destRoot, "a", "two.cfg")); !os.IsNotExist(err) {
		t.Fatalf("expected dangling dedup link to be removed, stat err=%v", err)
	}
}

func writeDedupManifestForTest(t *testing.T, destRoot string, entries []backup.DedupManifestEntry) {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
