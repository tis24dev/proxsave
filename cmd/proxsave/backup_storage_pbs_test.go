package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// pbsInitFixture writes a PVE_CONFIG_PATH with one PBS storage "pbs-main" and its
// password, and a fake proxmox-backup-client first in PATH answering with the
// measured shapes; versionAnswer replaces the version reply (to make the server fail).
func pbsInitFixture(t *testing.T, versionAnswer string, snapshots ...int64) *config.Config {
	t.Helper()
	pve := t.TempDir()
	priv := filepath.Join(pve, "priv", "storage")
	if err := os.MkdirAll(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	stanza := "pbs: pbs-main\n\tdatastore ds\n\tserver 192.0.2.10\n\tnamespace ns1\n\tusername u@pbs!t\n"
	if err := os.WriteFile(filepath.Join(pve, "storage.cfg"), []byte(stanza), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(priv, "pbs-main.pw"), []byte("secret-pbs-0000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := "["
	for i, at := range snapshots {
		if i > 0 {
			list += ","
		}
		list += fmt.Sprintf(`{"backup-id":"proxsave-node.lan","backup-time":%d,"backup-type":"host","files":[],"protected":false}`, at)
	}
	list += "]"
	if versionAnswer == "" {
		versionAnswer = `echo '{"client":{"release":"5","version":"4.2"},"server":{"release":"5","version":"4.2"}}'`
	}
	script := "#!/bin/sh\ncase \"$1\" in\nversion) " + versionAnswer + " ;;\n" +
		"status) echo '{\"avail\":2147483648,\"total\":4294967296,\"used\":2147483648}' ;;\n" +
		"snapshot) echo '" + list + "' ;;\nesac\nexit 0\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "proxmox-backup-client"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &config.Config{PBSTargetEnabled: true, PBSTargetStorage: "pbs-main", MaxPBSTargetBackups: 15, PVEConfigPath: pve, MinDiskPBSGB: 1}
}

func pbsInitOptions(cfg *config.Config, logger *logging.Logger, ptype types.ProxmoxType) backupModeOptions {
	return backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node.lan",
		envInfo: &environment.EnvironmentInfo{Type: ptype}}
}

func TestPBSInitBlockInitialized(t *testing.T) {
	cfg := pbsInitFixture(t, "", time.Now().Add(-time.Hour).Unix(), time.Now().Add(-25*time.Hour).Unix())
	var pbs *block.PBS
	got := captureStorageInit(t, func(logger *logging.Logger) {
		pbs = initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxVE), orchestrator.New(logger, false), nil)
	})
	requireBlock(t, got,
		"INFO     Path PBS: pbs-main",
		"INFO       Retention policy: simple (keep 15 newest)",
		"INFO     Checking PBS storage accessibility...",
		"INFO       Accessible",
		"INFO       Backups: 2",
		"INFO     ✓ PBS storage: initialized",
	)
	if !pbs.Initialized() {
		t.Fatal("block not initialized")
	}
}

func TestPBSInitBlockGFSTiers(t *testing.T) {
	cfg := pbsInitFixture(t, "", time.Now().Add(-time.Hour).Unix())
	cfg.RetentionPolicy = "gfs"
	cfg.RetentionDaily = 3
	got := captureStorageInit(t, func(logger *logging.Logger) {
		initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxDual), orchestrator.New(logger, false), nil)
	})
	requireBlock(t, got,
		"INFO     Path PBS: pbs-main",
		"INFO     Checking PBS storage accessibility...",
		"INFO       Accessible",
		"INFO       Backups: 1",
		"INFO       Daily: 1/3",
		"INFO       Weekly: 0/0",
		"INFO       Monthly: 0/0",
		"INFO       Yearly: 0/0",
		"INFO     ✓ PBS storage: initialized",
	)
}

func TestPBSInitBlockNotInitialized(t *testing.T) {
	cfg := pbsInitFixture(t, "")
	got := captureStorageInit(t, func(logger *logging.Logger) {
		initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxBS), orchestrator.New(logger, false), nil)
	})
	requireBlock(t, got,
		"INFO     Path PBS: pbs-main",
		"INFO       Retention policy: simple (keep 15 newest)",
		"INFO       Host: not a Proxmox VE host",
		"WARNING  ✗ PBS storage: not initialized",
	)

	cfg = pbsInitFixture(t, "echo 'Error: client error (Connect)' >&2; echo 'Caused by: tcp connect error: Connection refused (os error 111)' >&2; exit 255")
	orch := orchestrator.New(logging.New(types.LogLevelInfo, false), false)
	var pbs *block.PBS
	got = captureStorageInit(t, func(logger *logging.Logger) {
		pbs = initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxVE), orch, nil)
	})
	requireBlock(t, got,
		"INFO     Path PBS: pbs-main",
		"INFO       Retention policy: simple (keep 15 newest)",
		"INFO     Checking PBS storage accessibility...",
		"INFO       Connection refused",
		"WARNING  ✗ PBS storage: not initialized",
	)
	if pbs == nil || pbs.Initialized() {
		t.Fatal("a storage not initialized must stay registered and report so")
	}
}

func TestPBSInitDisabledAndSummaries(t *testing.T) {
	cfg := &config.Config{}
	got := captureStorageInit(t, func(logger *logging.Logger) {
		if initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxVE), orchestrator.New(logger, false), nil) != nil {
			t.Fatal("disabled PBS returned a block")
		}
		logPBSStorageSummary(cfg, nil)
		logPBSLogSummary(cfg, nil)
	})
	requireBlock(t, got, "SKIP     Path PBS: disabled", "SKIP       PBS storage: disabled", "SKIP       PBS: disabled")

	cfg = pbsInitFixture(t, "exit 255")
	got = captureStorageInit(t, func(logger *logging.Logger) {
		pbs := initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxVE), orchestrator.New(logger, false), nil)
		logPBSStorageSummary(cfg, pbs)
		logPBSLogSummary(cfg, pbs)
	})
	requireBlock(t, got[len(got)-2:], "INFO       PBS storage: pbs-main [not initialized]", "INFO       PBS: not initialized")

	cfg = pbsInitFixture(t, "")
	got = captureStorageInit(t, func(logger *logging.Logger) {
		pbs := initializePBSTarget(pbsInitOptions(cfg, logger, types.ProxmoxVE), orchestrator.New(logger, false), nil)
		logPBSStorageSummary(cfg, pbs)
		logPBSLogSummary(cfg, pbs)
	})
	requireBlock(t, got[len(got)-2:], "INFO       PBS storage: pbs-main [pbs]", "INFO       PBS: pbs-main")
}
