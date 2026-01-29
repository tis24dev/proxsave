package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/pbs"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectPBSCommandsWritesExpectedOutputs(t *testing.T) {
	pbsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(pbsRoot, "tape.cfg"), []byte("ok"), 0o640); err != nil {
		t.Fatalf("write tape.cfg: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = pbsRoot

	deps := CollectorDeps{
		LookPath: func(name string) (string, error) {
			return "/bin/" + name, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(fmt.Sprintf("%s %s", name, strings.Join(args, " "))), nil
		},
	}

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)
	datastores := []pbsDatastore{
		{Name: "store1", Path: "/data/store1"},
		{Name: "store2", Path: "/data/store2"},
	}

	if err := collector.collectPBSCommands(context.Background(), datastores); err != nil {
		t.Fatalf("collectPBSCommands error: %v", err)
	}

	commandsDir := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs")

	for _, rel := range []string{
		"pbs_version.txt",
		"node_config.json",
		"datastore_list.json",
		"datastore_store1_status.json",
		"acme_accounts.json",
		"acme_plugins.json",
		"notification_targets.json",
		"notification_matchers.json",
		"notification_endpoints_smtp.json",
		"notification_endpoints_sendmail.json",
		"notification_endpoints_gotify.json",
		"notification_endpoints_webhook.json",
		"user_list.json",
		"realms_ldap.json",
		"realms_ad.json",
		"realms_openid.json",
		"acl_list.json",
		"remote_list.json",
		"sync_jobs.json",
		"verification_jobs.json",
		"prune_jobs.json",
		"gc_jobs.json",
		"tape_drives.json",
		"tape_changers.json",
		"tape_pools.json",
		"network_list.json",
		"disk_list.json",
		"cert_info.txt",
		"traffic_control.json",
		"recent_tasks.json",
		"s3_endpoints.json",
	} {
		if _, err := os.Stat(filepath.Join(commandsDir, rel)); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}

	version, err := os.ReadFile(filepath.Join(commandsDir, "pbs_version.txt"))
	if err != nil {
		t.Fatalf("read pbs_version.txt: %v", err)
	}
	if !strings.Contains(string(version), "proxmox-backup-manager version") {
		t.Fatalf("unexpected version content: %s", string(version))
	}
}

func TestCollectPBSCommandsReturnsErrorWhenCriticalVersionFails(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = t.TempDir()

	deps := CollectorDeps{
		LookPath: func(name string) (string, error) {
			return "/bin/" + name, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "proxmox-backup-manager" && len(args) > 0 && args[0] == "version" {
				return []byte("nope"), errors.New("boom")
			}
			return []byte("ok"), nil
		},
	}

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)
	err := collector.collectPBSCommands(context.Background(), []pbsDatastore{{Name: "store1", Path: "/data/store1"}})
	if err == nil || !strings.Contains(err.Error(), "failed to get PBS version") {
		t.Fatalf("expected critical version error, got %v", err)
	}
}

func TestCollectPBSPxarMetadataProcessesMultipleDatastores(t *testing.T) {
	tmp := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 2
	cfg.PxarIntraConcurrency = 1

	collector := NewCollector(newTestLogger(), cfg, tmp, types.ProxmoxBS, false)

	makeDatastore := func(name string) (pbsDatastore, string) {
		dsPath := filepath.Join(tmp, "datastore-"+name)
		for _, sub := range []string{"vm", "ct"} {
			if err := os.MkdirAll(filepath.Join(dsPath, sub), 0o755); err != nil {
				t.Fatalf("mkdir %s/%s: %v", name, sub, err)
			}
		}
		if err := os.WriteFile(filepath.Join(dsPath, "vm", "backup1.pxar"), []byte("data"), 0o640); err != nil {
			t.Fatalf("write pxar: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dsPath, "ct", "backup2.pxar"), []byte("data"), 0o640); err != nil {
			t.Fatalf("write pxar: %v", err)
		}
		return pbsDatastore{Name: name, Path: dsPath, Comment: "test"}, dsPath
	}

	ds1, ds1Path := makeDatastore("ds1")
	ds2, ds2Path := makeDatastore("ds2")

	if err := collector.collectPBSPxarMetadata(context.Background(), []pbsDatastore{ds1, {Name: "skip", Path: ""}, ds2}); err != nil {
		t.Fatalf("collectPBSPxarMetadata error: %v", err)
	}

	metaDir := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "pxar", "metadata")
	for _, tc := range []struct {
		ds     pbsDatastore
		dsPath string
	}{
		{ds: ds1, dsPath: ds1Path},
		{ds: ds2, dsPath: ds2Path},
	} {
		base := filepath.Join(metaDir, tc.ds.Name)
		metadataPath := filepath.Join(base, "metadata.json")
		subdirsPath := filepath.Join(base, fmt.Sprintf("%s_subdirs.txt", tc.ds.Name))
		vmListPath := filepath.Join(base, fmt.Sprintf("%s_vm_pxar_list.txt", tc.ds.Name))
		ctListPath := filepath.Join(base, fmt.Sprintf("%s_ct_pxar_list.txt", tc.ds.Name))

		for _, path := range []string{metadataPath, subdirsPath, vmListPath, ctListPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected %s to exist: %v", path, err)
			}
		}

		meta, _ := os.ReadFile(metadataPath)
		if !strings.Contains(string(meta), tc.ds.Name) || !strings.Contains(string(meta), tc.dsPath) {
			t.Fatalf("metadata.json missing expected fields: %s", string(meta))
		}

		subdirs, _ := os.ReadFile(subdirsPath)
		for _, want := range []string{"vm", "ct"} {
			if !strings.Contains(string(subdirs), want) {
				t.Fatalf("subdirs report missing %q: %s", want, string(subdirs))
			}
		}

		vmList, _ := os.ReadFile(vmListPath)
		if !strings.Contains(string(vmList), "backup1.pxar") {
			t.Fatalf("vm list missing pxar file: %s", string(vmList))
		}

		ctList, _ := os.ReadFile(ctListPath)
		if !strings.Contains(string(ctList), "backup2.pxar") {
			t.Fatalf("ct list missing pxar file: %s", string(ctList))
		}
	}
}

func TestCollectPBSPxarMetadataReturnsErrorWhenTempVarIsFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "var"), []byte("not-a-dir"), 0o640); err != nil {
		t.Fatalf("write var file: %v", err)
	}

	collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	dsPath := t.TempDir()
	err := collector.collectPBSPxarMetadata(context.Background(), []pbsDatastore{{Name: "ds", Path: dsPath}})
	if err == nil || !strings.Contains(err.Error(), "failed to create PXAR metadata directory") {
		t.Fatalf("expected ensureDir failure, got %v", err)
	}
}

func TestCollectDatastoreConfigsCreatesConfigAndNamespaceFiles(t *testing.T) {
	origList := listNamespacesFunc
	t.Cleanup(func() { listNamespacesFunc = origList })
	listNamespacesFunc = func(name, path string) ([]pbs.Namespace, bool, error) {
		return []pbs.Namespace{{Ns: "root", Path: "/"}}, false, nil
	}

	cfg := GetDefaultCollectorConfig()
	deps := CollectorDeps{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"ok":true}`), nil
		},
	}

	tmp := t.TempDir()
	collector := NewCollectorWithDeps(newTestLogger(), cfg, tmp, types.ProxmoxBS, false, deps)
	ds := pbsDatastore{Name: "store", Path: "/fake/path"}
	if err := collector.collectDatastoreConfigs(context.Background(), []pbsDatastore{ds}); err != nil {
		t.Fatalf("collectDatastoreConfigs error: %v", err)
	}

	datastoreDir := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "datastores")
	if _, err := os.Stat(filepath.Join(datastoreDir, "store_config.json")); err != nil {
		t.Fatalf("expected config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(datastoreDir, "store_namespaces.json")); err != nil {
		t.Fatalf("expected namespaces file: %v", err)
	}
}

func TestCollectUserTokensSkipsInvalidUserListJSON(t *testing.T) {
	tmp := t.TempDir()
	collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)

	commandsDir := filepath.Join(tmp, "var/lib/proxsave-info", "commands", "pbs")
	usersDir := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "access-control")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	if err := os.MkdirAll(usersDir, 0o755); err != nil {
		t.Fatalf("mkdir users: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "user_list.json"), []byte("{invalid"), 0o640); err != nil {
		t.Fatalf("write user_list.json: %v", err)
	}

	collector.collectUserTokens(context.Background(), usersDir)
	if _, err := os.Stat(filepath.Join(usersDir, "tokens.json")); err == nil {
		t.Fatalf("tokens.json should not be created for invalid user list JSON")
	}
}

func TestCollectPBSCommandsSkipsTapeDetailsWhenHasTapeSupportErrors(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = t.TempDir()

	deps := CollectorDeps{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "proxmox-tape" && len(args) >= 2 && args[0] == "drive" && args[1] == "list" {
				return nil, errors.New("tape not available")
			}
			return []byte("ok"), nil
		},
	}

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)
	if err := collector.collectPBSCommands(context.Background(), []pbsDatastore{{Name: "store1", Path: "/data/store1"}}); err != nil {
		t.Fatalf("collectPBSCommands error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "tape_drives.json")); err == nil {
		t.Fatalf("tape_drives.json should not be created when tape support check fails")
	}
}

func TestCollectPBSConfigsEndToEndWithStubs(t *testing.T) {
	pbsRoot := t.TempDir()
	for _, name := range []string{
		"datastore.cfg",
		"user.cfg",
		"acl.cfg",
		"remote.cfg",
		"sync.cfg",
		"verification.cfg",
		"tape.cfg",
		"media-pool.cfg",
		"network.cfg",
		"prune.cfg",
	} {
		if err := os.WriteFile(filepath.Join(pbsRoot, name), []byte("ok"), 0o640); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	dsPath := t.TempDir()
	for _, sub := range []string{"vm", "ct"} {
		if err := os.MkdirAll(filepath.Join(dsPath, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dsPath, "vm", "backup.pxar"), []byte("data"), 0o640); err != nil {
		t.Fatalf("write pxar: %v", err)
	}

	origList := listNamespacesFunc
	t.Cleanup(func() { listNamespacesFunc = origList })
	listNamespacesFunc = func(name, path string) ([]pbs.Namespace, bool, error) {
		return []pbs.Namespace{{Ns: "root", Path: "/"}}, false, nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = pbsRoot
	cfg.PxarDatastoreConcurrency = 2
	cfg.PxarIntraConcurrency = 1

	deps := CollectorDeps{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "proxmox-backup-manager" {
				return []byte("ok"), nil
			}
			if len(args) >= 3 && args[0] == "datastore" && args[1] == "list" {
				return []byte(fmt.Sprintf(`[{"name":"store","path":"%s","comment":"main"}]`, dsPath)), nil
			}
			if len(args) >= 3 && args[0] == "user" && args[1] == "list" {
				return []byte(`[{"userid":"user@pam"},{"userid":"second@pve"}]`), nil
			}
			if len(args) >= 3 && args[0] == "user" && args[1] == "list-tokens" {
				return []byte(`{"token":"ok"}`), nil
			}
			return []byte(`{}`), nil
		},
	}

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, deps)
	if err := collector.CollectPBSConfigs(context.Background()); err != nil {
		t.Fatalf("CollectPBSConfigs error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc/proxmox-backup")); err != nil {
		t.Fatalf("expected etc/proxmox-backup to be copied: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "var/lib/proxsave-info", "pbs", "datastores", "store_config.json")); err != nil {
		t.Fatalf("expected datastore config to be collected: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "var/lib/proxsave-info", "pbs", "access-control", "tokens.json")); err != nil {
		t.Fatalf("expected tokens.json to be aggregated: %v", err)
	}

	pxarMeta := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "pbs", "pxar", "metadata", "store", "metadata.json")
	if _, err := os.Stat(pxarMeta); err != nil {
		t.Fatalf("expected PXAR metadata to be collected: %v", err)
	}
}

func TestCollectPBSConfigsReturnsErrorWhenNotPBSSystem(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = filepath.Join(t.TempDir(), "missing")
	cfg.BackupDatastoreConfigs = false
	cfg.BackupUserConfigs = false
	cfg.BackupRemoteConfigs = false
	cfg.BackupSyncJobs = false
	cfg.BackupVerificationJobs = false
	cfg.BackupTapeConfigs = false
	cfg.BackupPruneSchedules = false
	cfg.BackupPxarFiles = false

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	err := collector.CollectPBSConfigs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a PBS system") {
		t.Fatalf("expected not-a-PBS error, got %v", err)
	}
}

func TestPBSConfigPathUsesDefaultWhenUnset(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = ""
	cfg.SystemRootPrefix = filepath.Join(t.TempDir(), "root")

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	got := collector.pbsConfigPath()
	want := filepath.Join(cfg.SystemRootPrefix, "etc/proxmox-backup")
	if got != want {
		t.Fatalf("pbsConfigPath() = %q, want %q", got, want)
	}
}

func TestCollectPBSPxarMetadataStopsOnFirstDatastoreError(t *testing.T) {
	tmp := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.PxarDatastoreConcurrency = 2
	cfg.PxarIntraConcurrency = 1

	collector := NewCollector(newTestLogger(), cfg, tmp, types.ProxmoxBS, false)

	metaRoot := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "pxar", "metadata")
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		t.Fatalf("mkdir metaRoot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaRoot, "badds"), []byte("file"), 0o640); err != nil {
		t.Fatalf("write bad dsDir file: %v", err)
	}

	dsOKPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dsOKPath, "vm"), 0o755); err != nil {
		t.Fatalf("mkdir vm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dsOKPath, "vm", "ok.pxar"), []byte("data"), 0o640); err != nil {
		t.Fatalf("write ok.pxar: %v", err)
	}

	dsBadPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dsBadPath, "vm"), 0o755); err != nil {
		t.Fatalf("mkdir vm: %v", err)
	}

	err := collector.collectPBSPxarMetadata(context.Background(), []pbsDatastore{
		{Name: "badds", Path: dsBadPath},
		{Name: "okds", Path: dsOKPath},
	})
	if err == nil || !strings.Contains(err.Error(), "failed to create PXAR metadata directory") {
		t.Fatalf("expected datastore processing error, got %v", err)
	}
}
