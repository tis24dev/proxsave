package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectPBSDatastoreInventoryOfflineFromDatastoreCfg(t *testing.T) {
	root := t.TempDir()

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root

	pbsCfgDir := filepath.Join(root, "etc", "proxmox-backup")
	if err := os.MkdirAll(pbsCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir pbs cfg dir: %v", err)
	}

	datastoreCfg := `datastore: Data1
	path /mnt/datastore/Data1
	comment local

datastore: Synology-Archive
	path /mnt/Synology_NFS/PBS_Backup
`
	if err := os.WriteFile(filepath.Join(pbsCfgDir, "datastore.cfg"), []byte(datastoreCfg), 0o640); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "fstab"), []byte("UUID=1 / ext4 defaults 0 1\n//server/share /mnt/cifs cifs credentials=/etc/cifs-creds 0 0\nsshfs#example:/ /mnt/ssh fuse.sshfs defaults,_netdev,IdentityFile=/root/.ssh/id_rsa 0 0\n"), 0o644); err != nil {
		t.Fatalf("write fstab: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "crypttab"), []byte("crypt1 UUID=deadbeef /etc/keys/crypt1.key luks\n"), 0o600); err != nil {
		t.Fatalf("write crypttab: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc", "keys"), 0o755); err != nil {
		t.Fatalf("mkdir /etc/keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "keys", "crypt1.key"), []byte("keydata\n"), 0o600); err != nil {
		t.Fatalf("write crypt keyfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "cifs-creds"), []byte("username=alice\npassword=secret\n"), 0o600); err != nil {
		t.Fatalf("write cifs creds: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "root", ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir /root/.ssh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "root", ".ssh", "id_rsa"), []byte("PRIVATEKEY\n"), 0o600); err != nil {
		t.Fatalf("write ssh identity file: %v", err)
	}

	// iSCSI + multipath config data (secrets included in the backing files).
	nodesFile := filepath.Join(root, "etc", "iscsi", "nodes", "iqn.2026-01.test:target1", "127.0.0.1,3260,1", "default")
	if err := os.MkdirAll(filepath.Dir(nodesFile), 0o755); err != nil {
		t.Fatalf("mkdir iscsi nodes: %v", err)
	}
	if err := os.WriteFile(nodesFile, []byte("node.session.auth.password = secret\n"), 0o600); err != nil {
		t.Fatalf("write iscsi node file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc", "multipath"), 0o755); err != nil {
		t.Fatalf("mkdir multipath: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "multipath", "bindings"), []byte("mpatha 3600...\n"), 0o600); err != nil {
		t.Fatalf("write bindings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "multipath", "wwids"), []byte("3600...\n"), 0o600); err != nil {
		t.Fatalf("write wwids: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "var", "lib", "iscsi"), 0o755); err != nil {
		t.Fatalf("mkdir var/lib/iscsi: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "var", "lib", "iscsi", "example.txt"), []byte("state\n"), 0o600); err != nil {
		t.Fatalf("write var/lib/iscsi example: %v", err)
	}

	// systemd mount units + autofs maps (additional mount sources)
	unitPath := filepath.Join(root, "etc", "systemd", "system", "mnt-synology_nfs-pbs_backup.mount")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("mkdir systemd dir: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte("[Mount]\nWhat=server:/export\nWhere=/mnt/Synology_NFS/PBS_Backup\nType=nfs\n"), 0o644); err != nil {
		t.Fatalf("write mount unit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "auto.master"), []byte("/- /etc/auto.pbs\n"), 0o644); err != nil {
		t.Fatalf("write auto.master: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "auto.pbs"), []byte("/mnt/autofs -fstype=nfs4 server:/export\n"), 0o644); err != nil {
		t.Fatalf("write auto.pbs: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "etc", "lvm", "backup"), 0o755); err != nil {
		t.Fatalf("mkdir lvm backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "lvm", "backup", "vg0"), []byte("contents\n"), 0o600); err != nil {
		t.Fatalf("write lvm backup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc", "zfs"), 0o755); err != nil {
		t.Fatalf("mkdir zfs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "zfs", "zpool.cache"), []byte("cache\n"), 0o600); err != nil {
		t.Fatalf("write zpool cache: %v", err)
	}

	for _, dsPath := range []string{
		filepath.Join(root, "mnt", "datastore", "Data1"),
		filepath.Join(root, "mnt", "Synology_NFS", "PBS_Backup"),
	} {
		if err := os.MkdirAll(filepath.Join(dsPath, ".chunks"), 0o750); err != nil {
			t.Fatalf("mkdir chunks: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(dsPath, "vm"), 0o750); err != nil {
			t.Fatalf("mkdir vm: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dsPath, ".lock"), []byte(""), 0o640); err != nil {
			t.Fatalf("write lock: %v", err)
		}
	}

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	if err := collector.collectPBSDatastoreInventory(context.Background(), nil); err != nil {
		t.Fatalf("collectPBSDatastoreInventory error: %v", err)
	}

	reportPath := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "pbs_datastore_inventory.json")
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	var report pbsDatastoreInventoryReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}

	if report.HostCommands {
		t.Fatalf("expected host_commands=false in offline mode")
	}

	if snap, ok := report.Files["pbs_datastore_cfg"]; !ok || !snap.Exists || snap.Content == "" {
		t.Fatalf("expected datastore cfg snapshot, got: %+v", snap)
	}
	if snap, ok := report.Files["crypttab"]; !ok || !snap.Exists || snap.Content == "" {
		t.Fatalf("expected crypttab snapshot, got: %+v", snap)
	}
	if snap, ok := report.Files["multipath_bindings"]; !ok || !snap.Exists || snap.Content == "" {
		t.Fatalf("expected multipath bindings snapshot, got: %+v", snap)
	}
	if dir, ok := report.Dirs["iscsi_etc"]; !ok || !dir.Exists || len(dir.Files) == 0 {
		t.Fatalf("expected iscsi dir snapshot, got: %+v", dir)
	}
	if dir, ok := report.Dirs["systemd_mount_units"]; !ok || !dir.Exists || len(dir.Files) == 0 {
		t.Fatalf("expected systemd mount units snapshot, got: %+v", dir)
	}
	if snap, ok := report.Files["autofs_master"]; !ok || !snap.Exists || snap.Content == "" {
		t.Fatalf("expected autofs master snapshot, got: %+v", snap)
	}
	if snap, ok := report.Files["zfs_zpool_cache"]; !ok || !snap.Exists || snap.Content == "" {
		t.Fatalf("expected zpool cache snapshot, got: %+v", snap)
	}
	if dir, ok := report.Dirs["lvm_backup"]; !ok || !dir.Exists || len(dir.Files) == 0 {
		t.Fatalf("expected lvm backup snapshot, got: %+v", dir)
	}

	// Ensure iSCSI config was copied into the backup tree.
	copiedNodesFile := filepath.Join(collector.tempDir, "etc", "iscsi", "nodes", "iqn.2026-01.test:target1", "127.0.0.1,3260,1", "default")
	if _, err := os.Stat(copiedNodesFile); err != nil {
		t.Fatalf("expected copied iSCSI node file, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "keys", "crypt1.key")); err != nil {
		t.Fatalf("expected copied crypttab keyfile, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "cifs-creds")); err != nil {
		t.Fatalf("expected copied fstab credentials file, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "root", ".ssh", "id_rsa")); err != nil {
		t.Fatalf("expected copied ssh identity file, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "systemd", "system", "mnt-synology_nfs-pbs_backup.mount")); err != nil {
		t.Fatalf("expected copied systemd mount unit file, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "auto.pbs")); err != nil {
		t.Fatalf("expected copied autofs map file, got %v", err)
	}

	if len(report.Datastores) != 2 {
		t.Fatalf("expected 2 datastores, got %d", len(report.Datastores))
	}
	foundChunks := 0
	for _, ds := range report.Datastores {
		if ds.Name == "" || ds.Path == "" {
			t.Fatalf("unexpected datastore entry: %+v", ds)
		}
		if !ds.PathOK || !ds.PathIsDir {
			t.Fatalf("expected datastore path to be ok and dir: %+v", ds)
		}
		if ds.Markers.HasChunks {
			foundChunks++
		}
	}
	if foundChunks != 2 {
		t.Fatalf("expected HasChunks=true for both datastores, got %d", foundChunks)
	}
}

func TestCollectPBSDatastoreInventoryCapturesHostCommands(t *testing.T) {
	pbsRoot := t.TempDir()
	if err := os.MkdirAll(pbsRoot, 0o755); err != nil {
		t.Fatalf("mkdir pbsRoot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pbsRoot, "datastore.cfg"), []byte("datastore: Data1\npath /mnt/datastore/Data1\n"), 0o640); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = pbsRoot
	cfg.SystemRootPrefix = ""
	cfg.ExcludePatterns = append(cfg.ExcludePatterns,
		"**/etc/fstab",
		"**/etc/crypttab",
		"**/etc/systemd/**",
		"**/etc/auto.*",
		"**/etc/auto.master.d/**",
		"**/etc/autofs.conf",
		"**/etc/mdadm/**",
		"**/etc/lvm/**",
		"**/etc/zfs/**",
		"**/etc/iscsi/**",
		"**/var/lib/iscsi/**",
		"**/etc/multipath/**",
		"**/etc/multipath.conf",
	)

	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "uname":
				return []byte("Linux test\n"), nil
			case "lsblk":
				return []byte(`{"blockdevices":[]}`), nil
			case "findmnt":
				if len(args) >= 2 && args[0] == "-J" && args[1] == "-T" {
					return []byte(`{"filesystems":[{"target":"/mnt/datastore/Data1","source":"server:/export","fstype":"nfs4"}]}`), nil
				}
				return []byte(`{"filesystems":[]}`), nil
			case "nfsstat":
				return []byte("nfsstat -m output\n"), nil
			case "zpool":
				return []byte("zpool output\n"), nil
			case "zfs":
				return []byte("zfs output\n"), nil
			case "df":
				return []byte("df output\n"), nil
			default:
				return []byte("ok\n"), nil
			}
		},
	}

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)
	cli := []pbsDatastore{{Name: "Data1", Path: "/mnt/datastore/Data1"}}
	if err := collector.collectPBSDatastoreInventory(context.Background(), cli); err != nil {
		t.Fatalf("collectPBSDatastoreInventory error: %v", err)
	}

	reportPath := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "pbs_datastore_inventory.json")
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	var report pbsDatastoreInventoryReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}

	if !report.HostCommands {
		t.Fatalf("expected host_commands=true")
	}
	if got := report.Commands["lsblk_json"].Output; got != `{"blockdevices":[]}` {
		t.Fatalf("unexpected lsblk output: %q", got)
	}
	if len(report.Datastores) != 1 {
		t.Fatalf("expected 1 datastore, got %d", len(report.Datastores))
	}
	if got := report.Datastores[0].Findmnt.Output; got == "" {
		t.Fatalf("expected findmnt output to be captured")
	}
}
