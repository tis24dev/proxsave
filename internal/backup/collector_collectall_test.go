package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectorCollectAll_UnknownContinuesOnSystemInfoError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = t.TempDir()

	deps := defaultCollectorDeps()
	deps.LookPath = func(string) (string, error) {
		return "", errors.New("missing")
	}

	collector := NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxUnknown, false, deps)
	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll returned error: %v", err)
	}

	stats := collector.GetStats()
	if stats.FilesFailed == 0 {
		t.Fatalf("expected FilesFailed to be > 0 (system command collection should fail fast)")
	}
}

func TestCollectorCollectAll_ReturnsContextErrorImmediately(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)
	collector := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxUnknown, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := collector.CollectAll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCollectorCollectAll_PVEBranchWrapsCollectionError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = filepath.Join(t.TempDir(), "missing")

	collector := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxVE, false)
	err := collector.CollectAll(context.Background())
	if err == nil {
		t.Fatalf("expected error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "PVE collection failed:") {
		t.Fatalf("expected wrapped PVE collection error, got %v", err)
	}
}

func TestCollectorCollectAll_PBSBranchWrapsCollectionError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCollectorConfig()
	cfg.PBSConfigPath = filepath.Join(t.TempDir(), "missing")

	collector := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxBS, false)
	err := collector.CollectAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PBS collection failed:") {
		t.Fatalf("expected wrapped PBS collection error, got %v", err)
	}
}

type errFlipContext struct {
	context.Context
	calls int
}

func (c *errFlipContext) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return context.Canceled
}

func TestCollectorCollectAll_ChecksContextAfterSpecificCollectors(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)
	collector := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxUnknown, false)

	ctx := &errFlipContext{Context: context.Background()}
	if err := collector.CollectAll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCollectorCollectAll_PVESuccessInDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	systemRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(systemRoot, "etc", "pve"), 0o755); err != nil {
		t.Fatalf("mkdir /etc/pve: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = systemRoot

	deps := defaultCollectorDeps()
	deps.LookPath = func(name string) (string, error) {
		switch name {
		case "cat", "uname", "pveversion":
			return "/bin/true", nil
		default:
			return "", errors.New("missing")
		}
	}

	collector := NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxVE, true, deps)
	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll returned error: %v", err)
	}
}

func TestCollectorCollectAll_PBSSuccessInDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	systemRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(systemRoot, "etc", "proxmox-backup"), 0o755); err != nil {
		t.Fatalf("mkdir /etc/proxmox-backup: %v", err)
	}

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = systemRoot

	deps := defaultCollectorDeps()
	deps.LookPath = func(name string) (string, error) {
		switch name {
		case "cat", "uname", "proxmox-backup-manager":
			return "/bin/true", nil
		default:
			return "", errors.New("missing")
		}
	}
	deps.RunCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "proxmox-backup-manager" && len(args) >= 2 && args[0] == "datastore" && args[1] == "list" {
			return []byte("[]"), nil
		}
		return []byte("[]"), nil
	}

	collector := NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxBS, true, deps)
	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll returned error: %v", err)
	}
}
