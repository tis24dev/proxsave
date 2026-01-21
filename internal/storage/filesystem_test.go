package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFilesystemDetectorTestOwnershipSupportRejectsNonDirectory(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())

	tmp := t.TempDir()
	file := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if detector.testOwnershipSupport(context.Background(), file) {
		t.Fatalf("expected ownership support test to fail for non-directory path")
	}
}

func TestFilesystemDetectorTestOwnershipSupportSucceedsInTempDir(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	if ok := detector.testOwnershipSupport(context.Background(), t.TempDir()); !ok {
		t.Fatalf("expected ownership support test to succeed in temp dir")
	}
}

func TestParseFilesystemType_CoversKnownAndUnknownTypes(t *testing.T) {
	cases := []struct {
		in   string
		want FilesystemType
	}{
		{"ext4", FilesystemExt4},
		{"EXT3", FilesystemExt3},
		{"ext2", FilesystemExt2},
		{"xfs", FilesystemXFS},
		{"btrfs", FilesystemBtrfs},
		{"zfs", FilesystemZFS},
		{"jfs", FilesystemJFS},
		{"reiserfs", FilesystemReiserFS},
		{"overlay", FilesystemOverlay},
		{"tmpfs", FilesystemTmpfs},
		{"vfat", FilesystemFAT32},
		{"fat32", FilesystemFAT32},
		{"fat", FilesystemFAT},
		{"fat16", FilesystemFAT},
		{"exfat", FilesystemExFAT},
		{"ntfs", FilesystemNTFS},
		{"ntfs-3g", FilesystemNTFS},
		{"fuse", FilesystemFUSE},
		{"fuse.sshfs", FilesystemFUSE},
		{"nfs", FilesystemNFS},
		{"nfs4", FilesystemNFS4},
		{"cifs", FilesystemCIFS},
		{"smb", FilesystemCIFS},
		{"smbfs", FilesystemCIFS},
		{"definitely-unknown", FilesystemUnknown},
	}

	for _, tc := range cases {
		if got := parseFilesystemType(tc.in); got != tc.want {
			t.Fatalf("parseFilesystemType(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnescapeOctal(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`/mnt/with\\040space`, `/mnt/with\ space`}, // first backslash literal, second escapes octal
		{`/mnt/with\040space`, "/mnt/with space"},
		{`/mnt/with\011tab`, "/mnt/with\ttab"},
		{`/mnt/with\012nl`, "/mnt/with\nnl"},
		{`/mnt/invalid\0xx`, `/mnt/invalid\0xx`},
		{`/mnt/trailing\04`, `/mnt/trailing\04`}, // too short to parse
	}
	for _, tc := range cases {
		if got := unescapeOctal(tc.in); got != tc.want {
			t.Fatalf("unescapeOctal(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestFilesystemDetectorDetectFilesystem_ErrorsOnMissingPath(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	_, err := detector.DetectFilesystem(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "path does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFilesystemDetectorDetectFilesystem_SucceedsForTempDir(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	dir := t.TempDir()
	info, err := detector.DetectFilesystem(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectFilesystem error: %v", err)
	}
	if info == nil {
		t.Fatalf("expected FilesystemInfo")
	}
	if info.Path != dir {
		t.Fatalf("Path=%q want %q", info.Path, dir)
	}
	if info.MountPoint == "" {
		t.Fatalf("expected non-empty MountPoint")
	}
	if info.Device == "" {
		t.Fatalf("expected non-empty Device")
	}
	if info.SupportsOwnership != info.Type.SupportsUnixOwnership() && !info.Type.IsNetworkFilesystem() {
		t.Fatalf("SupportsOwnership=%v does not match SupportsUnixOwnership=%v for type=%q", info.SupportsOwnership, info.Type.SupportsUnixOwnership(), info.Type)
	}
}

func TestFilesystemDetectorGetMountPoint_PicksProcForProcPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this test depends on /proc mounts")
	}
	detector := NewFilesystemDetector(newTestLogger())
	mp, err := detector.getMountPoint("/proc/self")
	if err != nil {
		t.Fatalf("getMountPoint error: %v", err)
	}
	if mp != "/proc" {
		t.Fatalf("mountPoint=%q want %q", mp, "/proc")
	}
}

func TestFilesystemDetectorGetFilesystemType_ReturnsUnknownForProc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this test depends on /proc mounts and statfs")
	}
	detector := NewFilesystemDetector(newTestLogger())
	fsType, device, err := detector.getFilesystemType(context.Background(), "/proc")
	if err != nil {
		t.Fatalf("getFilesystemType error: %v", err)
	}
	if device == "" {
		t.Fatalf("expected non-empty device")
	}
	if fsType != FilesystemUnknown {
		t.Fatalf("fsType=%q want %q", fsType, FilesystemUnknown)
	}
}

func TestFilesystemDetectorGetFilesystemType_ErrorsWhenMountPointMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this test depends on statfs")
	}
	detector := NewFilesystemDetector(newTestLogger())
	_, _, err := detector.getFilesystemType(context.Background(), "/this/does/not/exist")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestFilesystemDetectorGetFilesystemType_ErrorsWhenMountPointNotInProcMounts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this test depends on /proc mounts and statfs")
	}
	detector := NewFilesystemDetector(newTestLogger())
	_, _, err := detector.getFilesystemType(context.Background(), "/proc/")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "filesystem type not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFilesystemDetectorDetectFilesystem_UsesInjectedHooksAndCoversNetworkAndAutoExclude(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	dir := t.TempDir()

	detector.mountPointLookup = func(path string) (string, error) {
		if path != dir {
			t.Fatalf("unexpected path: %q", path)
		}
		return "/mnt", nil
	}
	detector.filesystemTypeLookup = func(ctx context.Context, mountPoint string) (FilesystemType, string, error) {
		if mountPoint != "/mnt" {
			t.Fatalf("unexpected mountPoint: %q", mountPoint)
		}
		// Network filesystem triggers ownership runtime check.
		return FilesystemNFS, "server:/export", nil
	}

	// Cover both branches inside the network ownership check.
	detector.ownershipSupportTest = func(ctx context.Context, path string) bool { return true }
	info, err := detector.DetectFilesystem(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectFilesystem error: %v", err)
	}
	if !info.IsNetworkFS || info.Type != FilesystemNFS || !info.SupportsOwnership {
		t.Fatalf("unexpected info: %+v", info)
	}

	detector.ownershipSupportTest = func(ctx context.Context, path string) bool { return false }
	info, err = detector.DetectFilesystem(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectFilesystem error: %v", err)
	}
	if !info.IsNetworkFS || info.Type != FilesystemNFS || info.SupportsOwnership {
		t.Fatalf("unexpected info: %+v", info)
	}

	// Cover auto-exclude branch (no network check needed).
	detector.filesystemTypeLookup = func(ctx context.Context, mountPoint string) (FilesystemType, string, error) {
		return FilesystemFAT32, "/dev/sda1", nil
	}
	detector.ownershipSupportTest = nil
	info, err = detector.DetectFilesystem(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectFilesystem error: %v", err)
	}
	if info.Type != FilesystemFAT32 {
		t.Fatalf("Type=%q want %q", info.Type, FilesystemFAT32)
	}
}

func TestFilesystemDetectorDetectFilesystem_PropagatesHookErrors(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	dir := t.TempDir()

	detector.mountPointLookup = func(path string) (string, error) {
		return "", errors.New("mountpoint boom")
	}
	_, err := detector.DetectFilesystem(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "failed to get mount point") {
		t.Fatalf("err=%v; want mount point error", err)
	}

	detector.mountPointLookup = func(path string) (string, error) { return "/mnt", nil }
	detector.filesystemTypeLookup = func(ctx context.Context, mountPoint string) (FilesystemType, string, error) {
		return FilesystemUnknown, "", errors.New("fstype boom")
	}
	_, err = detector.DetectFilesystem(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "failed to detect filesystem type") {
		t.Fatalf("err=%v; want filesystem type error", err)
	}
}

func TestFilesystemDetectorSetPermissions_SkipsWhenOwnershipUnsupported(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	info := &FilesystemInfo{Type: FilesystemCIFS, SupportsOwnership: false}

	// Should no-op even if path doesn't exist.
	if err := detector.SetPermissions(context.Background(), "/no/such/path", 0, 0, 0o600, info); err != nil {
		t.Fatalf("SetPermissions error: %v", err)
	}
}

func TestFilesystemDetectorSetPermissions_ReturnsErrorWhenChmodFails(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	info := &FilesystemInfo{Type: FilesystemExt4, SupportsOwnership: true}

	err := detector.SetPermissions(context.Background(), filepath.Join(t.TempDir(), "missing"), 0, 0, 0o600, info)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFilesystemDetectorSetPermissions_SucceedsForExistingFile(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	uid := os.Getuid()
	gid := os.Getgid()
	if err := detector.SetPermissions(context.Background(), path, uid, gid, 0o600, nil); err != nil {
		t.Fatalf("SetPermissions error: %v", err)
	}
}

func TestFilesystemDetectorTestOwnershipSupport_FailsWhenDirNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to non-writable dirs; skip for determinism")
	}
	detector := NewFilesystemDetector(newTestLogger())

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if detector.testOwnershipSupport(context.Background(), dir) {
		t.Fatalf("expected ownership support test to fail when directory is not writable")
	}
}
