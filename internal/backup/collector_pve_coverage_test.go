package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pveTestBool(v bool) *bool {
	b := v
	return &b
}

func newPVEHappyCommandCollector(t *testing.T) *Collector {
	t.Helper()
	return newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "pveversion":
				return []byte("pve-manager/8.2"), nil
			case "pvenode":
				return []byte(`{"wakeonlan":"00:11:22:33:44:55"}`), nil
			case "pveum", "pvesm":
				return []byte("[]"), nil
			case "pvecm":
				return []byte("Cluster information\n"), nil
			case "pvesh":
				if len(args) >= 2 {
					switch {
					case args[1] == "/nodes":
						return []byte(`[{"node":"zeta"},{"node":"alpha"},{"node":"  "}]`), nil
					case args[1] == "/version":
						return []byte(`{"version":"8.2"}`), nil
					case strings.Contains(args[1], "/storage"):
						return []byte(`[
							{"storage":"zstore","path":"/z","type":"dir","content":"backup","active":"on","enabled":"yes","status":"available"},
							{"storage":"astore","path":"/a","type":"dir","content":"iso","active":"off","enabled":"no","status":"disabled"}
						]`), nil
					}
				}
				return []byte("[]"), nil
			default:
				return []byte("[]"), nil
			}
		},
	})
}

func markOutputPathAsDir(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("mkdir output marker %s: %v", name, err)
	}
}

func TestPVEManifestHelpersAdditionalBranches(t *testing.T) {
	var nilCollector *Collector
	nilCollector.populatePVEManifest()

	collector := newPVECollector(t)
	if got := pveManifestKey("", "etc/pve/user.cfg"); got != "etc/pve/user.cfg" {
		t.Fatalf("pveManifestKey empty temp = %q", got)
	}
	if got := pveManifestKey(collector.tempDir, collector.tempDir); got != filepath.ToSlash(collector.tempDir) {
		t.Fatalf("pveManifestKey self = %q", got)
	}

	srcDir := filepath.Join(t.TempDir(), "srcdir")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir srcDir: %v", err)
	}
	srcFile := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(srcFile, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write srcFile: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "destdir")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("mkdir destDir: %v", err)
	}

	if got := collector.describePathForManifest(srcFile, "", false); got.Status != StatusDisabled {
		t.Fatalf("disabled manifest status = %s", got.Status)
	}
	if got := collector.describePathForManifest(filepath.Join(t.TempDir(), "missing"), "", true); got.Status != StatusNotFound {
		t.Fatalf("missing manifest status = %s", got.Status)
	}
	if got := collector.describePathForManifest(srcFile, filepath.Join(t.TempDir(), "missing-dest"), true); got.Status != StatusFailed {
		t.Fatalf("missing dest manifest status = %s", got.Status)
	}
	if got := collector.describePathForManifest(srcDir, destDir, true); got.Status != StatusCollected || got.Size != 0 {
		t.Fatalf("directory manifest entry = %+v", got)
	}

	dryRunCollector := newPVECollector(t)
	dryRunCollector.dryRun = true
	if got := dryRunCollector.describePathForManifest(srcDir, "", true); got.Status != StatusCollected || got.Size != 0 {
		t.Fatalf("dry-run directory manifest entry = %+v", got)
	}

	manifestCollector := newPVECollector(t)
	pveRoot := manifestCollector.config.PVEConfigPath
	qemuDir := filepath.Join(pveRoot, "qemu-server")
	if err := os.MkdirAll(qemuDir, 0o755); err != nil {
		t.Fatalf("mkdir qemu dir: %v", err)
	}
	if err := os.MkdirAll(manifestCollector.targetPathFor(qemuDir), 0o755); err != nil {
		t.Fatalf("mkdir qemu dest: %v", err)
	}
	userCfg := filepath.Join(pveRoot, "user.cfg")
	if err := os.WriteFile(userCfg, []byte("user"), 0o644); err != nil {
		t.Fatalf("write user.cfg: %v", err)
	}
	userDest := manifestCollector.targetPathFor(userCfg)
	if err := os.MkdirAll(filepath.Dir(userDest), 0o755); err != nil {
		t.Fatalf("mkdir user dest: %v", err)
	}
	if err := os.WriteFile(userDest, []byte("user"), 0o644); err != nil {
		t.Fatalf("write user dest: %v", err)
	}
	manifestCollector.config.BackupPVEFirewall = false
	manifestCollector.populatePVEManifest()
	if len(manifestCollector.pveManifest) == 0 {
		t.Fatal("expected populatePVEManifest to record entries")
	}
}

func TestPVERuntimeCommandSuccessAndFailureBranches(t *testing.T) {
	t.Run("success paths parse runtime info", func(t *testing.T) {
		collector := newPVEHappyCommandCollector(t)
		commandsDir := t.TempDir()
		info := &pveRuntimeInfo{}

		if err := collector.collectPVECoreRuntime(context.Background(), commandsDir, info); err != nil {
			t.Fatalf("collectPVECoreRuntime: %v", err)
		}
		if err := collector.collectPVEACLRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectPVEACLRuntime: %v", err)
		}
		if err := collector.collectPVEClusterRuntime(context.Background(), commandsDir, true); err != nil {
			t.Fatalf("collectPVEClusterRuntime: %v", err)
		}
		if err := collector.collectPVEStorageRuntime(context.Background(), commandsDir, info); err != nil {
			t.Fatalf("collectPVEStorageRuntime: %v", err)
		}

		if len(info.Storages) != 2 || info.Storages[0].Name != "astore" || info.Storages[1].Name != "zstore" {
			t.Fatalf("storages not parsed and sorted: %+v", info.Storages)
		}
		collector.finalizePVERuntimeInfo(info)
		if len(info.Nodes) != 2 || info.Nodes[0] != "alpha" || info.Nodes[1] != "zeta" {
			t.Fatalf("nodes not finalized and sorted: %+v", info.Nodes)
		}
	})

	t.Run("acl disabled returns before commands", func(t *testing.T) {
		calls := 0
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return []byte("[]"), nil
			},
		})
		collector.config.BackupPVEACL = false
		if err := collector.collectPVEACLRuntime(context.Background(), t.TempDir()); err != nil {
			t.Fatalf("collectPVEACLRuntime disabled: %v", err)
		}
		if calls != 0 {
			t.Fatalf("expected no pveum calls when ACL backup disabled, got %d", calls)
		}
	})

	t.Run("critical pveversion failure is returned", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("boom"), errors.New("failed")
			},
		})
		if err := collector.collectPVECoreRuntime(context.Background(), t.TempDir(), &pveRuntimeInfo{}); err == nil {
			t.Fatal("expected critical pveversion failure")
		}
	})

	t.Run("core runtime output write errors", func(t *testing.T) {
		cases := []struct {
			name      string
			blockFile string
			wantErr   bool
		}{
			{name: "node config", blockFile: "node_config.txt", wantErr: true},
			{name: "api version", blockFile: "api_version.json", wantErr: true},
			{name: "nodes status warning", blockFile: "nodes_status.json", wantErr: false},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				collector := newPVEHappyCommandCollector(t)
				commandsDir := t.TempDir()
				markOutputPathAsDir(t, commandsDir, tt.blockFile)
				err := collector.collectPVECoreRuntime(context.Background(), commandsDir, &pveRuntimeInfo{})
				if tt.wantErr && err == nil {
					t.Fatal("expected output write error")
				}
				if !tt.wantErr && err != nil {
					t.Fatalf("expected warning-only output error, got %v", err)
				}
			})
		}
	})

	t.Run("acl runtime output write errors", func(t *testing.T) {
		for _, blockFile := range []string{"pve_users.json", "pve_groups.json", "pve_roles.json", "pools.json"} {
			t.Run(blockFile, func(t *testing.T) {
				collector := newPVEHappyCommandCollector(t)
				commandsDir := t.TempDir()
				markOutputPathAsDir(t, commandsDir, blockFile)
				if err := collector.collectPVEACLRuntime(context.Background(), commandsDir); err == nil {
					t.Fatal("expected ACL output write error")
				}
			})
		}
	})

	t.Run("cluster runtime output write errors", func(t *testing.T) {
		for _, blockFile := range []string{
			"cluster_status.txt",
			"cluster_nodes.txt",
			"ha_status.json",
			"mapping_pci.json",
			"mapping_usb.json",
			"mapping_dir.json",
		} {
			t.Run(blockFile, func(t *testing.T) {
				collector := newPVEHappyCommandCollector(t)
				commandsDir := t.TempDir()
				markOutputPathAsDir(t, commandsDir, blockFile)
				if err := collector.collectPVEClusterRuntime(context.Background(), commandsDir, true); err == nil {
					t.Fatal("expected cluster output write error")
				}
			})
		}
	})

	t.Run("storage runtime output write errors", func(t *testing.T) {
		cases := []struct {
			name      string
			blockFile string
			wantErr   bool
		}{
			{name: "disks list", blockFile: "disks_list.json", wantErr: true},
			{name: "storage status warning", blockFile: "storage_status.json", wantErr: false},
			{name: "pvesm status", blockFile: "pvesm_status.txt", wantErr: true},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				collector := newPVEHappyCommandCollector(t)
				commandsDir := t.TempDir()
				markOutputPathAsDir(t, commandsDir, tt.blockFile)
				err := collector.collectPVEStorageRuntime(context.Background(), commandsDir, &pveRuntimeInfo{})
				if tt.wantErr && err == nil {
					t.Fatal("expected storage runtime output write error")
				}
				if !tt.wantErr && err != nil {
					t.Fatalf("expected storage status warning only, got %v", err)
				}
			})
		}
	})
}

func TestPVEJobScheduleAndReplicationAdditionalBranches(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("[]"), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := collector.collectPVEBackupJobDefinitions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPVEBackupJobDefinitions canceled = %v", err)
	}
	if err := collector.collectPVEScheduleCrontab(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPVEScheduleCrontab canceled = %v", err)
	}
	if err := collector.collectPVEScheduleTimers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPVEScheduleTimers canceled = %v", err)
	}
	if err := collector.collectPVEScheduleCronFiles(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPVEScheduleCronFiles canceled = %v", err)
	}
	if err := collector.collectPVEReplicationDefinitions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPVEReplicationDefinitions canceled = %v", err)
	}
	if err := collector.collectPVEVZDumpCronSnapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collectPVEVZDumpCronSnapshot canceled = %v", err)
	}

	historyCalls := make(map[string]int)
	historyCollector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "pvesh" && len(args) >= 2 {
				historyCalls[args[1]]++
			}
			return []byte("[]"), nil
		},
	})
	if err := historyCollector.collectPVEBackupJobHistory(context.Background(), []string{"", "node1", " node1 ", "node2", "node2"}); err != nil {
		t.Fatalf("collectPVEBackupJobHistory: %v", err)
	}
	if historyCalls["/nodes/node1/tasks"] != 1 || historyCalls["/nodes/node2/tasks"] != 1 || len(historyCalls) != 2 {
		t.Fatalf("job history calls not deduplicated: %+v", historyCalls)
	}

	replicationCalls := make(map[string]int)
	replicationCollector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "pvesh" && len(args) >= 2 {
				replicationCalls[args[1]]++
			}
			return []byte("[]"), nil
		},
	})
	if err := replicationCollector.collectPVEReplicationStatus(context.Background(), []string{"", "node1", " node1 ", "node2", "node2"}); err != nil {
		t.Fatalf("collectPVEReplicationStatus: %v", err)
	}
	if replicationCalls["/nodes/node1/replication"] != 1 || replicationCalls["/nodes/node2/replication"] != 1 || len(replicationCalls) != 2 {
		t.Fatalf("replication calls not deduplicated: %+v", replicationCalls)
	}

	root := t.TempDir()
	cronDir := filepath.Join(root, "etc", "cron.d")
	if err := os.MkdirAll(filepath.Join(cronDir, "pve-dir"), 0o755); err != nil {
		t.Fatalf("mkdir cron subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cronDir, "pve-backup"), []byte("* * * * * root true"), 0o644); err != nil {
		t.Fatalf("write pve cron: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cronDir, "unrelated"), []byte("* * * * * root true"), 0o644); err != nil {
		t.Fatalf("write unrelated cron: %v", err)
	}
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	cronCollector := NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
	if err := cronCollector.collectPVEScheduleCronFiles(context.Background()); err != nil {
		t.Fatalf("collectPVEScheduleCronFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cronCollector.tempDir, "etc/cron.d/pve-backup")); err != nil {
		t.Fatalf("expected pve cron copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cronCollector.tempDir, "etc/cron.d/unrelated")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrelated cron should not be copied, got %v", err)
	}
}

func TestPVEGuestVMJobReplicationAndScheduleErrorBranches(t *testing.T) {
	t.Run("guest inventory success and output failures", func(t *testing.T) {
		collector := newPVEHappyCommandCollector(t)
		if err := collector.collectPVEGuestInventory(context.Background()); err != nil {
			t.Fatalf("collectPVEGuestInventory success: %v", err)
		}

		for _, blockFile := range []string{"qemu_vms.json", "lxc_containers.json"} {
			t.Run(blockFile, func(t *testing.T) {
				collector := newPVEHappyCommandCollector(t)
				commandsDir := collector.proxsaveCommandsDir("pve")
				if err := os.MkdirAll(commandsDir, 0o755); err != nil {
					t.Fatalf("mkdir commands dir: %v", err)
				}
				markOutputPathAsDir(t, commandsDir, blockFile)
				if err := collector.collectPVEGuestInventory(context.Background()); err == nil {
					t.Fatal("expected guest inventory output write error")
				}
			})
		}

		tempFile := filepath.Join(t.TempDir(), "temp-file")
		if err := os.WriteFile(tempFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		collector = NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempFile, "pve", false)
		if err := collector.collectPVEGuestInventory(context.Background()); err == nil {
			t.Fatal("expected commands directory creation error")
		}
	})

	t.Run("vm config wrappers return copy errors", func(t *testing.T) {
		collector := newPVECollector(t)
		qemuDir := filepath.Join(collector.config.PVEConfigPath, "qemu-server")
		if err := os.MkdirAll(qemuDir, 0o755); err != nil {
			t.Fatalf("mkdir qemu dir: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(collector.targetPathFor(qemuDir)), 0o755); err != nil {
			t.Fatalf("mkdir qemu dest parent: %v", err)
		}
		if err := os.WriteFile(collector.targetPathFor(qemuDir), []byte("block"), 0o644); err != nil {
			t.Fatalf("write qemu dest blocker: %v", err)
		}
		if err := collector.collectPVEQEMUConfigs(context.Background()); err == nil {
			t.Fatal("expected qemu config copy error")
		}

		collector = newPVECollector(t)
		lxcDir := filepath.Join(collector.config.PVEConfigPath, "lxc")
		if err := os.MkdirAll(lxcDir, 0o755); err != nil {
			t.Fatalf("mkdir lxc dir: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(collector.targetPathFor(lxcDir)), 0o755); err != nil {
			t.Fatalf("mkdir lxc dest parent: %v", err)
		}
		if err := os.WriteFile(collector.targetPathFor(lxcDir), []byte("block"), 0o644); err != nil {
			t.Fatalf("write lxc dest blocker: %v", err)
		}
		if err := collector.collectPVELXCConfigs(context.Background()); err == nil {
			t.Fatal("expected lxc config copy error")
		}
		if err := collector.collectVMConfigs(context.Background()); err == nil {
			t.Fatal("expected collectVMConfigs to return lxc copy error")
		}
	})

	t.Run("job replication and schedule write failures", func(t *testing.T) {
		collector := newPVEHappyCommandCollector(t)
		jobsDir := collector.pveJobsDir()
		if err := os.MkdirAll(jobsDir, 0o755); err != nil {
			t.Fatalf("mkdir jobs dir: %v", err)
		}
		markOutputPathAsDir(t, jobsDir, "backup_jobs.json")
		if err := collector.collectPVEBackupJobDefinitions(context.Background()); err == nil {
			t.Fatal("expected backup job definition write error")
		}

		tempFile := filepath.Join(t.TempDir(), "temp-file")
		if err := os.WriteFile(tempFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		collector = NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempFile, "pve", false)
		if err := collector.collectPVEBackupJobDefinitions(context.Background()); err == nil {
			t.Fatal("expected backup job definition ensureDir error")
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		collector = newPVEHappyCommandCollector(t)
		if err := collector.collectPVEBackupJobHistory(ctx, []string{"node1"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectPVEBackupJobHistory canceled = %v", err)
		}
		if err := collector.collectPVEReplicationStatus(ctx, []string{"node1"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectPVEReplicationStatus canceled = %v", err)
		}

		collector = newPVEHappyCommandCollector(t)
		repDir := collector.pveReplicationDir()
		if err := os.MkdirAll(repDir, 0o755); err != nil {
			t.Fatalf("mkdir replication dir: %v", err)
		}
		markOutputPathAsDir(t, repDir, "replication_jobs.json")
		if err := collector.collectPVEReplicationDefinitions(context.Background()); err == nil {
			t.Fatal("expected replication definitions write error")
		}

		collector = NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempFile, "pve", false)
		if err := collector.collectPVEReplicationDefinitions(context.Background()); err == nil {
			t.Fatal("expected replication definitions ensureDir error")
		}

		collector = NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempFile, "pve", false)
		if err := collector.collectPVEScheduleCrontab(context.Background()); err == nil {
			t.Fatal("expected crontab schedule ensureDir error")
		}
		if err := collector.collectPVEScheduleTimers(context.Background()); err == nil {
			t.Fatal("expected timer schedule ensureDir error")
		}
	})
}

func TestPVESnapshotAdditionalBranches(t *testing.T) {
	t.Run("cluster snapshot copies relative corosync cluster db and authkey", func(t *testing.T) {
		root := t.TempDir()
		pveRoot := filepath.Join(root, "etc", "pve")
		clusterRoot := filepath.Join(root, "var", "lib", "pve-cluster")
		authDir := filepath.Join(root, "etc", "corosync")
		for _, dir := range []string{pveRoot, clusterRoot, authDir} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		if err := os.WriteFile(filepath.Join(pveRoot, "relative-corosync.conf"), []byte("cluster_name: test"), 0o644); err != nil {
			t.Fatalf("write corosync: %v", err)
		}
		if err := os.WriteFile(filepath.Join(clusterRoot, "config.db"), []byte("db"), 0o644); err != nil {
			t.Fatalf("write config.db: %v", err)
		}
		if err := os.WriteFile(filepath.Join(authDir, "authkey"), []byte("key"), 0o600); err != nil {
			t.Fatalf("write authkey: %v", err)
		}
		cfg := GetDefaultCollectorConfig()
		cfg.SystemRootPrefix = root
		cfg.PVEConfigPath = "/etc/pve"
		cfg.PVEClusterPath = "/var/lib/pve-cluster"
		cfg.CorosyncConfigPath = "relative-corosync.conf"
		collector := NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
		if err := collector.collectPVEClusterSnapshot(context.Background(), true); err != nil {
			t.Fatalf("collectPVEClusterSnapshot: %v", err)
		}
		for _, src := range []string{
			filepath.Join(pveRoot, "relative-corosync.conf"),
			filepath.Join(clusterRoot, "config.db"),
			filepath.Join(authDir, "authkey"),
		} {
			if _, err := os.Stat(collector.targetPathFor(src)); err != nil {
				t.Fatalf("expected %s copied: %v", src, err)
			}
		}
	})

	t.Run("firewall file is copied", func(t *testing.T) {
		collector := newPVECollector(t)
		firewallPath := filepath.Join(collector.config.PVEConfigPath, "firewall")
		if err := os.WriteFile(firewallPath, []byte("[OPTIONS]\n"), 0o644); err != nil {
			t.Fatalf("write firewall file: %v", err)
		}
		if err := collector.collectPVEFirewallSnapshot(context.Background()); err != nil {
			t.Fatalf("collectPVEFirewallSnapshot: %v", err)
		}
		if _, err := os.Stat(collector.targetPathFor(firewallPath)); err != nil {
			t.Fatalf("expected firewall file copied: %v", err)
		}
	})

	t.Run("vzdump relative path is copied from PVE config dir", func(t *testing.T) {
		pveRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(pveRoot, "vzdump.conf"), []byte("mode: snapshot\n"), 0o644); err != nil {
			t.Fatalf("write vzdump.conf: %v", err)
		}
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveRoot
		cfg.VzdumpConfigPath = "vzdump.conf"
		collector := NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
		if err := collector.collectPVEVZDumpSnapshot(context.Background()); err != nil {
			t.Fatalf("collectPVEVZDumpSnapshot: %v", err)
		}
		src := filepath.Join(pveRoot, "vzdump.conf")
		if _, err := os.Stat(collector.targetPathFor(src)); err != nil {
			t.Fatalf("expected vzdump config copied: %v", err)
		}
	})
}

func TestPVEStorageScanMetadataAndSummaryEdges(t *testing.T) {
	collector := newPVECollector(t)

	if result, err := collector.preparePVEStorageScan(context.Background(), pveStorageEntry{Name: "empty"}, t.TempDir(), 0); err != nil || result != nil {
		t.Fatalf("empty storage path result=%v err=%v", result, err)
	}
	if result, err := collector.preparePVEStorageScan(context.Background(), pveStorageEntry{Name: "bad", Path: "/missing", Status: "error"}, t.TempDir(), 0); err != nil || result != nil {
		t.Fatalf("unavailable storage result=%v err=%v", result, err)
	}

	filePath := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write filePath: %v", err)
	}
	if result, err := collector.preparePVEStorageScan(context.Background(), pveStorageEntry{Name: "file", Path: filePath}, t.TempDir(), 0); err != nil || result != nil {
		t.Fatalf("file storage path result=%v err=%v", result, err)
	}

	storageDir := t.TempDir()
	baseFile := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(baseFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	if _, err := collector.preparePVEStorageScan(context.Background(), pveStorageEntry{Name: "local", Path: storageDir}, baseFile, 0); err == nil {
		t.Fatal("expected metadata directory creation failure")
	}

	validBase := t.TempDir()
	validResult, err := collector.preparePVEStorageScan(context.Background(), pveStorageEntry{Name: "local", Path: storageDir, Type: "dir", Content: "backup"}, validBase, 0)
	if err != nil {
		t.Fatalf("preparePVEStorageScan valid: %v", err)
	}
	if validResult == nil {
		t.Fatal("expected valid scan result")
	}

	dumpDir := filepath.Join(storageDir, "dump")
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		t.Fatalf("mkdir dump dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dumpDir, "backup.vma"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if err := collector.collectPVEStorageMetadataJSONStep(context.Background(), validResult, 0); err != nil {
		t.Fatalf("collectPVEStorageMetadataJSONStep valid: %v", err)
	}
	if err := collector.collectPVEStorageMetadataTextStep(context.Background(), validResult, 0); err != nil {
		t.Fatalf("collectPVEStorageMetadataTextStep valid: %v", err)
	}
	if len(validResult.DirSamples) == 0 || len(validResult.SampleFiles) == 0 || len(validResult.FileSampleLines) == 0 {
		t.Fatalf("expected metadata samples, got %+v", validResult)
	}

	missingResult := &pveStorageScanResult{
		Storage: pveStorageEntry{Name: "missing", Path: filepath.Join(t.TempDir(), "missing"), Type: "dir"},
		MetaDir: t.TempDir(),
	}
	if err := collector.collectPVEStorageMetadataJSONStep(context.Background(), missingResult, 0); err != nil {
		t.Fatalf("metadata JSON with missing path should write partial report: %v", err)
	}
	if err := collector.collectPVEStorageMetadataTextStep(context.Background(), missingResult, 0); err != nil {
		t.Fatalf("metadata text with missing path should write partial report: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelResult := &pveStorageScanResult{Storage: pveStorageEntry{Name: "cancel", Path: storageDir}, MetaDir: t.TempDir()}
	if err := collector.collectPVEStorageMetadataJSONStep(ctx, cancelResult, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("metadata JSON canceled = %v", err)
	}
	if err := collector.collectPVEStorageMetadataTextStep(ctx, cancelResult, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("metadata text canceled = %v", err)
	}

	metaFile := filepath.Join(t.TempDir(), "meta-file")
	if err := os.WriteFile(metaFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write metaFile: %v", err)
	}
	if err := collector.collectPVEStorageMetadataJSONStep(context.Background(), &pveStorageScanResult{
		Storage: pveStorageEntry{Name: "local", Path: storageDir},
		MetaDir: metaFile,
	}, 0); err == nil {
		t.Fatal("expected metadata JSON write failure")
	}
	if err := collector.collectPVEStorageMetadataTextStep(context.Background(), &pveStorageScanResult{
		Storage: pveStorageEntry{Name: "local", Path: storageDir},
		MetaDir: metaFile,
	}, 0); err != nil {
		t.Fatalf("metadata text write failure is logged, not returned: %v", err)
	}

	for _, step := range []func(context.Context, *pveStorageScanResult, time.Duration) error{
		collector.collectPVEStorageMetadataJSONStep,
		collector.collectPVEStorageMetadataTextStep,
		collector.collectPVEStorageBackupAnalysisStep,
	} {
		if err := step(context.Background(), nil, 0); err != nil {
			t.Fatalf("nil storage scan result returned error: %v", err)
		}
		if err := step(context.Background(), &pveStorageScanResult{SkipRemaining: true}, 0); err != nil {
			t.Fatalf("skipped storage scan result returned error: %v", err)
		}
	}

	if err := collector.writePVEStorageSummary(ctx, []pveStorageEntry{{Name: "local"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("writePVEStorageSummary canceled = %v", err)
	}
	if err := collector.writePVEStorageSummary(context.Background(), nil); err != nil {
		t.Fatalf("writePVEStorageSummary empty: %v", err)
	}

	if _, err := collector.sampleMetadataFileStats(ctx, storageDir, 3, 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("sampleMetadataFileStats canceled = %v", err)
	}
	brokenLink := filepath.Join(storageDir, "broken-sample")
	if err := os.Symlink(filepath.Join(storageDir, "missing-target"), brokenLink); err != nil {
		t.Fatalf("create broken symlink: %v", err)
	}
	if _, err := collector.sampleMetadataFileStats(context.Background(), storageDir, 3, 10, 0); err != nil {
		t.Fatalf("sampleMetadataFileStats with broken symlink: %v", err)
	}
	if err := collector.writeDatastoreMetadataText(t.TempDir(), pveStorageEntry{Name: "local", Path: storageDir}, []string{""}, nil, "ok", nil, []string{"sample"}, nil); err != nil {
		t.Fatalf("writeDatastoreMetadataText empty rel sample: %v", err)
	}
	if got := relativeDepth("/tmp/base", "/tmp/base/."); got != 0 {
		t.Fatalf("relativeDepth dot path = %d", got)
	}
}

func TestPVEStorageFormattingParsingAndPatternEdges(t *testing.T) {
	collector := newPVECollector(t)
	storage := pveStorageEntry{
		Name:    "local",
		Path:    "/var/lib/vz",
		Active:  pveTestBool(true),
		Enabled: pveTestBool(false),
		Status:  "weird",
	}
	if got := collector.formatPVEStorageRuntime(storage); got != " (active=true enabled=false status=weird)" {
		t.Fatalf("formatPVEStorageRuntime = %q", got)
	}
	if got := collector.formatPVEStorageRuntime(pveStorageEntry{}); got != "" {
		t.Fatalf("empty formatPVEStorageRuntime = %q", got)
	}
	for _, tt := range []struct {
		storage pveStorageEntry
		want    string
	}{
		{storage: pveStorageEntry{Enabled: pveTestBool(false)}, want: "enabled=false"},
		{storage: pveStorageEntry{Active: pveTestBool(false)}, want: "active=false"},
		{storage: pveStorageEntry{Status: "available"}, want: ""},
		{storage: pveStorageEntry{Status: "inactive"}, want: "status=inactive"},
		{storage: pveStorageEntry{Status: "error"}, want: "status=error"},
	} {
		if got := collector.pveStorageUnavailableReason(tt.storage); got != tt.want {
			t.Fatalf("pveStorageUnavailableReason(%+v) = %q, want %q", tt.storage, got, tt.want)
		}
	}

	parsed, err := parseNodeStorageList([]byte(`[
		{"storage":"yes-off","active":"yes","enabled":"off"},
		{"storage":"empty","active":"","enabled":"maybe"},
		{"storage":"array","active":[],"enabled":{}}
	]`))
	if err != nil {
		t.Fatalf("parseNodeStorageList: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("parsed storage count = %d", len(parsed))
	}
	if parsed[0].Active == nil || !*parsed[0].Active || parsed[0].Enabled == nil || *parsed[0].Enabled {
		t.Fatalf("yes/off bool parsing failed: %+v", parsed[0])
	}
	if parsed[1].Active != nil || parsed[1].Enabled != nil || parsed[2].Active != nil || parsed[2].Enabled != nil {
		t.Fatalf("invalid bool values should be nil: %+v", parsed)
	}

	if matchPattern("backup.vma", "[") {
		t.Fatal("invalid glob pattern should not match")
	}

	analysisDir := t.TempDir()
	writers := collector.newPatternWriters(pveStorageEntry{Name: "local", Path: "/storage"}, analysisDir, []string{"", "*.vma", "*.vma"})
	if len(writers) != 1 {
		t.Fatalf("expected one unique writer, got %d", len(writers))
	}
	for _, writer := range writers {
		if err := writer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
	}
	if _, err := newPatternWriter("local", "/storage", filepath.Join(t.TempDir(), "missing"), "*.vma", false); err == nil {
		t.Fatal("expected newPatternWriter open failure for missing analysis dir")
	}
	if writers := collector.newPatternWriters(pveStorageEntry{Name: "local", Path: "/storage"}, filepath.Join(t.TempDir(), "missing"), []string{"*.vma"}); len(writers) != 0 {
		t.Fatalf("expected no writers when analysis dir is missing, got %d", len(writers))
	}

	outside := filepath.Join(t.TempDir(), "outside.vma")
	if err := os.WriteFile(outside, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("stat outside file: %v", err)
	}
	writer, err := newPatternWriter("local", filepath.Join(t.TempDir(), "storage"), t.TempDir(), "*.vma", false)
	if err != nil {
		t.Fatalf("newPatternWriter: %v", err)
	}
	if err := writer.Write(outside, info); err != nil {
		t.Fatalf("writer.Write outside: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close outside: %v", err)
	}
	content, err := os.ReadFile(writer.filePath)
	if err != nil {
		t.Fatalf("read writer output: %v", err)
	}
	if !strings.Contains(string(content), outside) {
		t.Fatalf("expected fallback absolute path in writer output, got %q", string(content))
	}

	flushWriter, err := newPatternWriter("local", "/storage", t.TempDir(), "*.vma", false)
	if err != nil {
		t.Fatalf("newPatternWriter flush: %v", err)
	}
	if err := flushWriter.file.Close(); err != nil {
		t.Fatalf("close underlying file: %v", err)
	}
	if err := flushWriter.Close(); err == nil {
		t.Fatal("expected flush/close error after closing underlying file")
	}
	closeOnlyFile, err := os.Create(filepath.Join(t.TempDir(), "close-only"))
	if err != nil {
		t.Fatalf("create close-only file: %v", err)
	}
	if err := closeOnlyFile.Close(); err != nil {
		t.Fatalf("close close-only file: %v", err)
	}
	if err := (&patternWriter{file: closeOnlyFile}).Close(); err == nil {
		t.Fatal("expected close error from already closed file")
	}

	if err := collector.writePatternSummary(pveStorageEntry{Name: "local"}, filepath.Join(t.TempDir(), "missing"), nil, 0, 0); err == nil {
		t.Fatal("expected writePatternSummary open failure")
	}
	destBase := filepath.Join(t.TempDir(), "file-base")
	if err := os.WriteFile(destBase, []byte("x"), 0o644); err != nil {
		t.Fatalf("write destBase: %v", err)
	}
	if err := collector.copyBackupSample(context.Background(), outside, filepath.Join(destBase, "samples"), "sample"); err == nil {
		t.Fatal("expected copyBackupSample ensureDir failure")
	}
}

func TestCollectDetailedPVEBackupsAdditionalEdges(t *testing.T) {
	t.Run("blank include patterns skip writer creation", func(t *testing.T) {
		storagePath := t.TempDir()
		cfg := GetDefaultCollectorConfig()
		cfg.PxarFileIncludePatterns = []string{"   "}
		collector := NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, t.TempDir(), 0); err != nil {
			t.Fatalf("collectDetailedPVEBackups blank patterns: %v", err)
		}
	})

	t.Run("broken symlink stat errors are skipped", func(t *testing.T) {
		storagePath := t.TempDir()
		if err := os.Symlink(filepath.Join(storagePath, "missing-target"), filepath.Join(storagePath, "broken.vma")); err != nil {
			t.Fatalf("symlink broken backup: %v", err)
		}
		collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), "pve", false)
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, t.TempDir(), 0); err != nil {
			t.Fatalf("collectDetailedPVEBackups broken symlink: %v", err)
		}
	})

	t.Run("sample directory creation failures are logged", func(t *testing.T) {
		storagePath := t.TempDir()
		if err := os.WriteFile(filepath.Join(storagePath, "backup.vma"), []byte("payload"), 0o644); err != nil {
			t.Fatalf("write backup: %v", err)
		}
		tempFile := filepath.Join(t.TempDir(), "temp-file")
		if err := os.WriteFile(tempFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		cfg := GetDefaultCollectorConfig()
		cfg.BackupSmallPVEBackups = true
		cfg.MaxPVEBackupSizeBytes = 1024
		cfg.PVEBackupIncludePattern = "backup"
		collector := NewCollector(newTestLogger(), cfg, tempFile, "pve", false)
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, t.TempDir(), 0); err != nil {
			t.Fatalf("collectDetailedPVEBackups sample dir failure: %v", err)
		}
	})

	t.Run("analysis and summary write failures", func(t *testing.T) {
		storagePath := t.TempDir()
		if err := os.WriteFile(filepath.Join(storagePath, "backup.vma"), []byte("payload"), 0o644); err != nil {
			t.Fatalf("write backup: %v", err)
		}
		collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), "pve", false)

		metaFile := filepath.Join(t.TempDir(), "meta-file")
		if err := os.WriteFile(metaFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write meta file: %v", err)
		}
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, metaFile, 0); err == nil {
			t.Fatal("expected analysis directory creation error")
		}

		metaDir := t.TempDir()
		analysisDir := filepath.Join(metaDir, "backup_analysis")
		if err := os.MkdirAll(filepath.Join(analysisDir, "local_backup_summary.txt"), 0o755); err != nil {
			t.Fatalf("mkdir summary blocker: %v", err)
		}
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, metaDir, 0); err == nil {
			t.Fatal("expected summary write error")
		}
	})

	t.Run("exclude patterns skip roots and files", func(t *testing.T) {
		storagePath := t.TempDir()
		if err := os.WriteFile(filepath.Join(storagePath, "backup.vma"), []byte("payload"), 0o644); err != nil {
			t.Fatalf("write backup: %v", err)
		}
		cfg := GetDefaultCollectorConfig()
		cfg.ExcludePatterns = []string{"backup.vma"}
		collector := NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, t.TempDir(), 0); err != nil {
			t.Fatalf("collectDetailedPVEBackups file exclude: %v", err)
		}

		cfg.ExcludePatterns = []string{storagePath}
		collector = NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
		if err := collector.collectDetailedPVEBackups(context.Background(), pveStorageEntry{Name: "local", Path: storagePath}, t.TempDir(), 0); err != nil {
			t.Fatalf("collectDetailedPVEBackups root exclude: %v", err)
		}
	})
}

func TestPVECephAdditionalBranches(t *testing.T) {
	t.Run("ceph config paths ignore blank custom path", func(t *testing.T) {
		root := t.TempDir()
		cfg := GetDefaultCollectorConfig()
		cfg.SystemRootPrefix = root
		cfg.PVEConfigPath = "/etc/pve"
		cfg.CephConfigPath = "   "
		collector := NewCollector(newTestLogger(), cfg, t.TempDir(), "pve", false)
		paths := collector.cephConfigPaths()
		if len(paths) != 2 {
			t.Fatalf("expected only default ceph paths, got %+v", paths)
		}
	})

	for _, tt := range []struct {
		name       string
		pvesmOK    bool
		cephOK     bool
		pgrepOK    bool
		wantResult bool
	}{
		{name: "storage configured", pvesmOK: true, wantResult: true},
		{name: "status available", cephOK: true, wantResult: true},
		{name: "process running", pgrepOK: true, wantResult: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := GetDefaultCollectorConfig()
			cfg.SystemRootPrefix = root
			cfg.PVEConfigPath = "/etc/pve"
			collector := NewCollectorWithDeps(newTestLogger(), cfg, t.TempDir(), "pve", false, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					switch cmd {
					case "systemctl":
						return "/usr/bin/systemctl", nil
					case "pvesm":
						if tt.pvesmOK {
							return "/usr/bin/pvesm", nil
						}
					case "ceph":
						if tt.cephOK {
							return "/usr/bin/ceph", nil
						}
					case "pgrep":
						if tt.pgrepOK {
							return "/usr/bin/pgrep", nil
						}
					}
					return "", errors.New("missing")
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					switch name {
					case "systemctl":
						return nil, errors.New("inactive")
					case "pvesm":
						return []byte("rbd cephfs"), nil
					case "ceph":
						return []byte("HEALTH_OK"), nil
					case "pgrep":
						return []byte("1234"), nil
					default:
						return nil, errors.New("unexpected command")
					}
				},
			})
			if got := collector.isCephConfigured(context.Background()); got != tt.wantResult {
				t.Fatalf("isCephConfigured = %v, want %v", got, tt.wantResult)
			}
		})
	}

	t.Run("config snapshot returns ensureDir error after detection", func(t *testing.T) {
		cephDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(cephDir, "ceph.conf"), []byte("fsid = abc"), 0o644); err != nil {
			t.Fatalf("write ceph.conf: %v", err)
		}
		tempFile := filepath.Join(t.TempDir(), "temp-file")
		if err := os.WriteFile(tempFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		cfg := GetDefaultCollectorConfig()
		cfg.CephConfigPath = cephDir
		collector := NewCollector(newTestLogger(), cfg, tempFile, "pve", false)
		if err := collector.collectPVECephConfigSnapshot(context.Background()); err == nil {
			t.Fatal("expected ceph config ensureDir error")
		}
	})

	t.Run("runtime propagates output write error", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				if cmd == "ceph" {
					return "/usr/bin/ceph", nil
				}
				return "", errors.New("missing")
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("ok"), nil
			},
		})
		cephDir := collector.pveCephDir()
		if err := os.MkdirAll(filepath.Join(cephDir, "ceph_osd_df.txt"), 0o755); err != nil {
			t.Fatalf("mkdir ceph output blocker: %v", err)
		}
		if err := collector.collectPVECephRuntime(context.Background()); err == nil {
			t.Fatal("expected ceph runtime output write error")
		}
	})

	t.Run("context and ensureDir errors", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		collector := newPVECollector(t)
		if err := collector.collectPVECephConfigSnapshot(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectPVECephConfigSnapshot canceled = %v", err)
		}
		if err := collector.collectPVECephRuntime(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectPVECephRuntime canceled = %v", err)
		}

		tempFile := filepath.Join(t.TempDir(), "temp-file")
		if err := os.WriteFile(tempFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		cfg := GetDefaultCollectorConfig()
		collector = NewCollector(newTestLogger(), cfg, tempFile, "pve", false)
		if err := collector.collectPVECephRuntime(context.Background()); err == nil {
			t.Fatal("expected ceph runtime ensureDir error")
		}
	})
}

func TestPVEFinalizeAggregateAndVersionErrorBranches(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "temp-file")
	if err := os.WriteFile(tempFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	for _, tt := range []struct {
		name string
		run  func(*Collector, context.Context) error
	}{
		{name: "aliases", run: (*Collector).createPVECoreAliases},
		{name: "backup history aggregate", run: (*Collector).createPVEBackupHistoryAggregate},
		{name: "replication aggregate", run: (*Collector).createPVEReplicationAggregate},
		{name: "version info", run: (*Collector).createPVEVersionInfo},
	} {
		t.Run(tt.name+" ensureDir", func(t *testing.T) {
			collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempFile, "pve", false)
			if err := tt.run(collector, context.Background()); err == nil {
				t.Fatal("expected ensureDir error")
			}
		})
	}

	collector := newPVEHappyCommandCollector(t)
	baseInfoDir := collector.pveInfoDir()
	if err := os.MkdirAll(filepath.Join(baseInfoDir, "pve_version.txt"), 0o755); err != nil {
		t.Fatalf("mkdir version blocker: %v", err)
	}
	if err := collector.createPVEVersionInfo(context.Background()); err == nil {
		t.Fatal("expected version info write error")
	}
	if err := collector.writePVEVersionInfo(context.Background(), baseInfoDir); err == nil {
		t.Fatal("expected writePVEVersionInfo error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := collector.aggregateBackupHistory(ctx, t.TempDir(), filepath.Join(t.TempDir(), "out.json")); !errors.Is(err, context.Canceled) {
		t.Fatalf("aggregateBackupHistory canceled = %v", err)
	}
	if err := collector.aggregateReplicationStatus(ctx, t.TempDir(), filepath.Join(t.TempDir(), "out.json")); !errors.Is(err, context.Canceled) {
		t.Fatalf("aggregateReplicationStatus canceled = %v", err)
	}

	jobsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(jobsDir, "node_backup_history.json"), 0o755); err != nil {
		t.Fatalf("mkdir history dir entry: %v", err)
	}
	if err := collector.aggregateBackupHistory(context.Background(), jobsDir, filepath.Join(t.TempDir(), "history.json")); err != nil {
		t.Fatalf("aggregateBackupHistory dir entry: %v", err)
	}

	repDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repDir, "node_replication_status.json"), 0o755); err != nil {
		t.Fatalf("mkdir replication dir entry: %v", err)
	}
	if err := collector.aggregateReplicationStatus(context.Background(), repDir, filepath.Join(t.TempDir(), "replication.json")); err != nil {
		t.Fatalf("aggregateReplicationStatus dir entry: %v", err)
	}
}

func TestPVEClusterDetectionAdditionalBranches(t *testing.T) {
	t.Run("multiple nodes with corosync file", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "", errors.New("should not need commands")
			},
		})
		pveRoot := collector.config.PVEConfigPath
		collector.config.CorosyncConfigPath = filepath.Join(pveRoot, "corosync.conf")
		if err := os.WriteFile(filepath.Join(pveRoot, "corosync.conf"), []byte("totem {}"), 0o644); err != nil {
			t.Fatalf("write corosync.conf: %v", err)
		}
		for _, node := range []string{"node1", "node2"} {
			if err := os.MkdirAll(filepath.Join(pveRoot, "nodes", node), 0o755); err != nil {
				t.Fatalf("mkdir node %s: %v", node, err)
			}
		}
		clustered, err := collector.isClusteredPVE(context.Background())
		if err != nil {
			t.Fatalf("isClusteredPVE: %v", err)
		}
		if !clustered {
			t.Fatal("expected clustered from nodes directory with corosync file")
		}
	})

	t.Run("active corosync service with corosync file", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				if cmd == "systemctl" {
					return "/usr/bin/systemctl", nil
				}
				return "", errors.New("missing")
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("active"), nil
			},
		})
		collector.config.CorosyncConfigPath = filepath.Join(collector.config.PVEConfigPath, "corosync.conf")
		if err := os.WriteFile(filepath.Join(collector.config.PVEConfigPath, "corosync.conf"), []byte("totem {}"), 0o644); err != nil {
			t.Fatalf("write corosync.conf: %v", err)
		}
		clustered, err := collector.isClusteredPVE(context.Background())
		if err != nil {
			t.Fatalf("isClusteredPVE: %v", err)
		}
		if !clustered {
			t.Fatal("expected clustered from active corosync service with config")
		}
	})

	t.Run("pvecm generic failure is returned", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				switch cmd {
				case "systemctl":
					return "", errors.New("missing systemctl")
				case "pvecm":
					return "/usr/bin/pvecm", nil
				default:
					return "", errors.New("missing")
				}
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("generic failure"), errors.New("exit status 1")
			},
		})
		if _, err := collector.isClusteredPVE(context.Background()); err == nil {
			t.Fatal("expected pvecm generic failure")
		}
	})

	if isPvecmMissingCorosyncConfig("") {
		t.Fatal("empty pvecm output should not be treated as missing corosync config")
	}
	if newPVECollector(t).isServiceActive(context.Background(), "") {
		t.Fatal("empty service name should be inactive")
	}
}
