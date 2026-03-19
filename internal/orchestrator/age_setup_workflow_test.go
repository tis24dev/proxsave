package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/testutil"
)

type mockAgeSetupUI = testutil.AgeSetupUIStub[AgeRecipientDraft]

type renameFailFS struct {
	*FakeFS
	err error
}

func (f *renameFailFS) Rename(oldpath, newpath string) error {
	return f.err
}

func TestEnsureAgeRecipientsReadyWithUI_ReusesConfiguredRecipientsWithoutPrompting(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	ui := &mockAgeSetupUI{AbortErr: ErrAgeRecipientSetupAborted}
	orch := newEncryptionTestOrchestrator(&config.Config{
		EncryptArchive: true,
		BaseDir:        t.TempDir(),
		AgeRecipients:  []string{id.Recipient().String()},
	})

	if err := orch.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("EnsureAgeRecipientsReadyWithUI error: %v", err)
	}
	if ui.CollectCalls != 0 || ui.OverwriteCalls != 0 || ui.AddCalls != 0 {
		t.Fatalf("UI should not have been used when recipients already exist: %#v", ui)
	}
}

func TestEnsureAgeRecipientsReadyWithUI_ConfiguresRecipientsWithoutTTY(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	tmp := t.TempDir()
	ui := &mockAgeSetupUI{
		AbortErr: ErrAgeRecipientSetupAborted,
		Drafts: []*AgeRecipientDraft{
			{Kind: AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}
	cfg := &config.Config{EncryptArchive: true, BaseDir: tmp}
	orch := newEncryptionTestOrchestrator(cfg)

	if err := orch.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("EnsureAgeRecipientsReadyWithUI error: %v", err)
	}

	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", target, err)
	}
	if got := string(content); got != id.Recipient().String()+"\n" {
		t.Fatalf("content=%q; want %q", got, id.Recipient().String()+"\n")
	}
	if cfg.AgeRecipientFile != target {
		t.Fatalf("AgeRecipientFile=%q; want %q", cfg.AgeRecipientFile, target)
	}
}

func TestMapAgeSetupAbort_NormalizesAbortSignals(t *testing.T) {
	if !errors.Is(mapAgeSetupAbort(context.Canceled), ErrAgeRecipientSetupAborted) {
		t.Fatalf("expected context.Canceled to normalize to %v", ErrAgeRecipientSetupAborted)
	}
	if !errors.Is(mapAgeSetupAbort(ErrAgeRecipientSetupAborted), ErrAgeRecipientSetupAborted) {
		t.Fatalf("expected ErrAgeRecipientSetupAborted to remain normalized")
	}

	sentinel := errors.New("boom")
	if got := mapAgeSetupAbort(sentinel); got != sentinel {
		t.Fatalf("expected non-abort error passthrough, got %v", got)
	}
}

func TestEnsureAgeRecipientsReadyWithUI_ForceNewRecipientDeclineReturnsAbort(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ui := &mockAgeSetupUI{
		AbortErr:  ErrAgeRecipientSetupAborted,
		Overwrite: false,
	}
	orch := newEncryptionTestOrchestrator(&config.Config{
		EncryptArchive:   true,
		BaseDir:          tmp,
		AgeRecipientFile: target,
	})
	orch.SetForceNewAgeRecipient(true)

	err := orch.EnsureAgeRecipientsReadyWithUI(context.Background(), ui)
	if !errors.Is(err, ErrAgeRecipientSetupAborted) {
		t.Fatalf("err=%v; want %v", err, ErrAgeRecipientSetupAborted)
	}
	if ui.OverwriteCalls != 1 {
		t.Fatalf("overwriteCalls=%d; want 1", ui.OverwriteCalls)
	}
	if ui.CollectCalls != 0 {
		t.Fatalf("collectCalls=%d; want 0", ui.CollectCalls)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("recipient file should remain in place, stat err=%v", statErr)
	}
}

func TestEnsureAgeRecipientsReadyWithUI_ForceNewRecipientSuccessfulOverwriteCreatesBackupOnCommit(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	tmp := t.TempDir()
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ui := &mockAgeSetupUI{
		AbortErr:  ErrAgeRecipientSetupAborted,
		Overwrite: true,
		Drafts: []*AgeRecipientDraft{
			{Kind: AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}
	cfg := &config.Config{
		EncryptArchive:   true,
		BaseDir:          tmp,
		AgeRecipientFile: target,
	}
	fakeTime := &FakeTime{Current: time.Date(2026, 3, 17, 10, 11, 12, 0, time.UTC)}
	orch := newEncryptionTestOrchestrator(cfg)
	orch.SetForceNewAgeRecipient(true)
	orch.clock = fakeTime

	if err := orch.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("EnsureAgeRecipientsReadyWithUI error: %v", err)
	}

	backupPath := target + ".bak-" + fakeTime.Current.Format("20060102-150405")
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", backupPath, err)
	}
	if got := strings.TrimSpace(string(backup)); got != "old" {
		t.Fatalf("backup content=%q; want %q", got, "old")
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", target, err)
	}
	if got := strings.TrimSpace(string(content)); got != id.Recipient().String() {
		t.Fatalf("content=%q; want %q", got, id.Recipient().String())
	}
	if ui.OverwriteCalls != 1 {
		t.Fatalf("overwriteCalls=%d; want 1", ui.OverwriteCalls)
	}
}

func TestRunAgeSetupWorkflow_ForceNewRecipientBackupFailurePreservesOriginal(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	fs := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fs.Root) })
	fakeTime := &FakeTime{Current: time.Date(2026, 3, 17, 12, 0, 0, 0, time.UTC)}
	target := "/identity/age/recipient.txt"
	if err := fs.AddFile(target, []byte("old\n")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	backupPath := target + ".bak-" + fakeTime.Current.Format("20060102-150405")
	fs.OpenFileErr[filepath.Clean(backupPath)] = errors.New("disk full")

	ui := &mockAgeSetupUI{
		AbortErr:  ErrAgeRecipientSetupAborted,
		Overwrite: true,
		Drafts: []*AgeRecipientDraft{
			{Kind: AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}
	orch := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, AgeRecipientFile: target})
	orch.SetForceNewAgeRecipient(true)
	orch.fs = fs
	orch.clock = fakeTime

	_, _, err = orch.runAgeSetupWorkflow(context.Background(), target, ui)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "backup existing AGE recipients at "+target) {
		t.Fatalf("err=%v; want backup failure context", err)
	}

	content, readErr := fs.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile(%s): %v", target, readErr)
	}
	if got := strings.TrimSpace(string(content)); got != "old" {
		t.Fatalf("original content=%q; want %q", got, "old")
	}
	if _, statErr := fs.Stat(backupPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup stat err=%v; want not exist", statErr)
	}
}

func TestRunAgeSetupWorkflow_ForceNewRecipientWriteFailurePreservesOriginalAndBackup(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	baseFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(baseFS.Root) })
	fs := &renameFailFS{FakeFS: baseFS, err: errors.New("rename failed")}
	fakeTime := &FakeTime{Current: time.Date(2026, 3, 17, 12, 30, 0, 0, time.UTC)}
	target := "/identity/age/recipient.txt"
	if err := fs.AddFile(target, []byte("old\n")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}

	ui := &mockAgeSetupUI{
		AbortErr:  ErrAgeRecipientSetupAborted,
		Overwrite: true,
		Drafts: []*AgeRecipientDraft{
			{Kind: AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}
	orch := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, AgeRecipientFile: target})
	orch.SetForceNewAgeRecipient(true)
	orch.fs = fs
	orch.clock = fakeTime

	_, _, err = orch.runAgeSetupWorkflow(context.Background(), target, ui)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "write recipient file") {
		t.Fatalf("err=%v; want write recipient file failure", err)
	}

	backupPath := target + ".bak-" + fakeTime.Current.Format("20060102-150405")
	backup, readErr := fs.ReadFile(backupPath)
	if readErr != nil {
		t.Fatalf("ReadFile(%s): %v", backupPath, readErr)
	}
	if got := strings.TrimSpace(string(backup)); got != "old" {
		t.Fatalf("backup content=%q; want %q", got, "old")
	}

	content, readErr := fs.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile(%s): %v", target, readErr)
	}
	if got := strings.TrimSpace(string(content)); got != "old" {
		t.Fatalf("original content=%q; want %q", got, "old")
	}
}
