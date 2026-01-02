package orchestrator

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestExtractTarEntry_AllowsRootDestinationPaths(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	// Simulate a restore to system root ("/") but write under /tmp
	destRoot := string(os.PathSeparator)
	relPath := filepath.Join("tmp", "proxsave-test", t.Name())

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

func TestShouldSkipProxmoxSystemRestore(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantSkip bool
	}{
		{name: "skip domains.cfg", path: "etc/proxmox-backup/domains.cfg", wantSkip: true},
		{name: "skip user.cfg", path: "etc/proxmox-backup/user.cfg", wantSkip: true},
		{name: "skip acl.cfg", path: "etc/proxmox-backup/acl.cfg", wantSkip: true},
		{name: "skip proxy.cfg", path: "etc/proxmox-backup/proxy.cfg", wantSkip: true},
		{name: "skip proxy.pem", path: "etc/proxmox-backup/proxy.pem", wantSkip: true},
		{name: "skip ssl subtree", path: "etc/proxmox-backup/ssl/example.pem", wantSkip: true},
		{name: "skip lock subtree", path: "var/lib/proxmox-backup/lock/lockfile", wantSkip: true},
		{name: "skip clusterlock", path: "var/lib/proxmox-backup/.clusterlock", wantSkip: true},
		{name: "allow datastore.cfg", path: "etc/proxmox-backup/datastore.cfg", wantSkip: false},
		{name: "allow maintenance.cfg", path: "etc/proxmox-backup/maintenance.cfg", wantSkip: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := shouldSkipProxmoxSystemRestore(tc.path)
			if skip != tc.wantSkip {
				t.Fatalf("skip=%v want %v (reason=%q)", skip, tc.wantSkip, reason)
			}
			if skip && strings.TrimSpace(reason) == "" {
				t.Fatalf("expected skip reason to be non-empty")
			}
		})
	}
}

func TestExtractSymlink_SecurityValidation(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	destRoot := t.TempDir()

	// Create a target directory structure
	if err := os.MkdirAll(filepath.Join(destRoot, "etc"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destRoot, "etc/config.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Test 1: Malicious symlink attempting directory traversal
	maliciousHeader := &tar.Header{
		Name:     "malicious_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../../../etc/passwd",
		Uid:      1000,
		Gid:      1000,
	}
	maliciousTarget := filepath.Join(destRoot, maliciousHeader.Name)

	err := extractSymlink(maliciousTarget, maliciousHeader, destRoot, logger)
	if err == nil {
		t.Fatalf("expected error for malicious symlink, got nil")
	}
	if !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("expected 'escapes root' error, got: %v", err)
	}

	// Verify malicious symlink was not created
	if _, err := os.Lstat(maliciousTarget); err == nil {
		t.Fatalf("malicious symlink should not exist after failed validation")
	}

	// Test 2: Legitimate relative symlink
	legitimateHeader := &tar.Header{
		Name:     "good_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "etc/config.txt",
		Uid:      1000,
		Gid:      1000,
	}
	legitimateTarget := filepath.Join(destRoot, legitimateHeader.Name)

	err = extractSymlink(legitimateTarget, legitimateHeader, destRoot, logger)
	if err != nil {
		t.Fatalf("legitimate symlink should succeed: %v", err)
	}

	// Verify legitimate symlink was created correctly
	linkTarget, err := os.Readlink(legitimateTarget)
	if err != nil {
		t.Fatalf("legitimate symlink should exist: %v", err)
	}
	if linkTarget != "etc/config.txt" {
		t.Fatalf("symlink target = %q, want 'etc/config.txt'", linkTarget)
	}

	// Test 3: Absolute symlink target outside destRoot (should be rejected)
	absoluteHeader := &tar.Header{
		Name:     "absolute_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
		Uid:      1000,
		Gid:      1000,
	}
	absoluteTarget := filepath.Join(destRoot, absoluteHeader.Name)

	err = extractSymlink(absoluteTarget, absoluteHeader, destRoot, logger)
	if err == nil {
		t.Fatalf("expected error for absolute symlink target, got nil")
	}
	if !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("expected 'escapes root' error, got: %v", err)
	}

	// Test 4: Absolute symlink target within destRoot (should be allowed)
	absoluteWithinHeader := &tar.Header{
		Name:     "absolute_within_link",
		Typeflag: tar.TypeSymlink,
		Linkname: filepath.Join(destRoot, "etc", "config.txt"),
		Uid:      1000,
		Gid:      1000,
	}
	absoluteWithinTarget := filepath.Join(destRoot, absoluteWithinHeader.Name)

	err = extractSymlink(absoluteWithinTarget, absoluteWithinHeader, destRoot, logger)
	if err != nil {
		t.Fatalf("absolute symlink within destRoot should succeed: %v", err)
	}

	linkTarget, err = os.Readlink(absoluteWithinTarget)
	if err != nil {
		t.Fatalf("absolute symlink within destRoot should exist: %v", err)
	}
	if linkTarget != absoluteWithinHeader.Linkname {
		t.Fatalf("symlink target = %q, want %q", linkTarget, absoluteWithinHeader.Linkname)
	}

	// Test 5: Symlink in subdirectory pointing to parent (but still within destRoot)
	if err := os.MkdirAll(filepath.Join(destRoot, "subdir"), 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	safeParentHeader := &tar.Header{
		Name:     "subdir/parent_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "../etc/config.txt",
		Uid:      1000,
		Gid:      1000,
	}
	safeParentTarget := filepath.Join(destRoot, safeParentHeader.Name)

	err = extractSymlink(safeParentTarget, safeParentHeader, destRoot, logger)
	if err != nil {
		t.Fatalf("safe parent symlink should succeed: %v", err)
	}

	linkTarget, err = os.Readlink(safeParentTarget)
	if err != nil {
		t.Fatalf("safe parent symlink should exist: %v", err)
	}
	if linkTarget != "../etc/config.txt" {
		t.Fatalf("symlink target = %q, want '../etc/config.txt'", linkTarget)
	}
}

func TestExtractTarEntry_DoesNotFollowExistingSymlinkTargetPath(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	orig := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = orig })

	destRoot := t.TempDir()

	unitPath := filepath.Join(destRoot, "lib", "systemd", "system", "proxmox-backup.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	const unitData = "unit-file-content"
	if err := os.WriteFile(unitPath, []byte(unitData), 0o644); err != nil {
		t.Fatalf("write unit file: %v", err)
	}

	linkPath := filepath.Join(destRoot, "etc", "systemd", "system", "multi-user.target.wants", "proxmox-backup.service")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir link dir: %v", err)
	}

	relTarget, err := filepath.Rel(filepath.Dir(linkPath), unitPath)
	if err != nil {
		t.Fatalf("compute relative link target: %v", err)
	}
	if err := os.Symlink(relTarget, linkPath); err != nil {
		t.Fatalf("create existing symlink: %v", err)
	}

	header := &tar.Header{
		Name:     "etc/systemd/system/multi-user.target.wants/proxmox-backup.service",
		Typeflag: tar.TypeSymlink,
		Linkname: relTarget,
	}

	var tr *tar.Reader
	if err := extractTarEntry(tr, header, destRoot, logger); err != nil {
		t.Fatalf("extractTarEntry failed: %v", err)
	}

	unitInfo, err := os.Lstat(unitPath)
	if err != nil {
		t.Fatalf("lstat unit file: %v", err)
	}
	if unitInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unit file should remain a regular file, got symlink")
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	if string(data) != unitData {
		t.Fatalf("unit file content changed: got %q want %q", string(data), unitData)
	}

	createdTarget, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("read restored symlink: %v", err)
	}
	if createdTarget != relTarget {
		t.Fatalf("restored symlink target = %q, want %q", createdTarget, relTarget)
	}
}
