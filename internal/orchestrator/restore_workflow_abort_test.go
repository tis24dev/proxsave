package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestRunRestoreWorkflow_FstabPromptInputAborted_AbortsWorkflow(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareRestoreBundleFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareRestoreBundleFunc = origPrepare
		safetyFS = origSafetyFS
		safetyNow = origSafetyNow
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	compatFS = fakeFS
	safetyFS = fakeFS

	fakeNow := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeNow
	safetyNow = fakeNow.Now

	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	restoreCmd = runOnlyRunner{}

	// Make compatibility detection treat this as PVE.
	if err := fakeFS.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fakeFS.AddFile: %v", err)
	}

	currentFstab := "UUID=root / ext4 defaults 0 1\nUUID=swap none swap sw 0 0\n"
	if err := fakeFS.WriteFile("/etc/fstab", []byte(currentFstab), 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile(/etc/fstab): %v", err)
	}

	backupFstab := "UUID=root / ext4 defaults 0 1\nUUID=swap none swap sw 0 0\n192.168.1.246:/volume1/ProxmoxNFS /mnt/Synology_NFS nfs defaults 0 0\n"
	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/fstab": backupFstab,
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatalf("fakeFS.WriteFile(/bundle.tar): %v", err)
	}

	prepareRestoreBundleFunc = func(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     fakeNow.Now(),
				ClusterMode:   "standalone",
				ProxmoxType:   "pve",
				ScriptVersion: "vtest",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: "/bundle.tar",
			Manifest:    backup.Manifest{ArchivePath: "/bundle.tar"},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}
	ui := &fakeRestoreWorkflowUI{
		mode:              RestoreModeCustom,
		categories:        []Category{mustCategoryByID(t, "filesystem")},
		confirmRestore:    true,
		confirmFstabMerge: false,
		confirmFstabMergeErr: input.ErrInputAborted,
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}
