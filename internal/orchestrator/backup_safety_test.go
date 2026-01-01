package orchestrator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

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
