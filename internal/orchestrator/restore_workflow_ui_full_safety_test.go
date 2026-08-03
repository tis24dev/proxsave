// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestFullRestoreFallbackKeepsSafetyInvariants is the regression pin for the
// unprotected fallback: when category analysis fails, the run must still take a
// safety backup and must still keep export-only content off the live system. Before,
// it jumped straight to extraction with neither.
func TestFullRestoreFallbackKeepsSafetyInvariants(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	origCompatFS := compatFS
	origPrepare := prepareRestoreBundleFunc
	origAnalyze := analyzeRestoreArchiveFunc
	origSafetyFS := safetyFS
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
		compatFS = origCompatFS
		prepareRestoreBundleFunc = origPrepare
		analyzeRestoreArchiveFunc = origAnalyze
		safetyFS = origSafetyFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS
	restoreCmd = runOnlyRunner{}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePBS}

	// The category must exist on this system to enter the synthesized plan (the
	// fallback backs up only what is actually there), and it is ExportOnly, so its
	// content must not be written to /.
	if err := fakeFS.AddFile("/etc/proxmox-backup/node.cfg", []byte("live\n")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                   "127.0.0.1 localhost\n",
		"etc/proxmox-backup/node.cfg": "from-archive\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile tar: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile: %v", err)
	}

	prepareRestoreBundleFunc = stubPreparedRestoreBundle("/bundle.tar", &backup.Manifest{
		CreatedAt:     time.Unix(1700000000, 0),
		ClusterMode:   "standalone",
		ProxmoxType:   "pbs",
		ScriptVersion: "vtest",
	})
	analyzeRestoreArchiveFunc = func(archivePath string, logger *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
		return nil, nil, errors.New("boom")
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}
	// continuePBSServices: the fallback now goes through prepareRestoreServices, and
	// systemctl is unavailable in the test environment.
	ui := &fakeRestoreWorkflowUI{confirmRestore: true, confirmCompatible: true, continuePBSServices: true}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}

	// The fallback still extracts everything else - that is what makes it a FULL
	// restore, and the fix must not have turned it into a selective one.
	if _, err := fakeFS.ReadFile("/etc/hosts"); err != nil {
		t.Fatalf("fallback must still extract /etc/hosts: %v", err)
	}

	// Export-only content must not have reached the live path.
	live, err := fakeFS.ReadFile("/etc/proxmox-backup/node.cfg")
	if err != nil {
		t.Fatalf("read live export-only path: %v", err)
	}
	if strings.TrimSpace(string(live)) != "live" {
		t.Fatalf("export-only content overwrote the live system: %q", string(live))
	}

	// A safety backup must exist: createSafetyBackup writes restore_backup_*.tar.gz
	// under /tmp/proxsave via safetyFS.
	entries, err := fakeFS.ReadDir("/tmp/proxsave")
	if err != nil {
		t.Fatalf("no safety backup directory was created: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "restore_backup_") && strings.HasSuffix(e.Name(), ".tar.gz") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fallback ran without a safety backup; entries=%v", entries)
	}
}

// TestFullRestoreFallbackNeverWritesClusterDB pins the one thing the fallback must
// not do on a PVE node. It leaves NeedsClusterRestore off, so pve-cluster keeps
// running and /etc/pve stays mounted; if the extraction let /var/lib/pve-cluster/
// through, config.db would be overwritten under a live pmxcfs holding it open as
// SQLite, and corosync would carry the damage to the rest of the cluster.
//
// The /etc/pve guard in restore_archive_entries.go does NOT cover this: the database
// lives elsewhere. Deleting the cluster arm of skipPath turns this test RED.
func TestFullRestoreFallbackNeverWritesClusterDB(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	origCompatFS := compatFS
	origPrepare := prepareRestoreBundleFunc
	origAnalyze := analyzeRestoreArchiveFunc
	origSafetyFS := safetyFS
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
		compatFS = origCompatFS
		prepareRestoreBundleFunc = origPrepare
		analyzeRestoreArchiveFunc = origAnalyze
		safetyFS = origSafetyFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS
	restoreCmd = runOnlyRunner{}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	// The live cluster database pmxcfs is holding open. It must still hold this
	// content after the fallback runs.
	if err := fakeFS.AddFile("/var/lib/pve-cluster/config.db", []byte("live-cluster-db\n")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                     "127.0.0.1 localhost\n",
		"var/lib/pve-cluster/config.db": "from-archive\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("ReadFile tar: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile: %v", err)
	}

	prepareRestoreBundleFunc = stubPreparedRestoreBundle("/bundle.tar", &backup.Manifest{
		CreatedAt:     time.Unix(1700000000, 0),
		ClusterMode:   "cluster",
		ProxmoxType:   "pve",
		ScriptVersion: "vtest",
	})
	analyzeRestoreArchiveFunc = func(archivePath string, logger *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
		return nil, nil, errors.New("boom")
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}
	ui := &fakeRestoreWorkflowUI{confirmRestore: true, confirmCompatible: true, continuePBSServices: true}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}

	// Still a FULL restore: everything outside the cluster database is written.
	if _, err := fakeFS.ReadFile("/etc/hosts"); err != nil {
		t.Fatalf("fallback must still extract /etc/hosts: %v", err)
	}

	live, err := fakeFS.ReadFile("/var/lib/pve-cluster/config.db")
	if err != nil {
		t.Fatalf("read live cluster database: %v", err)
	}
	if strings.TrimSpace(string(live)) != "live-cluster-db" {
		t.Fatalf("fallback overwrote the live cluster database under a running pmxcfs: %q", string(live))
	}
}
