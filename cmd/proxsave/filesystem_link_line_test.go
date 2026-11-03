package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/storage"
)

// A storage path reached through a symlink names the link's target on its
// Filesystem line, after the mount.
func TestFilesystemLineNamesTheLinkTarget(t *testing.T) {
	info := &storage.FilesystemInfo{
		Type:        storage.FilesystemType("nfs4"),
		IsNetworkFS: true,
		MountPoint:  "/mnt/s6nfs",
		LinkTarget:  "/mnt/s6nfs/proxsave",
	}
	want := "nfs4 (no ownership) [network] [mount: /mnt/s6nfs] [link to: /mnt/s6nfs/proxsave]"
	if got := formatFilesystemDetail(info); got != want {
		t.Fatalf("formatFilesystemDetail = %q, want %q", got, want)
	}
	info.LinkTarget = ""
	if got := formatFilesystemDetail(info); got != "nfs4 (no ownership) [network] [mount: /mnt/s6nfs]" {
		t.Fatalf("without a link: %q", got)
	}
}
