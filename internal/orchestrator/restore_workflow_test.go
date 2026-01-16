package orchestrator

import (
	"archive/tar"
	"bufio"
	"context"
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

type fakeRestorePrompter struct {
	mode       RestoreMode
	categories []Category
	confirmed  bool

	modeErr       error
	categoriesErr error
	confirmErr    error
}

func (f fakeRestorePrompter) SelectRestoreMode(ctx context.Context, logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
	return f.mode, f.modeErr
}

func (f fakeRestorePrompter) SelectCategories(ctx context.Context, logger *logging.Logger, available []Category, systemType SystemType) ([]Category, error) {
	return f.categories, f.categoriesErr
}

func (f fakeRestorePrompter) ConfirmRestore(ctx context.Context, logger *logging.Logger) (bool, error) {
	return f.confirmed, f.confirmErr
}

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
	origPrompter := restorePrompter
	origSystem := restoreSystem
	origPrepare := prepareDecryptedBackupFunc
	t.Cleanup(func() {
		compatFS = origCompatFS
		restorePrompter = origPrompter
		restoreSystem = origSystem
		prepareDecryptedBackupFunc = origPrepare
	})

	fakeCompat := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeCompat.Root) })
	if err := fakeCompat.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fake compat fs: %v", err)
	}
	compatFS = fakeCompat

	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	restorePrompter = fakeRestorePrompter{
		mode:       RestoreModeCustom,
		categories: nil,
		confirmed:  true,
	}

	tmp := t.TempDir()
	archivePath := writeMinimalTar(t, tmp)
	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
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

	if err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest"); err != nil {
		t.Fatalf("RunRestoreWorkflow error: %v", err)
	}
}

func TestRunRestoreWorkflow_ConfirmFalseAborts(t *testing.T) {
	origCompatFS := compatFS
	origPrompter := restorePrompter
	origSystem := restoreSystem
	origPrepare := prepareDecryptedBackupFunc
	t.Cleanup(func() {
		compatFS = origCompatFS
		restorePrompter = origPrompter
		restoreSystem = origSystem
		prepareDecryptedBackupFunc = origPrepare
	})

	fakeCompat := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeCompat.Root) })
	if err := fakeCompat.AddFile("/usr/bin/qm", []byte("x")); err != nil {
		t.Fatalf("fake compat fs: %v", err)
	}
	compatFS = fakeCompat

	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	restorePrompter = fakeRestorePrompter{
		mode:       RestoreModeCustom,
		categories: nil,
		confirmed:  false,
	}

	tmp := t.TempDir()
	archivePath := writeMinimalTar(t, tmp)
	prepareDecryptedBackupFunc = func(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (*decryptCandidate, *preparedBundle, error) {
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

	err := RunRestoreWorkflow(context.Background(), cfg, logger, "vtest")
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}
