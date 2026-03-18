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

type fakeDecryptWorkflowUI struct {
	resolveExistingPathFn func(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error)
}

func (f *fakeDecryptWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	panic("unexpected RunTask call")
}

func (f *fakeDecryptWorkflowUI) ShowMessage(ctx context.Context, title, message string) error {
	panic("unexpected ShowMessage call")
}

func (f *fakeDecryptWorkflowUI) ShowError(ctx context.Context, title, message string) error {
	panic("unexpected ShowError call")
}

func (f *fakeDecryptWorkflowUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	panic("unexpected SelectBackupSource call")
}

func (f *fakeDecryptWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*decryptCandidate) (*decryptCandidate, error) {
	panic("unexpected SelectBackupCandidate call")
}

func (f *fakeDecryptWorkflowUI) PromptDestinationDir(ctx context.Context, defaultDir string) (string, error) {
	panic("unexpected PromptDestinationDir call")
}

func (f *fakeDecryptWorkflowUI) ResolveExistingPath(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
	if f.resolveExistingPathFn == nil {
		panic("unexpected ResolveExistingPath call")
	}
	return f.resolveExistingPathFn(ctx, path, description, failure)
}

func (f *fakeDecryptWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	panic("unexpected PromptDecryptSecret call")
}

type countingSecretPrompter struct {
	calls int
}

func (c *countingSecretPrompter) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	c.calls++
	return "unused", nil
}

func TestEnsureWritablePathWithUI_ReturnsCleanMissingPath(t *testing.T) {
	originalFS := restoreFS
	restoreFS = osFS{}
	defer func() { restoreFS = originalFS }()

	tmp := t.TempDir()
	target := filepath.Join(tmp, "subdir", "file.txt")
	dirty := target + string(filepath.Separator) + ".." + string(filepath.Separator) + "file.txt"

	got, err := ensureWritablePathWithUI(context.Background(), &fakeDecryptWorkflowUI{}, dirty, "test file")
	if err != nil {
		t.Fatalf("ensureWritablePathWithUI error: %v", err)
	}
	if got != target {
		t.Fatalf("ensureWritablePathWithUI path=%q, want %q", got, target)
	}
}

func TestEnsureWritablePathWithUI_OverwriteExisting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "existing.tar")
	if err := os.WriteFile(target, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	ui := &fakeDecryptWorkflowUI{
		resolveExistingPathFn: func(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
			if path != target {
				t.Fatalf("path=%q, want %q", path, target)
			}
			if failure != "" {
				t.Fatalf("unexpected failure message: %s", failure)
			}
			return PathDecisionOverwrite, "", nil
		},
	}

	got, err := ensureWritablePathWithUI(context.Background(), ui, target, "archive")
	if err != nil {
		t.Fatalf("ensureWritablePathWithUI error: %v", err)
	}
	if got != target {
		t.Fatalf("path=%q, want %q", got, target)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing file should be removed, stat err=%v", err)
	}
}

func TestEnsureWritablePathWithUI_NewPath(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "current.tar")
	if err := os.WriteFile(existing, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	nextPath := filepath.Join(tmp, "next.tar")

	var calls int
	ui := &fakeDecryptWorkflowUI{
		resolveExistingPathFn: func(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
			calls++
			if path != existing {
				t.Fatalf("path=%q, want %q", path, existing)
			}
			return PathDecisionNewPath, nextPath, nil
		},
	}

	got, err := ensureWritablePathWithUI(context.Background(), ui, existing, "bundle")
	if err != nil {
		t.Fatalf("ensureWritablePathWithUI error: %v", err)
	}
	if got != filepath.Clean(nextPath) {
		t.Fatalf("path=%q, want %q", got, filepath.Clean(nextPath))
	}
	if calls != 1 {
		t.Fatalf("expected 1 ResolveExistingPath call, got %d", calls)
	}
}

func TestEnsureWritablePathWithUI_AbortOnCancelDecision(t *testing.T) {
	path := mustCreateExistingFile(t)
	ui := &fakeDecryptWorkflowUI{
		resolveExistingPathFn: func(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
			return PathDecisionCancel, "", nil
		},
	}

	if _, err := ensureWritablePathWithUI(context.Background(), ui, path, "bundle"); !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted, got %v", err)
	}
}

func TestEnsureWritablePathWithUI_PropagatesPromptErrors(t *testing.T) {
	path := mustCreateExistingFile(t)
	wantErr := errors.New("boom")
	ui := &fakeDecryptWorkflowUI{
		resolveExistingPathFn: func(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
			return PathDecisionCancel, "", wantErr
		},
	}

	if _, err := ensureWritablePathWithUI(context.Background(), ui, path, "bundle"); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestPreparePlainBundleWithUICopiesRawArtifacts(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	tmp := t.TempDir()
	rawArchive := filepath.Join(tmp, "backup.tar")
	rawMetadata := rawArchive + ".metadata"
	rawChecksum := rawArchive + ".sha256"

	if err := os.WriteFile(rawArchive, []byte("payload-data"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := os.WriteFile(rawMetadata, []byte(`{"manifest":true}`), 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(rawChecksum, checksumLineForBytes("backup.tar", []byte("payload-data")), 0o640); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	cand := &decryptCandidate{
		Manifest: &backup.Manifest{
			ArchivePath:    rawArchive,
			EncryptionMode: "none",
			CreatedAt:      time.Now(),
			Hostname:       "node1",
		},
		Source:          sourceRaw,
		RawArchivePath:  rawArchive,
		RawMetadataPath: rawMetadata,
		RawChecksumPath: rawChecksum,
		DisplayBase:     "test-backup",
	}

	ctx := context.Background()
	prompter := &countingSecretPrompter{}
	prepared, err := preparePlainBundleWithUI(ctx, cand, "1.0.0", logger, prompter)
	if err != nil {
		t.Fatalf("preparePlainBundleWithUI error: %v", err)
	}
	defer prepared.Cleanup()

	if prepared.ArchivePath == "" {
		t.Fatalf("expected archive path to be set")
	}
	if prepared.Manifest.EncryptionMode != "none" {
		t.Fatalf("expected manifest encryption mode none, got %s", prepared.Manifest.EncryptionMode)
	}
	if prepared.Manifest.ScriptVersion != "1.0.0" {
		t.Fatalf("expected script version to propagate, got %s", prepared.Manifest.ScriptVersion)
	}
	if _, err := os.Stat(prepared.ArchivePath); err != nil {
		t.Fatalf("expected staged archive to exist: %v", err)
	}
	if prepared.Checksum == "" {
		t.Fatalf("expected checksum to be computed")
	}
	if prompter.calls != 0 {
		t.Fatalf("PromptDecryptSecret should not be called for plain backups, got %d calls", prompter.calls)
	}
}

func TestPreparePlainBundleWithUIRejectsInvalidCandidate(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	ctx := context.Background()
	prompter := &countingSecretPrompter{}
	if _, err := preparePlainBundleWithUI(ctx, nil, "", logger, prompter); err == nil {
		t.Fatalf("expected error for nil candidate")
	}
}

func TestPreparePlainBundleWithUIRejectsMissingUI(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	tmp := t.TempDir()
	rawArchive := filepath.Join(tmp, "backup.tar")
	rawMetadata := rawArchive + ".metadata"
	rawChecksum := rawArchive + ".sha256"

	if err := os.WriteFile(rawArchive, []byte("payload-data"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := os.WriteFile(rawMetadata, []byte(`{"manifest":true}`), 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(rawChecksum, checksumLineForBytes("backup.tar", []byte("payload-data")), 0o640); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	cand := &decryptCandidate{
		Manifest: &backup.Manifest{
			ArchivePath:    rawArchive,
			EncryptionMode: "none",
			CreatedAt:      time.Now(),
			Hostname:       "node1",
		},
		Source:          sourceRaw,
		RawArchivePath:  rawArchive,
		RawMetadataPath: rawMetadata,
		RawChecksumPath: rawChecksum,
		DisplayBase:     "test-backup",
	}

	if _, err := preparePlainBundleWithUI(context.Background(), cand, "1.0.0", logger, nil); err == nil {
		t.Fatalf("expected error for missing UI")
	}
}

func TestRunDecryptWorkflowWithUIRejectsMissingUI(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cfg := &config.Config{}

	err := runDecryptWorkflowWithUI(context.Background(), cfg, logger, "1.0.0", nil)
	if err == nil {
		t.Fatal("expected error for missing UI")
	}
	if got, want := err.Error(), "decrypt workflow UI not available"; got != want {
		t.Fatalf("error=%q, want %q", got, want)
	}
}

func mustCreateExistingFile(t *testing.T) string {
	t.Helper()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "existing.dat")
	if err := os.WriteFile(path, []byte("data"), 0o640); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
