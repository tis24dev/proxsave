package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/config"
)

func TestPrepareAgeRecipients_InteractiveWizardCanAbort(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(fd int) bool { return true }

	origStdin := os.Stdin
	origStdout := os.Stdout
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
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
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
	})

	go func() {
		_, _ = io.WriteString(inW, "4\n")
		_ = inW.Close()
	}()

	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, BaseDir: t.TempDir()})
	_, err = o.prepareAgeRecipients(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrAgeRecipientSetupAborted) {
		t.Fatalf("err=%v want=%v", err, ErrAgeRecipientSetupAborted)
	}
}

func TestPrepareAgeRecipients_InteractiveWizardSetsRecipientFile(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(fd int) bool { return true }

	tmp := t.TempDir()
	cfg := &config.Config{EncryptArchive: true, BaseDir: tmp}

	origStdin := os.Stdin
	origStdout := os.Stdout
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
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
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
	})

	go func() {
		// Option 1 (public recipient), then enter recipient, then "no" for additional recipients.
		_, _ = io.WriteString(inW, "1\n"+id.Recipient().String()+"\n"+"n\n")
		_ = inW.Close()
	}()

	o := newEncryptionTestOrchestrator(cfg)
	recs, err := o.prepareAgeRecipients(context.Background())
	if err != nil {
		t.Fatalf("prepareAgeRecipients error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("recipients=%d want=%d", len(recs), 1)
	}

	expectedPath := filepath.Join(tmp, "identity", "age", "recipient.txt")
	if cfg.AgeRecipientFile != expectedPath {
		t.Fatalf("AgeRecipientFile=%q want=%q", cfg.AgeRecipientFile, expectedPath)
	}
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", expectedPath, err)
	}
	if got := strings.TrimSpace(string(content)); got != id.Recipient().String() {
		t.Fatalf("file content=%q want=%q", got, id.Recipient().String())
	}
}

func TestRunAgeSetupWizard_ForceNewRecipientAbortKeepsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "identity", "age", "recipient.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origStdin := os.Stdin
	origStdout := os.Stdout
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
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
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = out.Close()
	})

	go func() {
		// Confirm deletion of existing recipients, then exit wizard.
		_, _ = io.WriteString(inW, "y\n4\n")
		_ = inW.Close()
	}()

	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, BaseDir: tmp})
	o.forceNewAgeRecipient = true

	_, _, err = o.runAgeSetupWizard(context.Background(), target)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrAgeRecipientSetupAborted) {
		t.Fatalf("err=%v want=%v", err, ErrAgeRecipientSetupAborted)
	}

	matches, err := filepath.Glob(target + ".bak-*")
	if err != nil {
		t.Fatalf("Glob(%s): %v", target+".bak-*", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no backup file on abort, got %v", matches)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", target, err)
	}
	if strings.TrimSpace(string(data)) != "old" {
		t.Fatalf("original content=%q want=%q", strings.TrimSpace(string(data)), "old")
	}
}
