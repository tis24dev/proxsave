package orchestrator

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// --------------------------------------------------------------------------
// sanitizeRestoreEntryTarget tests
// --------------------------------------------------------------------------

func TestSanitizeRestoreEntryTarget_EmptyName(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/tmp", "")
	if err == nil || !strings.Contains(err.Error(), "empty archive entry name") {
		t.Fatalf("expected empty name error, got: %v", err)
	}
}

func TestSanitizeRestoreEntryTarget_EmptyDestRoot(t *testing.T) {
	target, destRoot, err := sanitizeRestoreEntryTarget("", "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if destRoot == "" {
		t.Fatalf("expected non-empty destRoot")
	}
	if target == "" {
		t.Fatalf("expected non-empty target")
	}
}

func TestSanitizeRestoreEntryTarget_DotDot(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/tmp", "..")
	if err == nil || !strings.Contains(err.Error(), "illegal path") {
		t.Fatalf("expected illegal path error, got: %v", err)
	}
}

func TestSanitizeRestoreEntryTarget_DotDotSlash(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/tmp", "../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "illegal path") {
		t.Fatalf("expected illegal path error, got: %v", err)
	}
}

func TestSanitizeRestoreEntryTarget_ContainsDotDot(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/tmp", "foo/../../../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "illegal path") {
		t.Fatalf("expected illegal path error, got: %v", err)
	}
}

func TestSanitizeRestoreEntryTarget_ValidPath(t *testing.T) {
	target, destRoot, err := sanitizeRestoreEntryTarget("/tmp", "etc/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(target, filepath.Join("etc", "test.txt")) {
		t.Fatalf("unexpected target: %s", target)
	}
	if destRoot == "" {
		t.Fatalf("expected non-empty destRoot")
	}
}

func TestSanitizeRestoreEntryTarget_LeadingSlash(t *testing.T) {
	target, _, err := sanitizeRestoreEntryTarget("/tmp", "/etc/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(target, filepath.Join("etc", "test.txt")) {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestSanitizeRestoreEntryTarget_DotOnly(t *testing.T) {
	_, _, err := sanitizeRestoreEntryTarget("/tmp", ".")
	if err == nil || !strings.Contains(err.Error(), "invalid archive entry name") {
		t.Fatalf("expected invalid name error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// extractDirectory tests
// --------------------------------------------------------------------------

func TestExtractDirectory_Success(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	logger := logging.New(types.LogLevelDebug, false)
	destRoot := t.TempDir()
	target := filepath.Join(destRoot, "subdir")

	header := &tar.Header{
		Name: "subdir",
		Mode: 0o755,
		Uid:  os.Getuid(),
		Gid:  os.Getgid(),
	}

	if err := extractDirectory(target, header, logger); err != nil {
		t.Fatalf("extractDirectory failed: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
}

// --------------------------------------------------------------------------
// extractHardlink tests
// --------------------------------------------------------------------------

func TestExtractHardlink_AbsoluteTargetRejected(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	header := &tar.Header{
		Name:     "link",
		Linkname: "/absolute/path",
		Typeflag: tar.TypeLink,
	}

	err := extractHardlink("/tmp/dest", header, "/tmp/dest", logger)
	if err == nil || !strings.Contains(err.Error(), "absolute hardlink target not allowed") {
		t.Fatalf("expected absolute target error, got: %v", err)
	}
}

func TestExtractHardlink_EscapesRoot(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	header := &tar.Header{
		Name:     "link",
		Linkname: "../../../etc/passwd",
		Typeflag: tar.TypeLink,
	}

	err := extractHardlink("/tmp/dest/link", header, "/tmp/dest", logger)
	if err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("expected escape error, got: %v", err)
	}
}

func TestExtractHardlink_Success(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	logger := logging.New(types.LogLevelDebug, false)
	destRoot := t.TempDir()
	originalFile := filepath.Join(destRoot, "original.txt")
	linkFile := filepath.Join(destRoot, "link.txt")

	if err := os.WriteFile(originalFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}

	header := &tar.Header{
		Name:     "link.txt",
		Linkname: "original.txt",
		Typeflag: tar.TypeLink,
	}

	if err := extractHardlink(linkFile, header, destRoot, logger); err != nil {
		t.Fatalf("extractHardlink failed: %v", err)
	}

	data, err := os.ReadFile(linkFile)
	if err != nil {
		t.Fatalf("read link failed: %v", err)
	}
	if string(data) != "test" {
		t.Fatalf("content mismatch")
	}
}

// --------------------------------------------------------------------------
// shortHost tests
// --------------------------------------------------------------------------

func TestShortHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"node1.example.com", "node1"},
		{"node1", "node1"},
		{"", ""},
		{"pve.local", "pve"},
	}

	for _, tc := range tests {
		if got := shortHost(tc.input); got != tc.expected {
			t.Fatalf("shortHost(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// --------------------------------------------------------------------------
// sanitizeID tests
// --------------------------------------------------------------------------

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"local", "local"},
		{"local-backup", "local-backup"},
		{"local/backup", "local_backup"},
		{"a:b:c", "a_b_c"},
		{"test@123", "test_123"},
	}

	for _, tc := range tests {
		if got := sanitizeID(tc.input); got != tc.expected {
			t.Fatalf("sanitizeID(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// --------------------------------------------------------------------------
// shouldStopPBSServices tests
// --------------------------------------------------------------------------

func TestShouldStopPBSServices(t *testing.T) {
	tests := []struct {
		name       string
		categories []Category
		expected   bool
	}{
		{
			name:       "empty categories",
			categories: []Category{},
			expected:   false,
		},
		{
			name:       "no PBS categories",
			categories: []Category{{ID: "ssl", Type: CategoryTypePVE}, {ID: "network", Type: CategoryTypePVE}},
			expected:   false,
		},
		{
			name:       "has PBS type category",
			categories: []Category{{ID: "ssl", Type: CategoryTypePVE}, {ID: "datastore_pbs", Type: CategoryTypePBS}},
			expected:   true,
		},
		{
			name:       "has pbs_config with PBS type",
			categories: []Category{{ID: "pbs_config", Type: CategoryTypePBS}},
			expected:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldStopPBSServices(tc.categories); got != tc.expected {
				t.Fatalf("shouldStopPBSServices() = %v, want %v", got, tc.expected)
			}
		})
	}
}

// --------------------------------------------------------------------------
// splitExportCategories tests
// --------------------------------------------------------------------------

func TestSplitExportCategories(t *testing.T) {
	categories := []Category{
		{ID: "ssl", ExportOnly: false},
		{ID: "pve_cluster", ExportOnly: true},
		{ID: "network", ExportOnly: false},
		{ID: "secrets", ExportOnly: true},
	}

	normal, export := splitExportCategories(categories)
	if len(normal) != 2 {
		t.Fatalf("expected 2 normal, got %d", len(normal))
	}
	if len(export) != 2 {
		t.Fatalf("expected 2 export, got %d", len(export))
	}
}

// --------------------------------------------------------------------------
// redirectClusterCategoryToExport tests
// --------------------------------------------------------------------------

func TestRedirectClusterCategoryToExport(t *testing.T) {
	normal := []Category{
		{ID: "ssl", ExportOnly: false},
		{ID: "pve_cluster", ExportOnly: false},
		{ID: "network", ExportOnly: false},
	}
	export := []Category{}

	newNormal, newExport := redirectClusterCategoryToExport(normal, export)

	foundInExport := false
	for _, cat := range newExport {
		if cat.ID == "pve_cluster" {
			foundInExport = true
			break
		}
	}
	if !foundInExport {
		t.Fatalf("pve_cluster should be moved to export")
	}

	foundInNormal := false
	for _, cat := range newNormal {
		if cat.ID == "pve_cluster" {
			foundInNormal = true
			break
		}
	}
	if foundInNormal {
		t.Fatalf("pve_cluster should not be in normal after redirect")
	}
}

// --------------------------------------------------------------------------
// exportDestRoot tests
// --------------------------------------------------------------------------

func TestExportDestRoot(t *testing.T) {
	origTime := restoreTime
	t.Cleanup(func() { restoreTime = origTime })

	restoreTime = &FakeTime{
		Current: time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC),
	}

	result := exportDestRoot("/var/lib/proxsave")
	expected := "/var/lib/proxsave/proxmox-config-export-20240115-103045"
	if result != expected {
		t.Fatalf("expected %q, got %q", expected, result)
	}
}

// --------------------------------------------------------------------------
// setTimestamps tests
// --------------------------------------------------------------------------

func TestSetTimestamps_Success(t *testing.T) {
	destRoot := t.TempDir()
	target := filepath.Join(destRoot, "test.txt")
	if err := os.WriteFile(target, []byte("test"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	modTime := time.Date(2023, 6, 15, 12, 0, 0, 0, time.UTC)
	accessTime := time.Date(2023, 6, 15, 13, 0, 0, 0, time.UTC)

	header := &tar.Header{
		ModTime:    modTime,
		AccessTime: accessTime,
	}

	if err := setTimestamps(target, header); err != nil {
		t.Fatalf("setTimestamps failed: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if !info.ModTime().Equal(modTime) {
		t.Fatalf("modtime mismatch: %v != %v", info.ModTime(), modTime)
	}
}

func TestSetTimestamps_NonexistentFile(t *testing.T) {
	header := &tar.Header{
		ModTime:    time.Now(),
		AccessTime: time.Now(),
	}

	err := setTimestamps("/nonexistent/file.txt", header)
	if err == nil {
		t.Fatalf("expected error for nonexistent file")
	}
}

// --------------------------------------------------------------------------
// extractTarEntry tests for unsupported types
// --------------------------------------------------------------------------

func TestExtractTarEntry_SkipsUnsupportedTypes(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	destRoot := t.TempDir()
	header := &tar.Header{
		Name:     "device",
		Typeflag: tar.TypeBlock,
		Mode:     0o644,
	}

	err := extractTarEntry(nil, header, destRoot, logger)
	if err != nil {
		t.Fatalf("expected nil for unsupported type, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// parseStorageBlocks tests
// --------------------------------------------------------------------------

func TestParseStorageBlocks_MultipleBlocks(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	restoreFS = osFS{}

	cfgPath := filepath.Join(t.TempDir(), "storage.cfg")
	content := `storage: local
	path /var/lib/vz
	content images,rootdir

storage: nfs-backup
	path /mnt/pbs
	content backup
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	blocks, err := parseStorageBlocks(cfgPath)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].ID != "local" || blocks[1].ID != "nfs-backup" {
		t.Fatalf("unexpected block IDs: %v, %v", blocks[0].ID, blocks[1].ID)
	}
}

// --------------------------------------------------------------------------
// minDuration tests
// --------------------------------------------------------------------------

func TestMinDuration(t *testing.T) {
	if got := minDuration(1*time.Second, 2*time.Second); got != 1*time.Second {
		t.Fatalf("expected 1s, got %v", got)
	}
	if got := minDuration(3*time.Second, 2*time.Second); got != 2*time.Second {
		t.Fatalf("expected 2s, got %v", got)
	}
}

// --------------------------------------------------------------------------
// sleepWithContext tests
// --------------------------------------------------------------------------

func TestSleepWithContext_Normal(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	sleepWithContext(ctx, 50*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("sleep too short: %v", elapsed)
	}
}

func TestSleepWithContext_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	sleepWithContext(ctx, 1*time.Second)
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("sleep should have returned immediately: %v", elapsed)
	}
}

// --------------------------------------------------------------------------
// scanVMConfigs tests
// --------------------------------------------------------------------------

func TestScanVMConfigs_NoConfigs(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	entries, err := scanVMConfigs("/nonexistent", "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestScanVMConfigs_WithConfigs(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	qemuDir := "/export/etc/pve/nodes/node1/qemu-server"
	lxcDir := "/export/etc/pve/nodes/node1/lxc"

	_ = fakeFS.AddDir(qemuDir)
	_ = fakeFS.AddDir(lxcDir)
	_ = fakeFS.AddFile(filepath.Join(qemuDir, "100.conf"), []byte("name: vm100\nmemory: 2048"))
	_ = fakeFS.AddFile(filepath.Join(qemuDir, "101.conf"), []byte("name: vm101"))
	_ = fakeFS.AddFile(filepath.Join(lxcDir, "200.conf"), []byte("hostname: ct200"))

	entries, err := scanVMConfigs("/export", "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

// --------------------------------------------------------------------------
// readVMName tests
// --------------------------------------------------------------------------

func TestReadVMName_Success(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	confPath := "/etc/pve/qemu-server/100.conf"
	_ = fakeFS.AddFile(confPath, []byte("name: testvm\nmemory: 2048"))

	name := readVMName(confPath)
	if name != "testvm" {
		t.Fatalf("expected 'testvm', got %q", name)
	}
}

func TestReadVMName_NoName(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	confPath := "/etc/pve/qemu-server/100.conf"
	_ = fakeFS.AddFile(confPath, []byte("memory: 2048"))

	name := readVMName(confPath)
	if name != "" {
		t.Fatalf("expected empty name, got %q", name)
	}
}

func TestReadVMName_FileNotFound(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	name := readVMName("/nonexistent.conf")
	if name != "" {
		t.Fatalf("expected empty name, got %q", name)
	}
}

// --------------------------------------------------------------------------
// detectNodeForVM tests
// --------------------------------------------------------------------------

func TestDetectNodeForVM_ReturnsHostname(t *testing.T) {
	entry := vmEntry{
		Path: "/export/etc/pve/nodes/node1/qemu-server/100.conf",
	}
	node := detectNodeForVM(entry)
	// detectNodeForVM returns the current hostname, not the node from path
	if node == "" {
		t.Fatalf("expected non-empty node from hostname")
	}
}

// --------------------------------------------------------------------------
// detectConfiguredZFSPools tests
// --------------------------------------------------------------------------

func TestDetectConfiguredZFSPools_NoUnits(t *testing.T) {
	origFS := restoreFS
	origGlob := restoreGlob
	t.Cleanup(func() {
		restoreFS = origFS
		restoreGlob = origGlob
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreGlob = func(pattern string) ([]string, error) { return nil, nil }

	pools := detectConfiguredZFSPools()
	if len(pools) != 0 {
		t.Fatalf("expected 0 pools, got %d", len(pools))
	}
}
