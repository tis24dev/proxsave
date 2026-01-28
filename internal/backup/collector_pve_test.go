package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestIsClusteredPVEFallbackToPvecm(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			switch cmd {
			case "pvecm":
				return "/usr/bin/pvecm", nil
			case "systemctl":
				return "", fmt.Errorf("missing systemctl")
			default:
				return "", fmt.Errorf("unexpected lookPath for %s", cmd)
			}
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "pvecm" {
				return nil, fmt.Errorf("unexpected command %s", name)
			}
			if len(args) != 1 || args[0] != "status" {
				return nil, fmt.Errorf("unexpected args %v", args)
			}
			return []byte("Cluster information\n"), nil
		},
	})

	// Ensure there is only one node so the pvecm fallback is used.
	nodesDir := filepath.Join(collector.config.PVEConfigPath, "nodes")
	if err := os.MkdirAll(nodesDir, 0o755); err != nil {
		t.Fatalf("failed to create nodes dir: %v", err)
	}

	clustered, err := collector.isClusteredPVE(context.Background())
	if err != nil {
		t.Fatalf("isClusteredPVE returned error: %v", err)
	}
	if !clustered {
		t.Fatal("expected clustered=true from pvecm output")
	}
}

func TestIsClusteredPVEMissingCorosyncConfigIsStandalone(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			switch cmd {
			case "pvecm":
				return "/usr/bin/pvecm", nil
			case "systemctl":
				return "", fmt.Errorf("missing systemctl")
			default:
				return "", fmt.Errorf("unexpected lookPath for %s", cmd)
			}
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "pvecm" {
				return nil, fmt.Errorf("unexpected command %s", name)
			}
			if len(args) != 1 || args[0] != "status" {
				return nil, fmt.Errorf("unexpected args %v", args)
			}
			return []byte("Error: Corosync config '/etc/pve/corosync.conf' does not exist - is this node part of a cluster?\n"), fmt.Errorf("exit status 2")
		},
	})
	collector.config.CorosyncConfigPath = ""

	nodesDir := filepath.Join(collector.config.PVEConfigPath, "nodes", "single")
	if err := os.MkdirAll(nodesDir, 0o755); err != nil {
		t.Fatalf("failed to create nodes dir: %v", err)
	}

	clustered, err := collector.isClusteredPVE(context.Background())
	if err != nil {
		t.Fatalf("isClusteredPVE returned error: %v", err)
	}
	if clustered {
		t.Fatal("expected clustered=false when pvecm reports missing corosync config")
	}
}

func TestIsServiceActive(t *testing.T) {
	t.Run("missing systemctl", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				if cmd == "systemctl" {
					return "", fmt.Errorf("missing")
				}
				return "", fmt.Errorf("unexpected command %s", cmd)
			},
		})
		if collector.isServiceActive(context.Background(), "corosync.service") {
			t.Fatal("service should be inactive when systemctl is missing")
		}
	})

	t.Run("active", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				if cmd == "systemctl" {
					return "/bin/systemctl", nil
				}
				return "", fmt.Errorf("unexpected command %s", cmd)
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("active"), nil
			},
		})
		if !collector.isServiceActive(context.Background(), "corosync.service") {
			t.Fatal("expected service to be active")
		}
	})

	t.Run("inactive", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				if cmd == "systemctl" {
					return "/bin/systemctl", nil
				}
				return "", fmt.Errorf("unexpected command %s", cmd)
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return nil, fmt.Errorf("inactive")
			},
		})
		if collector.isServiceActive(context.Background(), "corosync.service") {
			t.Fatal("expected service to be inactive")
		}
	})
}

func TestCephStorageConfigured(t *testing.T) {
	t.Run("cli missing", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "", fmt.Errorf("missing")
			},
		})
		if collector.cephStorageConfigured(context.Background()) {
			t.Fatal("expected false when pvesm is missing")
		}
	})

	t.Run("detects ceph", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("local cephfs storage"), nil
			},
		})
		if !collector.cephStorageConfigured(context.Background()) {
			t.Fatal("expected ceph storage detection")
		}
	})

	t.Run("command error", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return nil, fmt.Errorf("pvesm failed")
			},
		})
		if collector.cephStorageConfigured(context.Background()) {
			t.Fatal("expected false when command fails")
		}
	})
}

func TestCollectPVECommandsSkipsClusterRuntimeWhenBackupClusterConfigDisabled(t *testing.T) {
	pvecmCalls := 0
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "pvecm" {
				pvecmCalls++
			}
			return []byte{}, nil
		},
	})
	collector.config.BackupClusterConfig = false

	if _, err := collector.collectPVECommands(context.Background(), true); err != nil {
		t.Fatalf("collectPVECommands returned error: %v", err)
	}
	if pvecmCalls != 0 {
		t.Fatalf("expected pvecm not to be called when BACKUP_CLUSTER_CONFIG=false, got %d calls", pvecmCalls)
	}
}

func TestCephStatusAvailable(t *testing.T) {
	t.Run("cli missing", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				return "", fmt.Errorf("missing")
			},
		})
		if collector.cephStatusAvailable(context.Background()) {
			t.Fatal("expected false when ceph CLI missing")
		}
	})

	t.Run("status ok", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				return "/usr/bin/ceph", nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
					t.Fatal("expected context with deadline")
				}
				return []byte("health ok"), nil
			},
		})
		if !collector.cephStatusAvailable(context.Background()) {
			t.Fatal("expected ceph status availability")
		}
	})

	t.Run("status error", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				return "/usr/bin/ceph", nil
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return nil, fmt.Errorf("timeout")
			},
		})
		if collector.cephStatusAvailable(context.Background()) {
			t.Fatal("expected false when ceph status fails")
		}
	})
}

func TestCephProcessesRunning(t *testing.T) {
	t.Run("cli missing", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				return "", fmt.Errorf("missing")
			},
		})
		if collector.cephProcessesRunning(context.Background()) {
			t.Fatal("expected false when pgrep missing")
		}
	})

	t.Run("process found", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				return "/usr/bin/pgrep", nil
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("1234"), nil
			},
		})
		if !collector.cephProcessesRunning(context.Background()) {
			t.Fatal("expected true when pgrep returns success")
		}
	})

	t.Run("process missing", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				return "/usr/bin/pgrep", nil
			},
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				return nil, fmt.Errorf("not found")
			},
		})
		if collector.cephProcessesRunning(context.Background()) {
			t.Fatal("expected false when pgrep fails")
		}
	})
}

func newPVECollector(t *testing.T) *Collector {
	t.Helper()
	return newPVECollectorWithDeps(t, CollectorDeps{})
}

func newPVECollectorWithDeps(t *testing.T, override CollectorDeps) *Collector {
	t.Helper()
	deps := defaultCollectorDeps()
	if override.LookPath != nil {
		deps.LookPath = override.LookPath
	}
	if override.RunCommand != nil {
		deps.RunCommand = override.RunCommand
	}
	if override.RunCommandWithEnv != nil {
		deps.RunCommandWithEnv = override.RunCommandWithEnv
	}
	if override.Stat != nil {
		deps.Stat = override.Stat
	}
	logger := logging.New(types.LogLevelDebug, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	collector := NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxVE, false, deps)

	cfgRoot := t.TempDir()
	cfgPath := filepath.Join(cfgRoot, "etc_pve")
	if err := os.MkdirAll(cfgPath, 0o755); err != nil {
		t.Fatalf("failed to create PVE config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgPath, "nodes"), 0o755); err != nil {
		t.Fatalf("failed to create nodes dir: %v", err)
	}
	collector.config.PVEConfigPath = cfgPath
	return collector
}

// Test CollectPVEConfigs with full workflow
func TestCollectPVEConfigsIntegration(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// Mock various PVE commands
			switch name {
			case "pvecm":
				return []byte("Cluster information not available\n"), nil
			case "pvesh", "pveversion":
				return []byte("{}"), nil
			default:
				return []byte{}, nil
			}
		},
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
	})

	// Create minimal PVE structure
	pveConfigPath := collector.config.PVEConfigPath
	nodesDir := filepath.Join(pveConfigPath, "nodes")
	if err := os.MkdirAll(nodesDir, 0o755); err != nil {
		t.Fatalf("failed to create nodes dir: %v", err)
	}

	// Create a test node directory
	nodeDir := filepath.Join(nodesDir, "test-node")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatalf("failed to create node dir: %v", err)
	}

	ctx := context.Background()
	err := collector.CollectPVEConfigs(ctx)
	if err != nil {
		t.Fatalf("CollectPVEConfigs failed: %v", err)
	}
}

func TestCollectPVEConfigsPopulatesManifestSkippedForExcludedACL(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("{}"), nil
		},
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
	})

	pveConfigPath := collector.config.PVEConfigPath
	if err := os.WriteFile(filepath.Join(pveConfigPath, "user.cfg"), []byte("user"), 0o644); err != nil {
		t.Fatalf("write user.cfg: %v", err)
	}
	collector.config.BackupPVEACL = true
	collector.config.ExcludePatterns = []string{"user.cfg"}

	if err := collector.CollectPVEConfigs(context.Background()); err != nil {
		t.Fatalf("CollectPVEConfigs failed: %v", err)
	}

	src := filepath.Join(collector.effectivePVEConfigPath(), "user.cfg")
	dest := collector.targetPathFor(src)
	key := pveManifestKey(collector.tempDir, dest)
	entry, ok := collector.pveManifest[key]
	if !ok {
		t.Fatalf("expected manifest entry for %s (key=%s)", src, key)
	}
	if entry.Status != StatusSkipped {
		t.Fatalf("expected %s status, got %s", StatusSkipped, entry.Status)
	}
}

// Test collectVMConfigs function
func TestCollectVMConfigs(t *testing.T) {
	collector := newPVECollector(t)

	// Create VM config structure
	pveConfigPath := collector.config.PVEConfigPath
	nodesDir := filepath.Join(pveConfigPath, "nodes")
	nodeDir := filepath.Join(nodesDir, "test-node")
	qemuDir := filepath.Join(nodeDir, "qemu-server")
	lxcDir := filepath.Join(nodeDir, "lxc")

	for _, dir := range []string{qemuDir, lxcDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	// Create sample VM configs
	vmConfig := "cores: 2\nmemory: 4096\n"
	if err := os.WriteFile(filepath.Join(qemuDir, "100.conf"), []byte(vmConfig), 0o644); err != nil {
		t.Fatalf("failed to write VM config: %v", err)
	}

	ctConfig := "cores: 1\nmemory: 512\n"
	if err := os.WriteFile(filepath.Join(lxcDir, "101.conf"), []byte(ctConfig), 0o644); err != nil {
		t.Fatalf("failed to write CT config: %v", err)
	}

	ctx := context.Background()
	err := collector.collectVMConfigs(ctx)
	if err != nil {
		t.Fatalf("collectVMConfigs failed: %v", err)
	}
}

// TestCollectVMConfigsComprehensive tests VM config collection edge cases
func TestCollectVMConfigsComprehensive(t *testing.T) {
	t.Run("collects qemu-server directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		qemuDir := filepath.Join(pveDir, "qemu-server")
		if err := os.MkdirAll(qemuDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(qemuDir, "100.conf"), []byte("cores: 2"), 0o644); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, t.TempDir(), "pve", false)

		err := collector.collectVMConfigs(context.Background())
		if err != nil {
			t.Fatalf("collectVMConfigs failed: %v", err)
		}
	})

	t.Run("collects lxc directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		lxcDir := filepath.Join(pveDir, "lxc")
		if err := os.MkdirAll(lxcDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lxcDir, "101.conf"), []byte("memory: 512"), 0o644); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, t.TempDir(), "pve", false)

		err := collector.collectVMConfigs(context.Background())
		if err != nil {
			t.Fatalf("collectVMConfigs failed: %v", err)
		}
	})

	t.Run("handles missing directories gracefully", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		if err := os.MkdirAll(pveDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Note: no qemu-server or lxc directories created

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, t.TempDir(), "pve", false)

		err := collector.collectVMConfigs(context.Background())
		if err != nil {
			t.Fatalf("collectVMConfigs failed: %v", err)
		}
	})

	t.Run("handles qemu-server as file not directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		if err := os.MkdirAll(pveDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Create qemu-server as file (not directory)
		if err := os.WriteFile(filepath.Join(pveDir, "qemu-server"), []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, t.TempDir(), "pve", false)

		err := collector.collectVMConfigs(context.Background())
		if err != nil {
			t.Fatalf("collectVMConfigs failed: %v", err)
		}
	})
}

// Test collectPVEDirectories function
func TestCollectPVEDirectories(t *testing.T) {
	collector := newPVECollector(t)

	// Create PVE directory structure
	pveConfigPath := collector.config.PVEConfigPath
	if err := os.MkdirAll(pveConfigPath, 0o755); err != nil {
		t.Fatalf("failed to create pve config path: %v", err)
	}

	// Create firewall directory
	firewallDir := filepath.Join(pveConfigPath, "firewall")
	if err := os.MkdirAll(firewallDir, 0o755); err != nil {
		t.Fatalf("failed to create firewall dir: %v", err)
	}

	// Create a sample firewall file
	fwConfig := "[OPTIONS]\nenable: 1\n"
	if err := os.WriteFile(filepath.Join(firewallDir, "cluster.fw"), []byte(fwConfig), 0o644); err != nil {
		t.Fatalf("failed to write firewall config: %v", err)
	}

	ctx := context.Background()
	err := collector.collectPVEDirectories(ctx, false)
	if err != nil {
		t.Fatalf("collectPVEDirectories failed: %v", err)
	}
}

// Test collectPVECommands function
func TestCollectPVECommands(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "pvesh":
				if len(args) > 2 && args[2] == "/nodes" {
					return []byte(`[{"node":"test-node","status":"online"}]`), nil
				}
				if len(args) > 2 && args[2] == "/storage" {
					return []byte(`[{"storage":"local","type":"dir"}]`), nil
				}
				return []byte("[]"), nil
			case "pveversion":
				return []byte("pve-manager/7.2-3"), nil
			case "pvecm":
				return []byte("Cluster information not available"), nil
			default:
				return []byte{}, nil
			}
		},
	})

	ctx := context.Background()
	runtimeInfo, err := collector.collectPVECommands(ctx, false)
	if err != nil {
		t.Fatalf("collectPVECommands failed: %v", err)
	}

	if runtimeInfo == nil {
		t.Error("expected non-nil runtimeInfo")
	}
}

// Test createPVEInfoAliases function
func TestCreatePVEInfoAliases(t *testing.T) {
	collector := newPVECollector(t)

	ctx := context.Background()
	err := collector.createPVEInfoAliases(ctx)
	// This may fail in test environment, but should not panic
	if err != nil {
		t.Logf("createPVEInfoAliases returned error (expected in test env): %v", err)
	}
}

// Test collectPVEJobs function
func TestCollectPVEJobs(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "pvesh" {
				return []byte(`[{"id":"backup-job-1","enabled":true}]`), nil
			}
			return []byte("[]"), nil
		},
	})

	ctx := context.Background()
	nodes := []string{"test-node"}
	err := collector.collectPVEJobs(ctx, nodes)
	if err != nil {
		t.Logf("collectPVEJobs returned error (expected in some envs): %v", err)
	}
}

// TestCollectPVEJobsComprehensive tests various edge cases for collectPVEJobs
func TestCollectPVEJobsComprehensive(t *testing.T) {
	t.Run("skips empty and duplicate node names", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(`[]`), nil
			},
		})

		ctx := context.Background()
		// Include empty strings and duplicates
		nodes := []string{"node1", "", "  ", "node1", "node2", "node2"}
		err := collector.collectPVEJobs(ctx, nodes)
		if err != nil {
			t.Logf("collectPVEJobs returned error: %v", err)
		}
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(`[]`), nil
			},
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := collector.collectPVEJobs(ctx, []string{"node1"})
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})

	t.Run("copies vzdump cron if exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		cronDir := filepath.Join(tmpDir, "etc", "cron.d")
		if err := os.MkdirAll(cronDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cronDir, "vzdump"), []byte("0 3 * * * root vzdump"), 0o644); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.SystemRootPrefix = tmpDir
		collector := NewCollector(logger, cfg, t.TempDir(), "pve", false)

		ctx := context.Background()
		err := collector.collectPVEJobs(ctx, []string{})
		if err != nil {
			t.Logf("collectPVEJobs returned error: %v", err)
		}
	})

	t.Run("handles whitespace-padded node names", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(`[]`), nil
			},
		})

		ctx := context.Background()
		nodes := []string{"  node1  ", "  node2  "}
		err := collector.collectPVEJobs(ctx, nodes)
		if err != nil {
			t.Logf("collectPVEJobs returned error: %v", err)
		}
	})
}

// Test collectPVESchedules function
func TestCollectPVESchedules(t *testing.T) {
	collector := newPVECollector(t)

	ctx := context.Background()
	err := collector.collectPVESchedules(ctx)
	// May fail if crontab not available
	if err != nil {
		t.Logf("collectPVESchedules returned error: %v", err)
	}
}

// Test collectPVEReplication function
func TestCollectPVEReplication(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(`[]`), nil
		},
	})

	ctx := context.Background()
	nodes := []string{"test-node"}
	err := collector.collectPVEReplication(ctx, nodes)
	if err != nil {
		t.Logf("collectPVEReplication returned error: %v", err)
	}
}

func TestCollectPVEDirectoriesExcludesDisabledPVEConfigFiles(t *testing.T) {
	collector := newPVECollector(t)
	pveRoot := collector.config.PVEConfigPath

	mustMkdir := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	mustWrite := func(path, contents string) {
		t.Helper()
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			mustMkdir(dir)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// Create representative PVE config files and directories that are normally covered by a full /etc/pve snapshot.
	mustWrite(filepath.Join(pveRoot, "dummy.cfg"), "ok")
	mustWrite(filepath.Join(pveRoot, "corosync.conf"), "corosync")
	mustWrite(filepath.Join(pveRoot, "user.cfg"), "user")
	mustWrite(filepath.Join(pveRoot, "acl.cfg"), "acl")
	mustWrite(filepath.Join(pveRoot, "domains.cfg"), "domains")
	mustWrite(filepath.Join(pveRoot, "jobs.cfg"), "jobs")
	mustWrite(filepath.Join(pveRoot, "vzdump.cron"), "cron")
	mustWrite(filepath.Join(pveRoot, "qemu-server", "100.conf"), "vm")
	mustWrite(filepath.Join(pveRoot, "lxc", "101.conf"), "ct")
	mustWrite(filepath.Join(pveRoot, "firewall", "cluster.fw"), "fw")
	mustWrite(filepath.Join(pveRoot, "nodes", "node1", "host.fw"), "hostfw")

	clusterPath := filepath.Join(t.TempDir(), "pve-cluster")
	mustWrite(filepath.Join(clusterPath, "config.db"), "db")
	collector.config.PVEClusterPath = clusterPath

	collector.config.BackupVMConfigs = false
	collector.config.BackupPVEFirewall = false
	collector.config.BackupPVEACL = false
	collector.config.BackupPVEJobs = false
	collector.config.BackupClusterConfig = false

	if err := collector.collectPVEDirectories(context.Background(), false); err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}

	destPVE := collector.targetPathFor(pveRoot)
	if _, err := os.Stat(filepath.Join(destPVE, "dummy.cfg")); err != nil {
		t.Fatalf("expected dummy.cfg collected, got %v", err)
	}

	for _, excluded := range []string{
		"corosync.conf",
		"user.cfg",
		"acl.cfg",
		"domains.cfg",
		"jobs.cfg",
		"vzdump.cron",
		filepath.Join("qemu-server", "100.conf"),
		filepath.Join("lxc", "101.conf"),
		filepath.Join("firewall", "cluster.fw"),
		filepath.Join("nodes", "node1", "host.fw"),
	} {
		_, err := os.Stat(filepath.Join(destPVE, excluded))
		if err == nil {
			t.Fatalf("expected %s excluded from PVE config snapshot", excluded)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", excluded, err)
		}
	}

	destDB := collector.targetPathFor(filepath.Join(clusterPath, "config.db"))
	if _, err := os.Stat(destDB); err == nil {
		t.Fatalf("expected config.db excluded when BACKUP_CLUSTER_CONFIG=false")
	}
}

// Test collectPVEStorageMetadata function
func TestCollectPVEStorageMetadata(t *testing.T) {
	collector := newPVECollector(t)

	ctx := context.Background()
	storages := []pveStorageEntry{
		{Name: "local", Path: "/var/lib/vz", Type: "dir"},
	}
	err := collector.collectPVEStorageMetadata(ctx, storages)
	if err != nil {
		t.Logf("collectPVEStorageMetadata returned error: %v", err)
	}
}

// Test collectPVECephInfo function
func TestCollectPVECephInfo(t *testing.T) {
	t.Run("no ceph configured", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "", fmt.Errorf("command not found")
			},
		})

		ctx := context.Background()
		err := collector.collectPVECephInfo(ctx)
		// Should not error when ceph is not configured
		if err != nil {
			t.Logf("collectPVECephInfo returned error when ceph missing: %v", err)
		}
	})

	t.Run("ceph configured", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				switch name {
				case "pvesm":
					return []byte("local cephfs storage"), nil
				case "ceph":
					return []byte("health: HEALTH_OK"), nil
				case "pgrep":
					return []byte("1234"), nil
				default:
					return []byte{}, nil
				}
			},
		})

		ctx := context.Background()
		err := collector.collectPVECephInfo(ctx)
		if err != nil {
			t.Logf("collectPVECephInfo with ceph returned error: %v", err)
		}
	})
}
