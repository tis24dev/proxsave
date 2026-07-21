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

// TestReadSystemFileFollowingSymlinksNoPrefix pins the real-root contract: a plain
// regular file reads back byte-for-byte. With no prefix the read is confined to
// root "/" via os.Root (structural G304 remedy), which resolves to the same file a
// plain os.ReadFile would.
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

// TestReadSystemFileFollowingSymlinksNoPrefixFollowsSymlink proves the real-root
// read still follows a symlink to its target (the os.Root confinement must not
// break symlink resolution the way a bare os.OpenRoot(dir).Open would refuse an
// escaping final component). The link and target sit in the same directory.
func TestReadSystemFileFollowingSymlinksNoPrefixFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("linked-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatal(err)
	}
	c := &Collector{config: &CollectorConfig{}}
	got, err := c.readSystemFileFollowingSymlinks(link)
	if err != nil {
		t.Fatalf("readSystemFileFollowingSymlinks: %v", err)
	}
	if string(got) != "linked-data" {
		t.Fatalf("got %q want linked-data", got)
	}
}
