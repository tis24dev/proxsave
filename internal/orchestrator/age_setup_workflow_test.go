package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/config"
)

type mockAgeSetupUI struct {
	overwrite bool
	drafts    []*AgeRecipientDraft
	addMore   []bool

	overwriteCalls int
	collectCalls   int
	addCalls       int
}

func (m *mockAgeSetupUI) ConfirmOverwriteExistingRecipient(ctx context.Context, recipientPath string) (bool, error) {
	m.overwriteCalls++
	return m.overwrite, nil
}

func (m *mockAgeSetupUI) CollectRecipientDraft(ctx context.Context, recipientPath string) (*AgeRecipientDraft, error) {
	m.collectCalls++
	if len(m.drafts) == 0 {
		return nil, ErrAgeRecipientSetupAborted
	}
	draft := m.drafts[0]
	m.drafts = m.drafts[1:]
	return draft, nil
}

func (m *mockAgeSetupUI) ConfirmAddAnotherRecipient(ctx context.Context, currentCount int) (bool, error) {
	m.addCalls++
	if len(m.addMore) == 0 {
		return false, nil
	}
	next := m.addMore[0]
	m.addMore = m.addMore[1:]
	return next, nil
}

func TestEnsureAgeRecipientsReadyWithUI_ReusesConfiguredRecipientsWithoutPrompting(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	ui := &mockAgeSetupUI{}
	orch := newEncryptionTestOrchestrator(&config.Config{
		EncryptArchive: true,
		BaseDir:        t.TempDir(),
		AgeRecipients:  []string{id.Recipient().String()},
	})

	if err := orch.EnsureAgeRecipientsReadyWithUI(context.Background(), ui); err != nil {
		t.Fatalf("EnsureAgeRecipientsReadyWithUI error: %v", err)
	}
	if ui.collectCalls != 0 || ui.overwriteCalls != 0 || ui.addCalls != 0 {
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
		drafts: []*AgeRecipientDraft{
			{Kind: AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		addMore: []bool{false},
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

func TestEnsureAgeRecipientsReadyWithUI_ForceNewRecipientDeclineReturnsAbort(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ui := &mockAgeSetupUI{overwrite: false}
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
	if ui.overwriteCalls != 1 {
		t.Fatalf("overwriteCalls=%d; want 1", ui.overwriteCalls)
	}
	if ui.collectCalls != 0 {
		t.Fatalf("collectCalls=%d; want 0", ui.collectCalls)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("recipient file should remain in place, stat err=%v", statErr)
	}
}
