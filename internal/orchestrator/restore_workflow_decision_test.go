package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func stubPreparedRestoreBundle(archivePath string, manifest *backup.Manifest) func(context.Context, *config.Config, *logging.Logger, string, RestoreWorkflowUI) (*backupCandidate, *preparedBundle, error) {
	return func(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*backupCandidate, *preparedBundle, error) {
		return &backupCandidate{
				DisplayBase: "test",
				Manifest:    manifest,
			}, &preparedBundle{
				ArchivePath: archivePath,
				Manifest:    backup.Manifest{ArchivePath: archivePath},
				cleanup:     func() {},
			}, nil
	}
}

func TestRunRestoreWorkflow_ClusterPromptUsesArchivePayloadNotManifest(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	origCompatFS := compatFS
	origPrepare := prepareRestoreBundleFunc
	origSafetyFS := safetyFS
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
		compatFS = origCompatFS
		prepareRestoreBundleFunc = origPrepare
		safetyFS = origSafetyFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS
	restoreCmd = runOnlyRunner{}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                     "127.0.0.1 localhost\n",
		"var/lib/pve-cluster/config.db": "db\n",
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
		ProxmoxType:   "pve",
		ScriptVersion: "vtest",
	})

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}
	ui := &fakeRestoreWorkflowUI{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "pve_cluster"),
		},
		confirmRestore: true,
		clusterMode:    ClusterRestoreSafe,
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}
	if ui.clusterRestoreModeCalls != 1 {
		t.Fatalf("clusterRestoreModeCalls=%d; want 1", ui.clusterRestoreModeCalls)
	}
}

func TestRunRestoreWorkflow_CompatibilityUsesArchivePayloadNotManifest(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	origCompatFS := compatFS
	origPrepare := prepareRestoreBundleFunc
	origSafetyFS := safetyFS
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
		compatFS = origCompatFS
		prepareRestoreBundleFunc = origPrepare
		safetyFS = origSafetyFS
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS
	restoreCmd = runOnlyRunner{}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/ssh/sshd_config": "Port 22\n",
		"etc/pve/jobs.cfg":    "jobs\n",
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

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}
	ui := &fakeRestoreWorkflowUI{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "ssh"),
		},
		confirmRestore:    true,
		confirmCompatible: false,
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}
	if ui.confirmCompatibilityCalls != 0 {
		t.Fatalf("confirmCompatibilityCalls=%d; want 0", ui.confirmCompatibilityCalls)
	}
}

func TestRunRestoreWorkflow_CompatibilityWarnsOnArchiveMismatchDespiteManifest(t *testing.T) {
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

	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/ssh/sshd_config":         "Port 22\n",
		"etc/proxmox-backup/sync.cfg": "sync\n",
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
		ProxmoxType:   "pve",
		ScriptVersion: "vtest",
	})

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}
	ui := &fakeRestoreWorkflowUI{
		mode: RestoreModeCustom,
		categories: []Category{
			mustCategoryByID(t, "ssh"),
		},
		confirmRestore:    true,
		confirmCompatible: true,
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}
	if ui.confirmCompatibilityCalls != 1 {
		t.Fatalf("confirmCompatibilityCalls=%d; want 1", ui.confirmCompatibilityCalls)
	}
	if ui.lastCompatibilityWarning == nil {
		t.Fatalf("expected compatibility warning to be passed to UI")
	}
}

func TestRunRestoreWorkflow_CompatibilityWarningStillRunsBeforeFullFallbackOnAnalysisError(t *testing.T) {
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

	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts": "127.0.0.1 localhost\n",
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
	ui := &fakeRestoreWorkflowUI{
		confirmRestore:    true,
		confirmCompatible: true,
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}
	if ui.confirmCompatibilityCalls != 1 {
		t.Fatalf("confirmCompatibilityCalls=%d; want 1", ui.confirmCompatibilityCalls)
	}
	if ui.lastCompatibilityWarning == nil {
		t.Fatalf("expected compatibility warning before fallback")
	}
	if _, err := fakeFS.ReadFile("/etc/hosts"); err != nil {
		t.Fatalf("expected full restore fallback to extract /etc/hosts: %v", err)
	}
}
