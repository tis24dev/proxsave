package orchestrator

import (
	"context"
	"os"
	"testing"
)

func TestApplyPBSRemoteCfgFromStage_WritesRemoteCfg(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	remoteCfg := "remote: pbs1\n  host 10.0.0.10\n  authid root@pam\n  password secret\n"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte(remoteCfg), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}

	if err := applyPBSRemoteCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSRemoteCfgFromStage error: %v", err)
	}

	if got, err := fakeFS.ReadFile("/etc/proxmox-backup/remote.cfg"); err != nil {
		t.Fatalf("expected restored remote.cfg: %v", err)
	} else if len(got) == 0 {
		t.Fatalf("expected non-empty remote.cfg")
	}
	if info, err := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); err != nil {
		t.Fatalf("stat remote.cfg: %v", err)
	} else if info.Mode().Perm() != 0o640 {
		t.Fatalf("remote.cfg mode=%#o want %#o", info.Mode().Perm(), 0o640)
	}
}

func TestApplyPBSRemoteCfgFromStage_RemovesWhenEmpty(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.WriteFile("/etc/proxmox-backup/remote.cfg", []byte("remote: old\n  host 1.2.3.4\n"), 0o640); err != nil {
		t.Fatalf("write existing remote.cfg: %v", err)
	}

	stageRoot := "/stage"
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/remote.cfg", []byte("   \n"), 0o640); err != nil {
		t.Fatalf("write staged remote.cfg: %v", err)
	}

	if err := applyPBSRemoteCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSRemoteCfgFromStage error: %v", err)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); err == nil {
		t.Fatalf("expected remote.cfg removed")
	}
}

func TestShouldApplyPBSDatastoreBlock_AllowsMountLikePathsOnRootFS(t *testing.T) {
	t.Parallel()

	dir, err := os.MkdirTemp("/mnt", "proxsave-test-ds-")
	if err != nil {
		t.Skipf("cannot create temp dir under /mnt: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	block := pbsDatastoreBlock{Name: "ds", Path: dir}
	ok, reason := shouldApplyPBSDatastoreBlock(block, newTestLogger())
	if !ok {
		t.Fatalf("expected datastore block to be applied, got ok=false reason=%q", reason)
	}
}
