package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
)

func TestMapInputAbortToAgeAbort(t *testing.T) {
	if mapInputAbortToAgeAbort(nil) != nil {
		t.Fatalf("expected nil")
	}
	if !errors.Is(mapInputAbortToAgeAbort(input.ErrInputAborted), ErrAgeRecipientSetupAborted) {
		t.Fatalf("expected ErrAgeRecipientSetupAborted for ErrInputAborted")
	}
	if !errors.Is(mapInputAbortToAgeAbort(context.Canceled), ErrAgeRecipientSetupAborted) {
		t.Fatalf("expected ErrAgeRecipientSetupAborted for context.Canceled")
	}

	sentinel := errors.New("sentinel")
	if mapInputAbortToAgeAbort(sentinel) != sentinel {
		t.Fatalf("expected passthrough for non-abort errors")
	}
}

func TestValidatePassphraseStrengthExported(t *testing.T) {
	if err := ValidatePassphraseStrength("Str0ng!Passphrase"); err != nil {
		t.Fatalf("expected strong passphrase to pass, got %v", err)
	}
	if err := ValidatePassphraseStrength("Short1!"); err == nil {
		t.Fatalf("expected short passphrase to fail")
	}
}

func TestValidateRecipientStringExported(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	if err := ValidateRecipientString("   "); err == nil {
		t.Fatalf("expected empty recipient to fail")
	}
	if err := ValidateRecipientString("not-a-recipient"); err == nil {
		t.Fatalf("expected invalid recipient to fail")
	}
	if err := ValidateRecipientString("  " + id.Recipient().String() + " "); err != nil {
		t.Fatalf("expected valid recipient to pass, got %v", err)
	}
}

func TestDedupeRecipientStringsExported(t *testing.T) {
	got := DedupeRecipientStrings([]string{"  age1alpha  ", "", "age1alpha", "ssh-ed25519 AAA", "ssh-ed25519 AAA"})
	want := []string{"age1alpha", "ssh-ed25519 AAA"}
	if len(got) != len(want) {
		t.Fatalf("got=%v; want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q; want %q", i, got[i], want[i])
		}
	}
}

func TestWriteRecipientFileExported(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "identity", "age", "recipient.txt")

	if err := WriteRecipientFile(path, []string{"  age1alpha  ", "", "age1alpha", "ssh-ed25519 AAA"}); err != nil {
		t.Fatalf("WriteRecipientFile error: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(content); got != "age1alpha\nssh-ed25519 AAA\n" {
		t.Fatalf("content=%q; want %q", got, "age1alpha\nssh-ed25519 AAA\n")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm=%#o; want %#o", perm, 0o600)
	}
}

func TestWriteRecipientFileExported_NoRecipientsFails(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "recipient.txt")

	if err := WriteRecipientFile(path, []string{"", "   "}); err == nil {
		t.Fatalf("expected error")
	}
}

func TestBackupAgeRecipientFileExported(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "recipient.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	if err := BackupAgeRecipientFile(path); err != nil {
		t.Fatalf("BackupAgeRecipientFile error: %v", err)
	}
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected backup file, got %v err=%v", matches, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original path should have been moved, stat err=%v", err)
	}
}

func TestBackupAgeRecipientFileExported_EmptyPathNoop(t *testing.T) {
	if err := BackupAgeRecipientFile(""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestDefaultAgeRecipientFile(t *testing.T) {
	if got := (&Orchestrator{}).defaultAgeRecipientFile(); got != "" {
		t.Fatalf("got=%q; want empty", got)
	}

	o := &Orchestrator{cfg: &config.Config{BaseDir: "/tmp/base"}}
	got := o.defaultAgeRecipientFile()
	if !strings.HasSuffix(got, "/tmp/base/identity/age/recipient.txt") {
		t.Fatalf("got=%q; want suffix %q", got, "/tmp/base/identity/age/recipient.txt")
	}
}

func TestPrepareAgeRecipients_NoEncryptionNoop(t *testing.T) {
	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: false})
	got, err := o.prepareAgeRecipients(context.Background())
	if err != nil {
		t.Fatalf("prepareAgeRecipients error: %v", err)
	}
	if got != nil {
		t.Fatalf("got=%v; want nil", got)
	}
}

func TestPrepareAgeRecipients_NoRecipientsNonInteractiveErrors(t *testing.T) {
	origIn := os.Stdin
	origOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = origIn
		os.Stdout = origOut
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		t.Fatalf("pipe stdout: %v", err)
	}
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()

	os.Stdin = inR
	os.Stdout = outW

	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, BaseDir: t.TempDir()})
	_, err = o.prepareAgeRecipients(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestWriteRecipientFile_CreateDirError(t *testing.T) {
	tmp := t.TempDir()
	// Make the would-be directory a file so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(tmp, "identity"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	err := writeRecipientFile(filepath.Join(tmp, "identity", "age", "recipient.txt"), []string{"age1alpha"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "create recipient directory") {
		t.Fatalf("err=%v; want create recipient directory error", err)
	}
}

func TestWriteRecipientFile_WriteError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "recipient.txt")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := writeRecipientFile(path, []string{"age1alpha"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "write recipient file") {
		t.Fatalf("err=%v; want write recipient file error", err)
	}
}

func TestBackupExistingRecipientFile_Noops(t *testing.T) {
	if err := backupExistingRecipientFile(""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if err := backupExistingRecipientFile(filepath.Join(t.TempDir(), "missing.txt")); err != nil {
		t.Fatalf("expected nil for missing file, got %v", err)
	}
}

func TestRunAgeSetupWizard_ExitReturnsAborted(t *testing.T) {
	tmp := t.TempDir()
	inputFile := filepath.Join(tmp, "stdin.txt")
	if err := os.WriteFile(inputFile, []byte("4\n"), 0o600); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	f, err := os.Open(inputFile)
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	defer f.Close()

	origIn := os.Stdin
	t.Cleanup(func() { os.Stdin = origIn })
	os.Stdin = f

	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, BaseDir: tmp})
	_, _, err = o.runAgeSetupWizard(context.Background(), filepath.Join(tmp, "recipient.txt"))
	if !errors.Is(err, ErrAgeRecipientSetupAborted) {
		t.Fatalf("err=%v; want %v", err, ErrAgeRecipientSetupAborted)
	}
}

func TestRunAgeSetupWizard_Option1WritesFile(t *testing.T) {
	tmp := t.TempDir()
	inputFile := filepath.Join(tmp, "stdin.txt")
	// Option 1 -> recipient -> no more recipients.
	if err := os.WriteFile(inputFile, []byte("1\nage1alpha\nn\n"), 0o600); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	f, err := os.Open(inputFile)
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	defer f.Close()

	origIn := os.Stdin
	t.Cleanup(func() { os.Stdin = origIn })
	os.Stdin = f

	o := newEncryptionTestOrchestrator(&config.Config{EncryptArchive: true, BaseDir: tmp})
	out, savedPath, err := o.runAgeSetupWizard(context.Background(), filepath.Join(tmp, "recipient.txt"))
	if err != nil {
		t.Fatalf("runAgeSetupWizard error: %v", err)
	}
	if savedPath == "" {
		t.Fatalf("expected saved path")
	}
	if len(out) != 1 || out[0] != "age1alpha" {
		t.Fatalf("out=%v; want %v", out, []string{"age1alpha"})
	}
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved: %v", err)
	}
	if string(data) != "age1alpha\n" {
		t.Fatalf("saved content=%q; want %q", string(data), "age1alpha\n")
	}
}
