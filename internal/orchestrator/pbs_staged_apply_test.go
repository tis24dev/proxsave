package orchestrator

import (
	"context"
	"os"
	"strings"
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

func TestApplyPBSDatastoreCfgFromStage_RecoversFromInventoryWhenFlattened(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"

	// This is a representative "flattened" datastore.cfg produced by an unsafe prefilter
	// (headers separated from their respective properties).
	staged := strings.Join([]string{
		"comment Local ext4 disk datastore",
		"comment Synology NFS sync target",
		"datastore: Data1",
		"datastore: Synology-Archive",
		"gc-schedule 05:00",
		"gc-schedule 06:30",
		"notification-mode notification-system",
		"notification-mode notification-system",
		"path /mnt/Synology_NFS/PBS_Backup",
		"path /mnt/datastore/Data1",
		"",
	}, "\n")
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/datastore.cfg", []byte(staged), 0o640); err != nil {
		t.Fatalf("write staged datastore.cfg: %v", err)
	}

	// Inventory contains a verbatim snapshot of the original datastore.cfg, which should be preferred.
	inventory := `{"files":{"pbs_datastore_cfg":{"content":"datastore: Synology-Archive\n    comment Synology NFS sync target\n    gc-schedule 05:00\n    notification-mode notification-system\n    path /mnt/Synology_NFS/PBS_Backup\n\ndatastore: Data1\n    comment Local ext4 disk datastore\n    gc-schedule 06:30\n    notification-mode notification-system\n    path /mnt/datastore/Data1\n"}}}`
	if err := fakeFS.WriteFile(stageRoot+"/var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json", []byte(inventory), 0o640); err != nil {
		t.Fatalf("write inventory: %v", err)
	}

	if err := applyPBSDatastoreCfgFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSDatastoreCfgFromStage error: %v", err)
	}

	out, err := fakeFS.ReadFile("/etc/proxmox-backup/datastore.cfg")
	if err != nil {
		t.Fatalf("read restored datastore.cfg: %v", err)
	}

	blocks, err := parsePBSDatastoreCfgBlocks(string(out))
	if err != nil {
		t.Fatalf("parse restored datastore.cfg: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 datastore blocks, got %d", len(blocks))
	}
	if reason := detectPBSDatastoreCfgDuplicateKeys(blocks); reason != "" {
		t.Fatalf("restored datastore.cfg still has duplicate keys: %s", reason)
	}

	// Verify the expected datastore paths are preserved.
	paths := map[string]string{}
	for _, b := range blocks {
		paths[b.Name] = b.Path
	}
	if paths["Synology-Archive"] != "/mnt/Synology_NFS/PBS_Backup" {
		t.Fatalf("Synology-Archive path=%q", paths["Synology-Archive"])
	}
	if paths["Data1"] != "/mnt/datastore/Data1" {
		t.Fatalf("Data1 path=%q", paths["Data1"])
	}
}
