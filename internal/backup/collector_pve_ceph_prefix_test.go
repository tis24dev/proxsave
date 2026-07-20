package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCephHasClusterConfigFollowsAbsoluteSymlinkUnderPrefix is the issue #255
// gap #3 regression. On a Proxmox host /etc/ceph/ceph.conf is an absolute symlink
// to /etc/pve/ceph.conf. Under a SYSTEM_ROOT_PREFIX the old os.ReadFile followed
// the absolute target against the container root (ENOENT) and the whole Ceph
// category was silently skipped. The prefix-aware read must re-anchor the target
// under the prefix and find the config.
func TestCephHasClusterConfigFollowsAbsoluteSymlinkUnderPrefix(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/pve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc/ceph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/pve/ceph.conf"), []byte("[global]\nfsid = 1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/pve/ceph.conf", filepath.Join(root, "etc/ceph/ceph.conf")); err != nil {
		t.Fatal(err)
	}

	c := &Collector{config: &CollectorConfig{SystemRootPrefix: root}}

	// systemPath("/etc/ceph") is what cephConfigPaths feeds cephHasClusterConfig.
	if !c.cephHasClusterConfig(c.systemPath("/etc/ceph")) {
		t.Fatal("cephHasClusterConfig should resolve the absolute symlink under the prefix and find the config")
	}
}

// TestReadSystemFileFollowingSymlinksNoPrefix keeps the real-root path a plain
// os.ReadFile (backward compatibility).
func TestReadSystemFileFollowingSymlinksNoPrefix(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Collector{config: &CollectorConfig{}}
	got, err := c.readSystemFileFollowingSymlinks(p)
	if err != nil {
		t.Fatalf("readSystemFileFollowingSymlinks: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("got %q want data", got)
	}
}
