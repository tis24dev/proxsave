package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
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
