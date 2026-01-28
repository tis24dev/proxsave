package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/pbs"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestGetDatastoreListNoBinary(t *testing.T) {
	collector := NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false, CollectorDeps{
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	})
	ds, err := collector.getDatastoreList(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(ds) != 0 {
		t.Fatalf("expected empty datastores when binary missing")
	}
}

func TestGetDatastoreListCommandErrorAndParseError(t *testing.T) {
	deps := CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("cmd fail")
		},
	}

	c := NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false, deps)
	if _, err := c.getDatastoreList(context.Background()); err == nil {
		t.Fatalf("expected error when command fails")
	}

	// Now simulate parse error
	c.deps.RunCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("{invalid"), nil
	}
	if _, err := c.getDatastoreList(context.Background()); err == nil {
		t.Fatalf("expected parse error for invalid JSON")
	}
}

func TestGetDatastoreListSuccess(t *testing.T) {
	c := NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false, CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"name":" ds1 ","path":"/store1","comment":" main "},{"name":"", "path":"/skip"}]`), nil
		},
	})
	ds, err := c.getDatastoreList(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ds) != 1 || ds[0].Name != "ds1" || ds[0].Path != "/store1" || ds[0].Comment != "main" {
		t.Fatalf("unexpected datastore parsed: %+v", ds)
	}
}

func TestGetDatastoreListOverridePaths(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSDatastorePaths = []string{"/override"}
	c := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"name":"ds1","path":"/auto1"},{"name":"ds2","path":"/auto2"}]`), nil
		},
	})

	ds, err := c.getDatastoreList(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ds) != 3 {
		t.Fatalf("expected 3 datastores (2 auto + 1 override), got %d", len(ds))
	}
	if ds[2].Name != "override" || ds[2].Path != "/override" || ds[2].Comment != "configured via PBS_DATASTORE_PATH" {
		t.Fatalf("override entry not appended as expected: %+v", ds[2])
	}
}

func TestCollectDatastoreNamespacesSuccessAndError(t *testing.T) {
	tmp := t.TempDir()
	c := NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false, CollectorDeps{})
	ds := pbsDatastore{Name: "store", Path: "/tmp/path"}
	targetDir := filepath.Join(tmp, "ds")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	origList := listNamespacesFunc
	listNamespacesFunc = func(name, path string) ([]pbs.Namespace, bool, error) {
		if name != ds.Name || path != ds.Path {
			t.Fatalf("unexpected args %s %s", name, path)
		}
		return []pbs.Namespace{{Ns: "root", Path: "/root"}}, true, nil
	}
	t.Cleanup(func() { listNamespacesFunc = origList })

	if err := c.collectDatastoreNamespaces(ds, targetDir); err != nil {
		t.Fatalf("collectDatastoreNamespaces error: %v", err)
	}
	nsPath := filepath.Join(targetDir, "store_namespaces.json")
	if _, err := os.Stat(nsPath); err != nil {
		t.Fatalf("expected namespaces file, got %v", err)
	}

	listNamespacesFunc = func(string, string) ([]pbs.Namespace, bool, error) {
		return nil, false, errors.New("fail")
	}
	if err := c.collectDatastoreNamespaces(ds, targetDir); err == nil {
		t.Fatalf("expected error when namespace listing fails")
	}
}

func TestCollectUserTokensAggregates(t *testing.T) {
	tmp := t.TempDir()
	c := NewCollectorWithDeps(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false, CollectorDeps{
		LookPath: func(string) (string, error) { return "/bin/echo", nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"token":"ok"}`), nil
		},
	})

	commandsDir := filepath.Join(tmp, "var/lib/proxsave-info", "commands", "pbs")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	userList := []map[string]string{{"userid": "user@pam"}, {"userid": "second@pve"}}
	data, _ := json.Marshal(userList)
	if err := os.WriteFile(filepath.Join(commandsDir, "user_list.json"), data, 0o640); err != nil {
		t.Fatalf("write user list: %v", err)
	}

	if err := c.collectUserConfigs(context.Background()); err != nil {
		t.Fatalf("collectUserConfigs error: %v", err)
	}

	aggPath := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "access-control", "tokens.json")
	if _, err := os.Stat(aggPath); err != nil {
		t.Fatalf("expected aggregated tokens.json, got %v", err)
	}
	payload, _ := os.ReadFile(aggPath)
	if !json.Valid(payload) {
		t.Fatalf("aggregated tokens not valid json: %s", string(payload))
	}
}

func TestCollectPBSConfigsWithCustomRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dummy.cfg"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write dummy cfg: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = root
	cfg.BackupDatastoreConfigs = false
	cfg.BackupUserConfigs = false
	cfg.BackupRemoteConfigs = false
	cfg.BackupSyncJobs = false
	cfg.BackupVerificationJobs = false
	cfg.BackupTapeConfigs = false
	cfg.BackupPruneSchedules = false
	cfg.BackupPxarFiles = false

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"name":"store1","path":"/fake"}]`), nil
		},
	})
	if err := collector.CollectPBSConfigs(context.Background()); err != nil {
		t.Fatalf("CollectPBSConfigs failed with custom root: %v", err)
	}

	commandsDir := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs")
	if _, err := os.Stat(commandsDir); err != nil {
		t.Fatalf("expected commands directory, got err: %v", err)
	}
}

func TestCollectPBSConfigsExcludesDisabledPBSConfigFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	mustWrite("dummy.cfg", "ok")
	mustWrite("datastore.cfg", "datastore")
	mustWrite("user.cfg", "user")
	mustWrite("acl.cfg", "acl")
	mustWrite("domains.cfg", "domains")
	mustWrite("remote.cfg", "remote")
	mustWrite("sync.cfg", "sync")
	mustWrite("verification.cfg", "verify")
	mustWrite("tape.cfg", "tape")
	mustWrite("media-pool.cfg", "media")
	mustWrite("network.cfg", "net")
	mustWrite("prune.cfg", "prune")

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = root
	cfg.BackupDatastoreConfigs = false
	cfg.BackupUserConfigs = false
	cfg.BackupRemoteConfigs = false
	cfg.BackupSyncJobs = false
	cfg.BackupVerificationJobs = false
	cfg.BackupTapeConfigs = false
	cfg.BackupPruneSchedules = false
	cfg.BackupNetworkConfigs = false
	cfg.BackupPxarFiles = false

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "proxmox-backup-manager" && len(args) >= 3 && args[0] == "datastore" && args[1] == "list" {
				return []byte(`[{"name":"store1","path":"/fake"}]`), nil
			}
			return []byte("ok"), nil
		},
	})

	if err := collector.CollectPBSConfigs(context.Background()); err != nil {
		t.Fatalf("CollectPBSConfigs failed: %v", err)
	}

	destDir := filepath.Join(collector.tempDir, "etc", "proxmox-backup")

	if _, err := os.Stat(filepath.Join(destDir, "dummy.cfg")); err != nil {
		t.Fatalf("expected dummy.cfg collected, got %v", err)
	}

	for _, excluded := range []string{
		"datastore.cfg",
		"user.cfg",
		"acl.cfg",
		"domains.cfg",
		"remote.cfg",
		"sync.cfg",
		"verification.cfg",
		"tape.cfg",
		"media-pool.cfg",
		"network.cfg",
		"prune.cfg",
	} {
		_, err := os.Stat(filepath.Join(destDir, excluded))
		if err == nil {
			t.Fatalf("expected %s excluded from PBS config snapshot", excluded)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", excluded, err)
		}
	}

	// Ensure related command output is also excluded when the feature flag is disabled.
	if _, err := os.Stat(filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "pbs", "remote_list.json")); err == nil {
		t.Fatalf("expected remote_list.json excluded when BACKUP_REMOTE_CONFIGS=false")
	}
}

func TestCollectPBSConfigFileReturnsSkippedWhenExcluded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "remote.cfg"), []byte("remote"), 0o644); err != nil {
		t.Fatalf("write remote.cfg: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = root
	cfg.ExcludePatterns = []string{"remote.cfg"}

	collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false, CollectorDeps{})

	entry := collector.collectPBSConfigFile(context.Background(), root, "remote.cfg", "Remote configuration", true)
	if entry.Status != StatusSkipped {
		t.Fatalf("expected StatusSkipped, got %s", entry.Status)
	}

	target := filepath.Join(collector.tempDir, "etc", "proxmox-backup", "remote.cfg")
	_, err := os.Stat(target)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s not to exist, stat err=%v", target, err)
	}
}
