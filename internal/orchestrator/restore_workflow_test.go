package orchestrator

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

type fakeSystemDetector struct {
	systemType SystemType
}

func (f fakeSystemDetector) DetectCurrentSystem() SystemType { return f.systemType }

func writeMinimalTar(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "archive.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	body := []byte("hello\n")
	hdr := &tar.Header{
		Name:     "etc/hosts",
		Mode:     0o640,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Unix(1700000000, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tw.Flush(); err != nil {
		t.Fatalf("flush tar: %v", err)
	}
	return path
}

func TestRunRestoreWorkflow_CustomModeNoCategories_Succeeds(t *testing.T) {
	origCompatFS := compatFS
	origSystem := restoreSystem
	origPrepare := prepareRestoreBundleFunc
	t.Cleanup(func() {
		compatFS = origCompatFS
		restoreSystem = origSystem
		prepareRestoreBundleFunc = origPrepare
	})

	fakeCompat := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeCompat.Root) })
	if err := fakeCompat.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fake compat fs: %v", err)
	}
	compatFS = fakeCompat

	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	tmp := t.TempDir()
	archivePath := writeMinimalTar(t, tmp)
	prepareRestoreBundleFunc = func(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     time.Unix(1700000000, 0),
				ClusterMode:   "standalone",
				ScriptVersion: "1.0.0",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: archivePath,
			Manifest:    backup.Manifest{ArchivePath: archivePath},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	cfg := &config.Config{BaseDir: tmp}
	ui := &fakeRestoreWorkflowUI{
		mode:           RestoreModeCustom,
		categories:     nil,
		confirmRestore: true,
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}
}

func TestRunRestoreWorkflow_ConfirmFalseAborts(t *testing.T) {
	origCompatFS := compatFS
	origSystem := restoreSystem
	origPrepare := prepareRestoreBundleFunc
	t.Cleanup(func() {
		compatFS = origCompatFS
		restoreSystem = origSystem
		prepareRestoreBundleFunc = origPrepare
	})

	fakeCompat := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeCompat.Root) })
	if err := fakeCompat.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fake compat fs: %v", err)
	}
	compatFS = fakeCompat

	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	tmp := t.TempDir()
	archivePath := writeMinimalTar(t, tmp)
	prepareRestoreBundleFunc = func(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     time.Unix(1700000000, 0),
				ClusterMode:   "standalone",
				ScriptVersion: "1.0.0",
			},
		}
		prepared := &preparedBundle{
			ArchivePath: archivePath,
			Manifest:    backup.Manifest{ArchivePath: archivePath},
			cleanup:     func() {},
		}
		return cand, prepared, nil
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	cfg := &config.Config{BaseDir: tmp}
	ui := &fakeRestoreWorkflowUI{
		mode:           RestoreModeCustom,
		categories:     nil,
		confirmRestore: false,
	}

	err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui)
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}

func TestRunRestoreWorkflow_AnalysisFailure_FallsBackToSafeFullRestore(t *testing.T) {
	origRestoreFS := restoreFS
	origRestoreCmd := restoreCmd
	origRestoreSystem := restoreSystem
	origRestoreTime := restoreTime
	origCompatFS := compatFS
	origPrepare := prepareRestoreBundleFunc
	origAnalyze := analyzeBackupCategoriesFunc
	origSafetyFS := safetyFS
	origSafetyNow := safetyNow
	t.Cleanup(func() {
		restoreFS = origRestoreFS
		restoreCmd = origRestoreCmd
		restoreSystem = origRestoreSystem
		restoreTime = origRestoreTime
		compatFS = origCompatFS
		prepareRestoreBundleFunc = origPrepare
		analyzeBackupCategoriesFunc = origAnalyze
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

	// Make compatibility detection treat this as PBS (to avoid compatibility prompts).
	if err := fakeFS.AddDir("/etc/proxmox-backup"); err != nil {
		t.Fatalf("fakeFS.AddDir: %v", err)
	}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ip route show default": []byte(""),
		},
		Errors: map[string]error{},
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

	prepareRestoreBundleFunc = func(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*decryptCandidate, *preparedBundle, error) {
		cand := &decryptCandidate{
			DisplayBase: "test",
			Manifest: &backup.Manifest{
				CreatedAt:     fakeNow.Now(),
				ClusterMode:   "standalone",
				ProxmoxType:   "pbs",
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

	analyzeBackupCategoriesFunc = func(ctx context.Context, archivePath string, logger *logging.Logger) ([]Category, error) {
		return nil, errors.New("simulated analysis failure")
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	cfg := &config.Config{BaseDir: "/base"}
	ui := &fakeRestoreWorkflowUI{
		confirmRestore: true,
		modeErr:        errors.New("unexpected SelectRestoreMode call"),
	}

	if err := runRestoreWorkflowWithUI(context.Background(), cfg, logger, "vtest", ui); err != nil {
		t.Fatalf("runRestoreWorkflowWithUI error: %v", err)
	}

	data, err := fakeFS.ReadFile("/tmp/proxsave/restore_backup_location.txt")
	if err != nil {
		t.Fatalf("expected safety backup location file: %v", err)
	}
	want := "/tmp/proxsave/restore_backup_20200102_030405.tar.gz"
	if got := string(data); got != want {
		t.Fatalf("restore_backup_location.txt=%q want %q", got, want)
	}
	if _, err := fakeFS.Stat(want); err != nil {
		t.Fatalf("expected safety backup archive %s to exist: %v", want, err)
	}
}
