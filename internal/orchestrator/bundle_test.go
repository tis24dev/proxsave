package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
)

type fakeCommandRunnerCreate struct {
	bundlePath string
	calls      int
}

func (f *fakeCommandRunnerCreate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls++
	// Simulate tar by creating the bundle file
	_ = os.WriteFile(f.bundlePath, []byte("bundle"), 0o640)
	return []byte("ok"), nil
}

func TestCreateBundle_UsesCommandRunnerAndCreatesFile(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")

	for _, suffix := range []string{"", ".sha256", ".metadata", ".metadata.sha256"} {
		if err := os.WriteFile(archive+suffix, []byte("data"), 0o640); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	fakeRunner := &fakeCommandRunnerCreate{bundlePath: archive + ".bundle.tar"}
	o := &Orchestrator{
		logger:    logger,
		fs:        osFS{},
		cmdRunner: fakeRunner,
	}

	path, err := o.createBundle(context.Background(), archive)
	if err != nil {
		t.Fatalf("createBundle: %v", err)
	}
	if path != fakeRunner.bundlePath {
		t.Fatalf("bundle path = %s, want %s", path, fakeRunner.bundlePath)
	}
	if fakeRunner.calls != 1 {
		t.Fatalf("expected command runner to be called once, got %d", fakeRunner.calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected bundle file, got %v", err)
	}
}

func TestRemoveAssociatedFiles_RemovesAll(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")
	files := []string{
		archive,
		archive + ".sha256",
		archive + ".metadata",
		archive + ".metadata.sha256",
		archive + ".manifest.json",
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o640); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	o := &Orchestrator{
		logger: logger,
		fs:     osFS{},
	}
	if err := o.removeAssociatedFiles(archive); err != nil {
		t.Fatalf("removeAssociatedFiles: %v", err)
	}
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got %v", f, err)
		}
	}
}
