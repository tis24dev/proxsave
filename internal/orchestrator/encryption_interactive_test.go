package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/config"
)

func TestEnsureAgeRecipientsReady_NoConfigOrDisabled(t *testing.T) {
	var nilOrch *Orchestrator
	if err := nilOrch.EnsureAgeRecipientsReady(context.Background()); err != nil {
		t.Fatalf("EnsureAgeRecipientsReady(nil) error = %v; want nil", err)
	}

	orch := &Orchestrator{cfg: &config.Config{EncryptArchive: false}}
	if err := orch.EnsureAgeRecipientsReady(context.Background()); err != nil {
		t.Fatalf("EnsureAgeRecipientsReady(disabled) error = %v; want nil", err)
	}
}

func TestEnsureAgeRecipientsReady_PreparesRecipients(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	cfg := &config.Config{
		EncryptArchive: true,
		AgeRecipients:  []string{id.Recipient().String()},
	}
	orch := newEncryptionTestOrchestrator(cfg)

	if err := orch.EnsureAgeRecipientsReady(context.Background()); err != nil {
		t.Fatalf("EnsureAgeRecipientsReady error = %v; want nil", err)
	}
}

func TestRunAgeSetupWizard_WritesRecipientFile(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	tmp := t.TempDir()
	targetPath := filepath.Join(tmp, "identity", "age", "recipient.txt")

	origStdin := os.Stdin
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = r
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		_ = r.Close()
		_ = devNull.Close()
	})

	go func() {
		// Option 1 (public recipient), then enter recipient, then "no" for additional recipients.
		_, _ = io.WriteString(w, "1\n"+id.Recipient().String()+"\n"+"n\n")
		_ = w.Close()
	}()

	orch := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, BaseDir: tmp})
	recs, savedPath, err := orch.runAgeSetupWizard(context.Background(), targetPath)
	if err != nil {
		t.Fatalf("runAgeSetupWizard error: %v", err)
	}
	if savedPath != targetPath {
		t.Fatalf("savedPath=%q; want %q", savedPath, targetPath)
	}
	if len(recs) != 1 || recs[0] != id.Recipient().String() {
		t.Fatalf("recipients=%v; want [%s]", recs, id.Recipient().String())
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", targetPath, err)
	}
	if got := strings.TrimSpace(string(content)); got != id.Recipient().String() {
		t.Fatalf("file content=%q; want %q", got, id.Recipient().String())
	}
}

func TestPromptPrivateKeyRecipient_ParsesSecretKey(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	orig := readPassword
	t.Cleanup(func() { readPassword = orig })
	readPassword = func(fd int) ([]byte, error) {
		return []byte(id.String()), nil
	}

	got, err := promptPrivateKeyRecipient(context.Background())
	if err != nil {
		t.Fatalf("promptPrivateKeyRecipient error: %v", err)
	}
	if got != id.Recipient().String() {
		t.Fatalf("recipient=%q; want %q", got, id.Recipient().String())
	}
}

func TestPromptAndConfirmPassphrase_Mismatch(t *testing.T) {
	orig := readPassword
	t.Cleanup(func() { readPassword = orig })

	var mu sync.Mutex
	inputs := [][]byte{
		[]byte("Str0ng!Passphrase"),
		[]byte("Different1!Passphrase"),
	}
	readPassword = func(fd int) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(inputs) == 0 {
			return nil, io.EOF
		}
		next := append([]byte(nil), inputs[0]...)
		inputs = inputs[1:]
		return next, nil
	}

	if _, err := promptAndConfirmPassphrase(context.Background()); err == nil {
		t.Fatalf("expected mismatch error, got nil")
	}
}

func TestPromptPassphraseRecipient_Success(t *testing.T) {
	orig := readPassword
	t.Cleanup(func() { readPassword = orig })

	var mu sync.Mutex
	inputs := [][]byte{
		[]byte("Str0ng!Passphrase"),
		[]byte("Str0ng!Passphrase"),
	}
	readPassword = func(fd int) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(inputs) == 0 {
			return nil, io.EOF
		}
		next := append([]byte(nil), inputs[0]...)
		inputs = inputs[1:]
		return next, nil
	}

	recipient, err := promptPassphraseRecipient(context.Background())
	if err != nil {
		t.Fatalf("promptPassphraseRecipient error: %v", err)
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("recipient=%q; want age1... format", recipient)
	}
}

func TestDeriveDeterministicRecipientFromPassphrase_ExportedWrapper(t *testing.T) {
	recipient, err := DeriveDeterministicRecipientFromPassphrase("passphrase")
	if err != nil {
		t.Fatalf("DeriveDeterministicRecipientFromPassphrase error: %v", err)
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("recipient=%q; want age1... format", recipient)
	}
}

func TestIsInteractiveShell_DoesNotPanic(t *testing.T) {
	orch := &Orchestrator{}
	_ = orch.isInteractiveShell()
}
