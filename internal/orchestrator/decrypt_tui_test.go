package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNormalizeProxmoxVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"8.1", "v8.1"},
		{"v7.4", "v7.4"},
		{"V9", "V9"},
	}

	for _, tt := range cases {
		if got := normalizeProxmoxVersion(tt.in); got != tt.want {
			t.Fatalf("normalizeProxmoxVersion(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildTargetInfo(t *testing.T) {
	manifest := &backup.Manifest{
		ProxmoxTargets: []string{"pbs", "node1"},
		ProxmoxVersion: "8.0",
		ClusterMode:    "cluster",
		CreatedAt:      time.Now(),
	}

	got := buildTargetInfo(manifest)
	want := "Targets: PBS+NODE1 v8.0 (cluster)"
	if got != want {
		t.Fatalf("buildTargetInfo()=%q, want %q", got, want)
	}

	manifest = &backup.Manifest{
		ProxmoxType: "pbs",
	}
	if got := buildTargetInfo(manifest); got != "Targets: PBS" {
		t.Fatalf("buildTargetInfo fallback=%q, want %q", got, "Targets: PBS")
	}
}

func TestFilterEncryptedCandidates(t *testing.T) {
	now := time.Now()
	encrypted := &decryptCandidate{Manifest: &backup.Manifest{EncryptionMode: "age", CreatedAt: now}}
	plain := &decryptCandidate{Manifest: &backup.Manifest{EncryptionMode: "none", CreatedAt: now}}

	filtered := filterEncryptedCandidates([]*decryptCandidate{nil, encrypted, plain, {}})
	if len(filtered) != 1 || filtered[0] != encrypted {
		t.Fatalf("filterEncryptedCandidates returned %+v, want only encrypted candidate", filtered)
	}
}

func TestEnsureWritablePathTUI_ReturnsCleanMissingPath(t *testing.T) {
	originalFS := restoreFS
	restoreFS = osFS{}
	defer func() { restoreFS = originalFS }()

	tmp := t.TempDir()
	target := filepath.Join(tmp, "subdir", "file.txt")
	dirty := target + string(filepath.Separator) + ".." + string(filepath.Separator) + "file.txt"

	path, err := ensureWritablePathTUI(dirty, "test file", "cfg", "sig")
	if err != nil {
		t.Fatalf("ensureWritablePathTUI returned error: %v", err)
	}
	if path != target {
		t.Fatalf("ensureWritablePathTUI path=%q, want %q", path, target)
	}
}

func TestEnsureWritablePathTUIOverwriteExisting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "existing.tar")
	if err := os.WriteFile(target, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	restore := stubPromptOverwriteAction(func(path, desc, failure, configPath, buildSig string) (string, error) {
		if failure != "" {
			t.Fatalf("unexpected failure message: %s", failure)
		}
		return pathActionOverwrite, nil
	})
	defer restore()

	got, err := ensureWritablePathTUI(target, "archive", "cfg", "sig")
	if err != nil {
		t.Fatalf("ensureWritablePathTUI error: %v", err)
	}
	if got != target {
		t.Fatalf("path = %q, want %q", got, target)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing file should be removed, stat err=%v", err)
	}
}

func TestEnsureWritablePathTUINewPath(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "current.tar")
	if err := os.WriteFile(existing, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	nextPath := filepath.Join(tmp, "new.tar")

	var promptCalls int
	restorePrompt := stubPromptOverwriteAction(func(path, desc, failure, configPath, buildSig string) (string, error) {
		promptCalls++
		if failure != "" {
			t.Fatalf("unexpected failure message: %s", failure)
		}
		return pathActionNew, nil
	})
	defer restorePrompt()

	restoreNew := stubPromptNewPath(func(current, configPath, buildSig string) (string, error) {
		if filepath.Clean(current) != filepath.Clean(existing) {
			t.Fatalf("promptNewPath received %q, want %q", current, existing)
		}
		return nextPath, nil
	})
	defer restoreNew()

	got, err := ensureWritablePathTUI(existing, "bundle", "cfg", "sig")
	if err != nil {
		t.Fatalf("ensureWritablePathTUI error: %v", err)
	}
	if got != filepath.Clean(nextPath) {
		t.Fatalf("path=%q, want %q", got, nextPath)
	}
	if promptCalls != 1 {
		t.Fatalf("expected 1 prompt call, got %d", promptCalls)
	}
}

func TestEnsureWritablePathTUIAbortOnCancel(t *testing.T) {
	path := mustCreateExistingFile(t)
	restore := stubPromptOverwriteAction(func(path, desc, failure, configPath, buildSig string) (string, error) {
		return pathActionCancel, nil
	})
	defer restore()

	if _, err := ensureWritablePathTUI(path, "bundle", "cfg", "sig"); !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted, got %v", err)
	}
}

func TestEnsureWritablePathTUIPropagatesPromptErrors(t *testing.T) {
	path := mustCreateExistingFile(t)
	wantErr := errors.New("boom")
	restore := stubPromptOverwriteAction(func(path, desc, failure, configPath, buildSig string) (string, error) {
		return "", wantErr
	})
	defer restore()

	if _, err := ensureWritablePathTUI(path, "bundle", "cfg", "sig"); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestEnsureWritablePathTUINewPathAbort(t *testing.T) {
	path := mustCreateExistingFile(t)
	restorePrompt := stubPromptOverwriteAction(func(path, desc, failure, configPath, buildSig string) (string, error) {
		return pathActionNew, nil
	})
	defer restorePrompt()

	restoreNew := stubPromptNewPath(func(current, configPath, buildSig string) (string, error) {
		return "", ErrDecryptAborted
	})
	defer restoreNew()

	if _, err := ensureWritablePathTUI(path, "bundle", "cfg", "sig"); !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted, got %v", err)
	}
}

func TestPreparePlainBundleTUICopiesRawArtifacts(t *testing.T) {
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
	if err := os.WriteFile(rawChecksum, []byte("checksum  backup.tar\n"), 0o640); err != nil {
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
	prepared, err := preparePlainBundleTUI(ctx, cand, "1.0.0", logger, "cfg", "sig")
	if err != nil {
		t.Fatalf("preparePlainBundleTUI error: %v", err)
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
}

func TestShowErrorModalAddsWizardPage(t *testing.T) {
	app := tui.NewApp()
	pages := tview.NewPages()

	showErrorModal(app, pages, "cfg", "sig", "boom", nil)

	if !pages.HasPage(errorModalPage) {
		t.Fatalf("expected %q page to be present", errorModalPage)
	}

	page := pages.GetPage(errorModalPage)
	flex, ok := page.(*tview.Flex)
	if !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
	content := flex.GetItem(3)
	modal, ok := content.(*tview.Modal)
	if !ok {
		t.Fatalf("expected *tview.Modal content, got %T", content)
	}
	if modal.GetTitle() != " Decrypt Error " {
		t.Fatalf("modal title=%q; want %q", modal.GetTitle(), " Decrypt Error ")
	}
}

func TestShowCandidatePageAddsCandidatesPageWithItems(t *testing.T) {
	app := tui.NewApp()
	pages := tview.NewPages()

	now := time.Unix(1700000000, 0)
	candidates := []*decryptCandidate{
		{
			Manifest: &backup.Manifest{
				CreatedAt:       now,
				EncryptionMode:  "age",
				ProxmoxTargets:  []string{"pve"},
				ProxmoxVersion:  "8.1",
				CompressionType: "zstd",
				ClusterMode:     "standalone",
				ScriptVersion:   "1.0.0",
			},
		},
		{
			Manifest: &backup.Manifest{
				CreatedAt:       now.Add(-time.Hour),
				EncryptionMode:  "age",
				ProxmoxTargets:  []string{"pbs"},
				CompressionType: "xz",
				ScriptVersion:   "1.0.0",
			},
		},
	}

	showCandidatePage(app, pages, candidates, "cfg", "sig", func(*decryptCandidate) {}, func() {})

	if !pages.HasPage("candidates") {
		t.Fatalf("expected candidates page to be present")
	}
	page := pages.GetPage("candidates")
	flex, ok := page.(*tview.Flex)
	if !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
	content := flex.GetItem(3)
	form, ok := content.(*tview.Form)
	if !ok {
		t.Fatalf("expected *tview.Form content, got %T", content)
	}
	if form.GetFormItemCount() != 1 {
		t.Fatalf("form items=%d; want 1", form.GetFormItemCount())
	}
	listItem, ok := form.GetFormItem(0).(*components.ListFormItem)
	if !ok {
		t.Fatalf("expected *components.ListFormItem, got %T", form.GetFormItem(0))
	}
	if got := listItem.GetItemCount(); got != len(candidates) {
		t.Fatalf("list items=%d; want %d", got, len(candidates))
	}
}

func TestShowDestinationFormAddsDestinationPageWithInput(t *testing.T) {
	app := tui.NewApp()
	pages := tview.NewPages()

	cfg := &config.Config{BaseDir: t.TempDir()}
	selected := &decryptCandidate{
		Manifest: &backup.Manifest{
			CreatedAt:      time.Unix(1700000000, 0),
			EncryptionMode: "age",
			ProxmoxTargets: []string{"pve"},
			ScriptVersion:  "1.0.0",
		},
	}

	showDestinationForm(app, pages, cfg, selected, "cfg", "sig", func(string) {})

	if !pages.HasPage("destination") {
		t.Fatalf("expected destination page to be present")
	}
	page := pages.GetPage("destination")
	flex, ok := page.(*tview.Flex)
	if !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
	content := flex.GetItem(3)
	inner, ok := content.(*tview.Flex)
	if !ok {
		t.Fatalf("expected inner *tview.Flex, got %T", content)
	}
	form, ok := inner.GetItem(1).(*tview.Form)
	if !ok {
		t.Fatalf("expected *tview.Form, got %T", inner.GetItem(1))
	}
	if form.GetFormItemCount() < 1 {
		t.Fatalf("expected at least 1 form item")
	}
	field, ok := form.GetFormItem(0).(*tview.InputField)
	if !ok {
		t.Fatalf("expected first form item to be *tview.InputField, got %T", form.GetFormItem(0))
	}
	if field.GetLabel() != "Destination directory" {
		t.Fatalf("label=%q; want %q", field.GetLabel(), "Destination directory")
	}
}

func TestPreparePlainBundleTUIRejectsInvalidCandidate(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	ctx := context.Background()
	if _, err := preparePlainBundleTUI(ctx, nil, "", logger, "cfg", "sig"); err == nil {
		t.Fatalf("expected error for nil candidate")
	}
}

func TestBuildWizardPageReturnsFlex(t *testing.T) {
	content := tview.NewBox()
	page := buildWizardPage("Title", "/etc/proxsave/backup.env", "sig", content)
	if page == nil {
		t.Fatalf("expected non-nil page")
	}
	if _, ok := page.(*tview.Flex); !ok {
		t.Fatalf("expected *tview.Flex, got %T", page)
	}
}

func stubPromptOverwriteAction(fn func(path, description, failureMessage, configPath, buildSig string) (string, error)) func() {
	orig := promptOverwriteActionFunc
	promptOverwriteActionFunc = fn
	return func() { promptOverwriteActionFunc = orig }
}

func stubPromptNewPath(fn func(current, configPath, buildSig string) (string, error)) func() {
	orig := promptNewPathInputFunc
	promptNewPathInputFunc = fn
	return func() { promptNewPathInputFunc = orig }
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
