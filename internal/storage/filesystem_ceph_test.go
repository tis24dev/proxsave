package storage

import (
	"context"
	"testing"
)

// CephFS had no case: the kernel client fell to unknown and ceph-fuse to FUSE, both
// without ownership, so SET_BACKUP_PERMISSIONS was never applied there. Both are now
// "ceph", a network filesystem whose ownership the write probe decides (root_squash
// is a client cap away).
func TestCephFSIsANetworkFilesystemTheProbeDecides(t *testing.T) {
	for _, s := range []string{"ceph", "fuse.ceph-fuse", "fuse.ceph"} {
		if got := parseFilesystemType(s); got != FilesystemCeph {
			t.Fatalf("parseFilesystemType(%q) = %q, want ceph", s, got)
		}
	}
	if !FilesystemCeph.IsNetworkFilesystem() || FilesystemCeph.ShouldAutoExclude() {
		t.Fatalf("ceph: network=%v autoExclude=%v, want true false", FilesystemCeph.IsNetworkFilesystem(), FilesystemCeph.ShouldAutoExclude())
	}
	for _, probe := range []bool{true, false} {
		d := NewFilesystemDetector(newTestLogger())
		d.mountPointLookup = func(string) (string, error) { return "/mnt/pve/cephfs", nil }
		d.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
			return FilesystemCeph, "10.0.0.1:/", nil
		}
		d.ownershipSupportTest = func(context.Context, string) bool { return probe }
		info, err := d.DetectFilesystem(context.Background(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if info.SupportsOwnership != probe {
			t.Fatalf("probe %v: SupportsOwnership = %v", probe, info.SupportsOwnership)
		}
	}
}
