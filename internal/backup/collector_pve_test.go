package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
		state.pve.clustered = true
	}, brickPVERuntimeCluster)
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

// TestCollectPVESnapshotBricks collects the PVE snapshot bricks used by the real recipe.
func TestCollectPVESnapshotBricks(t *testing.T) {
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

	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil,
		brickPVEConfigSnapshot,
		brickPVEFirewallSnapshot,
	)
}

// TestPVERuntimeBricks collects the PVE runtime bricks used by the real recipe.
func TestPVERuntimeBricks(t *testing.T) {
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

	state := runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil,
		brickPVERuntimeCore,
		brickPVERuntimeACL,
		brickPVERuntimeCluster,
		brickPVERuntimeStorage,
	)

	if state.pve.runtimeInfo == nil {
		t.Error("expected non-nil runtimeInfo")
	}
}

// TestPVEFinalizeBricks runs the real finalize bricks instead of the legacy wrapper.
func TestPVEFinalizeBricks(t *testing.T) {
	collector := newPVECollector(t)
	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil,
		brickPVEAliasCore,
		brickPVEAggregateBackupHistory,
		brickPVEAggregateReplicationStatus,
		brickPVEVersionInfo,
	)
}

// TestPVEJobBricks runs the real PVE job bricks instead of the legacy adapter.
func TestPVEJobBricks(t *testing.T) {
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

	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
		state.ensurePVERuntimeInfo().Nodes = []string{"test-node"}
	}, brickPVEBackupJobDefs, brickPVEBackupJobHistory, brickPVEVZDumpCron)
}

// TestPVEJobBricksComprehensive tests various edge cases for the real job bricks.
func TestPVEJobBricksComprehensive(t *testing.T) {
	t.Run("skips empty and duplicate node names", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(`[]`), nil
			},
		})

		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
			state.ensurePVERuntimeInfo().Nodes = []string{"node1", "", "  ", "node1", "node2", "node2"}
		}, brickPVEBackupJobDefs, brickPVEBackupJobHistory, brickPVEVZDumpCron)
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

		state := newCollectionState(collector)
		state.ensurePVERuntimeInfo().Nodes = []string{"node1"}
		err := runRecipe(ctx, recipe{
			Name: "pve-jobs-canceled",
			Bricks: []collectionBrick{
				requireBrick(t, newPVERecipe(), brickPVEBackupJobDefs),
				requireBrick(t, newPVERecipe(), brickPVEBackupJobHistory),
				requireBrick(t, newPVERecipe(), brickPVEVZDumpCron),
			},
		}, state)
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

		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil, brickPVEVZDumpCron)
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

		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
			state.ensurePVERuntimeInfo().Nodes = []string{"  node1  ", "  node2  "}
		}, brickPVEBackupJobHistory)
	})
}

// TestPVEScheduleBricks runs the real PVE schedule bricks.
func TestPVEScheduleBricks(t *testing.T) {
	collector := newPVECollector(t)
	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil,
		brickPVEScheduleCrontab,
		brickPVEScheduleTimers,
		brickPVEScheduleCronFiles,
	)
}

// TestPVEReplicationBricks runs the real replication bricks.
func TestPVEReplicationBricks(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(`[]`), nil
		},
	})

	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
		state.ensurePVERuntimeInfo().Nodes = []string{"test-node"}
	}, brickPVEReplicationDefs, brickPVEReplicationStatus)
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

	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil,
		brickPVEConfigSnapshot,
		brickPVEClusterSnapshot,
		brickPVEFirewallSnapshot,
		brickPVEVZDumpSnapshot,
	)

	destPVE := collector.targetPathFor(pveRoot)
	if _, err := os.Stat(filepath.Join(destPVE, "dummy.cfg")); err != nil {
		t.Fatalf("expected dummy.cfg collected, got %v", err)
	}

	for _, excluded := range []string{
		"corosync.conf",
		"user.cfg",
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

// TestPVEStoragePipeline runs the real PVE storage pipeline bricks.
func TestPVEStoragePipeline(t *testing.T) {
	collector := newPVECollector(t)

	storageDir := t.TempDir()
	storages := []pveStorageEntry{
		{Name: "local", Path: storageDir, Type: "dir"},
	}
	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
		state.ensurePVERuntimeInfo().Storages = storages
	}, brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON, brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary)

	metaPath := filepath.Join(collector.tempDir, "var/lib/pve-cluster/info/datastores", "local", "metadata.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("expected %s created, got %v", metaPath, err)
	}
}

func TestCollectPVEStorageMetadata_UsesPathSafeKeyForUnsafeStorageName(t *testing.T) {
	collector := newPVECollector(t)
	collector.config.BackupSmallPVEBackups = true
	collector.config.MaxPVEBackupSizeBytes = 1024 * 1024
	collector.config.PVEBackupIncludePattern = "vm-100"

	storageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(storageDir, "vm-100-backup.vma"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write backup file: %v", err)
	}

	storage := pveStorageEntry{Name: "../escape", Path: storageDir, Type: "dir"}
	runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
		state.ensurePVERuntimeInfo().Storages = []pveStorageEntry{storage}
	}, brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON, brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary)

	key := collectorPathKey(storage.Name)
	baseDir := filepath.Join(collector.tempDir, "var/lib/pve-cluster/info/datastores", key)
	for _, path := range []string{
		filepath.Join(baseDir, "metadata.json"),
		filepath.Join(baseDir, "backup_analysis", fmt.Sprintf("%s_backup_summary.txt", key)),
		filepath.Join(baseDir, "backup_analysis", fmt.Sprintf("%s__vma_list.txt", key)),
		filepath.Join(collector.tempDir, "var/lib/pve-cluster/small_backups", key, "vm-100-backup.vma"),
		filepath.Join(collector.tempDir, "var/lib/pve-cluster/selected_backups", key, "vm-100-backup.vma"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected safe output %s: %v", path, err)
		}
	}

	metaPath := filepath.Join(baseDir, "metadata.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	if !strings.Contains(string(metaBytes), storage.Name) {
		t.Fatalf("metadata should keep raw storage name, got %s", string(metaBytes))
	}

	rawMetaPath := filepath.Join(collector.tempDir, "var/lib/pve-cluster/info/datastores", storage.Name, "metadata.json")
	if rawMetaPath != metaPath {
		if _, err := os.Stat(rawMetaPath); !os.IsNotExist(err) {
			t.Fatalf("raw storage metadata path should not exist (%s), got err=%v", rawMetaPath, err)
		}
	}
}

func TestCollectPVEStorageMetadata_SkipReasonsAreUserFriendly(t *testing.T) {
	boolPtr := func(v bool) *bool {
		b := v
		return &b
	}
	t.Run("disabled storage uses SKIP with debug details", func(t *testing.T) {
		collector := newPVECollector(t)
		var out bytes.Buffer
		collector.logger.SetOutput(&out)

		storages := []pveStorageEntry{
			{Name: "HDD1", Path: "/mnt/pve/HDD1", Active: boolPtr(false), Enabled: boolPtr(false)},
		}
		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
			state.ensurePVERuntimeInfo().Storages = storages
		}, brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON, brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary)

		logText := out.String()
		if collector.logger.WarningCount() != 0 {
			t.Fatalf("expected 0 warnings, got %d", collector.logger.WarningCount())
		}
		if !strings.Contains(logText, "SKIP") || !strings.Contains(logText, "disabled in Proxmox") {
			t.Fatalf("expected user-friendly SKIP message for disabled storage, got logs:\n%s", logText)
		}
		if !strings.Contains(logText, "PVE datastore skip details:") {
			t.Fatalf("expected debug details for skipped storage, got logs:\n%s", logText)
		}
	})

	t.Run("offline storage uses WARNING with debug details", func(t *testing.T) {
		collector := newPVECollector(t)
		var out bytes.Buffer
		collector.logger.SetOutput(&out)

		storages := []pveStorageEntry{
			{Name: "HDD2", Path: "/mnt/pve/HDD2", Active: boolPtr(false), Enabled: boolPtr(true)},
		}
		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
			state.ensurePVERuntimeInfo().Storages = storages
		}, brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON, brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary)

		logText := out.String()
		if collector.logger.WarningCount() != 1 {
			t.Fatalf("expected 1 warning, got %d", collector.logger.WarningCount())
		}
		if !strings.Contains(logText, "WARNING") || !strings.Contains(logText, "storage is offline") {
			t.Fatalf("expected warning message for offline storage, got logs:\n%s", logText)
		}
		if !strings.Contains(logText, "PVE datastore skip details:") {
			t.Fatalf("expected debug details for skipped storage, got logs:\n%s", logText)
		}
	})

	t.Run("non-existent path uses WARNING with debug details", func(t *testing.T) {
		collector := newPVECollector(t)
		var out bytes.Buffer
		collector.logger.SetOutput(&out)

		storages := []pveStorageEntry{
			{Name: "MISSING", Path: filepath.Join(t.TempDir(), "does-not-exist"), Type: "dir"},
		}
		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), func(state *collectionState) {
			state.ensurePVERuntimeInfo().Storages = storages
		}, brickPVEStorageResolve, brickPVEStorageProbe, brickPVEStorageMetadataJSON, brickPVEStorageMetadataText, brickPVEStorageBackupAnalysis, brickPVEStorageSummary)

		logText := out.String()
		if collector.logger.WarningCount() != 1 {
			t.Fatalf("expected 1 warning, got %d", collector.logger.WarningCount())
		}
		if !strings.Contains(logText, "not accessible") {
			t.Fatalf("expected warning for missing path, got logs:\n%s", logText)
		}
		if !strings.Contains(logText, "reason=path_not_accessible") {
			t.Fatalf("expected debug details including reason, got logs:\n%s", logText)
		}
	})
}

// TestPVECephBricks runs the real Ceph bricks.
func TestPVECephBricks(t *testing.T) {
	t.Run("no ceph configured", func(t *testing.T) {
		collector := newPVECollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "", fmt.Errorf("command not found")
			},
		})

		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil, brickPVECephConfigSnapshot, brickPVECephRuntime)
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

		runSelectedBricksForTest(t, context.Background(), collector, newPVERecipe(), nil, brickPVECephConfigSnapshot, brickPVECephRuntime)
	})
}
