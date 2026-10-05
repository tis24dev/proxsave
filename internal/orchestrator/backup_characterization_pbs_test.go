package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

const (
	backupCharPBSStorage = "charpbs"
	backupCharPBSCalls   = "pbs-calls.log"
)

// backupCharPBSSnapshotTime is the time the fake server gives this run's snapshot.
var backupCharPBSSnapshotTime = backupCharStart.Add(25 * time.Second)

// backupCharFakePBSClient is a proxmox-backup-client that records its argv (no secret
// travels there) and answers with the shapes measured on pve-test. scenario selects the
// failure: not_initialized (connection refused), upload_failed (group of another
// owner), prune_denied (DatastoreBackup only).
func backupCharFakePBSClient(calls, scenario string) string {
	runTime := backupCharPBSSnapshotTime.Unix()
	snapshot := "host/proxsave-" + backupCharHost + "/" + backupCharPBSSnapshotTime.UTC().Format("2006-01-02T15:04:05Z")
	entry := func(at int64) string {
		return fmt.Sprintf(`{"backup-id":"proxsave-%s","backup-time":%d,"backup-type":"host","files":[{"crypt-mode":"none","filename":"proxsave.mpxar.didx","size":100},{"crypt-mode":"none","filename":"proxsave.ppxar.didx","size":100}],"owner":"charpbs@pbs!pve","protected":false,"size":200}`, backupCharHost, at)
	}
	return fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
scenario=%q
case "$1" in
version)
	if [ "$scenario" = not_initialized ]; then
		echo 'Error: client error (Connect)' >&2
		echo 'Caused by: error connecting to https://192.0.2.10:8007/ - tcp connect error: Connection refused (os error 111)' >&2
		exit 255
	fi
	echo '{"client":{"release":"5","version":"4.2"},"server":{"release":"5","version":"4.2"}}' ;;
status) echo '{"avail":8186691584,"backend-type":"filesystem","total":30080253952,"used":20560846848}' ;;
backup)
	if [ "$scenario" = upload_failed ]; then
		echo 'Error: backup owner check failed (charpbs@pbs!pve != other@pbs!tok)' >&2
		exit 255
	fi
	echo 'Starting backup: [charns]:%s    ' >&2
	echo 'proxsave.ppxar: had to backup 402 B of 3.067 MiB    ' >&2 ;;
prune)
	if [ "$scenario" = prune_denied ]; then
		echo 'Error: permission check failed - missing Datastore.Modify|Datastore.Prune on /datastore/charstore/charns' >&2
		exit 255
	fi
	echo '[{"backup-time":%d,"keep":false},{"backup-time":%d,"keep":true}]' ;;
snapshot)
	case "$2" in
	list) echo '[%s,%s]' ;;
	upload-log) echo 'Result: {' ; echo '  "data": null' ; echo '}' ;;
	esac ;;
esac
exit 0
`, calls, scenario, snapshot, runTime-7*86400, runTime, entry(runTime-86400), entry(runTime))
}

// setupBackupCharPBS enables the PBS storage in cfg, writes the PVE configuration the
// block reads (storage.cfg and the .pw file) and the fake client, and initializes the
// real block. The startup check logs to nowhere: its lines belong to cmd, not to
// RunGoBackup.
func setupBackupCharPBS(t *testing.T, dirs backupCharDirs, cfg *config.Config, scenario string) *block.PBS {
	t.Helper()
	cfg.PBSTargetEnabled = true
	cfg.PBSTargetStorage = backupCharPBSStorage
	cfg.MaxPBSTargetBackups = 5

	pveDir := filepath.Join(dirs.root, "pve")
	privDir := filepath.Join(pveDir, "priv", "storage")
	if err := os.MkdirAll(privDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", privDir, err)
	}
	storageCfg := "pbs: " + backupCharPBSStorage + "\n\tdatastore charstore\n\tserver 192.0.2.10\n\tcontent backup\n\tnamespace charns\n\tusername charpbs@pbs!pve\n"
	if err := os.WriteFile(filepath.Join(pveDir, "storage.cfg"), []byte(storageCfg), 0o640); err != nil {
		t.Fatalf("write storage.cfg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(privDir, backupCharPBSStorage+".pw"), []byte("char-secret-0000\n"), 0o600); err != nil {
		t.Fatalf("write .pw: %v", err)
	}
	script := backupCharFakePBSClient(filepath.Join(dirs.root, backupCharPBSCalls), scenario)
	if err := os.WriteFile(filepath.Join(dirs.bin, "proxmox-backup-client"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake client: %v", err)
	}
	t.Setenv("PATH", dirs.bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	startup := logging.New(types.LogLevelDebug, false)
	startup.SetOutput(io.Discard)
	p, report := block.InitPBS(context.Background(), block.PBSOptions{
		PVEConfigPath: pveDir,
		StorageID:     backupCharPBSStorage,
		Hostname:      backupCharHost,
		IsPVEHost:     true,
		Retention:     block.PBSRetentionFromConfig(cfg),
		Logger:        startup,
	})
	if want := scenario != "not_initialized"; report.Initialized() != want {
		t.Fatalf("PBS initialized = %v, want %v (fact=%v cause=%v)", report.Initialized(), want, report.Fact, report.Cause)
	}
	return p
}
