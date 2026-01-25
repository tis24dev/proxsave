package orchestrator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// mockFS is a test-only FS that allows error injection for specific operations.
// It extends FakeFS for sandboxed path operations.
type mockFS struct {
	*FakeFS
	mkdirAllErr   error
	createErr     error
	openErr       error
	openFileErr   error
	readDirErr    map[string]error
	readDirResult map[string][]os.DirEntry
	symlinkErr    error
	readlinkErr   error
	removeErr     error
}

func newMockFS() *mockFS {
	return &mockFS{
		FakeFS:        NewFakeFS(),
		readDirErr:    make(map[string]error),
		readDirResult: make(map[string][]os.DirEntry),
	}
}

func (m *mockFS) MkdirAll(path string, perm fs.FileMode) error {
	if m.mkdirAllErr != nil {
		return m.mkdirAllErr
	}
	return m.FakeFS.MkdirAll(path, perm)
}

func (m *mockFS) Create(name string) (*os.File, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.FakeFS.Create(name)
}

func (m *mockFS) Open(path string) (*os.File, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	return m.FakeFS.Open(path)
}

func (m *mockFS) OpenFile(path string, flag int, perm fs.FileMode) (*os.File, error) {
	if m.openFileErr != nil {
		return nil, m.openFileErr
	}
	return m.FakeFS.OpenFile(path, flag, perm)
}

func (m *mockFS) ReadDir(path string) ([]os.DirEntry, error) {
	cleanPath := filepath.Clean(path)
	// Check for injected errors using logical path
	if err, ok := m.readDirErr[cleanPath]; ok {
		return nil, err
	}
	// Check for injected results using logical path
	if entries, ok := m.readDirResult[cleanPath]; ok {
		return entries, nil
	}
	// Also check with on-disk path for backward compatibility
	onDiskPath := m.onDisk(path)
	if err, ok := m.readDirErr[onDiskPath]; ok {
		return nil, err
	}
	if entries, ok := m.readDirResult[onDiskPath]; ok {
		return entries, nil
	}
	return m.FakeFS.ReadDir(path)
}

func (m *mockFS) Symlink(oldname, newname string) error {
	if m.symlinkErr != nil {
		return m.symlinkErr
	}
	return m.FakeFS.Symlink(oldname, newname)
}

func (m *mockFS) Readlink(path string) (string, error) {
	if m.readlinkErr != nil {
		return "", m.readlinkErr
	}
	return m.FakeFS.Readlink(path)
}

func (m *mockFS) Remove(path string) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	return m.FakeFS.Remove(path)
}

// readDirMockFS wraps osFS but allows injecting ReadDir errors/results.
// Useful for tests that need real file operations but want to mock ReadDir.
type readDirMockFS struct {
	osFS
	readDirErr    map[string]error
	readDirResult map[string][]os.DirEntry
}

func newReadDirMockFS() *readDirMockFS {
	return &readDirMockFS{
		readDirErr:    make(map[string]error),
		readDirResult: make(map[string][]os.DirEntry),
	}
}

func (r *readDirMockFS) ReadDir(path string) ([]os.DirEntry, error) {
	cleanPath := filepath.Clean(path)
	if err, ok := r.readDirErr[cleanPath]; ok {
		return nil, err
	}
	if entries, ok := r.readDirResult[cleanPath]; ok {
		return entries, nil
	}
	return r.osFS.ReadDir(path)
}

// fakeDirEntry implements os.DirEntry for testing.
type fakeDirEntry struct {
	name    string
	isDir   bool
	mode    fs.FileMode
	infoErr error
}

func (f *fakeDirEntry) Name() string               { return f.name }
func (f *fakeDirEntry) IsDir() bool                { return f.isDir }
func (f *fakeDirEntry) Type() fs.FileMode          { return f.mode }
func (f *fakeDirEntry) Info() (fs.FileInfo, error) { return nil, f.infoErr }

func TestBackupFileAndDirectory(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root")
	if err := os.MkdirAll(filepath.Join(root, "dir/sub"), 0755); err != nil {
		t.Fatalf("failed to create directories: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir/sub/data.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write nested file: %v", err)
	}
	if err := os.Symlink("dir/sub/data.txt", filepath.Join(root, "link")); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupFile(tw, filepath.Join(root, "file.txt"), "file.txt", result, logger); err != nil {
		t.Fatalf("backupFile failed: %v", err)
	}
	if err := backupDirectory(tw, filepath.Join(root, "dir"), "dir", result, logger); err != nil {
		t.Fatalf("backupDirectory failed: %v", err)
	}
	if err := backupDirectory(tw, filepath.Join(root, "."), ".", result, logger); err != nil {
		t.Fatalf("backupDirectory root failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar writer close failed: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip writer close failed: %v", err)
	}

	reader, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader error: %v", err)
	}
	defer reader.Close()
	tr := tar.NewReader(reader)

	var files []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		files = append(files, hdr.Name)
	}

	expected := []string{"file.txt", "dir/sub/data.txt", "link"}
	for _, name := range expected {
		found := false
		for _, f := range files {
			if f == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %s in archive, got %v", name, files)
		}
	}
	if result.FilesBackedUp == 0 || result.TotalSize == 0 {
		t.Fatalf("unexpected result counters: %+v", result)
	}
}

func TestRestoreSafetyBackup(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	writeFile := func(name, content string, mode int64) {
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header failed: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content failed: %v", err)
		}
	}

	// Directory entry
	if err := tw.WriteHeader(&tar.Header{Name: "etc/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("write dir header failed: %v", err)
	}
	writeFile("etc/config.txt", "hello", 0644)
	if err := tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "etc/config.txt"}); err != nil {
		t.Fatalf("write symlink header failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close failed: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("failed to write archive: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	if err := RestoreSafetyBackup(logger, backupPath, restoreDir); err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "etc/config.txt")); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	linkTarget, err := os.Readlink(filepath.Join(restoreDir, "link"))
	if err != nil || linkTarget != "etc/config.txt" {
		t.Fatalf("symlink not restored correctly: target=%s err=%v", linkTarget, err)
	}
}

func TestRestoreSafetyBackup_AllowsAbsoluteSymlinkWithinDestRoot(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")
	restoreDir := filepath.Join(tmpDir, "restore")
	absLinkTarget := filepath.Join(restoreDir, "etc", "config.txt")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	writeFile := func(name, content string, mode int64) {
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header failed: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content failed: %v", err)
		}
	}

	if err := tw.WriteHeader(&tar.Header{Name: "etc/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("write dir header failed: %v", err)
	}
	writeFile("etc/config.txt", "hello", 0644)
	if err := tw.WriteHeader(&tar.Header{Name: "abs_link", Typeflag: tar.TypeSymlink, Linkname: absLinkTarget}); err != nil {
		t.Fatalf("write absolute symlink header failed: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "abs_escape", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatalf("write escaping symlink header failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close failed: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("failed to write archive: %v", err)
	}

	if err := RestoreSafetyBackup(logger, backupPath, restoreDir); err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	linkTarget, err := os.Readlink(filepath.Join(restoreDir, "abs_link"))
	if err != nil || linkTarget != absLinkTarget {
		t.Fatalf("absolute symlink not restored correctly: target=%s err=%v", linkTarget, err)
	}

	if _, err := os.Lstat(filepath.Join(restoreDir, "abs_escape")); err == nil {
		t.Fatalf("expected escaping absolute symlink to be skipped")
	}
}

func TestRestoreSafetyBackup_DoesNotFollowExistingSymlinkTargetPath(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")
	restoreDir := filepath.Join(tmpDir, "restore")

	unitPath := filepath.Join(restoreDir, "lib", "systemd", "system", "proxmox-backup.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	const unitData = "unit-file-content"
	if err := os.WriteFile(unitPath, []byte(unitData), 0o644); err != nil {
		t.Fatalf("write unit file: %v", err)
	}

	linkPath := filepath.Join(restoreDir, "etc", "systemd", "system", "multi-user.target.wants", "proxmox-backup.service")
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

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	if err := tw.WriteHeader(&tar.Header{Name: "etc/systemd/system/multi-user.target.wants/proxmox-backup.service", Typeflag: tar.TypeSymlink, Linkname: relTarget}); err != nil {
		t.Fatalf("write symlink header failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close failed: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	if err := RestoreSafetyBackup(logger, backupPath, restoreDir); err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
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
}

func TestCleanupOldSafetyBackups(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	// Create unique files directly under /tmp to match the glob pattern
	tag := strings.ReplaceAll(t.Name(), "/", "_")
	oldBackup := filepath.Join("/tmp", "restore_backup_"+tag+"_old")
	newBackup := filepath.Join("/tmp", "restore_backup_"+tag+"_new")
	if err := os.WriteFile(oldBackup, []byte("old"), 0644); err != nil {
		t.Fatalf("failed to create old backup: %v", err)
	}
	if err := os.WriteFile(newBackup, []byte("new"), 0644); err != nil {
		t.Fatalf("failed to create new backup: %v", err)
	}
	oldTime := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(oldBackup, oldTime, oldTime); err != nil {
		t.Fatalf("failed to chtimes old backup: %v", err)
	}

	if err := CleanupOldSafetyBackups(logger, 24*time.Hour); err != nil {
		t.Fatalf("CleanupOldSafetyBackups error: %v", err)
	}

	if _, err := os.Stat(oldBackup); err == nil {
		t.Fatalf("old backup should have been removed")
	}
	if _, err := os.Stat(newBackup); err != nil {
		t.Fatalf("new backup should remain: %v", err)
	}

	// Cleanup
	_ = os.Remove(newBackup)
}

func TestCreateSafetyBackupArchivesSelectedPaths(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	origFS := safetyFS
	safetyFS = fake
	t.Cleanup(func() { safetyFS = origFS })

	fixed := time.Date(2024, time.March, 1, 15, 4, 5, 0, time.UTC)
	origNow := safetyNow
	safetyNow = func() time.Time { return fixed }
	t.Cleanup(func() { safetyNow = origNow })

	destRoot := "/restore-target"
	if err := fake.AddFile(filepath.Join(destRoot, "etc/config.txt"), []byte("config-data")); err != nil {
		t.Fatalf("add config file: %v", err)
	}
	if err := fake.AddDir(filepath.Join(destRoot, "var/lib/app")); err != nil {
		t.Fatalf("add directory: %v", err)
	}
	if err := fake.WriteFile(filepath.Join(destRoot, "var/lib/app/state.txt"), []byte("state"), 0o640); err != nil {
		t.Fatalf("add state file: %v", err)
	}

	categories := []Category{
		{ID: "etc", Paths: []string{"./etc/config.txt"}},
		{ID: "var", Paths: []string{"./var/lib/app"}},
	}

	logger := logging.New(types.LogLevelInfo, false)

	result, err := CreateSafetyBackup(logger, categories, destRoot)
	if err != nil {
		t.Fatalf("CreateSafetyBackup error: %v", err)
	}

	expectedName := "restore_backup_" + fixed.Format("20060102_150405") + ".tar.gz"
	expectedPath := filepath.Join("/tmp", "proxsave", expectedName)
	if result.BackupPath != expectedPath {
		t.Fatalf("unexpected backup path: got %s want %s", result.BackupPath, expectedPath)
	}
	if !result.Timestamp.Equal(fixed) {
		t.Fatalf("timestamp mismatch: got %v want %v", result.Timestamp, fixed)
	}
	if result.FilesBackedUp != 2 {
		t.Fatalf("expected 2 files backed up, got %d", result.FilesBackedUp)
	}
	if result.TotalSize == 0 {
		t.Fatalf("expected non-zero total size")
	}

	archiveFile, err := os.Open(fake.onDisk(result.BackupPath))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer archiveFile.Close()

	gzr, err := gzip.NewReader(archiveFile)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	var entries []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		entries = append(entries, filepath.ToSlash(hdr.Name))
	}

	assertContains := func(name string) {
		for _, entry := range entries {
			if entry == name {
				return
			}
		}
		t.Fatalf("archive missing %s; entries=%v", name, entries)
	}

	assertContains("etc/config.txt")
	assertContains("var/lib/app/state.txt")

	locationData, err := os.ReadFile(fake.onDisk(filepath.Join("/tmp", "proxsave", "restore_backup_location.txt")))
	if err != nil {
		t.Fatalf("location file: %v", err)
	}
	if strings.TrimSpace(string(locationData)) != result.BackupPath {
		t.Fatalf("location file contents mismatch: %q", string(locationData))
	}
}

func TestRestoreSafetyBackup_MaliciousSymlinks(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "malicious.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Create a legitimate file first
	hdr := &tar.Header{Name: "etc/", Mode: 0755, Typeflag: tar.TypeDir}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write dir header: %v", err)
	}

	hdr = &tar.Header{
		Name: "etc/config.txt",
		Mode: 0644,
		Size: 5,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write file header: %v", err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatalf("write content: %v", err)
	}

	// Test case 1: Symlink attempting to escape via ../../../
	maliciousSymlink1 := &tar.Header{
		Name:     "link_escape",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../../../etc/passwd",
	}
	if err := tw.WriteHeader(maliciousSymlink1); err != nil {
		t.Fatalf("write malicious symlink header: %v", err)
	}

	// Test case 2: Symlink with multiple levels of traversal
	maliciousSymlink2 := &tar.Header{
		Name:     "subdir/link_escape2",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../../../../../../tmp/evil",
	}
	if err := tw.WriteHeader(maliciousSymlink2); err != nil {
		t.Fatalf("write malicious symlink2 header: %v", err)
	}

	// Test case 3: Legitimate symlink (should work)
	legitimateSymlink := &tar.Header{
		Name:     "link_good",
		Typeflag: tar.TypeSymlink,
		Linkname: "etc/config.txt",
	}
	if err := tw.WriteHeader(legitimateSymlink); err != nil {
		t.Fatalf("write legitimate symlink header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	if err := RestoreSafetyBackup(logger, backupPath, restoreDir); err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify malicious symlinks were NOT created
	if _, err := os.Lstat(filepath.Join(restoreDir, "link_escape")); err == nil {
		t.Fatalf("malicious symlink 'link_escape' should not have been created")
	}
	if _, err := os.Lstat(filepath.Join(restoreDir, "subdir/link_escape2")); err == nil {
		t.Fatalf("malicious symlink 'link_escape2' should not have been created")
	}

	// Verify legitimate symlink WAS created and points correctly
	linkTarget, err := os.Readlink(filepath.Join(restoreDir, "link_good"))
	if err != nil {
		t.Fatalf("legitimate symlink should exist: %v", err)
	}
	if linkTarget != "etc/config.txt" {
		t.Fatalf("legitimate symlink target = %q, want 'etc/config.txt'", linkTarget)
	}

	// Verify the legitimate symlink resolves within restoreDir
	resolvedPath := filepath.Join(filepath.Dir(filepath.Join(restoreDir, "link_good")), linkTarget)
	absRestoreDir, _ := filepath.Abs(restoreDir)
	absResolved, _ := filepath.Abs(resolvedPath)
	if !strings.HasPrefix(absResolved, absRestoreDir) {
		t.Fatalf("legitimate symlink resolves outside restore dir")
	}
}

func TestResolveAndCheckPathInsideRoot(t *testing.T) {
	root := t.TempDir()
	target, err := resolveAndCheckPath(root, filepath.Join("etc", "pve", "config.db"))
	if err != nil {
		t.Fatalf("resolveAndCheckPath returned error: %v", err)
	}
	if !strings.HasPrefix(target, root) {
		t.Fatalf("resolved path not inside root: %s", target)
	}
	if !strings.HasSuffix(target, filepath.Join("etc", "pve", "config.db")) {
		t.Fatalf("resolved path does not keep relative structure: %s", target)
	}
}

func TestResolveAndCheckPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveAndCheckPath(root, filepath.Join("..", "outside")); err == nil {
		t.Fatalf("expected traversal to be rejected")
	}
}

func TestResolveAndCheckPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "..", "outside-root")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	outsideFile := filepath.Join(outside, "data.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if _, err := resolveAndCheckPath(root, filepath.Join("escape-link", "data.txt")); err == nil {
		t.Fatalf("expected symlink escape to be rejected")
	}
}

// =====================================
// walkFS / walkFSRecursive tests
// =====================================

func TestWalkFS_SkipDir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var visited []string
	err := walkFS(osFS{}, tmpDir, func(path string, info os.FileInfo, err error) error {
		visited = append(visited, path)
		if info != nil && info.IsDir() && path != tmpDir {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walkFS error: %v", err)
	}

	// Should visit root and subdir, but not file inside subdir due to SkipDir
	for _, v := range visited {
		if strings.Contains(v, "file.txt") {
			t.Fatalf("should not have visited file inside skipped dir: %v", visited)
		}
	}
}

func TestWalkFS_CallbackError(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	testErr := errors.New("callback error")
	err := walkFS(osFS{}, tmpDir, func(path string, info os.FileInfo, err error) error {
		if info != nil && !info.IsDir() {
			return testErr
		}
		return nil
	})
	if !errors.Is(err, testErr) {
		t.Fatalf("expected callback error, got: %v", err)
	}
}

func TestWalkFS_StatError(t *testing.T) {
	mock := newMockFS()
	t.Cleanup(func() { _ = os.RemoveAll(mock.Root) })
	nonExistentPath := "/nonexistent/path"

	var callbackCalled bool
	var receivedErr error
	err := walkFS(mock, nonExistentPath, func(path string, info os.FileInfo, err error) error {
		callbackCalled = true
		receivedErr = err
		return err
	})

	if !callbackCalled {
		t.Fatal("callback should have been called with error")
	}
	if receivedErr == nil {
		t.Fatal("callback should have received an error")
	}
	if err == nil {
		t.Fatal("walkFS should return error when stat fails")
	}
}

func TestWalkFS_ReadDirError(t *testing.T) {
	tmpDir := t.TempDir()

	readDirErr := errors.New("permission denied")
	mock := newReadDirMockFS()
	mock.readDirErr[tmpDir] = readDirErr

	var errorCallbackCalled bool
	err := walkFS(mock, tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			errorCallbackCalled = true
			return err
		}
		return nil
	})

	if !errorCallbackCalled {
		t.Fatal("callback should have been called with ReadDir error")
	}
	if err == nil {
		t.Fatal("walkFS should return ReadDir error")
	}
}

func TestWalkFS_ReadDirErrorWithSkipDir(t *testing.T) {
	tmpDir := t.TempDir()

	readDirErr := errors.New("permission denied")
	mock := newReadDirMockFS()
	mock.readDirErr[tmpDir] = readDirErr

	err := walkFS(mock, tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		return nil
	})

	// When callback returns SkipDir for ReadDir error, walkFS still returns the original error
	if err == nil {
		t.Fatal("walkFS should return ReadDir error even when callback returns SkipDir")
	}
}

func TestWalkFS_EntryInfoError(t *testing.T) {
	tmpDir := t.TempDir()

	infoErr := errors.New("info error")
	mock := newReadDirMockFS()
	mock.readDirResult[tmpDir] = []os.DirEntry{
		&fakeDirEntry{name: "broken", isDir: false, infoErr: infoErr},
	}

	var infoErrorReceived bool
	err := walkFS(mock, tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil && info == nil {
			infoErrorReceived = true
		}
		return nil // continue walking
	})

	if err != nil {
		t.Fatalf("walkFS should not fail when callback handles info error: %v", err)
	}
	if !infoErrorReceived {
		t.Fatal("callback should have received info error")
	}
}

func TestWalkFS_EntryInfoErrorPropagation(t *testing.T) {
	tmpDir := t.TempDir()

	infoErr := errors.New("info error")
	mock := newReadDirMockFS()
	mock.readDirResult[tmpDir] = []os.DirEntry{
		&fakeDirEntry{name: "broken", isDir: false, infoErr: infoErr},
	}

	propagatedErr := errors.New("propagated error")
	err := walkFS(mock, tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return propagatedErr
		}
		return nil
	})

	if !errors.Is(err, propagatedErr) {
		t.Fatalf("expected propagated error, got: %v", err)
	}
}

func TestWalkFS_EntryInfoErrorWithSkipDir(t *testing.T) {
	tmpDir := t.TempDir()

	infoErr := errors.New("info error")
	mock := newReadDirMockFS()
	mock.readDirResult[tmpDir] = []os.DirEntry{
		&fakeDirEntry{name: "broken", isDir: false, infoErr: infoErr},
		&fakeDirEntry{name: "ok", isDir: false, infoErr: nil},
	}

	err := walkFS(mock, tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		return nil
	})

	// SkipDir on entry info error should continue to next entry
	if err != nil {
		t.Fatalf("walkFS should continue when callback returns SkipDir for info error: %v", err)
	}
}

func TestWalkFS_RecursiveSkipDir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	subSubDir := filepath.Join(subDir, "nested")
	if err := os.MkdirAll(subSubDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subSubDir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var visited []string
	// Return SkipDir from recursive call for nested directory
	err := walkFS(osFS{}, tmpDir, func(path string, info os.FileInfo, err error) error {
		visited = append(visited, path)
		if strings.Contains(path, "nested") && info != nil && info.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walkFS error: %v", err)
	}

	// Should visit nested dir but not the file inside it
	hasNested := false
	hasNestedFile := false
	for _, v := range visited {
		if strings.HasSuffix(v, "nested") {
			hasNested = true
		}
		if strings.Contains(v, "file.txt") {
			hasNestedFile = true
		}
	}
	if !hasNested {
		t.Fatal("should have visited nested directory")
	}
	if hasNestedFile {
		t.Fatal("should not have visited file in skipped nested directory")
	}
}

func TestWalkFS_NilInfo(t *testing.T) {
	// Test walkFSRecursive with nil info - it should return early
	err := walkFSRecursive(osFS{}, "/some/path", nil, func(path string, info os.FileInfo, err error) error {
		return nil
	})
	if err != nil {
		t.Fatalf("walkFSRecursive with nil info should return nil: %v", err)
	}
}

// =====================================
// RestoreSafetyBackup additional tests
// =====================================

func TestRestoreSafetyBackup_InvalidGzip(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "invalid.tar.gz")

	// Write invalid gzip data
	if err := os.WriteFile(backupPath, []byte("not gzip data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, filepath.Join(tmpDir, "restore"))
	if err == nil {
		t.Fatal("expected error for invalid gzip data")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("expected gzip error, got: %v", err)
	}
}

func TestRestoreSafetyBackup_CorruptedTar(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "corrupted.tar.gz")

	// Create valid gzip with corrupted tar inside
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	// Write some invalid tar data
	if _, err := gzw.Write([]byte("invalid tar content that is not a valid header")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, filepath.Join(tmpDir, "restore"))
	if err == nil {
		t.Fatal("expected error for corrupted tar")
	}
}

func TestRestoreSafetyBackup_OpenError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	err := RestoreSafetyBackup(logger, "/nonexistent/backup.tar.gz", "/restore")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "open backup") {
		t.Fatalf("expected open error, got: %v", err)
	}
}

func TestRestoreSafetyBackup_PathTraversal(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "traversal.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Create entry with path traversal
	hdr := &tar.Header{
		Name: "../../../etc/passwd",
		Mode: 0644,
		Size: 5,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte("evil!")); err != nil {
		t.Fatalf("write content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify path traversal entry was skipped (warning logged)
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") {
		t.Fatal("expected warning about skipping path traversal entry")
	}

	// Verify file was not created outside restore dir
	if _, err := os.Stat(filepath.Join(tmpDir, "etc", "passwd")); err == nil {
		t.Fatal("path traversal entry should not be created outside restore dir")
	}
}

func TestRestoreSafetyBackup_RelativePathEscape(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "escape.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Valid file first
	hdr := &tar.Header{Name: "good.txt", Mode: 0644, Size: 4}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte("good")); err != nil {
		t.Fatalf("write content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify good file was created
	if _, err := os.Stat(filepath.Join(restoreDir, "good.txt")); err != nil {
		t.Fatalf("good file should exist: %v", err)
	}
}

func TestRestoreSafetyBackup_SymlinkToAbsoluteOutsideRoot(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "symlink_abs.tar.gz")
	restoreDir := filepath.Join(tmpDir, "restore")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Symlink pointing to absolute path outside restore dir
	hdr := &tar.Header{
		Name:     "evil_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify symlink was NOT created
	if _, err := os.Lstat(filepath.Join(restoreDir, "evil_link")); err == nil {
		t.Fatal("evil symlink should not have been created")
	}

	// Verify warning was logged
	if !strings.Contains(logBuf.String(), "Skipping symlink") {
		t.Fatal("expected warning about skipping symlink")
	}
}

func TestRestoreSafetyBackup_DirectoryCreationError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Directory entry
	hdr := &tar.Header{
		Name:     "mydir/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	// Create a file where directory should be created to force error
	restoreDir := filepath.Join(tmpDir, "restore")
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	blockingFile := filepath.Join(restoreDir, "mydir")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	// Should not fail, just log warning
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify warning was logged
	if !strings.Contains(logBuf.String(), "Cannot create directory") {
		t.Fatal("expected warning about directory creation error")
	}
}

func TestRestoreSafetyBackup_FileCreationError(t *testing.T) {
	// Skip if running as root (can write to read-only dirs)
	if os.Getuid() == 0 {
		t.Skip("skipping test: running as root")
	}

	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Regular file
	content := "file content"
	hdr := &tar.Header{
		Name: "subdir/file.txt",
		Mode: 0644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("write content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	// Create read-only directory to prevent file creation
	restoreDir := filepath.Join(tmpDir, "restore")
	subDir := filepath.Join(restoreDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Make subdir read-only
	if err := os.Chmod(subDir, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(subDir, 0o755) })

	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	// Should not fail, just log warning
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify warning was logged about file creation
	if !strings.Contains(logBuf.String(), "Cannot create file") {
		t.Fatal("expected warning about file creation error")
	}
}

func TestRestoreSafetyBackup_MkdirAllParentError(t *testing.T) {
	// Skip if running as root
	if os.Getuid() == 0 {
		t.Skip("skipping test: running as root")
	}

	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// File in deep directory
	content := "content"
	hdr := &tar.Header{
		Name: "deep/nested/path/file.txt",
		Mode: 0644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("write content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	// Create a file where parent directory should be to cause mkdir error
	restoreDir := filepath.Join(tmpDir, "restore")
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		t.Fatalf("mkdir restore: %v", err)
	}
	// Create "deep" as a file to block directory creation
	if err := os.WriteFile(filepath.Join(restoreDir, "deep"), []byte("blocker"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Should have logged warning about directory creation
	if !strings.Contains(logBuf.String(), "Cannot create directory") {
		t.Fatal("expected warning about cannot create directory")
	}
}

func TestRestoreSafetyBackup_SymlinkCreationError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")
	restoreDir := filepath.Join(tmpDir, "restore")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Symlink that points within restore dir
	hdr := &tar.Header{
		Name:     "link_to_file",
		Typeflag: tar.TypeSymlink,
		Linkname: "target.txt",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// The symlink should be created (pointing to non-existent target.txt)
	linkPath := filepath.Join(restoreDir, "link_to_file")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("expected target.txt, got %s", target)
	}
}

func TestRestoreSafetyBackup_SymlinkExistsRemoveAndCreate(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.tar.gz")
	restoreDir := filepath.Join(tmpDir, "restore")

	// Pre-create restore dir with existing symlink
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existingLink := filepath.Join(restoreDir, "my_link")
	if err := os.Symlink("old_target", existingLink); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// New symlink with same name but different target
	hdr := &tar.Header{
		Name:     "my_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "new_target",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify symlink was replaced
	target, err := os.Readlink(existingLink)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != "new_target" {
		t.Fatalf("expected new_target, got %s", target)
	}
}

// =====================================
// backupDirectory additional tests
// =====================================

func TestBackupDirectory_WithNestedSymlinks(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root")
	if err := os.MkdirAll(filepath.Join(root, "dir/subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir/subdir/file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// Create nested symlink
	if err := os.Symlink("subdir/file.txt", filepath.Join(root, "dir/link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupDirectory(tw, filepath.Join(root, "dir"), "dir", result, logger); err != nil {
		t.Fatalf("backupDirectory failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Verify symlink was backed up
	gzr, _ := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	tr := tar.NewReader(gzr)

	var hasSymlink bool
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeSymlink && strings.Contains(hdr.Name, "link") {
			hasSymlink = true
			if hdr.Linkname != "subdir/file.txt" {
				t.Fatalf("expected symlink target 'subdir/file.txt', got %s", hdr.Linkname)
			}
		}
	}
	if !hasSymlink {
		t.Fatal("symlink should be in archive")
	}
}

func TestBackupDirectory_EmptyDir(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupDirectory(tw, emptyDir, "empty", result, logger); err != nil {
		t.Fatalf("backupDirectory failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Verify empty directory was backed up as a directory entry
	gzr, _ := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	tr := tar.NewReader(gzr)

	var hasDirEntry bool
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeDir && strings.HasPrefix(hdr.Name, "empty") {
			hasDirEntry = true
		}
	}
	if !hasDirEntry {
		t.Fatal("empty directory should be in archive as dir entry")
	}
}

func TestBackupDirectory_WalkError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	// Backup non-existent directory
	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	err := backupDirectory(tw, "/nonexistent/path", "nonexistent", result, logger)
	// Should return error from walkFS
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}

	tw.Close()
	gzw.Close()
}

// =====================================
// backupFile additional tests
// =====================================

func TestBackupFile_OpenError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	err := backupFile(tw, "/nonexistent/file.txt", "file.txt", result, logger)
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}

	tw.Close()
	gzw.Close()
}

func TestBackupFile_LargeFile(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	largePath := filepath.Join(tmpDir, "large.bin")
	// Create a 1MB file
	largeContent := make([]byte, 1024*1024)
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}
	if err := os.WriteFile(largePath, largeContent, 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupFile(tw, largePath, "large.bin", result, logger); err != nil {
		t.Fatalf("backupFile failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	if result.FilesBackedUp != 1 {
		t.Fatalf("expected 1 file backed up, got %d", result.FilesBackedUp)
	}
	if result.TotalSize != int64(len(largeContent)) {
		t.Fatalf("expected size %d, got %d", len(largeContent), result.TotalSize)
	}
}

func TestBackupFile_SpecialModes(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	// Create file with special permissions
	execFile := filepath.Join(tmpDir, "script.sh")
	if err := os.WriteFile(execFile, []byte("#!/bin/bash\necho hello"), 0o755); err != nil {
		t.Fatalf("write exec file: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupFile(tw, execFile, "script.sh", result, logger); err != nil {
		t.Fatalf("backupFile failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Verify permissions are preserved in archive
	gzr, _ := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	tr := tar.NewReader(gzr)

	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("read tar header: %v", err)
	}
	// Check that mode includes execute bits
	if hdr.Mode&0111 == 0 {
		t.Fatal("expected execute permission bits to be preserved")
	}
}

// =====================================
// CreateSafetyBackup additional tests
// =====================================

func TestCreateSafetyBackup_NonExistentPaths(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	origFS := safetyFS
	safetyFS = fake
	t.Cleanup(func() { safetyFS = origFS })

	fixed := time.Date(2024, time.March, 1, 15, 4, 5, 0, time.UTC)
	origNow := safetyNow
	safetyNow = func() time.Time { return fixed }
	t.Cleanup(func() { safetyNow = origNow })

	destRoot := "/restore-target"
	// Don't create any files - all paths will be non-existent

	categories := []Category{
		{ID: "etc", Paths: []string{"./etc/nonexistent.txt"}},
		{ID: "var", Paths: []string{"./var/lib/missing"}},
	}

	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	result, err := CreateSafetyBackup(logger, categories, destRoot)
	if err != nil {
		t.Fatalf("CreateSafetyBackup error: %v", err)
	}

	// Should succeed but with 0 files backed up
	if result.FilesBackedUp != 0 {
		t.Fatalf("expected 0 files backed up for non-existent paths, got %d", result.FilesBackedUp)
	}
}

func TestCreateSafetyBackup_MixedExistentNonExistent(t *testing.T) {
	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	origFS := safetyFS
	safetyFS = fake
	t.Cleanup(func() { safetyFS = origFS })

	fixed := time.Date(2024, time.March, 1, 15, 4, 5, 0, time.UTC)
	origNow := safetyNow
	safetyNow = func() time.Time { return fixed }
	t.Cleanup(func() { safetyNow = origNow })

	destRoot := "/restore-target"
	// Create only one file
	if err := fake.AddFile(filepath.Join(destRoot, "etc/exists.txt"), []byte("data")); err != nil {
		t.Fatalf("add file: %v", err)
	}

	categories := []Category{
		{ID: "etc", Paths: []string{"./etc/exists.txt", "./etc/missing.txt"}},
	}

	logger := logging.New(types.LogLevelInfo, false)

	result, err := CreateSafetyBackup(logger, categories, destRoot)
	if err != nil {
		t.Fatalf("CreateSafetyBackup error: %v", err)
	}

	// Should succeed with 1 file backed up (the existing one)
	if result.FilesBackedUp != 1 {
		t.Fatalf("expected 1 file backed up, got %d", result.FilesBackedUp)
	}
}

func TestCreateSafetyBackup_StatError(t *testing.T) {
	mock := newMockFS()
	t.Cleanup(func() { _ = os.RemoveAll(mock.Root) })
	origFS := safetyFS
	safetyFS = mock
	t.Cleanup(func() { safetyFS = origFS })

	fixed := time.Date(2024, time.March, 1, 15, 4, 5, 0, time.UTC)
	origNow := safetyNow
	safetyNow = func() time.Time { return fixed }
	t.Cleanup(func() { safetyNow = origNow })

	destRoot := "/restore-target"
	testFile := filepath.Join(destRoot, "etc/config.txt")
	if err := mock.AddFile(testFile, []byte("config")); err != nil {
		t.Fatalf("add file: %v", err)
	}

	// Inject stat error using the logical path (not the on-disk path)
	// FakeFS.Stat checks StatErrors using filepath.Clean(path) which is the logical path
	mock.StatErrors[filepath.Clean(testFile)] = errors.New("permission denied")

	categories := []Category{
		{ID: "etc", Paths: []string{"./etc/config.txt"}},
	}

	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	result, err := CreateSafetyBackup(logger, categories, destRoot)
	if err != nil {
		t.Fatalf("CreateSafetyBackup error: %v", err)
	}

	// Should succeed but skip the errored file
	if result.FilesBackedUp != 0 {
		t.Fatalf("expected 0 files backed up when stat errors, got %d", result.FilesBackedUp)
	}
	// Should have logged a warning
	if !strings.Contains(logBuf.String(), "Cannot stat") {
		t.Fatal("expected warning about stat error")
	}
}

// =====================================
// Additional edge case tests
// =====================================

func TestBackupDirectory_DeepNesting(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	// Create deeply nested structure
	deepPath := filepath.Join(tmpDir, "a/b/c/d/e")
	if err := os.MkdirAll(deepPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepPath, "deep.txt"), []byte("deep content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupDirectory(tw, filepath.Join(tmpDir, "a"), "a", result, logger); err != nil {
		t.Fatalf("backupDirectory failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Verify file was backed up
	if result.FilesBackedUp != 1 {
		t.Fatalf("expected 1 file backed up, got %d", result.FilesBackedUp)
	}
}

func TestRestoreSafetyBackup_EmptyArchive(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "empty.tar.gz")

	// Create empty but valid tar.gz
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed for empty archive: %v", err)
	}

	// Should succeed with 0 files restored
	if !strings.Contains(logBuf.String(), "0 files") {
		t.Fatal("expected 0 files restored for empty archive")
	}
}

func TestRestoreSafetyBackup_MultipleFiles(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "multi.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Add multiple files
	for i := 0; i < 5; i++ {
		content := fmt.Sprintf("content-%d", i)
		hdr := &tar.Header{
			Name: fmt.Sprintf("file%d.txt", i),
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify all files were restored
	for i := 0; i < 5; i++ {
		filePath := filepath.Join(restoreDir, fmt.Sprintf("file%d.txt", i))
		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read file%d.txt: %v", i, err)
		}
		expected := fmt.Sprintf("content-%d", i)
		if string(data) != expected {
			t.Fatalf("file%d.txt content mismatch: got %q, want %q", i, string(data), expected)
		}
	}
}

func TestResolveAndCheckPath_AbsolutePathInput(t *testing.T) {
	root := t.TempDir()
	absPath := filepath.Join(root, "etc", "config.txt")

	// Create the target path
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	resolved, err := resolveAndCheckPath(root, absPath)
	if err != nil {
		t.Fatalf("resolveAndCheckPath failed: %v", err)
	}

	if !strings.HasPrefix(resolved, root) {
		t.Fatalf("resolved path should be within root: %s", resolved)
	}
}

func TestRestoreSafetyBackup_ComplexStructure(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "complex.tar.gz")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	// Add nested directories
	dirs := []string{"etc/", "etc/app/", "var/", "var/lib/", "var/lib/app/"}
	for _, dir := range dirs {
		if err := tw.WriteHeader(&tar.Header{Name: dir, Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatalf("write dir header: %v", err)
		}
	}

	// Add files in various directories
	files := map[string]string{
		"etc/config.conf":    "config content",
		"etc/app/app.conf":   "app config",
		"var/lib/app/data":   "app data",
		"root.txt":           "root file",
	}
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write file header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}

	// Add a symlink
	if err := tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "root.txt"}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(backupPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "restore")
	err := RestoreSafetyBackup(logger, backupPath, restoreDir)
	if err != nil {
		t.Fatalf("RestoreSafetyBackup failed: %v", err)
	}

	// Verify structure
	for _, dir := range dirs {
		dirPath := filepath.Join(restoreDir, strings.TrimSuffix(dir, "/"))
		if info, err := os.Stat(dirPath); err != nil || !info.IsDir() {
			t.Fatalf("directory %s should exist", dir)
		}
	}

	for name, expectedContent := range files {
		filePath := filepath.Join(restoreDir, name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != expectedContent {
			t.Fatalf("%s content mismatch", name)
		}
	}

	// Verify symlink
	linkTarget, err := os.Readlink(filepath.Join(restoreDir, "link"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if linkTarget != "root.txt" {
		t.Fatalf("symlink target mismatch: %s", linkTarget)
	}
}

func TestBackupDirectory_WithMixedContent(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var logBuf bytes.Buffer
	logger.SetOutput(&logBuf)

	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "root")

	// Create mixed content: dirs, files, symlinks
	if err := os.MkdirAll(filepath.Join(root, "subdir1/nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "subdir2"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Regular files
	if err := os.WriteFile(filepath.Join(root, "file1.txt"), []byte("file1"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "subdir1/file2.txt"), []byte("file2"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "subdir1/nested/file3.txt"), []byte("file3"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Symlinks
	if err := os.Symlink("file1.txt", filepath.Join(root, "link1")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := os.Symlink("../file1.txt", filepath.Join(root, "subdir1/link2")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	var archive bytes.Buffer
	gzw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gzw)

	result := &SafetyBackupResult{}
	if err := backupDirectory(tw, root, "root", result, logger); err != nil {
		t.Fatalf("backupDirectory failed: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Verify counts
	if result.FilesBackedUp < 3 {
		t.Fatalf("expected at least 3 files backed up, got %d", result.FilesBackedUp)
	}

	// Verify archive content
	gzr, _ := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	tr := tar.NewReader(gzr)

	var entries []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		entries = append(entries, hdr.Name)
	}

	expectedPaths := []string{"root/file1.txt", "root/subdir1/file2.txt", "root/link1"}
	for _, expected := range expectedPaths {
		found := false
		for _, entry := range entries {
			if strings.Contains(entry, filepath.Base(expected)) {
				found = true
				break
			}
		}
		if !found {
			t.Logf("archive entries: %v", entries)
			t.Fatalf("expected to find entry containing %s", expected)
		}
	}
}
