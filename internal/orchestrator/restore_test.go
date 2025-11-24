package orchestrator

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestExtractTarEntry_AllowsRootDestinationPaths(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	// Simulate a restore to system root ("/") but write under /tmp
	destRoot := string(os.PathSeparator)
	relPath := filepath.Join("tmp", "proxmox-backup-test", t.Name())

	header := &tar.Header{
		Name:     relPath,
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}

	var tr *tar.Reader // unused for directory extraction

	if err := extractTarEntry(tr, header, destRoot, logger); err != nil {
		t.Fatalf("extractTarEntry failed for destRoot=%q and header.Name=%q: %v", destRoot, header.Name, err)
	}

	target := filepath.Join(destRoot, header.Name)
	t.Cleanup(func() {
		_ = os.RemoveAll(target)
	})

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected directory %s to be created, got error: %v", target, err)
	}
}

func TestExtractTarEntry_BlocksPathTraversal(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	destRoot := t.TempDir()

	header := &tar.Header{
		Name:     filepath.Join("..", "etc", "passwd"),
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}

	var tr *tar.Reader // unused for directory extraction

	if err := extractTarEntry(tr, header, destRoot, logger); err == nil {
		t.Fatalf("expected error for path traversal with destRoot=%q and header.Name=%q, got nil", destRoot, header.Name)
	}
}

func TestParsePoolNameFromUnit(t *testing.T) {
	tests := []struct {
		name     string
		unit     string
		expected string
	}{
		{name: "zfs-import service", unit: "zfs-import@backup_ext.service", expected: "backup_ext"},
		{name: "generic import service", unit: "import@tank.service", expected: "tank"},
		{name: "non service", unit: "import@pool.timer", expected: ""},
		{name: "unrelated unit", unit: "sshd.service", expected: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePoolNameFromUnit(tc.unit); got != tc.expected {
				t.Fatalf("expected %q got %q", tc.expected, got)
			}
		})
	}
}

func TestParseZpoolImportOutput(t *testing.T) {
	output := `
   pool: tank
     id: 123456789
  state: ONLINE

   pool: backup_ext
     id: 987654321
`

	pools := parseZpoolImportOutput(output)
	if len(pools) != 2 {
		t.Fatalf("expected 2 pools, got %d (%v)", len(pools), pools)
	}

	expected := []string{"tank", "backup_ext"}
	for _, want := range expected {
		found := false
		for _, pool := range pools {
			if pool == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected pool %q to be parsed from output", want)
		}
	}
}

func TestCombinePoolNames(t *testing.T) {
	got := combinePoolNames([]string{"tank", "pool2"}, []string{"pool2", "pool3"})
	expected := []string{"pool2", "pool3", "tank"}

	if len(got) != len(expected) {
		t.Fatalf("expected %d pools, got %d (%v)", len(expected), len(got), got)
	}

	for i, pool := range expected {
		if got[i] != pool {
			t.Fatalf("expected %v, got %v", expected, got)
		}
	}
}

func TestHasCategoryID(t *testing.T) {
	categories := []Category{
		{ID: "ssl"},
		{ID: "zfs"},
		{ID: "datastore_pbs"},
	}

	if !hasCategoryID(categories, "ssl") {
		t.Fatalf("expected to find category ssl")
	}
	if hasCategoryID(categories, "network") {
		t.Fatalf("did not expect to find category network")
	}
}

func TestShouldRecreateDirectories(t *testing.T) {
	catsPBS := []Category{
		{ID: "ssl"},
		{ID: "datastore_pbs"},
	}
	if !shouldRecreateDirectories(SystemTypePBS, catsPBS) {
		t.Fatalf("expected PBS datastore recreation when datastore category present")
	}
	if shouldRecreateDirectories(SystemTypePBS, []Category{{ID: "ssl"}}) {
		t.Fatalf("did not expect PBS recreation without datastore category")
	}

	catsPVE := []Category{
		{ID: "storage_pve"},
	}
	if !shouldRecreateDirectories(SystemTypePVE, catsPVE) {
		t.Fatalf("expected PVE storage recreation when storage category present")
	}
	if shouldRecreateDirectories(SystemTypePVE, []Category{{ID: "pve_cluster"}}) {
		t.Fatalf("did not expect PVE recreation without storage category")
	}

	if shouldRecreateDirectories(SystemTypeUnknown, catsPBS) {
		t.Fatalf("did not expect recreation for unknown system type")
	}
}
