package orchestrator

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestRunRestoreWorkflow_FstabPromptInputAborted_AbortsWorkflow(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestorePrompter := restorePrompter
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareDecryptedBackupFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restorePrompter = origRestorePrompter
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareDecryptedBackupFunc = origPrepare
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

	restorePrompter = fakeRestorePrompter{
		mode:       RestoreModeCustom,
		categories: []Category{mustCategoryByID(t, "filesystem")},
		confirmed:  true,
	}

	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
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

	// Simulate Ctrl+C behavior: stdin closed -> input.ErrInputAborted during the fstab prompt.
	oldIn := os.Stdin
	oldOut := os.Stdout
	oldErr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
		os.Stderr = oldErr
	})
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	errOut, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = out.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	os.Stderr = errOut
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
		_ = errOut.Close()
	})
	_ = inW.Close()

	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{BaseDir: "/base"}

	if err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest"); err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}
