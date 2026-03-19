package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/testutil"
)

type testAgeSetupUI = testutil.AgeSetupUIStub[orchestrator.AgeRecipientDraft]

func TestRunInitialEncryptionSetupWithUIReloadsConfig(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := "BASE_DIR=" + baseDir + "\nENCRYPT_ARCHIVE=true\nAGE_RECIPIENT=" + id.Recipient().String() + "\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := runInitialEncryptionSetupWithUI(context.Background(), configPath, nil)
	if err != nil {
		t.Fatalf("runInitialEncryptionSetupWithUI error: %v", err)
	}
	if result == nil || result.Config == nil {
		t.Fatalf("expected config result")
	}
	if len(result.Config.AgeRecipients) != 1 || result.Config.AgeRecipients[0] != id.Recipient().String() {
		t.Fatalf("AgeRecipients=%v; want [%s]", result.Config.AgeRecipients, id.Recipient().String())
	}
	if !result.ReusedExistingRecipients {
		t.Fatalf("expected ReusedExistingRecipients=true")
	}
	if result.WroteRecipientFile {
		t.Fatalf("expected WroteRecipientFile=false")
	}
	if result.RecipientPath != "" {
		t.Fatalf("RecipientPath=%q; want empty for reuse-only result", result.RecipientPath)
	}
}

func TestRunInitialEncryptionSetupWithUIUsesProvidedUI(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := "BASE_DIR=" + baseDir + "\nENCRYPT_ARCHIVE=true\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ui := &testAgeSetupUI{
		AbortErr: orchestrator.ErrAgeRecipientSetupAborted,
		Drafts: []*orchestrator.AgeRecipientDraft{
			{Kind: orchestrator.AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}

	result, err := runInitialEncryptionSetupWithUI(context.Background(), configPath, ui)
	if err != nil {
		t.Fatalf("runInitialEncryptionSetupWithUI error: %v", err)
	}

	expectedPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	if result == nil || result.Config == nil {
		t.Fatalf("expected setup result with config")
	}
	if result.RecipientPath != expectedPath {
		t.Fatalf("RecipientPath=%q; want %q", result.RecipientPath, expectedPath)
	}
	if !result.WroteRecipientFile {
		t.Fatalf("expected WroteRecipientFile=true")
	}
	if result.ReusedExistingRecipients {
		t.Fatalf("expected ReusedExistingRecipients=false")
	}
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected recipient file at %s: %v", expectedPath, err)
	}
}

func TestRunInitialEncryptionSetupWithUIReusesExistingFileWithoutReportingWrite(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	baseDir := t.TempDir()
	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	if err := os.MkdirAll(filepath.Dir(recipientPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(recipientPath, []byte(id.Recipient().String()+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", recipientPath, err)
	}

	configPath := filepath.Join(baseDir, "env", "backup.env")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(configPath), err)
	}
	content := "BASE_DIR=" + baseDir + "\nENCRYPT_ARCHIVE=true\nAGE_RECIPIENT_FILE=" + recipientPath + "\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", configPath, err)
	}

	result, err := runInitialEncryptionSetupWithUI(context.Background(), configPath, nil)
	if err != nil {
		t.Fatalf("runInitialEncryptionSetupWithUI error: %v", err)
	}

	if result == nil || result.Config == nil {
		t.Fatalf("expected setup result with config")
	}
	if !result.ReusedExistingRecipients {
		t.Fatalf("expected ReusedExistingRecipients=true")
	}
	if result.WroteRecipientFile {
		t.Fatalf("expected WroteRecipientFile=false")
	}
	if result.RecipientPath != "" {
		t.Fatalf("RecipientPath=%q; want empty for reuse-only result", result.RecipientPath)
	}
}

func TestRunNewKeySetupKeepsDefaultRecipientPathContract(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	ui := &testAgeSetupUI{
		AbortErr:  orchestrator.ErrAgeRecipientSetupAborted,
		Overwrite: true,
		Drafts: []*orchestrator.AgeRecipientDraft{
			{Kind: orchestrator.AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}

	recipientPath, err := runNewKeySetup(context.Background(), configPath, baseDir, nil, ui)
	if err != nil {
		t.Fatalf("runNewKeySetup error: %v", err)
	}

	target := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	if recipientPath != target {
		t.Fatalf("recipientPath=%q; want %q", recipientPath, target)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", target, err)
	}
	if got := string(content); got != id.Recipient().String()+"\n" {
		t.Fatalf("content=%q; want %q", got, id.Recipient().String()+"\n")
	}
}

func TestRunNewKeySetupUsesConfiguredRecipientFile(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "env", "backup.env")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(configPath), err)
	}

	customPath := filepath.Join(baseDir, "custom", "recipient.txt")
	content := "BASE_DIR=" + baseDir + "\nENCRYPT_ARCHIVE=true\nAGE_RECIPIENT_FILE=" + customPath + "\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", configPath, err)
	}

	ui := &testAgeSetupUI{
		AbortErr:  orchestrator.ErrAgeRecipientSetupAborted,
		Overwrite: true,
		Drafts: []*orchestrator.AgeRecipientDraft{
			{Kind: orchestrator.AgeRecipientInputExisting, PublicKey: id.Recipient().String()},
		},
		AddMore: []bool{false},
	}

	recipientPath, err := runNewKeySetup(context.Background(), configPath, baseDir, nil, ui)
	if err != nil {
		t.Fatalf("runNewKeySetup error: %v", err)
	}
	if recipientPath != customPath {
		t.Fatalf("recipientPath=%q; want %q", recipientPath, customPath)
	}

	customContent, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", customPath, err)
	}
	if got := string(customContent); got != id.Recipient().String()+"\n" {
		t.Fatalf("content=%q; want %q", got, id.Recipient().String()+"\n")
	}

	defaultPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Fatalf("default path %s should not be written, stat err=%v", defaultPath, err)
	}
}
