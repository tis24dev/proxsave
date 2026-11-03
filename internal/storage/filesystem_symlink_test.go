package storage

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// A BACKUP_PATH reached through a symlink got the type of the filesystem holding the
// link: the mount was looked up by matching the path string against /proc/mounts. On
// a link into an NFS root_squash share that read ext4 with ownership, and the security
// check stopped every run with exit 14. The mount is now found by device, so the link
// gets its target's filesystem, and the target is recorded for the Filesystem line.
func TestDetectFilesystemFollowsASymlinkToAnotherFilesystem(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("depends on /proc/self/mountinfo")
	}
	linkDir := t.TempDir()
	var target string
	for _, base := range []string{"/dev/shm", "/run", "/tmp"} {
		dir, err := os.MkdirTemp(base, "proxsave-fs-link-")
		if err != nil {
			continue
		}
		if deviceOf(t, dir) != deviceOf(t, linkDir) {
			target = dir
			break
		}
		_ = os.Remove(dir)
	}
	if target == "" {
		t.Skip("no writable directory on a filesystem other than the test's temp dir")
	}
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	link := filepath.Join(linkDir, "backup")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	detector := NewFilesystemDetector(newTestLogger())
	direct, err := detector.DetectFilesystem(context.Background(), target)
	if err != nil {
		t.Fatalf("DetectFilesystem(%s): %v", target, err)
	}
	viaLink, err := detector.DetectFilesystem(context.Background(), link)
	if err != nil {
		t.Fatalf("DetectFilesystem(%s): %v", link, err)
	}
	if viaLink.Type != direct.Type || viaLink.MountPoint != direct.MountPoint || viaLink.Device != direct.Device {
		t.Fatalf("through the link: type=%s mount=%s device=%s; the target is type=%s mount=%s device=%s",
			viaLink.Type, viaLink.MountPoint, viaLink.Device, direct.Type, direct.MountPoint, direct.Device)
	}
	want, _ := filepath.EvalSymlinks(target)
	if viaLink.LinkTarget != want || direct.LinkTarget != "" {
		t.Fatalf("LinkTarget: via link %q (want %q), direct %q (want empty)", viaLink.LinkTarget, want, direct.LinkTarget)
	}
}

func deviceOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(fi.Sys().(*syscall.Stat_t).Dev)
}

func TestMountPointForDevicePicksTheMountHoldingTheResolvedPath(t *testing.T) {
	lines := []string{
		"22 1 0:21 / /proc rw,nosuid - proc proc rw",
		"25 1 253:1 / / rw,relatime - ext4 /dev/mapper/pve-root rw",
		"60 25 0:53 / /mnt/pve/nas rw,relatime - nfs4 10.0.0.5:/export rw",
		"61 25 0:53 /proxsave /var/lib/lxc/101/rootfs/mnt/backup rw - nfs4 10.0.0.5:/export rw",
		"62 25 0:60 / /mnt/space\\040dir rw - tmpfs tmpfs rw",
	}
	for _, tc := range []struct {
		dev, resolved, want string
		found               bool
	}{
		{"0:53", "/mnt/pve/nas/proxsave", "/mnt/pve/nas", true},
		{"0:53", "/var/lib/lxc/101/rootfs/mnt/backup/x", "/var/lib/lxc/101/rootfs/mnt/backup", true},
		{"0:53", "/elsewhere", "/var/lib/lxc/101/rootfs/mnt/backup", true},
		{"0:60", "/mnt/space dir/proxsave", "/mnt/space dir", true},
		{"253:1", "/var/lib/proxsave/backup", "/", true},
		{"0:99", "/var/lib/proxsave/backup", "", false},
	} {
		got, found := mountPointForDevice(lines, tc.dev, tc.resolved)
		if got != tc.want || found != tc.found {
			t.Errorf("mountPointForDevice(%s, %s) = %q, %v; want %q, %v", tc.dev, tc.resolved, got, found, tc.want, tc.found)
		}
	}
	if got := deviceID(uint64(253<<8 | 1)); got != "253:1" {
		t.Errorf("deviceID = %q, want 253:1", got)
	}
}
