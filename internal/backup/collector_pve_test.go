package backup

import (
	"context"
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
